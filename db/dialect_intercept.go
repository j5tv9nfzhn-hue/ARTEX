package db

import "encoding/json"

// 本文件收口「审批链 audit 列」在两种引擎下的方言片段。
//
// ---------------------------------------------------------------------------
// 背景：audit 列在两边不是同一种类型
// ---------------------------------------------------------------------------
// PostgreSQL 侧 intercept_pending.audit 是 JSONB 列；迁移后的 SQLite schema 把
// JSONB 一律翻译成 TEXT，列里存的是同一份 JSON 文本（schema_sqlite.sql 里就是
// `audit TEXT`）。于是同一段业务逻辑要跨过三道语法差异：
//
//	PG 写法                    SQLite 结果                      本文件的替代方案
//	-----------------------------------------------------------------------------
//	$3::jsonb（参数位 ::type）  unrecognized token: ":"        占位符裸写成 $3
//	audit->>'k'（取键为文本）   unrecognized token: ":"        CAST(json_extract(audit,'$.k') AS TEXT)
//	audit || $3::jsonb（合并）  不报错，但结果是非法 JSON     json_patch(audit, $3)
//
// 第三行是最危险的一个：SQLite 的 `||` 是字符串拼接，`a || b` 会把两个 JSON 文档
// 首尾相接成 `{...}{...}`，语句照样执行成功、写库照样成功，直到 Go 侧 json.Unmarshal
// 才炸。静默写坏数据，所以必须显式换掉。
//
// ---------------------------------------------------------------------------
// 两条贯穿全文件的规则
// ---------------------------------------------------------------------------
//
// 1. 「参数位的 ::type 直接删」只对 SQLite 成立，对 PostgreSQL 不成立。
//    SQLite 的 $N 是纯位置参数，$3::jsonb 会被整段当成名为 "3::jsonb" 的参数名
//    而报 unrecognized token；而值类型本来就由 Go 侧绑定决定，删掉无语义损失。
//    但 PG 反过来：jsonb || $3 中 $3 的类型未知，删掉 ::jsonb 会报
//    "operator is not unique: unknown || jsonb"。
//    => 因此本文件的所有方言切换都做在**语句级分支**上：PG 语句在调用点逐字保留，
//       SQLite 语句在这里单写一份。两边各自可读，不靠拼接生成。
//
// 2. 参数值一律绑定，绝不拼进 SQL。本文件里的 $1..$6 全部是占位符，
//    连两段固定的 JSON 补丁也由 Go 侧构造后绑定（见 interceptPatch*）。
//
// ---------------------------------------------------------------------------
// json_patch 与 jsonb 的 `||` 为什么可以互换（本文件唯一的语义替换）
// ---------------------------------------------------------------------------
// 目标：audit 的「合并补丁」语义，即用补丁的顶层键覆盖 audit 的顶层键。
//
//	PG     audit || patch
//	SQLite json_patch(audit, patch)
//
// 两处差异，都已逐条核对过 audit 的实际写入方：
//
//	a) 嵌套对象。jsonb 的 `||` 对嵌套对象是递归合并（{"o":{"a":1,"b":2}} || {"o":{"b":3}}
//	    得到 {"o":{"a":1,"b":3}}）；json_patch 是 RFC 7396，整体替换（得到 {"o":{"b":3}}）。
//	    audit 的补丁由 ResolveIntercept / CompleteIntercept 用 json.Marshal 构造，
//	    字段全是顶层标量（字符串、布尔），Context 等嵌套字段只在**插入**时整体写入，
//	    从不被补丁局部修改，两边结果一致。
//
//	b) null 值。json_patch 视 null 为「删除该键」（RFC 7396）；jsonb 的 `||` 会把
//	    null 存下来。同样因为补丁只含字符串/布尔，永远不出现 null，两边一致。
//
// 另外两个边界：
//   - 输入非法 JSON 时 json_patch 报 malformed JSON，PG 的 jsonb 也会拒绝同样的输入，
//     失败模式一致。audit 的全部写入方都是 json.Marshal，列里非 NULL 即合法 JSON。
//   - 输入为 NULL 时两边都返回 NULL（PG: NULL || x = NULL），已实测确认。
//
// 唯一残留风险：如果将来出现绕过 json.Marshal 直接写 audit 列的代码，json_patch 会
// 比 PG 更早报错。这属于「快速失败」，比写入非法 JSON 安全，登记在此备查。

// interceptPatchExecutionUnknown / interceptPatchEmptyObject 是 PostgreSQL 语句里
// 两段 jsonb 字面量在 SQLite 侧的等价物。
//
// PG 原写法：
//
//	THEN '{"execution_status":"unknown"}'::jsonb ELSE '{}'::jsonb END
//
// 改法：字面量本身是常量、与任何用户输入无关，所以**在 Go 侧构造**成 JSON 文本后
// 当普通参数绑定，而不是塞进 SQL，也不交给 SQLite 的 json_object/json() 现场拼。
// 理由见文件头规则 2：SQLite 的 JSON 函数与 PG 的细节差异多（键序、类型推断、
// 非法输入的处理都不同），能让 Go 侧算完的部分就不交给 SQL。
// 用 json.Marshal 而不是手写字面量，是为了让「这块文本一定是合法 JSON」由编译器
// 无法保证、但由这段代码的构造方式保证。
var (
	interceptPatchExecutionUnknown = mustInterceptJSONPatch(map[string]any{"execution_status": "unknown"})
	interceptPatchEmptyObject      = mustInterceptJSONPatch(map[string]any{})
)

// mustInterceptJSONPatch 把补丁值序列化成 JSON 文本。
//
// 这里只接受 map[string]any 的标量字段，序列化必然成功；一旦将来有人传入含
// 不可序列化值的结构，init 期 panic 比运行期拼出坏 JSON 更早暴露问题。
func mustInterceptJSONPatch(v map[string]any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("intercept audit patch: " + err.Error())
	}
	return string(raw)
}

// interceptResolveAuditSQLite 是 ResolveIntercept 的 SQLite 版本。
//
// 逐处对照 PostgreSQL 原语句：
//
//	PG: audit = CASE WHEN audit IS NULL THEN NULL
//	                 ELSE audit || $3::jsonb ||
//	                      CASE WHEN $4='allow' AND audit->>'correlation' IS DISTINCT FROM 'exact'
//	                           THEN '{"execution_status":"unknown"}'::jsonb
//	                           ELSE '{}'::jsonb END
//	                 END
//
//	（1）audit IS NULL THEN NULL —— 原样保留。PG 的 NULL || x 结果是 NULL，
//	    json_patch(NULL, x) 同样是 NULL，两边都保住「没有快照就不凭空造快照」。
//	（2）audit || $3::jsonb || CASE... -> json_patch(json_patch(audit, $3), CASE...)
//	    两段合并合成嵌套两层，顺序与 PG 的左结合完全一致。
//	（3）audit->>'correlation' -> CAST(json_extract(audit,'$.correlation') AS TEXT)
//	    `->>` 取出来一定是文本；json_extract 对 JSON 字符串返回文本，但对 JSON 数字
//	    返回整数（{"n":123} 取出来是 123 而不是 '123'），直接拿去和文本比较会因
//	    类型不同恒不成立，所以必须补一层 CAST 才等价。键不存在时 json_extract 返回
//	    NULL，CAST 后仍是 NULL，与 PG 一致。
//	（4）IS DISTINCT FROM 'exact' -> IS NOT 'exact'
//	    PG 的 IS DISTINCT FROM 是 NULL 安全的：「键缺失 => NULL IS DISTINCT FROM
//	    'exact' => TRUE」，这正是本判断想要的语义。SQLite 的 IS NOT 同样 NULL 安全
//	    （普通 = 遇 NULL 得 NULL，IS NOT 得 TRUE/非 NULL），已实测两条写法在
//	    「有值/不同值/键缺失」三种输入上结果完全相同。
//	（5）两段 jsonb 字面量 -> $5 / $6 绑定 interceptPatchExecutionUnknown /
//	    interceptPatchEmptyObject，见上方说明。
//	    其中 '{}' 的合并在两边都是恒等操作（PG: x || {} = x；SQLite: json_patch(x,'{}') = x）。
//	（6）$3 的 ::jsonb 直接删除，见文件头规则 1。
//
// 注意 WHERE 条件和 status/decided_at 的写法两边完全一致，settle 的原子性
// （id + status='pending' 一次 UPDATE 决出唯一胜者）没有被方言分支削弱。
var interceptResolveAuditSQLite = `UPDATE intercept_pending SET status=$2, decided_at=NOW(),
	audit=CASE WHEN audit IS NULL THEN NULL
	           ELSE json_patch(json_patch(audit,$3),
	                CASE WHEN $4='allow' AND CAST(json_extract(audit,'$.correlation') AS TEXT) IS NOT 'exact'
	                     THEN $5 ELSE $6 END)
	           END
	WHERE id=$1 AND status='pending'`

// interceptCompleteAuditSQLite 是 CompleteIntercept 的 SQLite 版本。
//
// 逐处对照 PostgreSQL 原语句：
//
//	PG: SET audit = audit || $4::jsonb
//	    WHERE id=$1 AND audit->>'run_id'=$2 AND audit->>'tool_use_id'=$3
//	      AND audit->>'effective_action'='allow' AND audit->>'execution_status'='awaiting_result'
//
//	（1）audit || $4::jsonb -> json_patch(audit, $4)，理由同上。
//	    NULL 行在这里不可能被更新到：WHERE 里第一个 audit->> 判断就要求 run_id 非 NULL，
//	    json_extract 语义与 `->>` 一致（NULL 时该条件不成立，行不入选），
//	    所以 json_patch 的 NULL 输入分支根本走不到。
//	（2）四个 audit->>'k' -> CAST(json_extract(audit,'$.k') AS TEXT)，理由同上。
//	    比较目标都是 Go 侧的字符串 runID / toolUseID 与字面量 'allow' /
//	    'awaiting_result'，CAST 后与 PG 的 `->>` 文本比较逐字符对齐。
//	    这里 cast 不是可选项：若 audit 里的 run_id 恰好是 JSON 数字，
//	    json_extract 返回整数，而 $2 是文本参数，SQLite 不做跨类型比较，
//	    该行会被静默漏掉——正是本方法要防的「把结果挂到别的调用上」事故。
//	（3）$4 的 ::jsonb 直接删除。
//
// 四道守卫条件是本方法的安全边界（只更新那一次被记录在案的调用），方言改写后
// 逐条保留，一条不少。
var interceptCompleteAuditSQLite = `UPDATE intercept_pending SET audit=json_patch(audit,$4)
	WHERE id=$1 AND CAST(json_extract(audit,'$.run_id') AS TEXT)=$2
	  AND CAST(json_extract(audit,'$.tool_use_id') AS TEXT)=$3
	  AND CAST(json_extract(audit,'$.effective_action') AS TEXT)='allow'
	  AND CAST(json_extract(audit,'$.execution_status') AS TEXT)='awaiting_result'`

// interceptConversationActivitiesSQLite 是 GetInterceptExecution 在
// conversation 作用域下的 SQLite 版本。
//
// 逐处对照 PostgreSQL 原语句：
//
//	PG: SELECT id, NULL::bigint, COALESCE(worker,''), kind, COALESCE(tool,''), tool_use_id,
//	           is_error, COALESCE(summary,''), created_at, NULL::integer
//	    FROM conversation_activities WHERE conversation_id=$1
//
//	（1）NULL::bigint / NULL::integer -> CAST(NULL AS INTEGER)
//	    这两处是**表达式位**的 ::type（不是占位符），SQLite 直接报 unrecognized token。
//	    它们的作用只是给 node_id / main_seg 两个可空列声明类型，让扫描端分别落进
//	    *int64 / *int。CAST(NULL AS INTEGER) 在 SQLite 里同样是「带类型的 NULL」，
//	    database/sql 扫到 SQL NULL 时仍会把 *int64 / *int 置 nil，语义与 PG 一致。
//	    conversation_activities 这张表没有 node_id / main_seg 两列，所以补 NULL 而不是
//	    真去查列——这点两边相同，未改动。
//	（2）其余列、COALESCE、WHERE 完全一致，未改动。
//	    本语句不含 JSONB、不含 ::、不含行锁或数组操作，不存在其它方言点。
var interceptConversationActivitiesSQLite = `SELECT id, CAST(NULL AS INTEGER), COALESCE(worker,''), kind, COALESCE(tool,''), tool_use_id, is_error, COALESCE(summary,''), created_at, CAST(NULL AS INTEGER) FROM conversation_activities WHERE conversation_id=$1`
