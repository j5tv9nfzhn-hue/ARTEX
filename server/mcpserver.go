package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpserver"
)

// 对外 MCP 接入：ARTEX 不再当第 N 个 coding harness，而是一台「所有 harness
// 都能挂上的攻坚工具箱」。
//
// 设计要点
//   - 传输：stdio（`artex mcp serve`）+ Streamable HTTP（POST /mcp，Bearer）。
//   - 档位：L0 离线分析 / L1 知识库只读 / L2 主动探测 / L3 任务编排，
//     默认只开 L0+L1；开 L2 是控制台里的显式动作，开 L3 同理。
//   - 无状态：不维护会话，每次请求按当前设置重建工具面，改完开关立即生效。
//   - 计量：每次 tools/call 写一行 tool_usage（agent_key=mcp-server），
//     与控制台内部调用同一本账。
//   - 永不出：shell / 文件写入 / 删除 / 拦截规则 / LLM 配置。

// defaultMCPTiers 是没配置过时的档位：离线分析 + 只读知识库。
func defaultMCPTiers() []mcpserver.Tier {
	return []mcpserver.Tier{mcpserver.L0, mcpserver.L1}
}

// mcpEnabled 表示 /mcp 是否对外服务；默认开（仍需 Bearer token）。
func (s *Server) mcpEnabled() bool {
	if s.m == nil || s.m.pg == nil {
		return false
	}
	return s.m.pg.GetBool(settingMCPEnabled, true)
}

// mcpTiers 解析设置里的档位串；非法项被忽略，空结果回默认档。
func (s *Server) mcpTiers() []mcpserver.Tier {
	if s.m == nil || s.m.pg == nil {
		return defaultMCPTiers()
	}
	raw, ok, err := s.m.pg.GetSetting(settingMCPTiers)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return defaultMCPTiers()
	}
	seen := map[mcpserver.Tier]bool{}
	out := make([]mcpserver.Tier, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		t, ok := mcpserver.ParseTier(part)
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return defaultMCPTiers()
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Server) saveMCPTiers(tiers []mcpserver.Tier) error {
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, t.String())
	}
	return s.m.pg.SetSetting(settingMCPTiers, strings.Join(parts, ","))
}

// mcpFileGuard 决定 L0 工具能读哪些文件。
//
// 默认放行本机任意可读路径（分析样本本来就散落在桌面/下载目录），但仍拦
// ARTEX 自己的凭据文件与常见密钥库；设了 ARTEX_MCP_FILE_ROOT 就收紧到该目录。
func mcpFileGuard() *mcpserver.FileGuard {
	if root := strings.TrimSpace(os.Getenv("ARTEX_MCP_FILE_ROOT")); root != "" {
		return &mcpserver.FileGuard{Root: root}
	}
	return &mcpserver.FileGuard{AllowAny: true}
}

// buildMCPServer 按当前设置装配工具面。每次请求重建：工具是闭包，成本可忽略，
// 换来的是「改完开关立即生效」，没有缓存失效的坑。
func (s *Server) buildMCPServer() *mcpserver.Server {
	return mcpBuild(s, s.mcpTiers(), mcpFileGuard(), s.recordMCPUsage)
}

// NewMCPStdioServer 用一条独立数据库连接装配 MCP 服务端，供 `artex mcp serve`
// 的 stdio 模式使用：不起 Manager、不监听端口、不读 JWT。
// NewMCPHeadlessServer 给「按需拉起、用完自退」的后台实例用。
//
// 与 stdio 同源构建，但保留 L3：stdio 剔掉 L3 是因为它没有控制台进程承接委派；
// headless 只在控制台活着时才会被拉起，控制台不在时 L3 调用会明确报错。
func NewMCPHeadlessServer(pg *db.DB, tiers []mcpserver.Tier, fileRoot string) *mcpserver.Server {
	guard := &mcpserver.FileGuard{AllowAny: true}
	if root := strings.TrimSpace(fileRoot); root != "" {
		guard = &mcpserver.FileGuard{Root: root}
	}
	return mcpBuild(&Server{m: &Manager{pg: pg}}, tiers, guard, nil)
}

// NewMCPStdioBridgeServer 与 stdio 同源，但保留 L3 —— L3 的 handler 换成
// 「转发给控制台」。调用方（cmd/artex）负责在 bridge 里做探活与报错。
//
// 为什么不在这里直接连：server 包不该知道 8787 这个地址，也不该管进程。
func NewMCPStdioBridgeServer(pg *db.DB, tiers []mcpserver.Tier, fileRoot string,
	bridge func(ctx context.Context, name string, args json.RawMessage) (string, error)) *mcpserver.Server {
	guard := &mcpserver.FileGuard{AllowAny: true}
	if root := strings.TrimSpace(fileRoot); root != "" {
		guard = &mcpserver.FileGuard{Root: root}
	}
	owner := &Server{m: &Manager{pg: pg}, l3Bridge: bridge}
	return mcpBuild(owner, tiers, guard, nil)
}

// MCPTiersFromSettings 给 CLI 用：读控制台里保存的档位。
//
// `mcp serve --auto` 没显式给 --tiers 时用它，这样「外部 AI 能拿到什么」由界面
// 上的开关决定，而不是由某台机器上的一次命令行参数决定。
func MCPTiersFromSettings(pg *db.DB) []mcpserver.Tier {
	return (&Server{m: &Manager{pg: pg}}).mcpTiers()
}

func NewMCPStdioServer(pg *db.DB, tiers []mcpserver.Tier, fileRoot string) *mcpserver.Server {
	guard := &mcpserver.FileGuard{AllowAny: true}
	if root := strings.TrimSpace(fileRoot); root != "" {
		guard = &mcpserver.FileGuard{Root: root}
	}
	// 独立进程里没有正在跑的引擎：L3 建的任务不会被任何人执行，
	// 所以 stdio 模式一律不收 L3（要委派请走控制台进程的 /mcp）。
	kept := make([]mcpserver.Tier, 0, len(tiers))
	for _, t := range tiers {
		if t != mcpserver.L3 {
			kept = append(kept, t)
		}
	}
	return mcpBuild(&Server{m: &Manager{pg: pg}}, kept, guard, nil)
}

// mcpBuild 是两条入口（控制台 HTTP / CLI stdio）共用的装配函数。
func mcpBuild(owner *Server, tiers []mcpserver.Tier, guard *mcpserver.FileGuard, audit func(mcpserver.AuditEvent)) *mcpserver.Server {
	// 工具面从能力注册表生成：暴露与否是注册表里的数据，不是文档约定。
	// 顺序与历史行为一致（L0 → L1 → L2 recon → L2 webscan → L3），
	// 由 TestCapabilityGolden 逐字节钉住，改动必须显式更新 golden。
	tools := buildRegistry(owner, guard).Tools(mcpserver.L3)

	return mcpserver.New(mcpserver.Config{
		Name:         "artex",
		Title:        "ARTEX-REA",
		Version:      BuildVersion,
		Instructions: mcpInstructions() + mcpHostHints(),
		Tiers:        tiers,
		Tools:        tools,
		Resources:    owner.mcpResources(),
		ReadResource: owner.mcpReadResource,
		Prompts:      mcpPrompts(),
		GetPrompt:    owner.mcpGetPrompt,
		ToolTimeout:  180 * time.Second, // 180s：port_probe / http_fuzz 这类批量探测要留够时间
		Audit:        audit,
	})
}

func mcpInstructions() string {
	return strings.Join([]string{
		"ARTEX-REA MCP 服务器。它把『验证』这一步交给本地资产图与证据链，而不是让模型凭感觉下结论。",
		"",
		"分工建议：",
		"- 代码/命令层面的探索留在你自己的 shell；一旦需要『这条路径到底通不通』，调用 artex 工具并引用返回的证据。",
		"- 样本分诊先 file_identify → pe_info/entropy_scan → strings_scan → ioc_extract，再决定要不要深挖。",
		"- 资产与历史漏洞用 list_assets / list_findings / tactical_kb 查，不要重复扫描已知目标。",
		"- 主动探测（dns_recon / port_probe / http_*）只在明确授权范围内使用；它们只做验证，不做利用。",
		"",
		"所有工具只读或只做验证，不提供 shell、文件写入与删除能力。",
	}, "\n")
}

// mcpResources 暴露三份只读快照。资源名固定，客户端可以缓存 URI，内容每次现取。
func (s *Server) mcpResources() []mcpserver.Resource {
	return []mcpserver.Resource{
		{URI: "artex://assets", Name: "assets", Title: "资产图概览", Description: "按类型统计的资产数量与最近更新的条目", MIMEType: "application/json"},
		{URI: "artex://findings", Name: "findings", Title: "漏洞发现（最近 50 条）", Description: "跨任务最近发现，含等级、状态与摘要", MIMEType: "application/json"},
		{URI: "artex://tasks", Name: "tasks", Title: "任务列表", Description: "任务的 id / 名称 / 状态 / 域", MIMEType: "application/json"},
	}
}

func (s *Server) mcpReadResource(ctx context.Context, uri string) (string, string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", "", fmt.Errorf("数据库未就绪")
	}
	switch strings.TrimSpace(uri) {
	case "artex://assets":
		out := map[string]any{}
		for _, typ := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
			if n, err := s.m.pg.Assets().CountByType(typ); err == nil {
				out[typ] = n
			}
		}
		return mcpJSON(out)
	case "artex://findings":
		rows, err := s.m.pg.ListFindings(50)
		if err != nil {
			return "", "", err
		}
		return mcpJSON(rows)
	case "artex://tasks":
		rows, err := s.m.pg.ListTasks()
		if err != nil {
			return "", "", err
		}
		return mcpJSON(mcpTaskBriefs(rows, 0))
	}
	return "", "", fmt.Errorf("未知资源: %s", uri)
}

func mcpPrompts() []mcpserver.Prompt {
	return []mcpserver.Prompt{
		{
			Name:        "web-pentest",
			Title:       "Web 渗透验证",
			Description: "按 ARTEX 的证据链规范做一次只验证不破坏的 Web 安全检查",
			Arguments: []mcpserver.PromptArgument{
				{Name: "target", Description: "目标 URL 或域名", Required: true},
				{Name: "scope", Description: "授权范围与禁止动作", Required: false},
			},
		},
		{
			Name:        "pe-triage",
			Title:       "样本分诊",
			Description: "对单个可执行样本做静态分诊，产出结论与证据",
			Arguments: []mcpserver.PromptArgument{
				{Name: "path", Description: "样本路径", Required: true},
			},
		},
	}
}

func (s *Server) mcpGetPrompt(_ context.Context, name string, args map[string]string) (string, error) {
	switch strings.TrimSpace(name) {
	case "web-pentest":
		target := strings.TrimSpace(args["target"])
		if target == "" {
			return "", fmt.Errorf("缺少 target")
		}
		scope := strings.TrimSpace(args["scope"])
		if scope == "" {
			scope = "只做验证，不做破坏性利用；不碰非授权域名"
		}
		return fmt.Sprintf(strings.Join([]string{
			"对 %s 做一次只验证不破坏的安全检查。约束：%s。",
			"",
			"步骤：",
			"1. 先用 artex 的 dns_recon / tech_hints 摸清面，再用 list_assets 看历史资产，避免重复扫描。",
			"2. 用 list_findings 与 tactical_kb 查已知问题，优先复现历史结论。",
			"3. 每条结论必须附证据：调用 http_send / http_fuzz 拿到的请求与响应片段，或 tls_inspect 的证书字段。",
			"4. 结论按 已确认 / 可疑 / 未发现 三档给出，未验证的不要写成漏洞。",
			"", "输出用中文，先给结论表，再给证据。",
		}, "\n"), target, scope), nil
	case "pe-triage":
		p := strings.TrimSpace(args["path"])
		if p == "" {
			return "", fmt.Errorf("缺少 path")
		}
		return fmt.Sprintf(strings.Join([]string{
			"对样本 %s 做静态分诊。约束：不执行样本，只读文件。",
			"",
			"顺序：file_identify → pe_info（若有 PE 头）→ entropy_scan（看是否加壳/压缩）→ strings_scan（找 URL、命令、注册表、错误串）→ ioc_extract（汇总 IOC）。",
			"产出：文件类型与架构、编译线索、加壳判断（附熵值）、网络与持久化 IOC 表、下一步建议（动态分析/逆向）。",
			"每条结论后面写它来自哪个工具与哪个偏移/字段。",
		}, "\n"), p), nil
	}
	return "", fmt.Errorf("未知提示模板: %s", name)
}

// recordMCPUsage 把一次工具调用记进 tool_usage，与控制台内部调用同一本账。
func (s *Server) recordMCPUsage(ev mcpserver.AuditEvent) {
	if s.m == nil || s.m.pg == nil {
		return
	}
	_ = s.m.pg.InsertToolUsage(&db.ToolUsage{
		ToolKey:   ev.Tool,
		AgentKey:  "mcp-server",
		SessionID: ev.Client,
	})
}

// --- HTTP 面 ---

// mcpHandler 是挂在 POST /mcp 的处理器（不在 /api 下，鉴权用 Bearer 而不是 JWT）。
func (s *Server) mcpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.mcpEnabled() {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "MCP 服务端已在控制台关闭"})
			return
		}
		s.buildMCPServer().HTTPHandler(mcpserver.HTTPConfig{
			Authorize:   s.mcpAuthorize,
			AllowOrigin: mcpOriginAllowed,
			MaxBody:     8 << 20,
		}).ServeHTTP(w, r)
	})
}

// mcpOriginAllowed 只放行本机来源：MCP 客户端一般不带 Origin，
// 浏览器里的页面只可能是本机控制台（防 DNS rebinding）。
func mcpOriginAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// --- 控制台 API ---

// mcpExePath 返回当前可执行文件路径，接入卡片据此生成 stdio 配置。
func mcpExePath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return ""
}

// mcpServerStatus 是 GET /api/mcp/server：给「对外接入」卡片用的状态。
func (s *Server) mcpServerStatus(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	tok := s.mcpTokenValue()
	tiers := s.mcpTiers()
	enabled := map[string]bool{}
	for _, t := range tiers {
		enabled[t.String()] = true
	}
	srv := s.buildMCPServer()
	all := make([]map[string]any, 0, 4)
	for _, t := range []mcpserver.Tier{mcpserver.L0, mcpserver.L1, mcpserver.L2, mcpserver.L3} {
		all = append(all, map[string]any{
			"id": t.String(), "label": t.Label(), "enabled": enabled[t.String()],
		})
	}
	tools := make([]map[string]any, 0, 32)
	for _, t := range srv.Tools() {
		tools = append(tools, map[string]any{
			"name": t.Name, "tier": t.Tier.String(), "description": t.Description,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exe":        mcpExePath(),
		"enabled":    s.mcpEnabled(),
		"tiers":      tiers,
		"all_tiers":  all,
		"endpoint":   "/mcp",
		"token":      tok,
		"token_mask": maskToken(tok),
		"tools":      tools,
		"tool_count": len(tools),
	})
}

// mcpServerUpdate 是 PUT /api/mcp/server：开关与档位。改完立即生效（无缓存）。
func (s *Server) mcpServerUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool    `json:"enabled"`
		Tiers   []string `json:"tiers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是合法 JSON"})
		return
	}
	if req.Tiers != nil {
		tiers := make([]mcpserver.Tier, 0, len(req.Tiers))
		seen := map[mcpserver.Tier]bool{}
		for _, raw := range req.Tiers {
			t, ok := mcpserver.ParseTier(raw)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "未知档位: " + raw})
				return
			}
			if seen[t] {
				continue
			}
			seen[t] = true
			tiers = append(tiers, t)
		}
		if len(tiers) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "至少要保留一个档位"})
			return
		}
		if err := s.saveMCPTiers(tiers); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	if req.Enabled != nil {
		if err := s.m.pg.SetBool(settingMCPEnabled, *req.Enabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	s.mcpServerStatus(w, r)
}

// mcpServerRotate 是 POST /api/mcp/server/rotate：换 token，旧配置立即失效。
func (s *Server) mcpServerRotate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.rotateMCPToken(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.mcpServerStatus(w, r)
}

// mcpJSON 把任意结构序列化成紧凑 JSON 文本（工具输出经此回到模型）。
func mcpJSON(v any) (string, string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", "", err
	}
	if len(b) > 256<<10 {
		b = append(b[:256<<10], []byte("\n…（输出已截断）")...)
	}
	return "application/json", string(b), nil
}

// mcpTaskBriefs 把任务压成列表视图，只留外部 harness 关心的字段。
func mcpTaskBriefs(rows []*db.Task, limit int) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for i, t := range rows {
		if limit > 0 && i >= limit {
			break
		}
		out = append(out, map[string]any{
			"id": t.ID, "name": t.Name, "status": t.Status,
			"analysis_domain": t.AnalysisDomain, "created_at": t.CreatedAt,
			"goal": clipRunes(t.Goal, 200),
		})
	}
	return out
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
