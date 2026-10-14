// 本文件把「整行 → JSON 对象」这件事从 SQL 里搬到了 Go。
//
// ── 背景 ────────────────────────────────────────────────────────────────────
//
// db/finding_retests.go 的复测快照原本靠 PostgreSQL 的两个构造在 SQL 里就地
// 拼出整行 JSON：
//
//	jsonb_build_object('finding', to_jsonb(f),
//	                   'assets',      COALESCE((SELECT jsonb_agg(to_jsonb(a)) …), '[]'::jsonb),
//	                   'constraints', COALESCE((SELECT jsonb_agg(to_jsonb(c)) …), '[]'::jsonb))
//
// SQLite 两者都没有等价物：
//
//   - to_jsonb(<行>) 是「把这一行的每一列展开成一个 jsonb 对象」的专用构造，
//     没有对应的通用函数；
//   - jsonb_agg(<jsonb 表达式>) 聚合的是**已经算好的** jsonb 值，而整行在这里
//     根本还没有 jsonb 形态——不是先有行才有值，而是要先把行变成值。
//
// 于是同一份快照必须在 Go 侧重新组装一遍，并且两个引擎共用这一份 Go 代码。
//
// ── 为什么是 SELECT * + rows.Columns()，不是逐列 json_object ─────────────────
//
// 理论上的替代方案是把每一列硬写出来：
//
//	SELECT json_object('id', f.id, 'name', f.name, 'severity', f.severity, …)
//
// 这个方案有四个问题，越往后越致命：
//
//  1. 抄错列名不会报错。漏写一个字段，语句照样成功执行，快照安静地少一个键。
//     而这份快照是复测 Agent 判断「原始证据到底是什么」的唯一依据（见
//     server/finding_retests.go 的 get_finding_retest_context），字段缺失
//     直接让 Agent 在信息不足的情况下下结论。
//  2. schema 每加一列就要同步改这条 SQL。findings 15 列、assets 35 列、
//     task_constraints 7 列，三张表合计 57 个字段，是一处必然漏改的地方。
//  3. 类型映射要手写。BIGSERIAL 要变成 JSON number 还是 string、TIMESTAMPTZ
//     要按什么时区渲染、jsonb 列要内联还是转义，每一项都得重新决定一遍。
//  4. 两个引擎要写两遍。PostgreSQL 侧能写 json_object 不代表 SQLite 侧能写
//     出同样的结果，语句一长，分歧就会回来——那正是这次要消灭的东西。
//
// rows.Columns() 返回的是**数据库实际给出的列名**，因此：列的增删自动跟随
// schema（新增列自动出现在快照里，这正是 to_jsonb(f) 的行为）、SQL 语句在
// 两个引擎上逐字相同、列名不可能抄错。剩下的只有「值怎么变成 JSON」这一个
// 问题，由本文件的 jsonSafeValue 统一处理。
package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// rowsToJSONObjects 把一个结果集编码成 JSON 数组，每个元素是一个
// {"列名": 值} 的对象。空结果返回 `[]` 而不是 `null`。
//
// 调用方负责 rows.Close()。本函数会把结果集读到耗尽。
func rowsToJSONObjects(rows *sql.Rows) ([]byte, error) {
	objs, err := rowJSONObjects(rows)
	if err != nil {
		return nil, err
	}
	// 空集合必须是 [] 而不是 null：原来的 PG 语句用 COALESCE(…, '[]'::jsonb)
	// 保证了这一点，快照的 assets / constraints 键是数组而不是 null。
	if len(objs) == 0 {
		return []byte("[]"), nil
	}
	return marshalSnapshot(objs)
}

// drainRows 读完一个结果集并关掉它，关本身的错误也一并返回。
//
// 为什么必须在同一事务里发下一条语句前显式 Close：*sql.Tx 绑定在一条连接上，
// 而 SQLite 一条连接同一时刻只能有一个活动结果集——不关就会撞上
// "database table is locked"。rowsToJSONObjects 会读到耗尽，但耗尽不等于
// 释放，Close 才是。
func drainRows(rows *sql.Rows) ([]byte, error) {
	b, err := rowsToJSONObjects(rows)
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return b, err
}

// rowJSONObjects 是 rowsToJSONObjects 的结构化版本：同样按列名装配每一行，
// 但交给调用方的是 Go 值而不是已经编码好的字节。只需要首行、或需要先取出
// 某个标量列再拼装的调用点用它。
func rowJSONObjects(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	objs := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		objs = append(objs, rowJSONObject(cols, vals))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objs, nil
}

// rowJSONObject 按列名把一行值装配成 {"列名": 值} 对象，键顺序由
// json.Marshal 决定（字典序），与 PostgreSQL jsonb 的键序规范化一致。
func rowJSONObject(cols []string, vals []any) map[string]any {
	obj := make(map[string]any, len(cols))
	for i, name := range cols {
		obj[name] = jsonSafeValue(vals[i])
	}
	return obj
}

// jsonSafeValue 把一个 driver.Value 变成能安全进 JSON 的值。
//
// 两个引擎交给 database/sql 的原始类型（已逐条对过 pgx/v5 stdlib 的 Rows.Next
// 与 modernc.org/sqlite 的行为）：
//
//	PostgreSQL                SQLite
//	  INT2/4/8      → int64     INTEGER/BIGINT → int64
//	  FLOAT4/8      → float64   REAL          → float64
//	  BOOL          → bool      BOOLEAN        → int64（SQLite 无布尔类型）
//	  TEXT/VARCHAR  → string    TEXT           → string
//	  INT8[]/TEXT[]/INET/CIDR → string（同上，走默认分支）
//	  JSON/JSONB    → []byte    JSON 文本列    → string
//	  TIMESTAMPTZ   → time.Time TIMESTAMP（DSN 的 _texttotime=1）→ time.Time
//	  NULL          → nil       NULL           → nil
//
// 真正需要处理的只有三类：
//
//	· []byte / string —— JSON 文本，见 jsonText 的说明；
//	· time.Time      —— 渲染成 RFC3339Nano，与 PG to_jsonb(timestamptz) 同形；
//	· 非有限浮点     —— NaN / ±Inf 不是合法 JSON，降级成 null。
//
// 其它类型（int64 / bool / 未来驱动可能给出的数值类型）交给 json.Marshal，
// 它们本来就能正确编码。特别说明 int64 不做 0/1→bool 的猜测：本项目这三张表
// 没有 BOOLEAN 列，而 port、status_code、evidence_version 这类 INTEGER 列
// 取值 0/1 时被当成布尔才是真正的数据损坏。
func jsonSafeValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		// 与 json.Marshal 对 time.Time 的默认处理一致，但写出来是为了让
		// 「时间戳在快照里长什么样」这件事在本文件内自洽，不依赖外部默认值。
		return t.Format(time.RFC3339Nano)
	case float64:
		// json.Marshal 对 NaN / ±Inf 直接返回错误，会把整个快照打挂。
		// PG 的 to_jsonb 对非有限 double 同样报错，这里降级成 null 只会更宽松。
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil
		}
		return t
	case []byte:
		if raw, ok := jsonText(string(t)); ok {
			return raw
		}
		return string(t)
	case string:
		if raw, ok := jsonText(t); ok {
			return raw
		}
		return t
	default:
		return v
	}
}

// jsonText 判断一段文本「本身就是一份 JSON 值」，是就原样内联进快照，
// 否则按普通字符串处理。
//
// 为什么 string 也必须参与判断：SQLite 没有 JSON 类型，JSON 值就存放在
// TEXT 列里——findings.asset_ids 在 PostgreSQL 上是 JSONB，在 SQLite 上是
// TEXT；assets.task_ids / open_ports / params / extra 同理。如果只对
// []byte 内联（PostgreSQL 的 json/jsonb 才走这条路），两个引擎就会给出
// 结构不同的快照：同一列一边是数组、一边是数组的字符串文本。既然两种驱动
// 形态都要收敛到同一种 JSON，判定规则就必须对两者一致。
//
// 判定条件刻意收紧成「必须是数组或对象」，以免把数字 / 布尔 / 字符串形式的
// JSON 误判：findings.summary 这类自由文本里恰好写着 "[1,2]" 的概率不为零，
// 而 assets.task_ids / open_ports 这类列存的始终是数组或对象。
//
// ── 仍然存在的误判面（有意接受） ─────────────────────────────────────────────
//
// 自由文本列（findings.summary / evidence / report 等）的**整个取值**如果恰好
// 是一份合法 JSON 数组或对象，它会被内联成 JSON 值而不是字符串。实测：
// summary 写成 ["looks","like","json"] 时，快照里该字段是数组而非字符串。
//
// 之所以接受：SQLite 把 JSON 存成 TEXT，schema 里没有任何可用来区分「这一列
// 装的是 JSON」的元数据（findings.asset_ids 在 PG 是 JSONB、在 SQLite 声明成
// TEXT，与 summary 的声明类型一模一样）。唯一的精确做法是维护一份「哪些列是
// JSON」的清单，而那正是上面第 1、2 条否掉的方案：清单会随 schema 静默过期。
//
// 代价可控且两个引擎一致：只是少一层引号，字符一个不少；Agent 读到的内容
// 与之前相同。而另一个方向的代价（列清单漏维护）会让字段**消失**，危险得多。
func jsonText(s string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 2 {
		return nil, false
	}
	if trimmed[0] != '[' && trimmed[0] != '{' {
		return nil, false
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, false
	}
	return json.RawMessage(trimmed), true
}

// marshalSnapshot 编码快照。与 json.Marshal 的唯一差别是不转义 HTML 字符。
//
// 这份 JSON 不会离开本进程，而是被 server 原样交给复测 Agent 读取，证据
// 正文里的 < > & 保留原字符比 \u003c \u003e \u0026 更可读，也与 PostgreSQL
// to_jsonb 的输出一致（jsonb 从不做 HTML 转义）。
func marshalSnapshot(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder.Encode 会补一个换行，这里去掉，快照保持单行。
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
