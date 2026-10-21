package server

import (
	"net/http"

	"github.com/Autumn-27/artex/mcpserver"
)

// capabilitiesHandler 输出能力清单：谁暴露给外部 AI、谁永远不暴露、为什么。
//
// 这不是给用户看的花名册，是给前端与 CI 对账用的：
// 「接口有路由」和「AI 能调」是不是同一件事，靠这张表能一眼看出来。
func (s *Server) capabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	reg := buildRegistry(s, mcpFileGuard())
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    s.mcpEnabled(),
		"tiers":      tierNames(s.mcpTiers()),
		"exposed":    reg.Manifest(),
		"denied":     Denied(),
		"tool_count": len(reg.Tools(mcpserver.L3)),
	})
}

func tierNames(tiers []mcpserver.Tier) []string {
	out := make([]string, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, t.String())
	}
	return out
}
