package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// 外部接入页的三个接口：
//
//	GET  /api/harness          扫一遍本机（只读）
//	POST /api/harness/plan     算出会写什么（只读，返回 diff）
//	POST /api/harness/apply    真写（备份 + 原子替换 + 回读校验）
//
// 三个接口都只认 target id，不认路径：路径由服务端从注册表现算。
// 这样「前端让我写哪个文件」这件事根本不存在，写了一堆越权校验也就没用了。

type harnessRequestBody struct {
	Targets    []string `json:"targets"`
	ProjectDir string   `json:"project_dir"`
}

// harnessBaseURL 用请求自己的 Host 拼端点。
//
// 不能用固定 127.0.0.1:8787：用户可能改了 -addr，也可能从局域网访问。
// 配置要写进客户端的是「这台控制台此刻在哪个地址」，那就是 r.Host。
func harnessBaseURL(r *http.Request) string {
	host := strings.TrimSpace(r.Host)
	if host == "" || strings.ContainsAny(host, " \t\r\n/") {
		host = "127.0.0.1:8787"
	}
	return "http://" + host
}

// harnessListHandler 是 GET /api/harness：扫描结果 + 当前端点信息。
func (s *Server) harnessListHandler(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	reg, err := HarnessRegistryData()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	projectDir := strings.TrimSpace(r.URL.Query().Get("project_dir"))
	clients := harnessScanAll(projectDir)

	writeJSON(w, http.StatusOK, map[string]any{
		"version":     reg.Version,
		"generated":   reg.GeneratedAt,
		"project_dir": projectDir,
		"endpoint":    harnessBaseURL(r) + "/mcp",
		"exe":         mcpExePath(),
		"token_mask":  maskToken(s.mcpTokenValue()),
		"stats":       harnessStats(clients),
		"clients":     clients,
	})
}

// harnessPlanHandler 是 POST /api/harness/plan：只算不写。
func (s *Server) harnessPlanHandler(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req harnessRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是合法 JSON"})
		return
	}
	targets := harnessCleanTargets(req.Targets)
	if len(targets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有选择任何客户端"})
		return
	}
	in := HarnessPlanInput{
		Targets:    targets,
		ProjectDir: strings.TrimSpace(req.ProjectDir),
		BaseURL:    harnessBaseURL(r),
		Token:      s.mcpTokenValue(),
		Exe:        mcpExePath(),
	}
	changes := harnessPlanAll(in)
	writeJSON(w, http.StatusOK, map[string]any{
		"changes": changes,
		"summary": harnessPlanSummary(changes),
	})
}

// harnessApplyHandler 是 POST /api/harness/apply：重新 plan 再落盘。
//
// 注意「重新 plan」：请求里的 diff/handle 一律不采信，避免前端缓存过期后
// 写入一份用户没看过的内容。计划可以变，因为磁盘可能刚被人改过。
func (s *Server) harnessApplyHandler(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req harnessRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体不是合法 JSON"})
		return
	}
	targets := harnessCleanTargets(req.Targets)
	if len(targets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有选择任何客户端"})
		return
	}
	in := HarnessPlanInput{
		Targets:    targets,
		ProjectDir: strings.TrimSpace(req.ProjectDir),
		BaseURL:    harnessBaseURL(r),
		Token:      s.mcpTokenValue(),
		Exe:        mcpExePath(),
	}
	planned := harnessPlanAll(in)
	applied := harnessApplyAll(planned, in.ProjectDir, true)

	ok, failed := 0, 0
	for _, ch := range applied {
		if ch.Error != "" {
			failed++
		} else if ch.Applied {
			ok++
		}
	}
	// 写完再扫一遍：这一步才是「客户端真的变成已接入」的证据。
	//
	// 它只能证明「配置文件里现在有 artex 且我们读得到」，不能证明客户端解析成功
	// ——后者要么需要客户端自己在跑，要么需要它有 CLI 能列出 MCP。所以 B 级那批
	// 仍然标 needs_verify，不因为是「我们写的」就升格成已核实。
	states := make(map[string]string, len(targets))
	for _, st := range harnessScanAll(in.ProjectDir) {
		states[st.ID] = st.State
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"changes": applied,
		"states":  states,
		"summary": map[string]int{"applied": ok, "failed": failed, "skipped": len(applied) - ok - failed},
	})
}

// harnessCleanTargets 去空、去重、去太长的输入。
func harnessCleanTargets(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 64 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return sortedTargets(out)
}

func harnessPlanSummary(changes []HarnessChange) map[string]int {
	sum := map[string]int{"create": 0, "update": 0, "noop": 0, "manual": 0, "skip": 0}
	for _, ch := range changes {
		sum[ch.Action]++
	}
	return sum
}
