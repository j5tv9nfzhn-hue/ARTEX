package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/Autumn-27/artex/db"
)

// These routes live under the existing authenticated handler. No background
// cleanup or model-callable tool is registered. Mutations require JSON and, for
// browser requests that carry Origin, a same-host Origin.
func (s *Server) retentionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/retention/policy", s.getRetentionPolicy)
	mux.HandleFunc("PUT /api/retention/policy", s.putRetentionPolicy)
	mux.HandleFunc("POST /api/retention/preview", s.previewRetention)
	mux.HandleFunc("POST /api/retention/execute", s.executeRetention)
	mux.HandleFunc("GET /api/retention/audit", s.getRetentionAudit)
}

func (s *Server) retentionAvailable(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.m == nil || s.m.pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "数据库未就绪")
		return false
	}
	return true
}

func decodeRetentionJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "此操作要求 application/json")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != r.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			writeErr(w, http.StatusForbidden, "拒绝跨来源数据维护请求")
			return false
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	raw, err := io.ReadAll(r.Body)
	trimmed := bytes.TrimSpace(raw)
	if err != nil || len(trimmed) == 0 || trimmed[0] != '{' {
		writeErr(w, http.StatusBadRequest, "需要不超过 4 KiB 的 JSON 对象")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 参数或字段无效")
		return false
	}
	if dec.Decode(new(any)) != io.EOF {
		writeErr(w, http.StatusBadRequest, "只允许一个 JSON 对象")
		return false
	}
	return true
}

func (s *Server) retentionError(w http.ResponseWriter, r *http.Request, action string, err error) {
	// Never log a confirmation token or request body. Audit failures are visible,
	// not silently presented as a successful audit of a rejected operation.
	log.Printf("[retention] %s 失败：%v", action, err)
	auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if auditErr := s.m.pg.RecordRetentionFailure(auditCtx, action); auditErr != nil {
		log.Printf("[retention] 失败请求审计未写入：%v", auditErr)
	}
	switch {
	case errors.Is(err, db.ErrRetentionDisabled), errors.Is(err, db.ErrRetentionStale):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusServiceUnavailable, "数据维护请求已取消或超时；不要假定成功，请查询审计并重新预览")
	default:
		writeErr(w, http.StatusInternalServerError, "数据维护失败；请检查服务端日志与审计，未返回成功结果")
	}
}

func (s *Server) getRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.retentionAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, err := s.m.pg.RetentionPolicy(ctx)
	if err != nil {
		s.retentionError(w, r, "policy_read", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": p, "automatic_cleanup": false, "preview_ttl_seconds": 900, "scope": "人工执行；只清理保守筛选的无任务 usage、低级别非安全日志及已失去业务引用的终态通知。任务、证据、样本、审批、流量及计费任务账本不清理。"})
}

func (s *Server) putRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.retentionAvailable(w) {
		return
	}
	p := db.DefaultRetentionPolicy()
	if !decodeRetentionJSON(w, r, &p) {
		return
	}
	if err := p.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.m.pg.SaveRetentionPolicy(ctx, p); err != nil {
		s.retentionError(w, r, "policy_save", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": p, "automatic_cleanup": false, "old_previews_invalidated": true})
}

func (s *Server) previewRetention(w http.ResponseWriter, r *http.Request) {
	if !s.retentionAvailable(w) {
		return
	}
	var req struct{}
	if !decodeRetentionJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	preview, err := s.m.pg.CreateRetentionPreview(ctx)
	if err != nil {
		s.retentionError(w, r, "preview", err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) executeRetention(w http.ResponseWriter, r *http.Request) {
	if !s.retentionAvailable(w) {
		return
	}
	var req struct {
		Token        string `json:"confirmation_token"`
		Confirmation string `json:"confirmation"`
	}
	if !decodeRetentionJSON(w, r, &req) {
		return
	}
	if req.Confirmation != "DELETE_PREVIEWED_ROWS" || len(req.Token) != 64 {
		writeErr(w, http.StatusBadRequest, "必须明确确认 DELETE_PREVIEWED_ROWS 并提供有效预览令牌")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := s.m.pg.ExecuteRetention(ctx, req.Token)
	if err != nil {
		s.retentionError(w, r, "execute", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getRetentionAudit(w http.ResponseWriter, r *http.Request) {
	if !s.retentionAvailable(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	entries, err := s.m.pg.RetentionAudit(ctx)
	if err != nil {
		s.retentionError(w, r, "audit_read", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "limit": 100})
}
