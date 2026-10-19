package server

// software_re_export.go 落实逆向契约的「完整导出」：按契约操作经既有 reQuery
// 通道重放查询，汇总为带清单（manifest）的导出包。导出复用同一条证据与行归一
// 化管线，不直接触碰引擎 MCP，不落盘样本本体，也不导出引擎私有工程数据库。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// 导出配额（契约层强制）。
const (
	reExportMaxSections  = 12
	reExportMaxBytes     = 4 << 20
	reExportMaxLimit     = 256
	reExportDefaultLimit = 100
)

// reExportDefaultOps 是完整导出默认覆盖的无地址操作。
// decompile / xrefs / callgraph 需显式地址，只能通过附加步骤导出。
var reExportDefaultOps = []string{"status", "functions", "modules", "imports", "exports", "strings"}

// reExportQueriable 是允许出现在导出步骤中的查询操作全集。
var reExportQueriable = map[string]bool{
	"status": true, "functions": true, "decompile": true, "xrefs": true,
	"callgraph": true, "types": true, "modules": true, "imports": true,
	"exports": true, "strings": true,
}

// reExportStep 描述一步导出查询。address 必须遵守会话的 address_kind 纪律
// （REA 为 RVA；strings 行为 file_offset），禁止混用 ASLR 运行地址。
type reExportStep struct {
	Operation string `json:"operation"`
	Address   string `json:"address,omitempty"`
	Query     string `json:"query,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// reExportBundle 按契约执行完整导出。operation/address/query/limit 允许追加
// 一个显式地址步骤（如 decompile），其余步骤固定为无地址默认集；失败的步骤
// 记入 sections 的 is_error 项，不中断整包导出。
func (s *Server) reExportBundle(ctx context.Context, taskID, sessionID int64, operation, address, query string, limit int) (map[string]any, error) {
	sess, err := s.m.pg.RESession(taskID, sessionID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > reExportMaxLimit {
		limit = reExportDefaultLimit
	}
	steps := make([]reExportStep, 0, len(reExportDefaultOps)+1)
	for _, op := range reExportDefaultOps {
		steps = append(steps, reExportStep{Operation: op})
	}
	if operation != "" {
		if !reExportQueriable[operation] {
			return nil, fmt.Errorf("导出不支持操作 %s", operation)
		}
		steps = append(steps, reExportStep{Operation: operation, Address: address, Query: query})
	}
	sections := make([]map[string]any, 0, len(steps))
	total := 0
	truncated := false
	for _, st := range steps {
		if len(sections) >= reExportMaxSections || total >= reExportMaxBytes {
			truncated = true
			break
		}
		v, qerr := s.reQuery(ctx, taskID, sessionID, st.Operation, st.Address, st.Query, limit)
		if qerr != nil {
			sections = append(sections, map[string]any{
				"operation": st.Operation, "address": st.Address,
				"is_error": true, "diagnostic": qerr.Error(),
			})
			continue
		}
		raw, merr := json.Marshal(v)
		if merr != nil {
			return nil, merr
		}
		total += len(raw)
		sec := map[string]any{"operation": st.Operation, "data": v}
		if st.Address != "" {
			sec["address"] = st.Address
		}
		if total > reExportMaxBytes {
			sec["truncated"] = true
			truncated = true
		}
		sections = append(sections, sec)
	}
	kind, tier := "", ""
	if provider, perr := s.m.pg.REProvider(sess.ProviderID); perr == nil {
		kind = provider.Kind
		tier = reContractReport(*provider).Tier
	}
	return map[string]any{
		"manifest": map[string]any{
			"task_id":     taskID,
			"session_id":  sessionID,
			"sample_sha":  sess.SampleSHA,
			"provider_id": sess.ProviderID,
			"kind":        kind,
			"tier":        tier,
			"exported_at": time.Now().UTC().Format(time.RFC3339),
			"scope":       "完整导出按契约操作经 reQuery 重放；不含样本本体与引擎私有工程数据库",
		},
		"sections":  sections,
		"bytes":     total,
		"truncated": truncated,
	}, nil
}

// reExportHTTP：POST /api/tasks/{id}/re/sessions/{session}/export
// body 允许附加一个显式地址步骤：
//
//	{"operation":"decompile","address":"0x401000","query":"...","limit":100}
func (s *Server) reExportHTTP(w http.ResponseWriter, r *http.Request) {
	_, taskID, ok := s.reTask(w, r)
	if !ok {
		return
	}
	sessionID := reID(r.PathValue("session"))
	var a struct {
		Operation string `json:"operation"`
		Address   string `json:"address"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
	}
	if !reDecode(w, r, &a) {
		return
	}
	v, err := s.reExportBundle(r.Context(), taskID, sessionID, a.Operation, a.Address, a.Query, a.Limit)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
