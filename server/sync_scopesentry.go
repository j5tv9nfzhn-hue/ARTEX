package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
	"github.com/Autumn-27/artex/notify"
)

// ScopeSentry 资产同步。ASM 平台经 MCP 从项目、任务两个维度导入
// 子域名、Web 应用、服务等，映射为 ARTEX 公司与资产。
// 数据源为名为 ScopeSentry 的 http MCP 记录，在 mcp_servers 保存 URL 和 X-API-Key。
// 后台批量同步直接调用 mcphttp.Client.Call 获取原始 JSON，不弹 AskUser 审批。

const (
	scopeSentryMCPName = "ScopeSentry"
	syncMaxPerType     = 5000 // 每个目标、每种资产类型的保存安全上限。
	syncDefaultPage    = 100
)

// writeErr 与同步结果 warnings、errors 使用中文。
// dimension、targets、枚举、MCP 工具名和 JSON 字段保持原文；
// “数据源”与 zh.json 的 sync 命名空间一致。
const (
	errSSDataSourceMissingFmt = "数据源 %s 不存在，请先创建"
	errSSDataSourceNoURLFmt   = "数据源 %s 未设置 URL。请先进行设置"
	errSSListProjectsPrefix   = "调用 list_projects_data 失败: "
	errSSParseProjectsPrefix  = "无法解析项目列表: "
	errSSListTasksPrefix      = "调用 list_tasks 失败："
	errSSParseTasksPrefix     = "无法解析任务列表: "
	errSSDimension            = "dimension 的值必须为 project 或 task"
	errSSTargetsEmpty         = "targets 不能为空"
	warnSSProjectMetaFmt      = "无法获取项目 %s 的详情：%v"
	warnSSCompanyCreateFmt    = "创建公司 %s 失败：%v"
	warnSSUnknownAssetPrefix  = "未知资产类型，已跳过： "
	errSSFetchFmt             = "无法获取资产 %s(%s)：%v"
	warnSSTruncatedFmt        = "%s(%s) 达到 %d 条上限，已截断"
	errSSParseSubdomainPrefix = "无法解析 subdomain 响应："
	errSSParseAppPrefix       = "无法解析 app 响应: "
	errSSParseServicePrefix   = "无法解析 service 响应: "
)

// findMCPByName returns the MCP server row with the given name, or nil.
func (s *Server) findMCPByName(name string) (*db.MCPServer, error) {
	all, err := s.m.pg.ListMCP()
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, nil
}

// scopeSentryClient dials the configured ScopeSentry MCP (http transport). The env
// map doubles as HTTP headers (X-API-Key). Callers must Close the client.
func (s *Server) scopeSentryClient(ctx context.Context) (*mcphttp.Client, error) {
	m, err := s.findMCPByName(scopeSentryMCPName)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf(trMsg(errSSDataSourceMissingFmt), scopeSentryMCPName)
	}
	if m.URL == "" {
		return nil, fmt.Errorf(trMsg(errSSDataSourceNoURLFmt), scopeSentryMCPName)
	}
	// env/header 值是 enc:v1: 密文（计划 #49），拨号前解成明文；解不开就报错。
	env, err := db.MCPEnvPlain(m.Env)
	if err != nil {
		return nil, err
	}
	return mcphttp.New(ctx, m.Name, m.URL, env, m.Insecure)
}

// envHasValue reports whether the env/header map has any non-empty value
// (i.e. an API key / Authorization header was filled in).
func envHasValue(raw json.RawMessage) bool {
	for _, v := range jsonStrMap(raw) {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// ---------- GET /api/sync/scopesentry/status ----------

func (s *Server) syncSSStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	noStore(w) // 配置/连通性探针结果
	m, err := s.findMCPByName(scopeSentryMCPName)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := map[string]any{
		"exists":     m != nil,
		"configured": false,
		"enabled":    false,
		"reachable":  false,
		"tools":      []string{},
	}
	if m == nil {
		writeJSON(w, 200, resp)
		return
	}
	configured := m.URL != "" && envHasValue(m.Env)
	resp["configured"] = configured
	resp["enabled"] = m.Enabled
	resp["url"] = m.URL
	if m.Tools != nil {
		resp["tools"] = m.Tools
	}
	// Light reachability probe only when it can actually connect. 密文解不开
	// （主密钥丢失/损坏）时跳过探测：此时 reachable=false 就是诚实的回答，
	// 而把密文当头部发出去只会得到误导读者的 401。
	if configured && m.Enabled {
		if env, eerr := db.MCPEnvPlain(m.Env); eerr == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			if cl, cerr := mcphttp.New(ctx, m.Name, m.URL, env, m.Insecure); cerr == nil {
				if _, terr := cl.Tools(ctx); terr == nil {
					resp["reachable"] = true
				}
				_ = cl.Close()
			}
		}
	}
	writeJSON(w, 200, resp)
}

// ---------- POST /api/sync/scopesentry/datasource ----------
// Creates the placeholder row if missing, and/or sets URL + API key and enables it.

func (s *Server) syncSSDatasource(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	_ = decode(r, &body) // empty body = just create the placeholder
	body.URL = strings.TrimSpace(body.URL)
	body.APIKey = strings.TrimSpace(body.APIKey)

	m, err := s.findMCPByName(scopeSentryMCPName)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if m == nil {
		m = &db.MCPServer{Name: scopeSentryMCPName, Transport: "http", Env: json.RawMessage(`{"X-API-Key":""}`)}
	}
	if body.URL != "" {
		m.URL = body.URL
	}
	// 掩码值（如从 MCP 页面复制来的 "__masked__…"）含义是「保持原值」，
	// 绝不能当新 key 存进库——那会静默换成一把错的凭据。本页 UI 从不回显 key，
	// 这行是防御性的：语义上与 notify.MergeConfig 的掩码规则一致。
	if body.APIKey != "" && !notify.IsMasked(body.APIKey) {
		env, _ := json.Marshal(map[string]string{"X-API-Key": body.APIKey})
		m.Env = env
	}
	// Enable only once it can actually be used.
	m.Enabled = m.URL != "" && envHasValue(m.Env)

	id, err := pg.SaveMCP(m)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	m.ID = id
	// Best-effort tool discovery so the status card shows tools right away.
	if m.Enabled {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		_ = s.discoverAndCacheMCP(ctx, m)
		cancel()
	}
	writeJSON(w, 200, map[string]any{"id": id, "enabled": m.Enabled})
}

// ---------- GET /api/sync/scopesentry/projects ----------

func (s *Server) syncSSProjects(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()

	args := map[string]any{
		"pageIndex": queryInt(r, "page", 1),
		"pageSize":  queryInt(r, "size", 50),
	}
	if q := r.URL.Query().Get("search"); q != "" {
		args["search"] = q
	}
	text, err := cl.Call(ctx, "list_projects_data", args)
	if err != nil {
		writeErr(w, 502, errSSListProjectsPrefix+err.Error())
		return
	}
	// {result:{All:[{id,name,logo,AssetCount,tag}], <tag>:[...]}, tag:{...}}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
		Tag    map[string]int             `json:"tag"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		writeErr(w, 502, errSSParseProjectsPrefix+err.Error())
		return
	}
	projects := json.RawMessage("[]")
	if raw, ok := env.Result["All"]; ok && len(raw) > 0 {
		projects = raw
	}
	writeJSON(w, 200, map[string]any{"projects": projects, "tag": env.Tag})
}

// ---------- GET /api/sync/scopesentry/tasks ----------

func (s *Server) syncSSTasks(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()

	args := map[string]any{
		"pageIndex": queryInt(r, "page", 1),
		"pageSize":  queryInt(r, "size", 50),
	}
	if q := r.URL.Query().Get("search"); q != "" {
		args["search"] = q
	}
	text, err := cl.Call(ctx, "list_tasks", args)
	if err != nil {
		writeErr(w, 502, errSSListTasksPrefix+err.Error())
		return
	}
	var env struct {
		List json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		writeErr(w, 502, errSSParseTasksPrefix+err.Error())
		return
	}
	tasks := env.List
	if len(tasks) == 0 {
		tasks = json.RawMessage("[]")
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

// ---------- POST /api/sync/scopesentry/sync ----------

type ssSyncReq struct {
	Dimension     string   `json:"dimension"`   // "project" | "task"
	Targets       []string `json:"targets"`     // project ObjectIDs, or task names
	AssetTypes    []string `json:"asset_types"` // subdomain | app | service
	CreateCompany bool     `json:"create_company"`
	PageSize      int      `json:"page_size"`
}

func (s *Server) syncSSRun(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	as := s.assetStore()
	cs := s.companyStore()
	if as == nil || cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req ssSyncReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Dimension != "project" && req.Dimension != "task" {
		writeErr(w, 400, errSSDimension)
		return
	}
	if len(req.Targets) == 0 {
		writeErr(w, 400, errSSTargetsEmpty)
		return
	}
	if len(req.AssetTypes) == 0 {
		req.AssetTypes = []string{"subdomain", "service", "app"}
	}
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 500 {
		pageSize = syncDefaultPage
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()

	synced := map[string]int{"subdomain": 0, "app": 0, "service": 0, "ip": 0}
	var companies []string
	var warnings, errs []string
	madeCompany := false

	for _, target := range req.Targets {
		filter := map[string]any{}
		switch req.Dimension {
		case "project":
			filter["project"] = []string{target}
			if req.CreateCompany {
				name, roots, perr := s.ssProjectMeta(ctx, cl, target)
				if perr != nil {
					warnings = append(warnings, fmt.Sprintf(warnSSProjectMetaFmt, target, perr))
				} else if name != "" {
					cid, cerr := cs.UpsertByName(name)
					if cerr != nil {
						warnings = append(warnings, fmt.Sprintf(warnSSCompanyCreateFmt, name, cerr))
					} else {
						companies = append(companies, name)
						madeCompany = true
						if len(roots) > 0 {
							cs.AddScope(cid, roots, "scopesentry:"+name)
						}
					}
				}
			}
		case "task":
			filter["task"] = []string{target}
		}

		for _, at := range req.AssetTypes {
			ssType, ok := map[string]string{"subdomain": "subdomain", "service": "asset", "app": "app"}[at]
			if !ok {
				warnings = append(warnings, warnSSUnknownAssetPrefix+at)
				continue
			}
			items, truncated, ferr := s.ssPageAll(ctx, cl, ssType, filter, pageSize)
			if ferr != nil {
				errs = append(errs, fmt.Sprintf(trMsg(errSSFetchFmt), at, target, ferr))
				continue
			}
			if truncated {
				warnings = append(warnings, fmt.Sprintf(warnSSTruncatedFmt, at, target, syncMaxPerType))
			}
			for _, raw := range items {
				if e := s.ssIngest(as, at, raw, synced); e != "" {
					errs = append(errs, e)
				}
			}
		}
	}

	// Rebuild company attribution so freshly-synced assets attach to their company
	// (scope was written above; assets came after).
	if madeCompany {
		_ = cs.RecomputeAttribution()
	}

	writeJSON(w, 200, map[string]any{
		"synced":    synced,
		"companies": companies,
		"warnings":  warnings,
		"errors":    errs,
	})
}

// ssProjectMeta fetches a project's display name and its target root domains
// (get_project.target is a newline-separated list) for company + scope creation.
func (s *Server) ssProjectMeta(ctx context.Context, cl *mcphttp.Client, projectID string) (name string, roots []string, err error) {
	text, err := cl.Call(ctx, "get_project", map[string]any{"id": projectID})
	if err != nil {
		return "", nil, err
	}
	var p struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(p.Target, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			roots = append(roots, t)
		}
	}
	return p.Name, roots, nil
}

// ssPageAll pages through list_assets for one asset_type + filter until a short/empty
// page or the protection cap. The {list:[...]} envelope has no total, so we stop when
// a page returns fewer than pageSize items.
func (s *Server) ssPageAll(ctx context.Context, cl *mcphttp.Client, ssType string, filter map[string]any, pageSize int) (items []json.RawMessage, truncated bool, err error) {
	for page := 1; ; page++ {
		args := map[string]any{"asset_type": ssType, "pageIndex": page, "pageSize": pageSize}
		if len(filter) > 0 {
			args["filter"] = filter
		}
		text, cerr := cl.Call(ctx, "list_assets", args)
		if cerr != nil {
			return items, truncated, cerr
		}
		var env struct {
			List []json.RawMessage `json:"list"`
		}
		if uerr := json.Unmarshal([]byte(text), &env); uerr != nil {
			return items, truncated, uerr
		}
		if len(env.List) == 0 {
			break
		}
		items = append(items, env.List...)
		if len(items) >= syncMaxPerType {
			items = items[:syncMaxPerType]
			truncated = true
			break
		}
		if len(env.List) < pageSize {
			break
		}
	}
	return items, truncated, nil
}

// ssIngest maps one ScopeSentry asset JSON to the ARTEX asset store and upserts it.
// Returns a non-empty error string on failure. synced is incremented per kind.
func (s *Server) ssIngest(as *db.AssetStore, assetType string, raw json.RawMessage, synced map[string]int) string {
	switch assetType {
	case "subdomain":
		var it struct {
			Host  string   `json:"host"`
			Type  string   `json:"type"`
			Value []string `json:"value"`
			IP    []string `json:"ip"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return errSSParseSubdomainPrefix + err.Error()
		}
		if it.Host == "" {
			return ""
		}
		if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: it.Host, RecordType: it.Type, RecordValue: it.Value}); err != nil {
			return "subdomain " + it.Host + ": " + err.Error()
		}
		synced["subdomain"]++
		for _, ip := range it.IP {
			if ip != "" {
				if _, err := as.UpsertIP(db.UpsertIPReq{IP: ip, BoundDomains: []string{it.Host}}); err == nil {
					synced["ip"]++
				}
			}
		}
	case "app":
		var it struct {
			Name        string `json:"name"`
			Category    string `json:"category"`
			Description string `json:"description"`
			ICP         string `json:"icp"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return errSSParseAppPrefix + err.Error()
		}
		if it.Name == "" {
			return ""
		}
		if _, err := as.UpsertApp(db.UpsertAppReq{Name: it.Name, Category: it.Category, Description: it.Description, ICP: it.ICP}); err != nil {
			return "app " + it.Name + ": " + err.Error()
		}
		synced["app"]++
	case "service":
		var it struct {
			Domain   string   `json:"domain"`
			IP       string   `json:"ip"`
			Port     string   `json:"port"`
			Service  string   `json:"service"`
			URL      string   `json:"url"`
			Title    string   `json:"title"`
			Status   *int     `json:"status"`
			Products []string `json:"products"`
			Icon     string   `json:"icon"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return errSSParseServicePrefix + err.Error()
		}
		if it.Service == "http" || it.URL != "" {
			if it.URL == "" {
				return ""
			}
			if _, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
				URL: it.URL, Technologies: it.Products, StatusCode: it.Status,
				PageTitle: it.Title, FaviconMMH3: it.Icon, IP: it.IP,
			}); err != nil {
				return "service " + it.URL + ": " + err.Error()
			}
		} else {
			port, _ := strconv.Atoi(it.Port)
			if _, err := as.UpsertOtherService(db.UpsertOtherServiceReq{
				Domain: it.Domain, IP: it.IP, Port: port, ServiceName: it.Service,
			}); err != nil {
				return "service " + it.IP + ":" + it.Port + ": " + err.Error()
			}
		}
		synced["service"]++
	}
	return ""
}

// queryInt reads an int query param with a default fallback.
func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
