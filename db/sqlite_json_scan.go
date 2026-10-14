package db

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// 本文件收敛「把数据库里的一个 JSON 文本列读进 Go」这件事：一个落点类型
// （jsonValue）加一套约定，供 db 包内所有 JSON 列复用——目前是通知的
// config / filter / snapshot，后续 task_archives 等包可以照抄。
//
// ── 为什么需要它（2026-10-14 实测） ───────────────────────────────────────────
//
// json.RawMessage 在 Go 1.27 上不是普通具名类型，而是 encoding/json/jsontext.Value
// 的**别名**：
//
//	GOROOT/src/encoding/json/v2_stream.go:190   type RawMessage = jsontext.Value
//	GOROOT/src/encoding/json/jsontext/value.go:66  type Value []byte
//
// 「别名」+「具名的 []byte 类型」这两点合起来，正好落在 database/sql 的盲区：
// convertAssign（GOROOT/src/database/sql/convert.go）只对**精确类型** *[]byte
// 写了两个分支（源是 string 时 *d = []byte(s)，源是 []byte 时 *d = bytes.Clone(s)），
// 具名的 []byte 命名类型一个都不沾；再往后只剩反射兜底，而那条路径只处理
// String/Int/Uint/Float/Ptr，dest 是 Slice，于是报出
//
//	unsupported Scan, storing driver.Value type string into type *jsontext.Value
//
// 实测矩阵（Go 1.27.2 / modernc.org/sqlite v1.52.0 / pgx v5.10.0）：
//
//	列形态          驱动交出的源类型   dest = *json.RawMessage
//	PG  JSONB       []byte             成功（两边类型完全相同，反射兜底直接 Set）
//	SQLite TEXT     string             失败 ← 本 bug
//	SQLite BLOB     []byte             成功（同上）
//	任意             nil                失败
//
// 也就是说：PostgreSQL 侧一直正常，是因为 pgx 把 JSONB 交成了 []byte，恰好撞在
// 「类型相同」这条反射兜底上；SQLite 把 JSON 存在 TEXT 列、交出 string，就掉进
// 了没有分支的空档。这不是 SQLite 驱动的缺陷，而是列的存储类型差异暴露了
// database/sql 的一个盲点。
//
// ── 为什么字段类型仍然是 json.RawMessage ─────────────────────────────────────
//
// 直觉上应该把 NotificationChannel.Config 之类的字段直接换成 jsonValue，
// 但本项目**换不了**：db/notification_test.go 等（不可改动的测试文件）里有
//
//	ch := &NotificationChannel{Config: json.RawMessage(`{"webhook":"…"}`)}
//
// 而 json.RawMessage（即 jsontext.Value）与 jsonValue **都是具名类型**，Go 不允许
// 两个具名类型互相赋值。实测（Go 1.27.2）：
//
//	cannot use json.RawMessage(`{}`) (value of slice type "encoding/json".RawMessage)
//	as NamedJSON value in variable declaration
//
// 既然测试文件不能改，字段类型就只能原样保留。于是采取「把类型收敛到读取点」
// 的写法：结构体字段继续用 json.RawMessage，扫描时先落进 jsonValue，读完赋回。
// 代价是每个扫描点多两行样板；换来的是**字段类型、JSON 序列化形状、以及
// server 层代码全部零改动**——前端看到的 JSON 与改动前逐字节相同。
// 这比「字段类型好看」重要得多：本项目里这些字段同时是 HTTP 响应体和归档内容。
//
// 新增 JSON 列扫描时照抄这三行，不要再直接 Scan(&someJSONField)：
//
//	var col jsonValue
//	err := row.Scan(&col)
//	field = col.Raw()

// jsonValue 是一个「数据库里的 JSON 文本列」的落点类型。
//
// 零值（RawMessage 为 nil）表示 SQL NULL。调用方把它当中间量用：
// 先 Scan 进来，再 Raw() 取走赋给结构体字段。
type jsonValue struct {
	json.RawMessage
}

// 编译期断言：四个接口全部由本类型提供，方法签名写错会在编译期暴露，
// 而不是等到某条查询在某个引擎上跑出来才发现。
var (
	_ sql.Scanner      = (*jsonValue)(nil)
	_ driver.Valuer    = jsonValue{}
	_ json.Marshaler   = jsonValue{}
	_ json.Unmarshaler = (*jsonValue)(nil)
)

// Scan 实现 sql.Scanner，三种源都要接：PG 交 []byte、SQLite 的 TEXT 列交 string、
// 空列交 nil。
//
// 字节一律复制一份再存（源是 string 时本来就要新建底层数组；源是 []byte 时
// 驱动可能复用缓冲区，database/sql 自己在 *[]byte 分支上也是 bytes.Clone）。
// NULL 与「空串」保持可区分：前者得到 nil，后者得到长度 0 的非 nil 切片——
// SaveNotificationChannel 正是靠 `c.Config == nil` 来区分「没传配置」与
// 「配置是空对象 {}」，两者落到库里必须不同。
func (v *jsonValue) Scan(src any) error {
	switch t := src.(type) {
	case nil:
		v.RawMessage = nil
	case []byte:
		v.RawMessage = bytes.Clone(t)
	case string:
		v.RawMessage = json.RawMessage(t)
	default:
		return fmt.Errorf("JSON 列不是文本也不是字节，而是 %T", src)
	}
	return nil
}

// Value 实现 driver.Valuer，写库时固定交 []byte。
//
// 固定字节而不是字符串，是为了让两个引擎落到同一种归档内容：PG 侧 pgx 收到
// []byte 会按 JSON 文本解析后写进 JSONB 列，与改动前传 `string(c.Config)`
// 得到的值完全一致（PG 对 JSONB 参数一律先按 JSON 文本解析，字节还是字符串
// 只影响传输编码，不影响存进去的 JSON 值）；SQLite 侧驱动把 []byte 原样写进
// TEXT 列。
//
// SQL NULL 走 nil，好让「没配过」与「配了空对象 {}」在库里继续可区分。
func (v jsonValue) Value() (driver.Value, error) {
	if v.RawMessage == nil {
		return nil, nil
	}
	return []byte(v.RawMessage), nil
}

// MarshalJSON 把落点内容还原成 JSON 值。
//
// 判定用 json.Valid：内容本身就是一份 JSON 就**原样内联**，否则当成普通字符串
// 加引号输出。
//
// 为什么必须这么判，而不是无条件内联：PostgreSQL 侧这些列是 JSONB，pgx 交出来的
// 是「已经解析过的 JSON 值」。如果这里把内容降级成字符串，通知归档里同一个字段
// 就会从 {"a":1} 变成 "{\"a\":1}"——归档格式在两个引擎之间分叉，而分叉的是
// **已经落库的历史数据**，事后没法对齐。
//
// 为什么不用 jsontext.Value 自带的 MarshalJSON：它是按 v1 json.RawMessage 的行为
// 「原样吐出、完全不校验」实现的（见 GOROOT jsontext/value.go:234 的注释
// "It performs no validation"）。内容一旦不是合法 JSON，编码器会把非法字节直接
// 拼进响应，产出**整份响应都不合法**的输出，而且往往要到最外层才报错，定位成本
// 很高。
//
// ── 误判面（有意接受） ───────────────────────────────────────────────────────
//
// 判定条件收紧成「整段内容（去掉首尾空白后）本身就是合法 JSON」才内联，于是：
//
//   - 内容是 JSON（对象/数组/数字/字符串/布尔/null）→ 原样内联，与 PG 行为一致；
//   - 内容是裸文本、半截 JSON、带尾随垃圾 → 当普通字符串输出，不会污染外层文档。
//
// 代价是「内容恰好长得像 JSON 的普通文本」会被内联：比如某列存的字面文本
// `[1,2]`，输出的是数组 1、2 而不是字符串 "[1,2]"；`123` 会输出数字而不是
// `"123"`。
//
// 对本文件服务的列（config / filter / snapshot）这个误判不成立：它们存的**始终**
// 是 json.Marshal 产生的 JSON 对象，写入路径见 SaveNotificationChannel 的
// `json.RawMessage(`{}`)` 兜底与 RecordNotificationEventTx 的 json.Marshal(snap)，
// 不存在「文本恰好像 JSON」的输入。
// 真正会中招的是将来把它复用到自由文本列的场景——那种场景的正确解法是换一个
// 类型（比如 db/rowjson.go 里 jsonText 的做法），而不是把这里的判定放宽：放宽会把
// 半截 JSON 也内联，那才是真正的数据损坏。
func (v jsonValue) MarshalJSON() ([]byte, error) {
	raw := bytes.TrimSpace(v.RawMessage)
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	if json.Valid(raw) {
		return raw, nil
	}
	return json.Marshal(string(v.RawMessage))
}

// UnmarshalJSON 存下一份原始 JSON 文本，并校验它确实合法。
//
// 校验是刻意加的：jsontext.Value 自带的 UnmarshalJSON 是「原样存、不校验」
// （见 GOROOT jsontext/value.go:244），非法输入会一路带进数据库，等渲染推送
// 消息时才炸，那时已经无法判断是「谁写进去的」。在这里失败，报错点离现场最近。
//
// 字节同样复制一份：jsontext.Value 的实现是 `*v = append((*v)[:0], b...)`，
// 会复用调用方切片（乃至结构体里已有值的底层数组）的容量。
func (v *jsonValue) UnmarshalJSON(b []byte) error {
	if v == nil {
		return errors.New("db.jsonValue: UnmarshalJSON 作用在 nil 指针上")
	}
	if !json.Valid(b) {
		return fmt.Errorf("不是合法的 JSON：%s", briefJSON(b))
	}
	v.RawMessage = bytes.Clone(b)
	return nil
}

// Raw 取回内容，交给结构体字段（字段类型仍是 json.RawMessage）。
func (v jsonValue) Raw() json.RawMessage { return v.RawMessage }

// briefJSON 给「这不是合法 JSON」这类错误截一段可读的前缀，避免把整条响应体
// 或整段凭据塞进日志。按字节切，可能落在半个 UTF-8 字符上——错误信息而已。
func briefJSON(b []byte) string {
	const max = 120
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…（已截断）"
}
