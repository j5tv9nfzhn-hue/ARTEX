package db

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// =====================================================================
// 企业主体层的方言层（PostgreSQL inet/cidr ↔ SQLite TEXT）
//
// 本文件收口 db/companies.go 与 db/findings.go 里的三类 SQLite 报错：
//
//	1. unrecognized token ":"     —— 残留的 ::text / ::cidr / ::inet / ::jsonb
//	2. unrecognized token ":"     —— PostgreSQL 专属的 regexp_replace
//	3. near "ON"                  —— ON CONFLICT ON CONSTRAINT <name>
//	4. unrecognized token "="     —— net >>= ip（PostgreSQL 的网段包含运算符）
//	5. unrecognized token "@"     —— asset_ids @> ANY($n::jsonb[])
//	6. 语法不支持                  —— DISTINCT ON / UPDATE … FROM（用窗口函数改写）
//
// ---------------------------------------------------------------------
// 最要紧的一处：net >>= ip 到底在做什么
// ---------------------------------------------------------------------
//
// 它是 PostgreSQL 的「网段包含」运算符（inet_ops.c 的 network_contains），
// 语义等价于「把右边的地址按左边的网络做掩码后比前缀」，也就是「这个 IP 是否
// 落在这条网段规则里」。db/companies.go 有两处依赖它：
//
//	recomputeAttributionTx  重算归属时，网段规则按 masklen 从长到短择优
//	resolveCompanyWithICP   资产写入时同步解析归属
//
// SQLite 侧不能把它删掉——删掉之后「归属企业」对所有网段/IP 规则直接失效，
// TestCompanyScopeAttribution / TestResolveCompany / TestExplicitCompanyAttributionSurvivesScopeRebuild
// 都会失败。也不能靠 Go 侧预筛来顶替：重算语句要在一个事务里对整张 assets 表
// 做批量 UPDATE，把候选网段搬到 Go 侧会把归属判定拆成"读—判—写"三段。
//
// 因此这里在 SQLite 侧注册 artex_inet_contains(net, ip) 标量函数重实现它，
// 并逐条对齐 PostgreSQL 的判定顺序（见 inetContains 的注释）。
//
// ---------------------------------------------------------------------
// 为什么注册成函数而不是拼 SQL 片段
// ---------------------------------------------------------------------
//
// 与 db/dialect_arrays.go 的 artex_json_contains 同理：
//
//  1. 合法性判定必须复用 Go 的 net.ParseIP / net.ParseCIDR。写成
//     EXISTS(... json_each ...) / 字符串前缀比较之类的片段，等于把地址解析
//     在 SQLite 里重写一遍，而重写一遍就一定会和 Go 侧 ParseScopeLine 的
//     规范化分叉。
//  2. 非法输入必须返回"不匹配"而不是报错。assets.ip 是自由文本（Agent / 资产
//     API 可能写进主机名），一条脏数据不能把整条重算语句打挂——这正是
//     db/schema_sqlite.sql 里 try_inet 用 EXCEPTION 捕获 22P02 的原因。
//  3. 参数绑定：函数把值当普通实参收，调用点永远只写 $N。
//
// 函数名一律带 artex_ 前缀，与 dialect_arrays.go 的注册约定一致，也避免和
// 并行迁移任务注册的同名函数撞车（MustRegister* 对重名会 panic）。

// ---------------------------------------------------------------------
// 注册（init）
// ---------------------------------------------------------------------

func init() {
	// try_inet(value text) → inet
	sqlite.MustRegisterDeterministicScalarFunction("artex_try_inet", 1, artexTryInetFunc)
	// net >>= ip（网段包含）
	sqlite.MustRegisterDeterministicScalarFunction("artex_inet_contains", 2, artexInetContainsFunc)
	// masklen(net)
	sqlite.MustRegisterDeterministicScalarFunction("artex_inet_masklen", 1, artexInetMasklenFunc)
	// lower(regexp_replace(x, '[[:space:]]+', '', 'g'))
	sqlite.MustRegisterDeterministicScalarFunction("artex_icp_key", 1, artexICPKeyFunc)
	// jsonb_array_length(x)
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_array_length", 1, artexJSONArrayLengthFunc)
	// asset_ids @> ANY($n::jsonb[])
	sqlite.MustRegisterDeterministicScalarFunction("artex_json_contains_any", 2, artexJSONContainsAnyFunc)
}

// ---------------------------------------------------------------------
// 文本参数
// ---------------------------------------------------------------------

// dialectText 把绑定参数/列值读成文本。第二个返回值区分「NULL」与「空串」，
// 因为 PostgreSQL 侧的 try_inet 是 STRICT 的，NULL 必须原样变成 NULL。
//
// 与 sqlite_compat.go 的 argString 的差别：int64 不会被静默吞成空串。资产 id
// 之类的整型即使误传进来，这里也会给出它的十进制文本，随后由解析函数判非法。
func dialectText(v driver.Value) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, true
	case []byte:
		return string(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return fmt.Sprint(v), true
	}
}

// ---------------------------------------------------------------------
// inet / cidr 解析
// ---------------------------------------------------------------------

// parseInetText 解析一个「inet」形态的文本，返回地址与掩码位数。
//
// 对齐 PostgreSQL 的 `::inet` 语义：
//   - '10.0.0.1'         → 地址 10.0.0.1，掩码 32 位（inet 缺省按满位算，
//     与 masklen('10.0.0.1'::inet) = 32 一致）；
//   - '2001:db8::1'      → 掩码 128 位；
//   - '10.1.2.3/24'      → 保留主机位，掩码 24 位（inet 不像 cidr 会清零）；
//   - 'notanip'          → ok=false（PostgreSQL 抛 22P02，这里由调用方转成 NULL）。
func parseInetText(raw string) (net.IP, int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, 0, false
	}
	if strings.ContainsRune(s, '/') {
		ip, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			return nil, 0, false
		}
		ones, bits := ipnet.Mask.Size()
		if bits == 0 {
			return nil, 0, false
		}
		return normalizeIP(ip), ones, true
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, 0, false
	}
	v4 := ip.To4()
	if v4 != nil {
		return v4, 32, true
	}
	return ip.To16(), 128, true
}

// parseScopeNetText 解析 company_scope.net 一列。它在两个引擎里的物理类型是
// CIDR / TEXT(存文本)，内容由 ParseScopeLine 产出（要么 ipnet.String()，要么
// 单 IP 加 /32、/128），因此一定带掩码；这里对不带掩号的输入按满位兜底，
// 与 PostgreSQL 里 inet 的缺省行为保持一致。
func parseScopeNetText(raw string) (*net.IPNet, bool) {
	s := strings.TrimSpace(raw)
	if strings.ContainsRune(s, '/') {
		_, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			return nil, false
		}
		return ipnet, true
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, false
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, true
	}
	return &net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)}, true
}

// normalizeIP 把地址统一成与网络同宽的字节形式：IPv4（含 ::ffff: 开头的
// v4-mapped 形式）取 4 字节，其余取 16 字节。PostgreSQL 的 network_contains
// 也把 v4-mapped 的 IPv6 当 IPv4 处理，两边对「地址族不同」的判定因此一致。
func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip.To16()
}

// inetContains 是 `net >>= ip` 的等价实现，逐条对齐 PostgreSQL
// inet_ops.c 的 network_contains 的判定顺序：
//
//  1. 左操作数解析不出网段 → false。PG 侧 net 是 CIDR 列，走不到这里；
//     SQLite 侧 net 是自由文本，脏值按"不匹配"处理而不是报错。
//  2. 右操作数解析不出地址 → false。这正是 try_inet 的 EXCEPTION 语义：
//     assets.ip 是自由文本，一条 "api.example.com" 不能打挂整条重算语句。
//  3. 地址族不同（IPv4 网段 vs IPv6 地址，或反之）→ false。
//  4. 左操作数掩码位数 > 右操作数掩码位数 → false。右边若是 '10.0.0.0/8'
//     这种带主机位的 inet，PostgreSQL 只比前 8 位，所以比 /24 的网段还不匹配；
//     Go 的 IPNet.Contains 在这里会判 true，因此必须显式挡掉，不能直接用 Contains。
//  5. 否则把两边都按网段掩码清零后逐位比较 —— 这就是普通的「IP 落在网段内」。
//
// 返回值永远是 true/false，不返回 error：整个重算流程必须对脏数据免疫。
func inetContains(netText, ipText string) bool {
	ipnet, ok := parseScopeNetText(netText)
	if !ok {
		return false
	}
	ip, ipBits, ok := parseInetText(ipText)
	if !ok {
		return false
	}
	netIP := normalizeIP(ipnet.IP)
	ones, bits := ipnet.Mask.Size()
	if bits == 0 || len(netIP) != bits/8 {
		return false
	}
	if len(ip) != len(netIP) {
		return false // 地址族不同（IPv4 网段 vs IPv6 地址，或反之）
	}
	if ones > ipBits {
		return false
	}
	mask := ipnet.Mask
	return netIP.Mask(mask).Equal(ip.Mask(mask))
}

// ---------------------------------------------------------------------
// SQLite 侧标量函数实现
// ---------------------------------------------------------------------

// artexTryInetFunc 实现 artex_try_inet(value text)。
//
// 对应 db/schema_sqlite.sql:15 的
//
//	CREATE OR REPLACE FUNCTION try_inet(value text) RETURNS inet AS $$
//	BEGIN RETURN value::inet; EXCEPTION WHEN others THEN RETURN NULL; END;
//	$$ LANGUAGE plpgsql IMMUTABLE STRICT;
//
// STRICT ⇒ NULL 进 NULL 出；EXCEPTION ⇒ 非法文本返回 NULL 而不是抛 22P02。
// 返回值是规范化后的地址文本（本项目只拿它去 >>= 和判 IS NULL，文本本身
// 不进业务逻辑），形状与 PostgreSQL 的 inet 输出一致：无掩码时给纯地址，
// 有掩码时给 "addr/bits"。
func artexTryInetFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 {
		return nil, nil
	}
	s, ok := dialectText(args[0])
	if !ok {
		return nil, nil // STRICT
	}
	ip, bits, ok := parseInetText(s)
	if !ok {
		return nil, nil // EXCEPTION WHEN others THEN RETURN NULL
	}
	if strings.ContainsRune(strings.TrimSpace(s), '/') {
		return fmt.Sprintf("%s/%d", ip.String(), bits), nil
	}
	return ip.String(), nil
}

// artexInetContainsFunc 实现 artex_inet_contains(net, ip) → 0/1。
// 语义见 inetContains。
func artexInetContainsFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return int64(0), nil
	}
	netText, ok := dialectText(args[0])
	if !ok {
		return int64(0), nil // net IS NULL → NULL >>= x 是 NULL，连接条件不成立
	}
	ipText, ok := dialectText(args[1])
	if !ok {
		return int64(0), nil
	}
	if inetContains(netText, ipText) {
		return int64(1), nil
	}
	return int64(0), nil
}

// artexInetMasklenFunc 实现 artex_inet_masklen(net) → 前缀位数 / NULL。
// 对应 PostgreSQL 的 masklen(inet)，用于「更长（更窄）的网段优先」那条
// ORDER BY。解析不出网段时给 NULL，与 PG 的 masklen(NULL) 一致。
func artexInetMasklenFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 {
		return nil, nil
	}
	s, ok := dialectText(args[0])
	if !ok {
		return nil, nil
	}
	ipnet, ok := parseScopeNetText(s)
	if !ok {
		return nil, nil
	}
	ones, bits := ipnet.Mask.Size()
	if bits == 0 {
		return nil, nil
	}
	return int64(ones), nil
}

// artexICPKeyFunc 实现 artex_icp_key(x)。
//
// 对应 PostgreSQL 的
//
//	lower(regexp_replace(COALESCE(x,''), '[[:space:]]+', '', 'g'))
//
// SQLite 没有 regexp_replace。与其把 POSIX 字符类翻译成一段 SQL 片段再去猜
// 它和 Go 侧的正则行为差在哪，不如直接复用 db/company_scope.go 的 NormalizeICP：
// 规则值 cs.value 本来就是 NormalizeICP 写进去的（ParseScopeInput → icp 分支），
// 两边用同一个函数算 key 才能保证「存的」和「比的」严格同形。
//
// NormalizeICP 去的是全部 Unicode 空白字符（含 U+00A0、U+3000 这类
// PostgreSQL 的 [[:space:]] 在 C 语言 locale 下不认的），范围严格大于
// PostgreSQL 表达式，只会让匹配更宽松，不会把原本该匹配的判成不匹配。
func artexICPKeyFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 {
		return nil, nil
	}
	s, ok := dialectText(args[0])
	if !ok {
		return nil, nil
	}
	return NormalizeICP(s), nil
}

// artexJSONArrayLengthFunc 实现 artex_json_array_length(x)。
//
// 对应 PostgreSQL 的 jsonb_array_length(x)。走 jsonArrayOf 归一化，因此
// NULL、空串、'{}'（数组列的 schema 默认值）、PG 数组字面量 '{5}'
// 全部退化成 0，而不是像 SQLite 内建的 json_array_length 那样对 '{}'
// 返回 0、对损坏文本直接报 malformed JSON。
func artexJSONArrayLengthFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 1 {
		return int64(0), nil
	}
	return int64(len(jsonArrayOf(argString(args[0])))), nil
}

// artexJSONContainsAnyFunc 实现 artex_json_contains_any(col, idsJSON)。
//
// 对应 PostgreSQL 的
//
//	col @> ANY($n::jsonb[])
//
// 其中 $n 是 assetIDContainments 产出的 ["[1]","[2]",…] 数组：jsonb 数组的
// 包含判定是"元素集合相交"，@> '[1]' 等价于「1 是 col 的元素之一」，ANY 再
// 把它摊成"存在任一 id 命中"。
//
// 这里把右操作数从 PG 的 jsonb[] 换成一段 JSON 数组文本（Go 侧用 jsonArrayText
// 产出，永远合法），再逐元素比 jsonScalarKey——与 dialect_arrays.go 的
// artex_json_contains 用的是同一把尺子，因此数字 5 不会和字符串 "5" 互相误命中。
func artexJSONContainsAnyFunc(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return int64(0), nil
	}
	got := jsonArrayOf(argString(args[0]))
	if len(got) == 0 {
		return int64(0), nil
	}
	for _, want := range jsonArrayOf(argString(args[1])) {
		key := jsonScalarKey(want)
		for _, el := range got {
			if jsonScalarKey(el) == key {
				return int64(1), nil
			}
		}
	}
	return int64(0), nil
}

// ---------------------------------------------------------------------
// SQL 片段助手
// ---------------------------------------------------------------------

// companyInetContains 表达 `<网段> >>= <地址>`。
//
//	PostgreSQL: net >>= $1::inet
//	SQLite:     artex_inet_contains(net, $1)
//
// 两个操作数都必须是「值」的表达式（列或 $N 占位符），实现见 inetContains。
func companyInetContains(netExpr, addrExpr string) string {
	return "artex_inet_contains(" + netExpr + ", " + addrExpr + ")"
}

// companyInetMasklen 表达 `masklen(<网段>)`。
//
//	PostgreSQL: masklen(net)
//	SQLite:     artex_inet_masklen(net)
//
// 只用于「更长（更窄）的网段优先」那条 ORDER BY：PostgreSQL 的 masklen 对
// IPv4 返回 0..32、对 IPv6 返回 0..128，两族可以同序比较；SQLite 侧照抄同一
// 口径，跨族比较的结果不变。
func companyInetMasklen(netExpr string) string {
	return "artex_inet_masklen(" + netExpr + ")"
}

// companyTryInet 表达 `try_inet(<文本>)`。
//
//	PostgreSQL: try_inet(a.ip)
//	SQLite:     artex_try_inet(a.ip)
//
// 用在两个地方：网段归属的「安全转 inet」，以及 malformedIPAssetWarning 里
// 「哪些资产的 ip 解析不出来」那条告警查询。
func companyTryInet(valueExpr string) string {
	return "artex_try_inet(" + valueExpr + ")"
}

// companyICPKey 表达 ICP 归属用的规范化 key。
//
//	PostgreSQL: lower(regexp_replace(COALESCE(x,''), '[[:space:]]+', '', 'g'))
//	SQLite:     artex_icp_key(x)
//
// 语义见 artexICPKeyFunc：两边都要算成「去空白 + 小写」，与写入 cs.value 的
// NormalizeICP 同形。
func companyICPKey(valueExpr string) string {
	return "artex_icp_key(" + valueExpr + ")"
}

// companyJSONArrayLength 表达 `jsonb_array_length(COALESCE(<col>, '[]'::jsonb))`，
// 即「这个 jsonb 数组列里有几个元素」。
//
//	PostgreSQL: jsonb_array_length(COALESCE(f.asset_ids, '[]'::jsonb))
//	SQLite:     artex_json_array_length(COALESCE(f.asset_ids, '[]'))
//
// SQLite 侧把 '[]'::jsonb 里的 ::jsonb 去掉：SQLite 的 COALESCE 是逐值的通用
// 表达式、没有类型标注位置，而 '[]' 本身在 SQLite 里就是一个普通文本字面量。
// NULL / 空串 / '{}' 这三种历史形状由 artex_json_array_length 内部统一归一成
// 长度 0，所以调用点不需要再包一层 CASE。
func companyJSONArrayLength(col string) string {
	return "artex_json_array_length(COALESCE(" + col + ", '[]'))"
}

// companyCidrArg 表达写入 company_scope.net（PostgreSQL 是 CIDR 列）时的转换。
//
//	PostgreSQL: $3::cidr
//	SQLite:     $3
//
// SQLite 侧 net 就是 TEXT 列，值由 ParseScopeLine 产出（已经是规范化 CIDR
// 文本），不需要也不应该有类型转换；而 `$3::cidr` 在 SQLite 上会被报
// unrecognized token ":"。
func companyCidrArg(ph string) string {
	return ph
}

// companyInetAddrArg 表达「把绑定参数当成一个 inet 值」的转换，用作
// companyInetContains 的右操作数。
//
//	PostgreSQL: $1::inet
//	SQLite:     $1
//
// PostgreSQL 侧靠这个转换把 Go 绑过去的字符串变成 inet；SQLite 侧 net 是
// TEXT，artex_inet_contains 自己就做文本解析，不需要（也不能）有转换——
// 裸的 `$1::inet` 会被 SQLite 报 unrecognized token ":"。
func companyInetAddrArg(ph string) string {
	return ph
}

// companyScopeConflictTarget 表达 company_scope 插入语句的冲突目标。
//
//	PostgreSQL: ON CONFLICT ON CONSTRAINT uq_sv2_domain
//	SQLite:     ON CONFLICT (company_id, domain)
//
// 约束名 → 列清单的依据（两个 schema 一致，逐字对照）：
//
//	db/schema_sqlite.sql:125        CONSTRAINT uq_sv2_domain UNIQUE (company_id, domain)
//	db/schema_sqlite.sql:163  CONSTRAINT uq_sv2_domain UNIQUE (company_id, domain)
//	db/schema_sqlite.sql:126        CONSTRAINT uq_sv2_net    UNIQUE (company_id, net)
//	db/schema_sqlite.sql:164  CONSTRAINT uq_sv2_net    UNIQUE (company_id, net)
//
// SQLite 不支持按约束名指定冲突目标，但表级 UNIQUE 约束同样会建出一个唯一
// 索引（sqlite_autoindex_*），`ON CONFLICT (列…)` 正是按这个索引做仲裁。
// 两个约束都是复合的，所以必须给完整列清单，只写一列会匹配不上任何唯一索引，
// 直接报 "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint"。
//
// **不会重复插行**：ON CONFLICT (company_id, domain) 与 ON CONFLICT (company_id, net)
// 各自都命中上面对应的那个 UNIQUE 索引，重复规则仍然被 DO NOTHING 吞掉。
func companyScopeConflictTarget(constraint string, cols ...string) string {
	return "ON CONFLICT (" + strings.Join(cols, ", ") + ")"
}

// findingAssetScopeIDsCond 表达「findings.asset_ids 与这批资产 id 有交集」。
//
//	PostgreSQL: f.asset_ids @> ANY($2::jsonb[])   —— 参数是 []string 的 jsonb[]
//	SQLite:     artex_json_contains_any(f.asset_ids, $2) —— 参数是一段 JSON 数组文本
//
// 返回值里已经带好 $N 编号，并把对应参数追加进 args。
//
// 参数形态为什么不一样：现代 SQLite 驱动无法把 []string / []int64 当成一个
// 参数绑定（只会得到 "unsupported type []string"），ANY() 本身也不是 SQLite
// 函数。与 dialect_arrays.go 的 appendInPlaceholders 同源，这里选择把集合在
// Go 侧编码成一段 JSON 文本再交给 UDF——值仍然是绑定进去的，SQL 文本里没有
// 任何用户输入。
func findingAssetScopeIDsCond(colExpr string, ids []int64, args *[]any) string {
	*args = append(*args, companyJSONIDSet(ids))
	return fmt.Sprintf("artex_json_contains_any(%s, $%d)", colExpr, len(*args))
}

// companyJSONIDSet 把资产 id 列表编码成 SQLite 侧的 JSON 数组文本，
// 供 artex_json_contains_any 使用。空集合编码成 "[]"，与 findings.asset_ids
// 的列默认值同一形状。
func companyJSONIDSet(ids []int64) string {
	vals := make([]any, 0, len(ids))
	for _, id := range ids {
		vals = append(vals, id)
	}
	return jsonArrayText(vals)
}

// findingAssetRowsGoneCond 表达「这条发现关联的资产已经全没了」。
//
//	PostgreSQL: jsonb_array_elements_text(f.asset_ids) 与 assets 联表
//	SQLite:     json_each(artex_json_array(f.asset_ids)) 与 assets 联表
//
// 语义：asset_ids 为空（长度 0），或者里面列出的 id 在 assets 表里一个都查不到
// （资产已被删除）。两类都要算进「未关联资产」桶，否则桶上的计数会大于点开后
// 能查到的条数。
//
// SQLite 侧先把 f.asset_ids 过一遍 artex_json_array 归一化再交给 json_each：
// 内建 json_each 遇到损坏文本会直接报 malformed JSON，而 findings 历史上可能
// 存在 '{}'（PostgreSQL 空数组字面量）这类不是合法 JSON 数组的值。归一化之后
// 传给 json_each 的永远是合法 JSON 数组（至少是 '[]'），脏数据只会得到"0 行"
// 而不是让整条筛选语句失败。
//
// 元素侧的 typeof(e.value) = 'integer' 是刻意加的：PostgreSQL 那边
// e.v::bigint 对非数字会抛错，SQLite 侧 CAST('abc' AS INTEGER) 会静默变成 0；
// 用 typeof 过滤等于把两者统一成"非整数元素不参与匹配"，与 asset_ids 永远只存
// 整数的事实一致。
func findingAssetRowsGoneCond(jsonExpr string) string {
	return `NOT EXISTS (
			SELECT 1 FROM json_each(artex_json_array(` + jsonExpr + `)) e
			WHERE typeof(e.value) = 'integer'
			  AND EXISTS (SELECT 1 FROM assets a WHERE a.id = e.value)
		)`
}

// findingNodePayloadSetExpr 表达「把 payload.<jsonKey> 设成字符串 $1」。
//
//	PostgreSQL: jsonb_set(payload, '{<key>}', to_jsonb($1::text))
//	本实现:     json_set(payload, '$.<key>', json_quote($1))
//
//	SQLite:     json_set(payload, '$.<key>', json_quote($1))
//
// 三处差别都要对齐：
//   - 路径语法：PostgreSQL 用 '{key}'，SQLite 的 json_set 要求 '$.key'；
//   - 值必须强制成 JSON 字符串。PostgreSQL 的 to_jsonb($1::text) 一定产出
//     字符串；而 SQLite 的 json_set 会把"看起来像合法 JSON"的值原样嵌进去
//     （json_set('{}','$.a','123') 得到数字而不是字符串），所以外面要套一层
//     json_quote 才等价。
//   - jsonKey 必须是可信常量（severity / name / vulnclass），与调用点
//     setFindingCol 的既有约定一致，绝不接受用户输入。
func findingNodePayloadSetExpr(jsonKey string) string {
	return "json_set(payload, '$." + jsonKey + "', json_quote($1))"
}

// ---------------------------------------------------------------------
// 时间戳落点
// ---------------------------------------------------------------------

// findingTimestampScan 承接 ListFindingGroups 里 MAX(f.created_at) 的落点。
//
// 差异不在 SQL 而在驱动：现代 SQLite 驱动只有当 sqlite3_column_decltype 返回
// DATE/DATETIME/TIMESTAMP 时才把 TEXT 解析成 time.Time，而 MAX(...) 是表达式，
// decltype 为空，于是交出来就是一段和列里一模一样的文本。PostgreSQL 侧 pgx 直接
// 交 time.Time。
//
// 两条路都不能改 SQL：改 SQL（例如换成一个保持 decltype 的写法）没有等价写法，
// 而在 Scan 落点上分叉是零风险的——读到的文本与列里存的是同一串字符，按驱动自己
// 的时间布局解析回去就是同一个时刻。
type findingTimestampScan struct {
	Time time.Time
	Text sql.NullString
}

// dest 返回应当交给 rows.Scan 的落点。
func (s *findingTimestampScan) dest() any {
	return &s.Text
}

// value 解析出最终的时间。SQL 值只可能是两种：驱动交出来的 time.Time，或
// NULL（分组内没有任何 finding 时 MAX 为 NULL）；解析不出来按错误报出，
// 不静默塞一个零值——那会把"时间戳坏了"伪装成"时间戳是零值"。
func (s *findingTimestampScan) value() (time.Time, error) {
	if !s.Text.Valid {
		return time.Time{}, nil
	}
	return parseSQLiteTimestamp(s.Text.String)
}

// parseSQLiteTimestampLayouts 与 modernc.org/sqlite 的 parseTimeFormats 保持一致
// （sqlite.go 的日期时间语法 1–7），少一条就会把某种形状误判成"坏时间戳"。
var parseSQLiteTimestampLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseSQLiteTimestamp 解析 SQLite 侧读出的时间戳文本。首条命中的布局就是写入
// 路径用的那一条：列默认值是 strftime('%Y-%m-%d %H:%M:%f+00:00','now')，
// now() UDF 是 2006-01-02 15:04:05.000-07:00，两者都落在第一条布局上。
func parseSQLiteTimestamp(s string) (time.Time, error) {
	for _, layout := range parseSQLiteTimestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析 SQLite 时间戳 %q", s)
}
