package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPConfig 是 Streamable HTTP 传输的配置。
type HTTPConfig struct {
	// Authorize 在每个请求上执行；返回非 nil 即回 401。nil = 不鉴权（仅测试用）。
	Authorize func(r *http.Request) error
	// AllowOrigin 校验浏览器来源；nil = 拒绝一切带 Origin 的请求（防 DNS rebinding）。
	AllowOrigin func(origin string) bool
	// MaxBody 是请求体上限，默认 4 MiB。
	MaxBody int64
}

// HTTPHandler 实现 MCP Streamable HTTP 传输的「无状态」子集：
//
//	POST /mcp   —— 提交单条或批量 JSON-RPC，回 application/json 或 text/event-stream
//	GET /mcp    —— 405：本实现不提供服务端主动推送的长连接
//	DELETE /mcp —— 405：无状态实现没有可删除的会话
//	OPTIONS     —— 204 + CORS 预检（浏览器里的客户端需要）
//
// 规范允许服务端在 POST 上二选一回 JSON 或 SSE；这里默认回 JSON，
// 只有客户端明确「只要 event-stream」时才退回 SSE，兼容两种解析器。
func (s *Server) HTTPHandler(cfg HTTPConfig) http.Handler {
	maxBody := cfg.MaxBody
	if maxBody <= 0 {
		maxBody = 4 << 20
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("MCP-Protocol-Version", ProtocolVersion)

		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			if cfg.AllowOrigin == nil || !cfg.AllowOrigin(origin) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, MCP-Protocol-Version, Mcp-Session-Id")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPost:
			// 唯一入口，往下走。
		case http.MethodGet, http.MethodDelete:
			w.Header().Set("Allow", "POST, OPTIONS")
			writeRPC(w, http.StatusMethodNotAllowed, errorResponse(nil, CodeInvalidRequest,
				"stateless server: only POST /mcp is supported"))
			return
		default:
			w.Header().Set("Allow", "POST, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if cfg.Authorize != nil {
			if err := cfg.Authorize(r); err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="artex-mcp"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			writeRPC(w, http.StatusBadRequest, errorResponse(nil, CodeParseError, "read body: "+err.Error()))
			return
		}
		if int64(len(body)) > maxBody {
			writeRPC(w, http.StatusRequestEntityTooLarge,
				errorResponse(nil, CodeInvalidRequest, fmt.Sprintf("body exceeds %d bytes", maxBody)))
			return
		}

		resp := s.HandleFrame(r.Context(), body)
		if resp == nil {
			// 纯通知：规范允许 202 无内容。
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if wantsSSE(r) {
			writeSSE(w, resp)
			return
		}
		writeRPC(w, http.StatusOK, resp)
	})
}

func writeRPC(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeSSE(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// wantsSSE 只在客户端「明确只要事件流」时返回 true；
// 同时接受 application/json 或 */* 的客户端拿到更省事的 JSON。
func wantsSSE(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if !strings.Contains(accept, "text/event-stream") {
		return false
	}
	return !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*")
}
