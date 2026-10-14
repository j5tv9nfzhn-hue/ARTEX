package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/config"

	"modernc.org/sqlite"
)

// =====================================================================
// 数组列方言层（PostgreSQL 数组 ↔ SQLite 的 TEXT/JSON 数组）
//
// ---------------------------------------------------------------------
// 背景：这些列在两个引擎里的物理形态完全不同
// ---------------------------------------------------------------------
//
// PostgreSQL（db/schema.sql）里它们是真数组：
//
//	task_ids      BIGINT[]
//	bound_domains TEXT[]
//	open_ports    TEXT[]      -- 元素其实是 jsonb 对象 {"port":22,"service":"ssh"}
//	record_value  TEXT[]
//	technologies  TEXT[]
//	auth          TEXT[]      -- 元素是 jsonb 对象
//	params        TEXT[]      -- 元素是 jsonb 对象
//	findings.asset_ids  jsonb
//
// SQLite（db/schema_sqlite.sql）里同一批列是普通标量列，数组语义全部下沉到 Go：
//
//	task_ids        BIGINT[] NOT NULL DEFAULT '{}'   -- SQLite 按列亲和性规则取 INTEGER
//	bound_domains   TEXT[]  NOT NULL DEFAULT '{}'
//	open_ports      TEXT[]  NOT NULL DEFAULT '{}'
//	record_value    TEXT[]
//	technologies    TEXT[]  NOT NULL DEFAULT '{}'
//	auth            TEXT[]  NOT NULL DEFAULT '{}'
//	params          TEXT[]  NOT NULL DEFAULT '{}'
//	asset_ids       TEXT    NOT NULL DEFAULT '[]'
//
// 注意两点，它们决定了下面所有辅助函数的写法：
//
//  1. SQLite 认不出 "BIGINT[]" / "TEXT[]" 这种类型名，但**列亲和性**规则仍然
//     生效：类型名里含 "INT" 的取 INTEGER 亲和性，含 "TEXT" 的取 TEXT 亲和性。
//     所以这些列在 SQLite 里就是普通的 INTEGER / TEXT 列，里面放什么由写入方决定。
//
//  2. 列的 DEFAULT 仍然是 PostgreSQL 的空数组字面量 `'{}'`，而 `{}` **不是合法
//     JSON**。凡是打算用 json_each() / json_valid() 打开这些列的地方，都必须先
//     过一层归一化，否则一条 schema 默认值就会让整条语句报 malformed JSON。
//     jsonArrayOf() 就是这层归一化。
//
// ---------------------------------------------------------------------
// 这个文件收口什么
// ---------------------------------------------------------------------
//
// 三类 PostgreSQL 专属语法，各自有 SQLite 等价物，全部集中在这里：
//
//	A. 写入数组     $6::bigint[] / $3::text[] / ARRAY[$1::jsonb]
//	               → Go 侧直接产出 JSON 数组文本（encodeTextArray / encodeJSONBArray
//	                 / encodeTaskIDArray），SQL 里去掉 ::type[] 转换，参数原样绑定。
//	               参数值仍然走绑定，没有任何 SQL 拼接。
//
//	B. 包含判断     $1 = ANY(task_ids) / col = ANY($1::text[]) / col @> ARRAY[x]
//	               → artex_json_contains(col, x)：等价于
//	                 EXISTS (SELECT 1 FROM json_each(col) WHERE value = x)。
//	               这里没有把它写成 SQL 片段再 fmt.Sprintf 拼占位符，而是注册成标量
//	               函数，原因见下面的"为什么用函数而不是 SQL 片段"。
//
//	C. 展开         unnest(a || b) / ARRAY(SELECT DISTINCT unnest(...))
//	               → artex_json_merge(a, b)：先 concat 再按首现去重。
//	               → artex_json_merge_ports(a, b)：open_ports 的 DISTINCT ON (端口)
//	                 + ORDER BY 端口/服务名 的等价物。
//	               → artex_json_merge_params(a, b)：params 的
//	                 DISTINCT ON (location, name) + ORDER BY elem::text DESC 的等价物。
//	               → artex_json_add / artex_json_remove：array_append / array_remove。
//
//	D. 量长度       cardinality(x) / array_length(x, d)
//	               → 同名注册（见下面"为什么用同名注册而不是 artex_json_* 前缀"）。
//
// 读出数组列 array_to_json(x)::text → array_to_json(x)：
//
//	PostgreSQL 侧的 array_to_json(anyarray) 直接返回 JSON 文本，::text 只是为了让
//	pgx 把 json 映射成 []byte 给 scanAssets 的 json.Unmarshal；去掉 ::text 不改变
//	读到的字节（database/sql 会把 string 也能塞进 *[]byte）。
//	SQLite 侧注册同名函数 array_to_json，语义就是"归一化后原样吐回"，于是这一列
//	投影在两个引擎上字面相同，不需要方言分支，asset_dsl.go 也就不用改。
//
// ---------------------------------------------------------------------
// 为什么用标量函数而不是 `EXISTS (SELECT 1 FROM json_each(col) ...)` 片段
// ---------------------------------------------------------------------
//
// 语义正确的写法当然可以是 SQL 片段，但这里有三处它做不到等价：
//
//  1. assets 的六个数组列里，open_ports / auth / params 的元素是 **jsonb 对象**。
//     去重要按"整个对象相等"（auth）、"对象某个键相等"（ports、params）来做，而且
//     DISTINCT ON 必须带 ORDER BY 才能确定保留哪一个。拼成 SQL 片段需要把
//     (elem->>'port')::int、elem::text 这些 PostgreSQL 专有表达式一并翻译过去，
//     翻译出来的片段比原语句更难读，也更难证明等价。
//  2. task_ids 的历史值可能仍是 PostgreSQL 数组字面量（`'{}'` 是 schema 默认值，
//     旧版本写进去的是 `'{5}'`）。SQL 片段需要额外写一层 CASE WHEN json_valid
//     来兜底，否则老数据直接 malformed JSON。函数里可以统一处理。
//  3. 参数绑定。函数把"值"当普通实参收，调用处永远只写 $N，不存在把值拼进 SQL
//     文本的机会。任务里明确要求不得产生注入，这里天然满足。
//
// ---------------------------------------------------------------------
// 根因记录：数组"没落库"其实是量长度的函数缺失（cardinality / array_length）
// ---------------------------------------------------------------------
//
// 现象：assets 的数组合并路径看起来整体失效——追加的 auth / params / open_ports /
// bound_domains / technologies 一律读到 0 条。注意形态是 **"空" 而不是 "少一个"**。
//
// 实测（本机 SQLite + modernc.org/sqlite，逐条 dump 原始列值并重跑测试里的原始 SQL）：
//
//	写入侧完全正确。连续两次 UpsertIP 之后列里存的是
//	    open_ports = [{"port":22,...},{"port":80,...},{"port":443,...}]
//	    bound_domains = ["a.example.com","b.example.com"]
//	    task_ids = [1,2]
//	UpsertHTTPService 之后 technologies = ["nginx","vue","react","webpack"]、
//	auth = 2 个元素、params = 2 个元素；走 Go 的 QueryByType 读回来同样正确。
//	也就是说 artex_json_merge / artex_json_merge_ports / artex_json_merge_params
//	以及 encodeTextArray / encodeJSONBArray / encodeTaskIDArray 这一整层都是对的。
//
// 真正断掉的是**读侧**：这些数组列的长度是用 PostgreSQL 的
//
//	SELECT cardinality(auth)            FROM assets WHERE id = $1
//	SELECT array_length(technologies,1) FROM assets WHERE id = $1
//
// 量出来的，而 SQLite 根本没有 cardinality / array_length 这两个函数，
//	驱动直接回 `SQL logic error: no such function: cardinality`，
//	调用方 Scan 拿到 error（多数地方没判），目标变量停在零值，于是打印成 "got 0"。
//	换句话说 "got 0" 是**查询失败**的假象，不是"合并算出了空集"。
//
// 修法：在本文件注册同名标量函数（见 init 里那段注释与 jsonCardinalityFunc /
// jsonArrayLengthFunc）。之所以不加 artex_ 前缀、而是让调用 SQL 保持
// `cardinality(col)` / `array_length(col, 1)` 字面不变——
//
//  1. 这两个名字在 SQLite 里完全没有内建，不会遮蔽任何既有函数；不像
//     json_array_length 那样和内建同名、语义还不同。
//  2. 调用方（诊断 SQL、测试断言）本来写的就是 PG 写法，同名注册之后同一句话在
//     两个引擎上都成立，不需要在调用点做引擎分支，也就不存在"改了 PG 分支"的
//     可能——PG 分支一个字都不用动，线上行为完全不变。
//  3. 语义逐条对齐 PG：NULL 进 → NULL 出；cardinality 对空数组返回 0，
//     array_length 对空数组/越界维度返回 NULL（PG 就是 NULL，Scan 成 int 会
//     报 "converting NULL to int is unsupported"，与 PG 行为一致，不去掩盖）。
//
// 仍然存在、本文件管不到的一类残留：raw SQL 里的 PostgreSQL 类型转换 `::text`。
// SQLite 的分词器遇到标识符后面的 ':' 会直接报 unrecognized token，
// 属于整个迁移的共性问题（db 包里还有另外几处同样原因失败），
// 与数组合并无关，这里不动。
//
// ---------------------------------------------------------------------
// PostgreSQL 分支
// ---------------------------------------------------------------------
//
// 本文件只负责 SQLite 侧。PostgreSQL 分支在 db/assets.go、db/finding_assets.go、
// db/task_assets.go 里逐字保留原语句，包括 ARRAY(...)、unnest(...)、::bigint[]
// 这些写法——项目仍然支持 postgres，不能因为 SQLite 迁移把它们删掉。
// 判定用 config.Engine() == config.EnginePostgres，与 db/dialect_softdelete.go 一致。

// ---------------------------------------------------------------------
// 注册（init）
// ---------------------------------------------------------------------

// 函数名统一带 artex_ 前缀，三个注册名是例外：array_to_json、cardinality、array_length。
//
//	array_to_json：它必须是这个名字才能让 assetSelectCols 这段投影在两个引擎上
//	  字面相同（见本文件头部的说明）。
//	cardinality / array_length：它们是**纯读侧**的 PostgreSQL 数组函数，SQLite
//	  完全没有内建（这正是数组合并"看起来整体失效"的直接原因，见上面那段根因记录）。
//	  仍按原名注册，是为了让调用方的 `cardinality(col)` / `array_length(col, 1)`
//	  一句话在两个引擎上都成立，从而不必在调用点分引擎、PG 分支一个字都不用动。
//
// 注册时机与 db/sqlite_compat.go 的 regexp / now 一致：必须在第一次 sql.Open 之前，
// 因为 modernc.org/sqlite 是语句 prepare 时解析函数名的。放在 init() 里即可。
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("array_to_json", 1, jsonArrayTextFunc)
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_array", 1, jsonArrayTextFunc)

	// cardinality(col) / array_length(col, 1)：数组元素个数。PostgreSQL 独有，
	// SQLite 无内建；同名注册后调用 SQL 保持 PG 字面不变。
	sqlite.MustRegisterDeterministicScalarFunction("cardinality", 1, jsonCardinalityFunc)
	sqlite.MustRegisterDeterministicScalarFunction("array_length", 2, jsonArrayLengthFunc)

	// $1 = ANY(task_ids)、col = ANY($1::text[])、col @> ARRAY[$1]
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_contains", 2, jsonContainsFunc)
	// task_ids = ARRAY[$1]::bigint[]（逐元素、同长度、同顺序）
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_equals", 2, jsonEqualsFunc)
	// ARRAY(SELECT DISTINCT unnest(a || b))
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_merge", 2, jsonMergeFunc)
	// CASE WHEN $1=ANY(a) THEN a ELSE array_append(a,$1) END
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_add", 2, jsonAddFunc)
	// array_remove(a, $1)
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_remove", 2, jsonRemoveFunc)
	// open_ports 的 DISTINCT ON (端口) + ORDER BY 端口/服务名
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_merge_ports", 2, jsonMergePortsFunc)
	// params 的 DISTINCT ON (location, name) + ORDER BY elem::text DESC
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_merge_params", 2, jsonMergeParamsFunc)
}

// ---------------------------------------------------------------------
// 读出数组列
// ---------------------------------------------------------------------

// assetProjection 是 assets 表的宽投影，scanAssets 的列顺序与之严格对应。
// QueryByType / QueryByCompany / QueryByTask 与 assetSelectCols 共用它，避免
// 同一份列表散落五份、改漏一处。
//
// 三处调整：
//
//	array_to_json(x)::text → array_to_json(x)。
//	  PostgreSQL 侧的 array_to_json(anyarray) 本来就返回 JSON 文本，::text 只是
//	  为了让 pgx 把它交成 []byte；去掉转换读到的内容不变。SQLite 侧 array_to_json
//	  是本文件注册的同名函数，语义是"归一化后原样吐回"，于是这一行在两个引擎上
//	  字面完全相同，不需要方言分支——这也是 asset_dsl.go 里复用 assetSelectCols
//	  时不用跟着改的原因。
//	c_segment::text → CAST(c_segment AS TEXT)。SQLite 没有 `::` 运算符；而
//	  DSN 里的 _texttotime=1 会把 TIMESTAMP 列交成 time.Time，c_segment 在
//	  PostgreSQL 上是 inet、投影里要的是文本，两边都需要显式 CAST。
//	last_seen::text → CAST(last_seen AS TEXT)。同一个 _texttotime 问题：Asset
//	  结构体的 LastSeen 是 string，不 CAST 就会 Scan 失败。
const assetProjection = `SELECT id, type, company_id, array_to_json(task_ids),
       COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''),
       COALESCE(CAST(c_segment AS TEXT),''), port,
       COALESCE(icp,''), array_to_json(bound_domains), array_to_json(open_ports), COALESCE(record_type,''),
       array_to_json(record_value), COALESCE(bundle_id,''), COALESCE(app_name,''),
       COALESCE(category,''), COALESCE(app_description,''), COALESCE(app_icp,''),
       COALESCE(url,''), COALESCE(service_type,''), COALESCE(service_name,''),
       COALESCE(favicon_mmh3,''), status_code, content_length,
       COALESCE(page_title,''), array_to_json(technologies), array_to_json(auth),
       COALESCE(method,''), array_to_json(params), extra, CAST(last_seen AS TEXT)`

// assetsFromPrefix 供三处按条件过滤的读查询拼 FROM 子句。
const assetsFromPrefix = "\nFROM assets "

// assetContainsTaskID 表达 `$1 = ANY(task_ids)`。
//
// PostgreSQL 侧就是原句；SQLite 侧 artex_json_contains 是"元素集合里有没有这个值"
// 的等价物（EXISTS (SELECT 1 FROM json_each(task_ids) WHERE value = $1)），差别
// 只在于它是一个函数调用，因此没有任何把值拼进 SQL 的机会。
func assetContainsTaskID() string {
	if config.Engine() == config.EnginePostgres {
		return "$1 = ANY(task_ids)"
	}
	return "artex_json_contains(task_ids, $1)"
}

// assetContainsTaskIDAlias 是 assetContainsTaskID 的带表别名版本，
// 用于 hostsForTaskDeletion 里 `t.id = ANY(a.task_ids)` 这种跨表引用。
func assetContainsTaskIDAlias(alias string) string {
	if config.Engine() == config.EnginePostgres {
		return "t.id=ANY(" + alias + ".task_ids)"
	}
	return "artex_json_contains(" + alias + ".task_ids, t.id)"
}

// taskIDsAnyExpr 表达 `<左值> = ANY(<列>)`，其中 <列> 是某个数组列
// （通常是带表别名的 assets.task_ids）。
//
// PostgreSQL 侧就是原句。SQLite 侧换成 artex_json_contains(col, 左值)，
// 即"这个列的元素集合里有没有左值"，等价于
// EXISTS (SELECT 1 FROM json_each(col) WHERE value = 左值)。
// 左值始终是 $N 形式或表列名，函数调用把值当普通实参收，
// 任何地方都没有把值拼进 SQL 文本的机会。
func taskIDsAnyExpr(left, col string) string {
	if config.Engine() == config.EnginePostgres {
		return left + "=ANY(" + col + ")"
	}
	return "artex_json_contains(" + col + ", " + left + ")"
}

// ---------------------------------------------------------------------
// 写入侧：Go 产出 JSON 数组文本
// ---------------------------------------------------------------------

// encodeTextArray 把 []string 编码成该引擎认的数组列文本。
//
//	PostgreSQL: {"a","b"}  —— text[] 字面量（marshalStringArray）
//	SQLite:     ["a","b"]  —— JSON 数组
//
// 空集合分别编码成 "{}" 与 "[]"，与 schema 里两边的默认值形状一致。
func encodeTextArray(items []string) string {
	if config.Engine() == config.EnginePostgres {
		return marshalStringArray(items)
	}
	if len(items) == 0 {
		return "[]"
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// encodeJSONBArray 把 []map[string]any 编码成该引擎认的数组列文本。
//
//	PostgreSQL: {"{\"port\":22}"}  —— jsonb[] 字面量（marshalJSONBArray）
//	SQLite:     [{"port":22}]      —— JSON 数组
//
// PostgreSQL 分支逐字沿用原来的 marshalJSONBArray，保证线上行为不变；
// SQLite 分支就是标准 JSON 编码，元素结构与 Go 侧 struct tag 一致。
func encodeJSONBArray(items []map[string]any) (string, error) {
	if config.Engine() == config.EnginePostgres {
		return marshalJSONBArray(items)
	}
	if len(items) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// encodeTaskIDArray 编码 assets.task_ids 的写入值。
//
//	PostgreSQL: taskID>0 → "{5}"，否则 "{}"
//	SQLite:     taskID>0 → "[5]"，否则 "[]"
//
// 只编码一个元素，因为所有调用点都是 UpsertXxx(req.TaskID) 这种"挂到某个任务"。
func encodeTaskIDArray(taskID int64) string {
	if config.Engine() == config.EnginePostgres {
		if taskID > 0 {
			return fmt.Sprintf("{%d}", taskID)
		}
		return "{}"
	}
	if taskID > 0 {
		return fmt.Sprintf("[%d]", taskID)
	}
	return "[]"
}

// appendInPlaceholders 为 `col = ANY($1::text[])` / `col = ANY($1::bigint[])`
// 生成 IN (...) 占位符，并把值按同样的顺序追加进 args。
//
// 为什么不能继续用 ANY($1)：现代 SQLite 驱动无法把 []int64 / []string 这类切片当
// 成一个参数绑定（它只会变成 unsupported driver.Value 类型），而 ANY() 本身也根本
// 不是 SQLite 函数。展开成 IN (?,?,?) 是唯一既能被参数绑定、又能保持"集合相等"
// 语义的写法。占位符编号从 args 当前长度继续递增，与包里既有的 pageClause 约定一致。
func appendInPlaceholders(args *[]any, ids []int64) string {
	ph := make([]string, len(ids))
	for i, id := range ids {
		*args = append(*args, id)
		ph[i] = fmt.Sprintf("$%d", len(*args))
	}
	return strings.Join(ph, ",")
}

// appendInPlaceholdersStr 是 appendInPlaceholders 的 string 版本，
// 用于 `a.domain = ANY($1::text[])`。
func appendInPlaceholdersStr(args *[]any, vals []string) string {
	ph := make([]string, len(vals))
	for i, v := range vals {
		*args = append(*args, v)
		ph[i] = fmt.Sprintf("$%d", len(*args))
	}
	return strings.Join(ph, ",")
}

// ---------------------------------------------------------------------
// SQLite 侧标量函数实现
// ---------------------------------------------------------------------

// jsonArrayTextFunc 实现 array_to_json(x) / artex_json_array(x)。
//
// x 是数组列的原始文本，返回归一化后的 JSON 数组文本；永远不会是 NULL，
// 至少是 "[]"。PostgreSQL 侧 array_to_json(NULL) 返回 SQL NULL，scanAssets 用
// len(raw) > 0 判空，两边都得到"空集合"，只是 SQLite 这边顺带把 schema 默认的
// '{}' 修正成了 '[]'，让 json.Unmarshal 拿到的是它真正认识的形状。
func jsonArrayTextFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 {
		return "[]", nil
	}
	return jsonArrayText(jsonArrayOf(argString(args[0]))), nil
}

// jsonCardinalityFunc 实现 cardinality(x)：数组的元素个数。
//
// 对应 PostgreSQL 的 cardinality(anyarray)。逐条对齐 PG 的三条规则：
//
//	NULL 入参     → NULL。PG 的 cardinality 是 STRICT 的，这里显式判一次，
//	                而不是让 jsonArrayOf 把 NULL 变成"空数组"再回 0——
//	                那会把"这一行没值"和"这一行确实是空数组"混成同一个答案。
//	空数组       → 0。schema 默认值 '{}' 与 Go 侧写出的 '[]' 都归一到空集合，
//	                于是同样得到 0，与 PG 对 '{}'::text[] 的结果一致。
//	非数组标量   → 0（PG 这里是 "cannot get cardinality of a scalar" 的运行期错误）。
//	                这里刻意不报错：本函数是纯读侧的量长度助手，一个标量退化成 0
//	                比让整条查询炸掉好，也与本文件 artex_json_array_length 的取舍一致。
//
// 结果返回 int64，SQLite 里就是 INTEGER，与 PG 的 integer 返回类型一致，
// 直接 Scan 进 int / int64 都不需要额外转换。
func jsonCardinalityFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 || args[0] == nil {
		return nil, nil
	}
	return int64(len(jsonArrayOf(argString(args[0])))), nil
}

// jsonArrayLengthFunc 实现 array_length(x, dim)：指定维度的元素个数。
//
// 对应 PostgreSQL 的 array_length(anyarray, integer)。本项目的数组列一律是一维
// （task_ids / bound_domains / open_ports / technologies / auth / params），
// 所以只有 dim = 1 有意义，其余一律返回 NULL：
//
//	NULL 入参 → NULL（同上，STRICT）。
//	dim = 1 且非空 → 元素个数。
//	dim = 1 且空数组 → NULL。这不是缺陷而是 PG 的原样行为：PG 的 array_length 对
//	                空数组返回 NULL，所以 `Scan(&int)` 会拿到
//	                "converting NULL to int is unsupported"。调用方若不判这个
//	                error，目标变量停在零值，看上去就是 "got 0"——这恰恰是本文件
//	                顶部那段根因记录里的同一个坑。与其在这里把它粉饰成 0，让
//	                "列是空的"继续伪装成"查询失败"，不如和 PG 保持一致。
//	dim ≠ 1     → NULL：维度越界 PG 就是 NULL；PG 对 dim < 1 是报错，这里宽松返回
//	                NULL，同样是"读不出来"而不是"炸掉查询"。
func jsonArrayLengthFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 || args[0] == nil {
		return nil, nil
	}
	if argInt(args[1]) != 1 {
		return nil, nil
	}
	n := len(jsonArrayOf(argString(args[0])))
	if n == 0 {
		return nil, nil
	}
	return int64(n), nil
}

// jsonContainsFunc 实现 $1 = ANY(col) 与 col = ANY($1)。
//
// PG 侧两种写法在这里是同一个动作：col 的元素集合里是否存在一个等于目标标量的值。
// 逐元素比较用"类型标签 + 规范化字面量"（jsonScalarKey）而不是字符串相等，
// 这样 number 5 不会和 string "5" 互相误命中，跟 PG 的类型语义一致。
func jsonContainsFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return int64(0), nil
	}
	want := driverScalarKey(args[1])
	for _, el := range jsonArrayOf(argString(args[0])) {
		if jsonScalarKey(el) == want {
			return int64(1), nil
		}
	}
	return int64(0), nil
}

// jsonEqualsFunc 实现 task_ids = ARRAY[$1]::bigint[]。
//
// PG 的数组相等是"长度相同且逐元素相等"，顺序敏感，所以 ['[5]'] != ['[5','6]']。
func jsonEqualsFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return int64(0), nil
	}
	got := jsonArrayOf(argString(args[0]))
	want := driverIncoming(args[1])
	if len(got) != len(want) {
		return int64(0), nil
	}
	for i := range got {
		if jsonScalarKey(got[i]) != jsonScalarKey(want[i]) {
			return int64(0), nil
		}
	}
	return int64(1), nil
}

// jsonMergeFunc 实现 (SELECT ARRAY(SELECT DISTINCT unnest(a || b)))。
//
// 三步与 PG 一一对应：
//
//	a || b            → 顺序是"a 在前、b 在后"整体拼接
//	SELECT DISTINCT   → 按整元素相等去重，PG 保留每组第一次出现的行
//	ARRAY(...)        → 把剩下的行重新装回数组
//
// 注意 PG 这里的 `||` **不去重**，去重完全来自外面的 DISTINCT；两边都要留。
// auth 列去重的键是 elem::text（元素整体），也走这个函数。
func jsonMergeFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return "[]", nil
	}
	elems := append(jsonArrayOf(argString(args[0])), jsonIncoming(argString(args[1]))...)
	seen := make(map[string]struct{}, len(elems))
	out := make([]any, 0, len(elems))
	for _, el := range elems {
		key := jsonCanonicalText(el)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, el)
	}
	return jsonArrayText(out), nil
}

// jsonAddFunc 实现
//
//	CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END
//
// 即"没有才追加"。task_assets.go 的 AttachAssetsToTask 依赖这一点保证幂等。
func jsonAddFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return "[]", nil
	}
	cur := jsonArrayOf(argString(args[0]))
	incoming := driverIncoming(args[1])
	if len(incoming) == 0 {
		return jsonArrayText(cur), nil
	}
	for _, el := range cur {
		if jsonScalarKey(el) == jsonScalarKey(incoming[0]) {
			return jsonArrayText(cur), nil
		}
	}
	return jsonArrayText(append(cur, incoming[0])), nil
}

// jsonRemoveFunc 实现 array_remove(a, $1)。
//
// PG 的 array_remove 删掉**全部**匹配元素并保持其余元素的相对顺序，不是只删第一个。
func jsonRemoveFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return "[]", nil
	}
	cur := jsonArrayOf(argString(args[0]))
	incoming := driverIncoming(args[1])
	if len(incoming) == 0 {
		return jsonArrayText(cur), nil
	}
	drop := jsonScalarKey(incoming[0])
	out := make([]any, 0, len(cur))
	for _, el := range cur {
		if jsonScalarKey(el) == drop {
			continue
		}
		out = append(out, el)
	}
	return jsonArrayText(out), nil
}

// jsonMergePortsFunc 实现 assets.open_ports 的合并：
//
//	SELECT ARRAY(
//	    SELECT DISTINCT ON ((elem->>'port')::int) elem
//	    FROM unnest(assets.open_ports || EXCLUDED.open_ports) AS elem
//	    ORDER BY (elem->>'port')::int,
//	             CASE WHEN elem->>'service' IS NOT NULL AND elem->>'service' <> '' THEN 0 ELSE 1 END
//	)
//
// 语义拆开是：
//   - 一个端口只保留一条（键 = elem->>'port'）；
//   - 同端口冲突时保留 service 非空的那条（ORDER BY 里的 0/1 那一列）；
//   - 输出数组按端口号升序。
//
// SQLite 侧 port 可能是 JSON 数字（Go 写出来的），PG 侧 elem->>'port' 拿到的是
// 文本再 ::int。这里统一按文本取端口、再转整数排序，取不到或转不动的一律排到
// 最后（PG 里 NULL 在 ASC 里也是最后）。
func jsonMergePortsFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return "[]", nil
	}
	elems := append(jsonArrayOf(argString(args[0])), jsonIncoming(argString(args[1]))...)
	type entry struct {
		key    string
		num    int64
		hasNum bool
		seq    int
		el     any
	}
	best := make(map[string]*entry, len(elems))
	order := make([]string, 0, len(elems))
	for i, el := range elems {
		portText, _ := jsonStringField(el, "port")
		e := &entry{key: portText, seq: i, el: el}
		if num, err := strconv.ParseInt(portText, 10, 64); err == nil {
			e.num, e.hasNum = num, true
		}
		if prev, ok := best[portText]; ok {
			// 同端口：service 非空优先。PG 的 ORDER BY 是
			// (service 非空 -> 0, 否则 1) 升序，DISTINCT ON 取第一条，
			// 等价于"0 赢 1"。两个 tiebreak 相同时 PG 的行序不确定，
			// 这里沿用先出现的那条，与 merge 的"首现优先"保持一致。
			if portServiceRank(el) < portServiceRank(prev.el) {
				e.seq = i
				best[portText] = e
			}
		} else {
			best[portText] = e
			order = append(order, portText)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := best[order[i]], best[order[j]]
		if a.hasNum && b.hasNum {
			if a.num != b.num {
				return a.num < b.num
			}
			return a.seq < b.seq
		}
		if a.hasNum != b.hasNum {
			return a.hasNum // 端口号升序，NULL 在后
		}
		return a.key < b.key
	})
	out := make([]any, 0, len(order))
	for _, key := range order {
		out = append(out, best[key].el)
	}
	return jsonArrayText(out), nil
}

// portServiceRank 就是 PG 里那一列
// CASE WHEN service 非空 THEN 0 ELSE 1 END。数字小者胜。
func portServiceRank(el any) int {
	if svc, ok := jsonStringField(el, "service"); ok && svc != "" {
		return 0
	}
	return 1
}

// jsonMergeParamsFunc 实现 assets.params 的合并：
//
//	SELECT ARRAY(
//	    SELECT DISTINCT ON ((elem->>'location'), (elem->>'name')) elem
//	    FROM unnest(assets.params || EXCLUDED.params) AS elem
//	    ORDER BY (elem->>'location'), (elem->>'name'), elem::text DESC
//	)
//
// 语义拆开是：
//   - 键 = (location, name)；
//   - 同键冲突时保留 elem::text **最大**的那条（DESC）；
//   - 输出数组按 location、name 升序，NULL 排最后（PG 的 ASC 默认）。
func jsonMergeParamsFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return "[]", nil
	}
	elems := append(jsonArrayOf(argString(args[0])), jsonIncoming(argString(args[1]))...)
	type key struct{ location, name string }
	type entry struct {
		key         key
		hasLocation bool
		hasName     bool
		text        string
		el          any
	}
	best := make(map[key]*entry, len(elems))
	for _, el := range elems {
		loc, hasLoc := jsonStringField(el, "location")
		name, hasName := jsonStringField(el, "name")
		k := key{location: loc, name: name}
		cand := &entry{
			key: k, hasLocation: hasLoc, hasName: hasName,
			text: jsonCanonicalText(el), el: el,
		}
		prev, ok := best[k]
		if !ok || cand.text > prev.text {
			best[k] = cand
		}
	}
	out := make([]*entry, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.hasLocation != b.hasLocation {
			return a.hasLocation // NULL 最后
		}
		if a.key.location != b.key.location {
			return a.key.location < b.key.location
		}
		if a.hasName != b.hasName {
			return a.hasName // NULL 最后
		}
		return a.key.name < b.key.name
	})
	res := make([]any, 0, len(out))
	for _, e := range out {
		res = append(res, e.el)
	}
	return jsonArrayText(res), nil
}

// ---------------------------------------------------------------------
// 归一化与比较的公共零件
// ---------------------------------------------------------------------

// jsonArrayOf 把一列的原始文本解析成 []any，并且**永远返回一个可解析的数组**：
//
//   - 合法 JSON 数组 → 原样；
//   - PostgreSQL 数组字面量 `{...}`（含 schema 默认值 '{}' 与旧版本写入的 '{5}'）
//     → 按字面量规则切开再逐元素解码；
//   - 其它（NULL、空串、损坏文本）→ 空数组。
//
// 这一层是"schema 默认值 '{}' 不是合法 JSON"的直接解药：没有它，任何
// json_each(col) / json_valid(col) 都可能被一条默认值打挂。
func jsonArrayOf(raw string) []any {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if v, err := decodeJSONAny(s); err == nil {
		if arr, ok := v.([]any); ok {
			return arr
		}
		return nil // 合法 JSON 但不是数组（如 "null" / 5），按空集合处理
	}
	return parsePGArrayLiteral(s)
}

// driverIncoming 解释**绑定参数**形式的"新来的那一坨"，是 jsonIncoming 的入口版。
//
// 必须单独写一个入口：argString 只认 string / []byte，int64 任务 id 经它一手会变成
// 空串，array_remove 就成了"删一个空值"，什么也不删。所以这里先按 driver.Value 的
// 真实类型分派：
//
//	nil          → 空数组
//	int64/float64/bool → 单元素数组（对应 PG 的 ARRAY[$1::bigint] 等）
//	string/[]byte      → 交给 jsonIncoming 按文本判断（JSON 数组 / JSON 标量 /
//	                     PG 数组字面量 / 纯文本单元素）
func driverIncoming(v driver.Value) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case int64, float64, bool:
		return []any{driverScalarValue(v)}
	case []byte:
		return jsonIncoming(string(t))
	case string:
		return jsonIncoming(t)
	default:
		return jsonIncoming(fmt.Sprint(v))
	}
}

// driverScalarValue 把绑定参数还原成 JSON 值，让它能和数组里的元素按同一种
// jsonScalarKey 比较。
func driverScalarValue(v driver.Value) any {
	switch t := v.(type) {
	case nil:
		return nil
	case int64:
		return json.Number(strconv.FormatInt(t, 10))
	case float64:
		return json.Number(normalizeNumberText(strconv.FormatFloat(t, 'g', -1, 64)))
	case bool:
		return t
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

// jsonIncoming 解释"新来的那一坨"，用来替代 PG 的 ARRAY[$1::type]：
//
//   - 是合法 JSON 数组 → 整段当作数组（对应 EXCLUDED.xxx）；
//   - 是其它合法 JSON（对象/数字/字符串）→ 包成单元素数组（对应 ARRAY[$1::jsonb]）；
//   - 长得像 PG 数组字面量 → 按字面量切（对应 EXCLUDED.xxx 仍是 '{}' 的旧写法）；
//   - 其余（普通文本）→ 包成单元素字符串（对应 ARRAY[$1::text]）。
func jsonIncoming(raw string) []any {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if v, err := decodeJSONAny(s); err == nil {
		if arr, ok := v.([]any); ok {
			return arr
		}
		return []any{v}
	}
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		return parsePGArrayLiteral(s)
	}
	return []any{s}
}

// argInt 把绑定参数读成 int64，供 array_length(x, dim) 这类"维度号"实参使用。
//
// array_length 的第二参在 PG 声明是 integer，实际到手的 driver.Value 可能是
// int64（`array_length(col, 1)` 的字面量 1）、float64、[]byte 或 string
// （列名/表达式形式），所以四种都要认；认不出来返回 0，调用方据此判成
// "维度越界 → NULL"，而不是让整条查询失败。
func argInt(v driver.Value) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case []byte:
		n, err := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
		if err != nil {
			return 0
		}
		return n
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// decodeJSONAny 用 UseNumber 解码，这样 bigint 不会先被压成 float64 再丢掉精度。
func decodeJSONAny(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// parsePGArrayLiteral 解析 PostgreSQL 的数组字面量 `{a,b,"c,d"}`。
//
// 元素有三种形态，本项目只会产生前两种：
//
//	"字符串"      —— 双引号包裹，内部 \" 和 \\ 反斜杠转义（marshalStringArray 的输出）
//	{"k":1}       —— jsonb 对象，原样 JSON
//	123           —— 数字，原样 JSON
//
// 切分必须按括号深度 + 引号状态来做：jsonb 元素内部既可能有逗号也可能有引号。
func parsePGArrayLiteral(s string) []any {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}
	inner := s[1 : len(s)-1]
	parts := splitPGArrayElements(inner)
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		out = append(out, decodePGArrayElement(part))
	}
	return out
}

func splitPGArrayElements(inner string) []string {
	var parts []string
	depth := 0
	inQuote := false
	escaped := false
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inQuote:
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case inQuote:
			// 引号内部的括号与逗号都不是分隔符
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, inner[start:i])
			start = i + 1
		}
	}
	if start < len(inner) {
		parts = append(parts, inner[start:])
	}
	return parts
}

// decodePGArrayElement 把切出来的单个元素还原成 JSON 值。
//
// 双引号包裹的按 PG 字面量规则解转义；其余按标准 JSON 解。解不出来就退回原始
// 文本当字符串——宁可多留一个字符串元素，也不要在合并资产时静默丢数据。
func decodePGArrayElement(part string) any {
	if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
		inner := part[1 : len(part)-1]
		var sb strings.Builder
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
				switch inner[i] {
				case '"', '\\':
					sb.WriteByte(inner[i])
				case 'n':
					sb.WriteByte('\n')
				case 't':
					sb.WriteByte('\t')
				default:
					sb.WriteByte(inner[i])
				}
				continue
			}
			sb.WriteByte(inner[i])
		}
		return sb.String()
	}
	if v, err := decodeJSONAny(part); err == nil {
		return v
	}
	return part
}

// jsonCanonicalText 给元素算一个稳定表示，用作去重 / 取最大值的键。
//
// Go 的 json.Marshal 会把 map 的键按字典序输出，正好对应 PostgreSQL jsonb 的
// ::text 规范化形态（键有序、无多余空白），所以"两个元素的 elem::text 相等"
// 在这里就是"两个元素的规范化文本相等"。
func jsonCanonicalText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// jsonArrayText 统一出口：空集合输出 "[]"，绝不输出 "null"。
func jsonArrayText(vals []any) string {
	if len(vals) == 0 {
		return "[]"
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// jsonScalarKey 给 JSON 标量算比较键，带类型标签，保证数字 5 和字符串 "5"
// 不会互相误命中（PG 的 bigint[] 与 text[] 不会）。
func jsonScalarKey(v any) string {
	switch t := v.(type) {
	case nil:
		return "null:"
	case string:
		return "str:" + t
	case json.Number:
		return "num:" + normalizeNumberText(t.String())
	case float64:
		return "num:" + normalizeNumberText(strconv.FormatFloat(t, 'g', -1, 64))
	case bool:
		return "bool:" + strconv.FormatBool(t)
	default:
		return "json:" + jsonCanonicalText(t)
	}
}

// driverScalarKey 是 jsonScalarKey 的绑定参数版本。
func driverScalarKey(v driver.Value) string {
	switch t := v.(type) {
	case nil:
		return "null:"
	case int64:
		return "num:" + strconv.FormatInt(t, 10)
	case float64:
		return "num:" + normalizeNumberText(strconv.FormatFloat(t, 'g', -1, 64))
	case bool:
		return "bool:" + strconv.FormatBool(t)
	case []byte:
		return "str:" + string(t)
	case string:
		return "str:" + t
	default:
		return "str:" + fmt.Sprint(v)
	}
}

// normalizeNumberText 把 5.0 / 5e0 / 5 统一成 "5"，避免同一数值两种文本。
func normalizeNumberText(s string) string {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(i, 10)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// jsonStringField 取对象元素的某个字段并渲染成 PG 里 ->>'k' 会得到的文本。
// 第二个返回值区分"字段不存在 / 为 null"和"字段是空字符串"，DISTINCT ON 的
// 键与 ORDER BY 的 NULL 语义都依赖这个区分。
func jsonStringField(el any, field string) (string, bool) {
	m, ok := el.(map[string]any)
	if !ok {
		return "", false
	}
	v, present := m[field]
	if !present || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case float64:
		return normalizeNumberText(strconv.FormatFloat(t, 'g', -1, 64)), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return jsonCanonicalText(t), true
	}
}
