package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// =====================================================================
// 统一资产表
// =====================================================================

// Asset is a row in the assets table.
type Asset struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	CompanyID  *int64  `json:"company_id,omitempty"`
	TaskIDs    []int64 `json:"task_ids"`
	Domain     string  `json:"domain,omitempty"`
	RootDomain string  `json:"root_domain,omitempty"`
	IP         string  `json:"ip,omitempty"`
	CSegment   string  `json:"c_segment,omitempty"`
	Port       *int    `json:"port,omitempty"`
	ICP        string  `json:"icp,omitempty"`
	// ip fields
	BoundDomains []string         `json:"bound_domains,omitempty"`
	OpenPorts    []map[string]any `json:"open_ports,omitempty"`
	// subdomain fields
	RecordType  string   `json:"record_type,omitempty"`
	RecordValue []string `json:"record_value,omitempty"`
	// app fields
	BundleID       string `json:"bundle_id,omitempty"`
	AppName        string `json:"app_name,omitempty"`
	Category       string `json:"category,omitempty"`
	AppDescription string `json:"app_description,omitempty"`
	AppICP         string `json:"app_icp,omitempty"`
	// service fields
	URL           string           `json:"url,omitempty"`
	ServiceType   string           `json:"service_type,omitempty"`
	ServiceName   string           `json:"service_name,omitempty"`
	FaviconMMH3   string           `json:"favicon_mmh3,omitempty"`
	StatusCode    *int             `json:"status_code,omitempty"`
	ContentLength *int64           `json:"content_length,omitempty"`
	PageTitle     string           `json:"page_title,omitempty"`
	Technologies  []string         `json:"technologies,omitempty"`
	Auth          []map[string]any `json:"auth,omitempty"`
	// endpoint fields
	Method string           `json:"method,omitempty"`
	Params []map[string]any `json:"params,omitempty"`
	// meta
	Extra             map[string]any `json:"extra,omitempty"`
	LastSeen          string         `json:"last_seen"`
	TaskSource        string         `json:"task_source,omitempty"`
	TaskSourceSummary string         `json:"task_source_summary,omitempty"`
	TaskSourceNodeID  *int64         `json:"task_source_node_id,omitempty"`
}

// AuthItem is one entry in the auth array.
type AuthItem struct {
	Type        string `json:"type,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Token       string `json:"token,omitempty"`
	Description string `json:"description,omitempty"`
}

// ParamItem is one entry in the params array.
type ParamItem struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	Type     string `json:"type,omitempty"`
}

// PortService is one entry in open_ports: {"port":22,"service":"ssh"}.
type PortService struct {
	Port    int    `json:"port"`
	Service string `json:"service,omitempty"`
}

// AssetStore operates on the assets table.
type AssetStore struct {
	db      *DB
	company *CompanyStore
	tx      *sql.Tx
}

// Assets returns the asset store.
func (d *DB) Assets() *AssetStore {
	return &AssetStore{db: d, company: d.Companies()}
}

// Companies returns the company store associated with this asset store.
func (s *AssetStore) Companies() *CompanyStore { return s.company }

// DB returns the underlying database handle for cross-task read-only queries.
func (s *AssetStore) DB() *DB {
	if s == nil {
		return nil
	}
	return s.db
}

// withCompanyScopeMutation serializes scope resolution and every asset write
// that consumes its result in one transaction. Nested asset side effects reuse
// the same transaction through the scoped store.
func (s *AssetStore) withCompanyScopeMutation(fn func(*AssetStore) (int64, error)) (int64, error) {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockCompanyScopeMutation(tx); err != nil {
		return 0, err
	}
	scoped := &AssetStore{db: s.db, company: s.company, tx: tx}
	id, err := fn(scoped)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *AssetStore) resolveCompanyWithICP(rootDomain, ipStr, icp string) (*int64, error) {
	if s.tx != nil {
		return resolveCompanyWithICP(s.tx, rootDomain, ipStr, icp)
	}
	return s.company.ResolveCompanyWithICP(rootDomain, ipStr, icp)
}

// =====================================================================
// Helpers
// =====================================================================

// calcCSegment computes the /24 (IPv4) or /48 (IPv6) network for an IP string.
func calcCSegment(ipStr string) string {
	if ipStr == "" {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		// IPv4 /24
		parts := strings.Split(ipStr, ".")
		if len(parts) == 4 {
			return parts[0] + "." + parts[1] + "." + parts[2] + ".0/24"
		}
		return ""
	}
	// IPv6 /48
	_, ipnet, err := net.ParseCIDR(ipStr + "/48")
	if err != nil {
		return ""
	}
	return ipnet.String()
}

// normalizeURL lowercases scheme and host, strips trailing slash from bare roots.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	s := u.String()
	if strings.HasSuffix(s, "/") && u.Path == "/" && u.RawQuery == "" {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

// parseURL extracts domain, port, service_name from a URL.
func parseURL(raw string) (domain string, port int, serviceName string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, ""
	}
	domain = strings.ToLower(u.Hostname())
	port = defaultPort(strings.ToLower(u.Scheme), u.Port())
	switch strings.ToLower(u.Scheme) {
	case "https":
		serviceName = "HTTPS"
	case "http":
		serviceName = "HTTP"
	default:
		serviceName = strings.ToUpper(u.Scheme)
	}
	return
}

// marshalJSONBArray builds the PostgreSQL jsonb[] array literal. It stays
// byte-identical because the PostgreSQL branch must not change; the SQLite side
// goes through encodeJSONBArray in db/dialect_arrays.go, which emits a plain
// JSON array instead.
func marshalJSONBArray(items []map[string]any) (string, error) {
	if len(items) == 0 {
		return "{}", nil
	}
	parts := make([]string, len(items))
	for i, m := range items {
		b, err := json.Marshal(m)
		if err != nil {
			return "", err
		}
		// PostgreSQL array literal for jsonb[]: each element must be
		// double-quoted with internal backslashes and double-quotes escaped.
		s := strings.ReplaceAll(string(b), `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts[i] = `"` + s + `"`
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// marshalStringArray builds the PostgreSQL text[] array literal. Call sites that
// feed a real array column must use encodeTextArray (db/dialect_arrays.go), which
// picks the right encoding per engine; this raw literal form stays for the
// PostgreSQL branch and for the unit test that pins the literal format.
func marshalStringArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	escaped := make([]string, len(items))
	for i, s := range items {
		escaped[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(escaped, ",") + "}"
}

// marshalPortServices encodes assets.open_ports, whose elements are
// {"port":N,"service":"..."} objects. The element shape is identical on both
// engines; only the array wrapper differs (PostgreSQL array literal vs JSON
// array), so the encoding decision is delegated to encodeJSONBArray.
func marshalPortServices(ps []PortService) (string, error) {
	items := make([]map[string]any, len(ps))
	for i, p := range ps {
		items[i] = map[string]any{"port": p.Port, "service": p.Service}
	}
	return encodeJSONBArray(items)
}

// nullableInt64 returns sql.NullInt64.
func nullableInt(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// =====================================================================
// UpsertRootDomain
// =====================================================================

// UpsertRootDomainReq is the input for UpsertRootDomain.
type UpsertRootDomainReq struct {
	Domain string
	ICP    string
	TaskID int64
}

// UpsertRootDomain idempotently inserts or merges a root domain asset.
func (s *AssetStore) UpsertRootDomain(req UpsertRootDomainReq) (int64, error) {
	domain := DomainKey(req.Domain)
	if domain == "" {
		return 0, fmt.Errorf("domain is required")
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertRootDomain(req)
		})
	}
	companyID, err := s.resolveCompanyWithICP(domain, "", req.ICP)
	if err != nil {
		return 0, err
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	var icpVal any
	if req.ICP != "" {
		icpVal = req.ICP
	}

	var id int64
	err = s.tx.QueryRow(upsertRootDomainSQL(), domain, icpVal, companyID, taskIDs).Scan(&id)
	return id, err
}

// upsertRootDomainSQL 返回该引擎认的 upsert 语句。
//
// PostgreSQL 分支逐字保留迁移前的写法：$4::bigint[] 的数组字面量转换，以及
// task_ids 的 ARRAY(SELECT DISTINCT unnest(...)) 去重合并。
//
// SQLite 分支的三处替换（依据见 db/dialect_arrays.go 文件头）：
//
//	$4::bigint[]  → $4。task_ids 在 SQLite 里是 TEXT 列存 JSON 数组，
//	  值由 encodeTaskIDArray 在 Go 侧生成（"[5]" / "[]"），SQL 不再转换类型。
//	  `::` 不是 SQLite 语法：驱动把 `$4::bigint` 当成一个名叫 "4::bigint" 的
//	  具名参数，于是报 unrecognized token / near "[]"，所以必须删掉。
//	ARRAY(SELECT DISTINCT unnest(a || b)) → artex_json_merge(a, b)：先拼接再按
//	  整元素去重、保留首现顺序，与 ARRAY+SELECT DISTINCT 的行为一致。
//	assets.extra || EXCLUDED.extra 保持原样——它是 jsonb 合并而不是数组操作，
//	  属于 jsonb 方言层，不在本函数范围内。
func upsertRootDomainSQL() string {

	return `
INSERT INTO assets(type, domain, root_domain, icp, company_id, company_source, task_ids)
VALUES ('root_domain', $1, $1, $2, $3, 'scope', $4)
ON CONFLICT (domain) WHERE type = 'root_domain' DO UPDATE SET
    icp        = COALESCE(EXCLUDED.icp, assets.icp),
    company_id = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    task_ids   = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    extra      = assets.extra || EXCLUDED.extra,
    last_seen  = now()
RETURNING id`
}

// ErrAssetIPInvalid marks a non-address value in an asset's ip field. Both
// insert_assets and the asset API report it per item with the item index, so one
// bad entry never costs the rest of the batch.
var ErrAssetIPInvalid = errors.New("invalid asset ip")

// ValidateAssetIP keeps hostnames out of assets.ip. Network attribution casts
// that column to inet (see try_inet in schema_sqlite.sql), so a hostname stored here is
// silently invisible to every IP/CIDR scope rule — the asset simply never gets
// attributed and nobody can tell why. The message states the fix rather than
// just the fault, so an agent can correct the item on its next turn. An empty
// value is accepted: the ip field is optional for service and endpoint assets.
func ValidateAssetIP(value string) error {
	if value == "" || net.ParseIP(value) != nil {
		return nil
	}
	return fmt.Errorf(
		"%w: ip 必须是 IPv4/IPv6 地址，收到 %q。若这是主机名，请改用 type=subdomain 并填 domain 字段；"+
			"若确实要登记地址，请先解析出 A/AAAA 记录，再用解析出的地址填 ip",
		ErrAssetIPInvalid, value)
}

// =====================================================================
// UpsertIP
// =====================================================================

// UpsertIPReq is the input for UpsertIP.
type UpsertIPReq struct {
	IP           string
	BoundDomains []string
	OpenPorts    []PortService
	TaskID       int64
}

// UpsertIP idempotently inserts or merges an IP asset.
func (s *AssetStore) UpsertIP(req UpsertIPReq) (int64, error) {
	if req.IP == "" {
		return 0, fmt.Errorf("ip is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertIP(req)
		})
	}
	cseg := calcCSegment(req.IP)
	companyID, err := s.resolveCompanyWithICP("", req.IP, "")
	if err != nil {
		return 0, err
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	boundDomains := encodeTextArray(req.BoundDomains)
	openPortsJSON, err := marshalPortServices(req.OpenPorts)
	if err != nil {
		return 0, err
	}

	var csegVal any
	if cseg != "" {
		csegVal = cseg
	}

	var id int64
	err = s.tx.QueryRow(upsertIPSQL(), req.IP, csegVal, boundDomains, openPortsJSON, companyID, taskIDs).Scan(&id)
	return id, err
}

// upsertIPSQL 返回该引擎认的 upsert 语句。
//
// SQLite 分支的四处替换：
//
//	$2::cidr / $3::text[] / $4::jsonb[] / $6::bigint[] 全部去掉。
//	  四个数组列（bound_domains、open_ports、task_ids）在 SQLite 里就是 TEXT 列，
//	  值由 encodeTextArray / marshalPortServices / encodeTaskIDArray 在 Go 侧生成为
//	  JSON 数组，列本身不需要类型转换；c_segment 同样是 TEXT 列。`::cidr` 这类
//	  裸转换不是 SQLite 语法，会被驱动解析成具名参数 "2::cidr" 并报
//	  missing named argument，因此必须删。
//	bound_domains 的 ARRAY(SELECT DISTINCT unnest(...)) → artex_json_merge：
//	  拼接后按整元素去重、保留首现顺序。
//	open_ports 的 DISTINCT ON (端口) + ORDER BY 端口/服务名 → artex_json_merge_ports：
//	  一端口一条、同端口优先留 service 非空的、结果按端口号升序。
//	task_ids 同上 → artex_json_merge。
func upsertIPSQL() string {

	return `
INSERT INTO assets(type, ip, c_segment, bound_domains, open_ports, company_id, company_source, task_ids)
VALUES ('ip', $1, $2, $3, $4, $5, 'scope', $6)
ON CONFLICT (ip) WHERE type = 'ip' DO UPDATE SET
    bound_domains = artex_json_merge(assets.bound_domains, EXCLUDED.bound_domains),
    open_ports    = artex_json_merge_ports(assets.open_ports, EXCLUDED.open_ports),
    c_segment  = COALESCE(assets.c_segment, EXCLUDED.c_segment),
    company_id = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    task_ids   = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    last_seen  = now()
RETURNING id`
}

// AppendIPPort appends a {port, service} entry to an existing IP asset's open_ports.
func (s *AssetStore) AppendIPPort(ipStr string, port int, serviceName string) error {
	if ipStr == "" || port == 0 {
		return nil
	}
	entry, _ := json.Marshal(map[string]any{"port": port, "service": serviceName})
	// Postgres 侧是 unnest(open_ports || ARRAY[$1::jsonb])：$1 只是一个 jsonb 对象，
	// ARRAY[...] 把它包成单元素数组再拼接。SQLite 侧的 artex_json_merge_ports 会对
	// 第二个参数做同样的"不是数组就包成单元素"处理，所以这里传的仍是裸对象 JSON，
	// 不需要在 Go 侧多包一层数组。

	_, err := s.db.Exec(`
UPDATE assets SET
    open_ports = artex_json_merge_ports(open_ports, $1),
    last_seen = now()
WHERE type = 'ip' AND ip = $2`, string(entry), ipStr)
	return err
}

// AppendIPBoundDomain appends a domain to an existing IP asset's bound_domains.
func (s *AssetStore) AppendIPBoundDomain(ipStr, domain string) error {
	if ipStr == "" || domain == "" {
		return nil
	}
	// Postgres: bound_domains || ARRAY[$1::text]，DISTINCT 保证同一个域名不重复。
	// SQLite: artex_json_merge 把 $1 当单元素数组拼进去并按元素去重，语义相同。

	_, err := s.db.Exec(`
UPDATE assets SET
    bound_domains = artex_json_merge(bound_domains, $1),
    last_seen = now()
WHERE type = 'ip' AND ip = $2`, domain, ipStr)
	return err
}

// =====================================================================
// UpsertSubdomain
// =====================================================================

// UpsertSubdomainReq is the input for UpsertSubdomain.
type UpsertSubdomainReq struct {
	Domain      string
	RecordType  string
	RecordValue []string
	ICP         string
	TaskID      int64
}

// UpsertSubdomain idempotently inserts or merges a subdomain asset and triggers
// side effects: root_domain upsert + IP bound_domains update.
func (s *AssetStore) UpsertSubdomain(req UpsertSubdomainReq) (id int64, err error) {
	domain := DomainKey(req.Domain)
	if domain == "" {
		return 0, fmt.Errorf("domain is required")
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertSubdomain(req)
		})
	}

	rootDomain, _ := RootDomain(domain)
	if rootDomain == "" {
		rootDomain = domain
	}

	// Resolve by root domain first, then the asset's exact normalized ICP.
	companyID, err := s.resolveCompanyWithICP(rootDomain, "", req.ICP)
	if err != nil {
		return 0, err
	}

	// side effect 1: ensure root domain exists
	_, _ = s.UpsertRootDomain(UpsertRootDomainReq{
		Domain: rootDomain,
		TaskID: req.TaskID,
	})

	// side effect 2: if A/AAAA record, upsert each IP + bind domain.
	// RecordValue is []string; each element may itself be comma-separated (legacy).
	var ipStr string // first valid IP, used for the subdomain row itself
	if req.RecordType == "A" || req.RecordType == "AAAA" {
		for _, rv := range req.RecordValue {
			for _, part := range strings.Split(rv, ",") {
				candidate := strings.TrimSpace(part)
				if candidate == "" || net.ParseIP(candidate) == nil {
					continue
				}
				if ipStr == "" {
					ipStr = candidate
				}
				_, _ = s.UpsertIP(UpsertIPReq{
					IP:           candidate,
					BoundDomains: []string{domain},
					TaskID:       req.TaskID,
				})
			}
		}
	}

	cseg := calcCSegment(ipStr)
	if companyID == nil && ipStr != "" {
		companyID, _ = s.resolveCompanyWithICP("", ipStr, "")
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	var icpVal, csegVal, ipVal any
	if req.ICP != "" {
		icpVal = req.ICP
	}
	if cseg != "" {
		csegVal = cseg
	}
	if ipStr != "" {
		ipVal = ipStr
	}
	recordType := req.RecordType
	recordValueArr := encodeTextArray(req.RecordValue)

	err = s.tx.QueryRow(upsertSubdomainSQL(), domain, rootDomain, recordType, recordValueArr, ipVal, csegVal, icpVal, companyID, taskIDs).Scan(&id)
	return id, err
}

// upsertSubdomainSQL 返回该引擎认的 upsert 语句。
//
// 与 upsertIPSQL 同理：SQLite 分支去掉 $4::text[] / $6::cidr / $9::bigint[]
// 三处类型转换（record_value、c_segment、task_ids 在 SQLite 里都是 TEXT 列，
// 值已由 encodeTextArray / encodeTaskIDArray 生成为 JSON 数组），并把两处
// ARRAY(SELECT DISTINCT unnest(...)) 换成 artex_json_merge。
//
// ON CONFLICT (domain, COALESCE(record_type,”)) 是 SQLite 原生支持的表达式
// 冲突目标，对应 schema 里的部分唯一索引 uq_av2_subdomain，两边行为一致。
func upsertSubdomainSQL() string {

	return `
INSERT INTO assets(type, domain, root_domain, record_type, record_value, ip, c_segment, icp, company_id, company_source, task_ids)
VALUES ('subdomain', $1, $2, $3, $4, $5, $6, $7, $8, 'scope', $9)
ON CONFLICT (domain, COALESCE(record_type,'')) WHERE type = 'subdomain' DO UPDATE SET
    ip           = COALESCE(EXCLUDED.ip, assets.ip),
    c_segment    = COALESCE(EXCLUDED.c_segment, assets.c_segment),
    icp          = COALESCE(EXCLUDED.icp, assets.icp),
    company_id   = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    task_ids     = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    record_value = artex_json_merge(assets.record_value, EXCLUDED.record_value),
    extra        = assets.extra || EXCLUDED.extra,
    last_seen    = now()
RETURNING id`
}

// =====================================================================
// UpsertApp
// =====================================================================

// UpsertAppReq is the input for UpsertApp.
type UpsertAppReq struct {
	Name        string
	BundleID    string
	Category    string
	Description string
	ICP         string
	CompanyID   *int64 // explicit override; nil = exact ICP auto-attribution when available
	TaskID      int64
}

// UpsertApp idempotently inserts or merges an app asset.
func (s *AssetStore) UpsertApp(req UpsertAppReq) (int64, error) {
	if req.Name == "" {
		return 0, fmt.Errorf("app name is required")
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertApp(req)
		})
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	var bundleVal, catVal, descVal, icpVal, companyIDVal any
	companySource := "scope"
	if req.BundleID != "" {
		bundleVal = req.BundleID
	}
	if req.Category != "" {
		catVal = req.Category
	}
	if req.Description != "" {
		descVal = req.Description
	}
	if req.ICP != "" {
		icpVal = req.ICP
	}
	if req.CompanyID != nil {
		companyIDVal = *req.CompanyID
		companySource = "explicit"
	} else {
		companyID, err := s.resolveCompanyWithICP("", "", req.ICP)
		if err != nil {
			return 0, err
		}
		if companyID != nil {
			companyIDVal = *companyID
		}
	}

	var id int64
	var err error

	if req.BundleID != "" {
		err = s.tx.QueryRow(upsertAppByBundleSQL(), bundleVal, req.Name, catVal, descVal, icpVal, companyIDVal, companySource, taskIDs).Scan(&id)
	} else {
		err = s.tx.QueryRow(upsertAppByNameSQL(), req.Name, catVal, descVal, icpVal, companyIDVal, companySource, taskIDs).Scan(&id)
	}
	return id, err
}

// upsertAppByBundleSQL / upsertAppByNameSQL 是 UpsertApp 的两条冲突分支。
//
// SQLite 分支同样只去掉 $8::bigint[] / $7::bigint[] 这一处类型转换（task_ids
// 在 SQLite 里是 TEXT 列存 JSON 数组），并把 task_ids 的
// ARRAY(SELECT DISTINCT unnest(...)) 换成 artex_json_merge。
//
// 注意：这条语句没有列出 bound_domains / open_ports / technologies / auth /
// params 这些列，SQLite 会给它们填 schema 默认值 '{}'——那不是合法 JSON。
// 读取侧与合并侧都经过 db/dialect_arrays.go 的 jsonArrayOf 归一化，所以 '{}'
// 会被当作空数组，不会触发 malformed JSON。
func upsertAppByBundleSQL() string {

	return `
INSERT INTO assets(type, bundle_id, app_name, category, app_description, app_icp, company_id, company_source, task_ids)
VALUES ('app', $1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (bundle_id) WHERE type = 'app' AND bundle_id IS NOT NULL DO UPDATE SET
    app_name        = COALESCE(EXCLUDED.app_name, assets.app_name),
    category        = COALESCE(EXCLUDED.category, assets.category),
    app_description = COALESCE(EXCLUDED.app_description, assets.app_description),
    app_icp         = COALESCE(EXCLUDED.app_icp, assets.app_icp),
    company_id      = CASE
        WHEN EXCLUDED.company_source = 'explicit' THEN EXCLUDED.company_id
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source  = CASE
        WHEN EXCLUDED.company_source = 'explicit' THEN 'explicit'
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    task_ids        = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    last_seen       = now()
RETURNING id`
}

func upsertAppByNameSQL() string {

	return `
INSERT INTO assets(type, bundle_id, app_name, category, app_description, app_icp, company_id, company_source, task_ids)
VALUES ('app', NULL, $1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (app_name) WHERE type = 'app' AND bundle_id IS NULL DO UPDATE SET
    category        = COALESCE(EXCLUDED.category, assets.category),
    app_description = COALESCE(EXCLUDED.app_description, assets.app_description),
    app_icp         = COALESCE(EXCLUDED.app_icp, assets.app_icp),
    company_id      = CASE
        WHEN EXCLUDED.company_source = 'explicit' THEN EXCLUDED.company_id
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source  = CASE
        WHEN EXCLUDED.company_source = 'explicit' THEN 'explicit'
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    task_ids        = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    last_seen       = now()
RETURNING id`
}

// =====================================================================
// UpsertHTTPService
// =====================================================================

// UpsertHTTPServiceReq is the input for UpsertHTTPService.
type UpsertHTTPServiceReq struct {
	URL           string
	Technologies  []string
	StatusCode    *int
	ContentLength *int64
	PageTitle     string
	FaviconMMH3   string
	Auth          []map[string]any
	IP            string // optional, from async DNS
	TaskID        int64
}

// UpsertHTTPService inserts or merges an HTTP service asset. Domain, port,
// service_name, and root_domain are auto-extracted from URL.
func (s *AssetStore) UpsertHTTPService(req UpsertHTTPServiceReq) (int64, error) {
	if req.URL == "" {
		return 0, fmt.Errorf("url is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertHTTPService(req)
		})
	}
	normURL := normalizeURL(req.URL)
	domain, port, serviceName := parseURL(normURL)
	rootDomain, _ := RootDomain(domain)
	if rootDomain == "" {
		rootDomain = domain
	}

	cseg := calcCSegment(req.IP)
	companyID, err := s.resolveCompanyWithICP(rootDomain, req.IP, "")
	if err != nil {
		return 0, err
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	techsArr := encodeTextArray(req.Technologies)
	authJSON, err := encodeJSONBArray(req.Auth)
	if err != nil {
		return 0, err
	}

	var domainVal, ipVal, csegVal, titleVal, faviconVal any
	if domain != "" {
		domainVal = domain
	}
	if req.IP != "" {
		ipVal = req.IP
	}
	if cseg != "" {
		csegVal = cseg
	}
	if req.PageTitle != "" {
		titleVal = req.PageTitle
	}
	if req.FaviconMMH3 != "" {
		faviconVal = req.FaviconMMH3
	}

	var id int64
	err = s.tx.QueryRow(upsertHTTPServiceSQL(),
		normURL, serviceName, domainVal, ipVal, nullableInt(port), rootDomain,
		csegVal, faviconVal, techsArr, req.StatusCode, req.ContentLength, titleVal,
		authJSON, companyID, taskIDs,
	).Scan(&id)
	if err != nil {
		return 0, err
	}

	// side effects: register root_domain + subdomain as their own assets too
	s.linkHostAssets(domain, rootDomain, req.TaskID)
	if req.IP != "" {
		var boundDomains []string
		if domain != "" {
			boundDomains = []string{domain}
		}
		var openPorts []PortService
		if port > 0 {
			openPorts = []PortService{{Port: port, Service: serviceName}}
		}
		_, _ = s.UpsertIP(UpsertIPReq{
			IP:           req.IP,
			BoundDomains: boundDomains,
			OpenPorts:    openPorts,
			TaskID:       req.TaskID,
		})
	}
	return id, nil
}

// upsertHTTPServiceSQL 返回该引擎认的 HTTP 服务 upsert 语句。
//
// SQLite 分支的替换：
//
//	$7::cidr / $9::text[] / $13::jsonb[] / $15::bigint[] → 去掉。technologies 与
//	  auth 在 SQLite 里是 TEXT 列存 JSON 数组，值由 encodeTextArray /
//	  encodeJSONBArray 生成；c_segment 是 TEXT 列。`::` 转换在 SQLite 不是语法。
//	technologies 的 ARRAY(SELECT DISTINCT unnest(...)) → artex_json_merge。
//	auth 的 DISTINCT ON (elem::text) → artex_json_merge：键是"整个元素相等"，
//	  与 elem::text 的去重键等价（Go 的 json.Marshal 会把对象键排序，正好对上
//	  PostgreSQL jsonb ::text 的规范化形态）。
//	task_ids 同上 → artex_json_merge。
func upsertHTTPServiceSQL() string {

	return `
INSERT INTO assets(
    type, url, service_type, service_name, domain, ip, port, root_domain,
    c_segment, favicon_mmh3, technologies, status_code, content_length, page_title,
    auth, company_id, company_source, task_ids
)
VALUES (
    'service', $1, 'http', $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11, $12,
    $13, $14, 'scope', $15
)
ON CONFLICT (url) WHERE type = 'service' AND service_type = 'http' DO UPDATE SET
    status_code    = COALESCE(EXCLUDED.status_code,    assets.status_code),
    content_length = COALESCE(EXCLUDED.content_length, assets.content_length),
    page_title     = COALESCE(EXCLUDED.page_title,     assets.page_title),
    favicon_mmh3   = COALESCE(EXCLUDED.favicon_mmh3,   assets.favicon_mmh3),
    ip             = COALESCE(EXCLUDED.ip,             assets.ip),
    c_segment      = COALESCE(EXCLUDED.c_segment,      assets.c_segment),
    technologies   = artex_json_merge(assets.technologies, EXCLUDED.technologies),
    auth           = artex_json_merge(assets.auth, EXCLUDED.auth),
    task_ids       = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    company_id     = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    last_seen      = now()
RETURNING id`
}

// =====================================================================
// UpsertOtherService
// =====================================================================

// UpsertOtherServiceReq is the input for UpsertOtherService.
type UpsertOtherServiceReq struct {
	Domain      string // domain or ip required
	IP          string
	Port        int
	ServiceName string
	Auth        []map[string]any
	TaskID      int64
}

// UpsertOtherService inserts or merges a non-HTTP service asset.
func (s *AssetStore) UpsertOtherService(req UpsertOtherServiceReq) (int64, error) {
	if req.Domain == "" && req.IP == "" {
		return 0, fmt.Errorf("domain or ip is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	if req.Port == 0 {
		return 0, fmt.Errorf("port is required")
	}
	if req.ServiceName == "" {
		return 0, fmt.Errorf("service_name is required")
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertOtherService(req)
		})
	}
	// normalize service_name (lowercase) so the (domain,ip,port,service_name)
	// dedup key doesn't split "SSH" and "ssh" into separate rows.
	serviceName := strings.ToLower(strings.TrimSpace(req.ServiceName))

	domain := DomainKey(req.Domain)
	var rootDomain string
	if domain != "" {
		rootDomain, _ = RootDomain(domain)
		if rootDomain == "" {
			rootDomain = domain
		}
	}

	cseg := calcCSegment(req.IP)
	companyID, err := s.resolveCompanyWithICP(rootDomain, req.IP, "")
	if err != nil {
		return 0, err
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	authJSON, err := encodeJSONBArray(req.Auth)
	if err != nil {
		return 0, err
	}

	var domainVal, rootDomainVal, ipVal, csegVal any
	if domain != "" {
		domainVal = domain
	}
	if rootDomain != "" {
		rootDomainVal = rootDomain
	}
	if req.IP != "" {
		ipVal = req.IP
	}
	if cseg != "" {
		csegVal = cseg
	}

	var id int64
	err = s.tx.QueryRow(upsertOtherServiceSQL(),
		serviceName, domainVal, ipVal, req.Port, rootDomainVal,
		csegVal, authJSON, companyID, taskIDs,
	).Scan(&id)
	if err != nil {
		return 0, err
	}

	// side effects
	if req.IP != "" && req.Port > 0 {
		var boundDomains []string
		if domain != "" {
			boundDomains = []string{domain}
		}
		_, _ = s.UpsertIP(UpsertIPReq{
			IP:           req.IP,
			BoundDomains: boundDomains,
			OpenPorts:    []PortService{{Port: req.Port, Service: serviceName}},
			TaskID:       req.TaskID,
		})
	}
	s.linkHostAssets(domain, rootDomain, req.TaskID)
	return id, nil
}

// upsertOtherServiceSQL 返回该引擎认的非 HTTP 服务 upsert 语句。
//
// SQLite 分支与 upsertHTTPServiceSQL 同构：去掉 $6::cidr / $7::jsonb[] /
// $9::bigint[] 三处类型转换，auth 的 DISTINCT ON (elem::text) 与 task_ids 的
// ARRAY(SELECT DISTINCT unnest(...)) 都换成 artex_json_merge。
func upsertOtherServiceSQL() string {

	return `
INSERT INTO assets(
    type, service_type, service_name, domain, ip, port, root_domain,
    c_segment, auth, company_id, company_source, task_ids
)
VALUES ('service', 'other', $1, $2, $3, $4, $5, $6, $7, $8, 'scope', $9)
ON CONFLICT (COALESCE(domain,''), COALESCE(ip,''), port, service_name) WHERE type = 'service' AND service_type = 'other' DO UPDATE SET
    domain     = COALESCE(EXCLUDED.domain,     assets.domain),
    ip         = COALESCE(EXCLUDED.ip,         assets.ip),
    c_segment  = COALESCE(EXCLUDED.c_segment,  assets.c_segment),
    auth       = artex_json_merge(assets.auth, EXCLUDED.auth),
    task_ids   = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    company_id = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    last_seen  = now()
RETURNING id`
}

// linkHostAssets ensures a service/endpoint's host is also registered as its own
// root_domain and (when it's a real subdomain, not the apex or an IP) subdomain
// asset — so those asset types stay populated and can anchor task scope. Best-effort.
func (s *AssetStore) linkHostAssets(domain, rootDomain string, taskID int64) {
	if rootDomain != "" {
		_, _ = s.UpsertRootDomain(UpsertRootDomainReq{Domain: rootDomain, TaskID: taskID})
	}
	if domain != "" && domain != rootDomain && net.ParseIP(domain) == nil {
		_, _ = s.UpsertSubdomain(UpsertSubdomainReq{Domain: domain, TaskID: taskID})
	}
}

// =====================================================================
// UpsertEndpoint
// =====================================================================

// UpsertEndpointReq is the input for UpsertEndpoint.
type UpsertEndpointReq struct {
	URL    string
	Method string
	Params []map[string]any
	IP     string // optional
	TaskID int64
}

// UpsertEndpoint inserts or merges an endpoint asset. Domain, port, root_domain
// are auto-extracted from the URL.
func (s *AssetStore) UpsertEndpoint(req UpsertEndpointReq) (int64, error) {
	if req.URL == "" {
		return 0, fmt.Errorf("url is required")
	}
	if req.Method == "" {
		return 0, fmt.Errorf("method is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	if s.tx == nil {
		return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
			return scoped.UpsertEndpoint(req)
		})
	}
	method := strings.ToUpper(req.Method)
	normURL := normalizeURL(req.URL)
	domain, port, _ := parseURL(normURL)
	rootDomain, _ := RootDomain(domain)
	if rootDomain == "" {
		rootDomain = domain
	}

	cseg := calcCSegment(req.IP)
	companyID, err := s.resolveCompanyWithICP(rootDomain, req.IP, "")
	if err != nil {
		return 0, err
	}

	taskIDs := encodeTaskIDArray(req.TaskID)

	paramsJSON, err := encodeJSONBArray(req.Params)
	if err != nil {
		return 0, err
	}

	var domainVal, rootDomainVal, ipVal, csegVal any
	if domain != "" {
		domainVal = domain
	}
	if rootDomain != "" {
		rootDomainVal = rootDomain
	}
	if req.IP != "" {
		ipVal = req.IP
	}
	if cseg != "" {
		csegVal = cseg
	}

	var id int64
	err = s.tx.QueryRow(upsertEndpointSQL(),
		normURL, method, domainVal, ipVal, nullableInt(port), rootDomainVal,
		paramsJSON, companyID, taskIDs, csegVal,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	// side effects: endpoint previously registered none — register its host as
	// root_domain + subdomain(+IP) so those asset types get populated too.
	s.linkHostAssets(domain, rootDomain, req.TaskID)
	if req.IP != "" {
		_, _ = s.UpsertIP(UpsertIPReq{IP: req.IP, TaskID: req.TaskID})
	}
	return id, nil
}

// upsertEndpointSQL 返回该引擎认的 endpoint upsert 语句。
//
// SQLite 分支去掉 $7::jsonb[] / $9::bigint[] / $10::cidr 三处类型转换，并把
//
//	SELECT ARRAY(
//	    SELECT DISTINCT ON ((elem->>'location'), (elem->>'name')) elem
//	    FROM unnest(assets.params || EXCLUDED.params) AS elem
//	    ORDER BY (elem->>'location'), (elem->>'name'), elem::text DESC
//	)
//
// 换成 artex_json_merge_params：键是 (location, name)，同键保留 elem::text 最大的
// 一条（PG 的 ORDER BY ... DESC + DISTINCT ON 就是这个意思），结果按 location、
// name 升序排列，NULL 排最后。详见 db/dialect_arrays.go。
func upsertEndpointSQL() string {

	return `
INSERT INTO assets(
    type, url, method, domain, ip, port, root_domain,
    params, company_id, company_source, task_ids, c_segment
)
VALUES ('endpoint', $1, $2, $3, $4, $5, $6, $7, $8, 'scope', $9, $10)
ON CONFLICT (url, method) WHERE type = 'endpoint' DO UPDATE SET
    params     = artex_json_merge_params(assets.params, EXCLUDED.params),
    ip         = COALESCE(EXCLUDED.ip,        assets.ip),
    c_segment  = COALESCE(EXCLUDED.c_segment, assets.c_segment),
    task_ids   = artex_json_merge(assets.task_ids, EXCLUDED.task_ids),
    company_id = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id
        ELSE COALESCE(EXCLUDED.company_id, assets.company_id)
    END,
    company_source = CASE
        WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit'
        WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope'
        ELSE assets.company_source
    END,
    last_seen  = now()
RETURNING id`
}

// =====================================================================
// Query helpers
// =====================================================================

// QueryByType returns assets rows of a given type, newest first.
func (s *AssetStore) QueryByType(typ string, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(assetProjection+assetsFromPrefix+`
WHERE type = $1
ORDER BY last_seen DESC, id DESC
LIMIT $2 OFFSET $3`, typ, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// CountByType returns the total number of assets of a type (for server-side pagination).
func (s *AssetStore) CountByType(typ string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM assets WHERE type = $1`, typ).Scan(&n)
	return n, err
}

// QueryByCompany returns assets for a company, optionally filtered by type.
// limit <= 0 means no limit.
func (s *AssetStore) QueryByCompany(companyID int64, typ string, limit, offset int) ([]*Asset, error) {
	q := assetProjection + assetsFromPrefix + `WHERE company_id = $1`
	args := []any{companyID}
	if typ != "" {
		args = append(args, typ)
		q += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	q += pageClause(&args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

func (s *AssetStore) CountByCompany(companyID int64, typ string) (int, error) {
	q := `SELECT count(*) FROM assets WHERE company_id = $1`
	args := []any{companyID}
	if typ != "" {
		args = append(args, typ)
		q += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

func pageClause(args *[]any, limit, offset int) string {
	q := ` ORDER BY last_seen DESC, id DESC`
	if limit > 0 {
		*args = append(*args, limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(*args))
	}
	if offset > 0 {
		*args = append(*args, offset)
		q += fmt.Sprintf(` OFFSET $%d`, len(*args))
	}
	return q
}

// QueryByTask returns assets rows that have a given task_id in task_ids.
func (s *AssetStore) QueryByTask(taskID int64, typ string, limit, offset int) ([]*Asset, error) {
	q := assetProjection + assetsFromPrefix + `WHERE ` + assetContainsTaskID()
	args := []any{taskID}
	if typ != "" {
		args = append(args, typ)
		q += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	q += pageClause(&args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateTaskAssetSources(taskID, assets); err != nil {
		return nil, err
	}
	return assets, nil
}

func (s *AssetStore) CountByTask(taskID int64, typ string) (int, error) {
	q := `SELECT count(*) FROM assets WHERE ` + assetContainsTaskID()
	args := []any{taskID}
	if typ != "" {
		args = append(args, typ)
		q += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

func (s *AssetStore) CountsByTypeForTask(taskID int64) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT type, COUNT(*) FROM assets WHERE `+assetContainsTaskID()+` GROUP BY type`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTypeCounts(rows)
}

// DeleteByTaskID removes assets owned only by taskID and detaches taskID from
// assets shared with other tasks. Full task deletion uses the coordinated
// transaction in DeleteTaskCascadePrepared; this method remains for callers
// that explicitly manage only asset associations.
//
// 两条语句都含数组运算：
//
//	task_ids = ARRAY[$1]::bigint[]
//	  → artex_json_equals(task_ids, $1)。PG 的数组相等是"同长度 + 逐元素相等 +
//	    顺序敏感"，只有 task_ids 恰好是 {taskID} 这一种形状的行会被删。
//	array_remove(task_ids, $1) ... WHERE $1 = ANY(task_ids)
//	  → artex_json_remove / artex_json_contains。$1 在两个语句里复用同一个编号，
//	    两个引擎都支持占位符复用。
func (s *AssetStore) DeleteByTaskID(taskID int64) (int64, error) {
	var err error
	var res sql.Result
	res, err = s.db.Exec(`DELETE FROM assets WHERE artex_json_equals(task_ids, $1)`, encodeTaskIDArray(taskID))
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()
	_, err = s.db.Exec(`
UPDATE assets SET task_ids = artex_json_remove(task_ids, $1)
WHERE artex_json_contains(task_ids, $1)`, taskID)
	if err != nil {
		return deleted, err
	}
	return deleted, nil
}

// HostsByTask returns the exact HTTP host candidates attached to a task's
// assets. Domain/IP columns cover root domains, subdomains and non-HTTP
// services; URL covers HTTP services and endpoints.
func (s *AssetStore) HostsByTask(taskID int64) ([]string, error) {
	rows, err := s.db.Query(`
SELECT COALESCE(domain,''), COALESCE(ip,''), COALESCE(url,'')
FROM assets WHERE `+assetContainsTaskID(), taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := make(map[string]struct{})
	add := func(host string) {
		host = strings.TrimSpace(strings.ToLower(host))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	for rows.Next() {
		var domain, ip, rawURL string
		if err := rows.Scan(&domain, &ip, &rawURL); err != nil {
			return nil, err
		}
		add(domain)
		add(ip)
		if u, err := url.Parse(rawURL); err == nil {
			add(u.Hostname())
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(hosts))
	for host := range hosts {
		out = append(out, host)
	}
	sort.Strings(out)
	return out, nil
}

// HostsForTaskDeletion returns hosts that belong to the task being deleted and
// are not referenced by any other live task. Current-task candidates include
// both task_ids ownership and exploration anchors so legacy seeded assets (which
// were anchor-only) are covered. Protection is host-wide: if another live task
// references any asset for a candidate host, that host's global traffic remains.
func (s *AssetStore) HostsForTaskDeletion(taskID, explorationID int64) ([]string, error) {
	return hostsForTaskDeletion(s.db, taskID, explorationID)
}

type rowsQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// hostsForTaskDeletion is shared by the read-only AssetStore API and the task
// deletion transaction. Coordinated traffic deletion must call it through the
// transaction path so asset/anchor writes stay locked until the task delete is
// committed.
func hostsForTaskDeletion(q rowsQuerier, taskID, explorationID int64) ([]string, error) {
	rows, err := q.Query(`
WITH current_assets AS (
  SELECT id FROM assets WHERE `+assetContainsTaskID()+`
  UNION
  SELECT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes n ON n.id=ea.node_id
  WHERE n.exploration_id=$2
),
other_assets AS (
  SELECT DISTINCT a.id
  FROM assets a
  WHERE EXISTS (
    SELECT 1 FROM tasks t
    WHERE t.id<>$1 AND t.deleted_at IS NULL AND `+assetContainsTaskIDAlias("a")+`
  ) OR EXISTS (
    SELECT 1
    FROM exploration_anchors ea
    JOIN exploration_nodes n ON n.id=ea.node_id
    JOIN tasks t ON t.exploration_id=n.exploration_id
    WHERE ea.asset_id=a.id AND t.id<>$1 AND t.deleted_at IS NULL
  )
)
SELECT COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.url,''), true
FROM assets a JOIN current_assets c ON c.id=a.id
UNION ALL
SELECT COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.url,''), false
FROM assets a JOIN other_assets o ON o.id=a.id`, taskID, explorationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make(map[string]struct{})
	protected := make(map[string]struct{})
	add := func(dst map[string]struct{}, raw string) {
		raw = strings.TrimSpace(strings.ToLower(raw))
		if raw != "" {
			dst[raw] = struct{}{}
		}
	}
	for rows.Next() {
		var domain, ip, rawURL string
		var candidate bool
		if err := rows.Scan(&domain, &ip, &rawURL, &candidate); err != nil {
			return nil, err
		}
		dst := protected
		if candidate {
			dst = candidates
		}
		add(dst, domain)
		add(dst, ip)
		if u, err := url.Parse(rawURL); err == nil {
			add(dst, u.Hostname())
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(candidates))
	for host := range candidates {
		if _, shared := protected[host]; !shared {
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out, nil
}

// assetSelectCols is the projection plus FROM clause. It is also consumed by
// db/asset_dsl.go, so the column list lives in db/dialect_arrays.go
// (assetProjection) where the array-read dialect decision is documented.
const assetSelectCols = assetProjection + `
FROM assets`

// GetByIDs returns assets with the given ids (order preserved by id array order).
func (s *AssetStore) GetByIDs(ids []int64) ([]*Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	sql := assetSelectCols + " WHERE id IN (" + strings.Join(placeholders, ",") + ") ORDER BY last_seen DESC"
	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// DeleteByCompanyID hard-deletes all assets belonging to a company. Returns rows deleted.
func (s *AssetStore) DeleteByCompanyID(companyID int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM assets WHERE company_id = $1`, companyID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteByHost hard-deletes every asset whose host exactly matches the given value:
// root_domain / subdomain / service / endpoint (they carry the host in domain or
// root_domain) plus an ip asset and its services/endpoints (ip column). The host is
// normalized the same way it is stored (DomainKey: lowercase/trim/strip trailing dot)
// so matching is exact, not fuzzy. Passing a root domain also removes its subdomains
// and their services/endpoints (they carry root_domain = that host); passing a
// subdomain/IP removes only that host's own assets. Referencing exploration_anchors
// rows are cleaned by ON DELETE CASCADE. Returns rows deleted, grouped by type.
func (s *AssetStore) DeleteByHost(host string) (map[string]int64, error) {
	h := DomainKey(host)
	if h == "" {
		return nil, fmt.Errorf("host is required")
	}
	rows, err := s.db.Query(`
DELETE FROM assets
WHERE domain = $1 OR root_domain = $1 OR ip = $1
RETURNING type`, h)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		counts[t]++
	}
	return counts, rows.Err()
}

// DeleteByIDs hard-deletes assets by their IDs. Returns the number of rows deleted.
func (s *AssetStore) DeleteByIDs(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	res, err := s.db.Exec("DELETE FROM assets WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountsByType returns asset counts per type.
func (s *AssetStore) CountsByType() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT type, COUNT(*) FROM assets GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTypeCounts(rows)
}

func scanTypeCounts(rows *sql.Rows) (map[string]int, error) {
	out := map[string]int{}
	for rows.Next() {
		var typ string
		var cnt int
		if err := rows.Scan(&typ, &cnt); err != nil {
			return nil, err
		}
		out[typ] = cnt
	}
	return out, rows.Err()
}

// scanAssets scans the wide SELECT that covers all type columns.
func scanAssets(rows *sql.Rows) ([]*Asset, error) {
	var out []*Asset
	for rows.Next() {
		a := &Asset{}
		var taskIDsRaw, boundDomainsRaw, openPortsRaw, recordValueRaw, techsRaw, authRaw, paramsRaw, extraRaw []byte
		var port, statusCode sql.NullInt64
		var contentLength sql.NullInt64
		var companyID sql.NullInt64
		if err := rows.Scan(
			&a.ID, &a.Type, &companyID, &taskIDsRaw,
			&a.Domain, &a.RootDomain, &a.IP, &a.CSegment, &port,
			&a.ICP, &boundDomainsRaw, &openPortsRaw, &a.RecordType,
			&recordValueRaw, &a.BundleID, &a.AppName,
			&a.Category, &a.AppDescription, &a.AppICP,
			&a.URL, &a.ServiceType, &a.ServiceName,
			&a.FaviconMMH3, &statusCode, &contentLength,
			&a.PageTitle, &techsRaw, &authRaw,
			&a.Method, &paramsRaw, &extraRaw, &a.LastSeen,
		); err != nil {
			return nil, err
		}
		if companyID.Valid {
			cid := companyID.Int64
			a.CompanyID = &cid
		}
		if port.Valid {
			p := int(port.Int64)
			a.Port = &p
		}
		if statusCode.Valid {
			sc := int(statusCode.Int64)
			a.StatusCode = &sc
		}
		if contentLength.Valid {
			cl := contentLength.Int64
			a.ContentLength = &cl
		}
		// parse arrays
		if len(taskIDsRaw) > 0 {
			_ = json.Unmarshal(taskIDsRaw, &a.TaskIDs)
		}
		if len(boundDomainsRaw) > 0 {
			_ = json.Unmarshal(boundDomainsRaw, &a.BoundDomains)
		}
		if len(openPortsRaw) > 0 {
			_ = json.Unmarshal(openPortsRaw, &a.OpenPorts)
		}
		if len(recordValueRaw) > 0 {
			_ = json.Unmarshal(recordValueRaw, &a.RecordValue)
		}
		if len(techsRaw) > 0 {
			_ = json.Unmarshal(techsRaw, &a.Technologies)
		}
		if len(authRaw) > 0 {
			_ = json.Unmarshal(authRaw, &a.Auth)
		}
		if len(paramsRaw) > 0 {
			_ = json.Unmarshal(paramsRaw, &a.Params)
		}
		if len(extraRaw) > 0 {
			_ = json.Unmarshal(extraRaw, &a.Extra)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
