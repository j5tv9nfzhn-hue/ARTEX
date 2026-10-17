package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func (s *Server) reRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/re/samples", s.reLibrary)
	mux.HandleFunc("GET /api/re/mcp-candidates", s.reMCPCandidates)
	mux.HandleFunc("POST /api/re/providers", s.reSaveProvider)
	mux.HandleFunc("POST /api/re/providers/{provider}/discover", s.reDiscover)
	mux.HandleFunc("GET /api/tasks/{id}/re", s.reOverview)
	mux.HandleFunc("POST /api/tasks/{id}/re/samples", s.reUpload)
	mux.HandleFunc("POST /api/tasks/{id}/re/attach", s.reAttach)
	mux.HandleFunc("POST /api/tasks/{id}/re/sessions", s.reOpenHTTP)
	mux.HandleFunc("POST /api/tasks/{id}/re/sessions/{session}/query", s.reQueryHTTP)
	mux.HandleFunc("POST /api/tasks/{id}/re/sessions/{session}/control", s.reControlHTTP)
	mux.HandleFunc("GET /api/tasks/{id}/re/sessions/{session}/data", s.reDataHTTP)
	mux.HandleFunc("GET /api/tasks/{id}/re/evidence/{evidence}", s.reEvidenceHTTP)
	mux.HandleFunc("POST /api/tasks/{id}/re/observations", s.reObservation)
}
func reDecode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeErr(w, 400, err.Error())
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		writeErr(w, 400, "只允许一个 JSON 对象")
		return false
	}
	return true
}
func (s *Server) reTask(w http.ResponseWriter, r *http.Request) (*Task, int64, bool) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return nil, 0, false
	}
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "任务不存在")
		return nil, 0, false
	}
	if t.AnalysisDomain != db.AnalysisDomainSoftwareRE {
		writeErr(w, 400, "仅软件逆向任务可使用分析工作台")
		return nil, 0, false
	}
	return t, reID(t.ID), true
}
func (s *Server) reOverviewData(taskID int64) (map[string]any, error) {
	samples, err := s.m.pg.RESamples(taskID)
	if err != nil {
		return nil, err
	}
	providers, err := s.m.pg.REProviders()
	if err != nil {
		return nil, err
	}
	sessions, err := s.m.pg.RESessions(taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"samples": samples, "providers": providers, "sessions": sessions, "limit": 100, "scope": "最多 100 个样本与最近 100 个会话；Provider 目录就绪不等于分析引擎或目标就绪"}, nil
}
func (s *Server) reOverview(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	v, err := s.reOverviewData(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reLibrary(w http.ResponseWriter, r *http.Request) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	items, err := s.m.pg.RESamples(0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"samples": items, "limit": 100})
}
func (s *Server) reMCPCandidates(w http.ResponseWriter, r *http.Request) {
	ms, err := s.m.pg.ListMCP()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		items = append(items, map[string]any{"id": m.ID, "name": m.Name, "transport": m.Transport, "enabled": m.Enabled, "cached_tools": m.Tools})
	}
	// Deliberately omit URLs, commands, environment and credentials.
	writeJSON(w, 200, map[string]any{"servers": items})
}
func (s *Server) reSaveProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var p db.REProvider
	if !reDecode(w, r, &p) {
		return
	}
	if err := reValidateConfig(p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := s.reMCP(p.MCPID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	lock := s.reProviderLock(p.ID)
	if !lock.TryLock() {
		writeErr(w, 409, "Provider 正忙")
		return
	}
	defer lock.Unlock()
	id, err := s.m.pg.RESaveProvider(p)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}
func (s *Server) reDiscover(w http.ResponseWriter, r *http.Request) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	id := reID(r.PathValue("provider"))
	lock := s.reProviderLock(id)
	if !lock.TryLock() {
		writeErr(w, 409, "Provider 正忙")
		return
	}
	defer lock.Unlock()
	p, err := s.m.pg.REProvider(id)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	var active bool
	if err = s.m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM re_sessions WHERE provider_id=$1 AND state IN ('opening','ready','busy','closing','interrupted'))`, id).Scan(&active); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if active {
		writeErr(w, 409, "已有活动或不确定会话，不能启动第二个发现连接")
		return
	}
	m, err := s.reMCP(p.MCPID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	cl, err := connectMCP(ctx, m)
	if err != nil {
		_ = s.m.pg.RECatalog(id, "[]", "", "unavailable", err.Error())
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()
	tools, err := cl.Tools(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	catalog, hash := reCatalog(tools)
	if len(catalog) > 4<<20 {
		writeErr(w, 502, "工具目录超过 4 MiB 上限")
		return
	}
	state, message := "catalog_verified", "仅校验 tools/list 与映射；尚未确认引擎、许可证、目标架构或运行状态"
	if err = reValidateCatalog(*p, tools); err != nil {
		state = "mapping_invalid"
		message = err.Error()
	}
	if err = s.m.pg.RECatalog(id, catalog, hash, state, message); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	v, err := s.m.pg.REProvider(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reUpload(w http.ResponseWriter, r *http.Request) {
	_, taskID, ok := s.reTask(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (128<<20)+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		writeErr(w, 400, "只接受一个 file 样本字段")
		return
	}
	defer part.Close()
	name := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if len(name) > 256 {
		name = name[:256]
	}
	root, err := s.reStoreRoot()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	f, err := os.CreateTemp(root, ".upload-*")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	var size int64
	for {
		if err = r.Context().Err(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		n, e := part.Read(buf)
		if n > 0 {
			size += int64(n)
			if size > 128<<20 {
				writeErr(w, 413, "样本超过 128 MiB 上限")
				return
			}
			if _, err = f.Write(buf[:n]); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			_, _ = hash.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
	}
	if _, err = reader.NextPart(); err != io.EOF {
		writeErr(w, 400, "每次仅可上传一个样本")
		return
	}
	if err = f.Sync(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err = f.Close(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	path, err := s.reSamplePath(sha)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		writeErr(w, 400, "样本分区目录无效")
		return
	}
	// Atomic no-replace publication. Unsupported hard links fail rather than overwriting.
	if err = os.Link(temp, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			writeErr(w, 500, "无法原子发布样本（需要同卷硬链接支持）："+err.Error())
			return
		}
		if err = reVerifyFile(r.Context(), path, sha); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
	}
	if err = os.Chmod(path, 0400); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	meta, err := agent.InspectBinaryFile(r.Context(), path, 0)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if meta["sha256"] != sha {
		writeErr(w, 409, "存储样本哈希发生改变")
		return
	}
	delete(meta, "file_path")
	raw, err := json.Marshal(meta)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sample := db.RESample{SHA256: sha, Name: strings.ToValidUTF8(name, "�"), Size: size, Metadata: raw}
	if err = s.m.pg.RERegisterSample(taskID, sample); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, sample)
}
func (s *Server) reAttach(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	var body struct {
		SHA string `json:"sample_sha"`
	}
	if !reDecode(w, r, &body) {
		return
	}
	var sample db.RESample
	var meta string
	err := s.m.pg.QueryRow(`SELECT sha256,name,size,metadata FROM re_samples WHERE sha256=$1`, body.SHA).Scan(&sample.SHA256, &sample.Name, &sample.Size, &meta)
	if err != nil {
		writeErr(w, 404, "样本不存在")
		return
	}
	sample.Metadata = json.RawMessage(meta)
	if err = s.m.pg.RERegisterSample(id, sample); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) reOpenHTTP(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	var body struct {
		SampleSHA  string `json:"sample_sha"`
		ProviderID int64  `json:"provider_id"`
	}
	if !reDecode(w, r, &body) {
		return
	}
	v, err := s.reOpen(r.Context(), id, body.SampleSHA, body.ProviderID)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 201, v)
}
func (s *Server) reQueryHTTP(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Operation string `json:"operation"`
		Address   string `json:"address"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
	}
	if !reDecode(w, r, &body) {
		return
	}
	v, err := s.reQuery(r.Context(), id, reID(r.PathValue("session")), body.Operation, body.Address, body.Query, body.Limit)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reControlHTTP(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Action    string `json:"action"`
		Confirmed bool   `json:"confirmed"`
	}
	if !reDecode(w, r, &body) {
		return
	}
	if err := s.reEnd(r.Context(), id, reID(r.PathValue("session")), body.Action, body.Confirmed); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) reData(taskID, sessionID, before int64, kind string) (map[string]any, error) {
	if _, err := s.m.pg.RESession(taskID, sessionID); err != nil {
		return nil, err
	}
	if kind == "evidence" {
		items, err := s.m.pg.REEvidenceList(taskID, sessionID, before)
		if err != nil {
			return nil, err
		}
		more := len(items) > 100
		if more {
			items = items[:100]
		}
		var next int64
		if len(items) > 0 {
			next = items[len(items)-1].ID
		}
		return map[string]any{"items": items, "has_more": more, "next_before": next}, nil
	}
	if kind != "functions" && kind != "xrefs" && kind != "callgraph" && kind != "types" {
		return nil, errors.New("数据类型无效")
	}
	items, err := s.m.pg.REEntities(taskID, sessionID, before, kind)
	if err != nil {
		return nil, err
	}
	more := len(items) > 100
	if more {
		items = items[:100]
	}
	var next int64
	if len(items) > 0 {
		next = items[len(items)-1].ID
	}
	return map[string]any{"items": items, "has_more": more, "next_before": next}, nil
}
func (s *Server) reDataHTTP(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	v, err := s.reData(id, reID(r.PathValue("session")), reID(r.URL.Query().Get("before")), r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reEvidenceHTTP(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	v, err := s.m.pg.REEvidence(id, reID(r.PathValue("evidence")))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) reObservation(w http.ResponseWriter, r *http.Request) {
	t, id, ok := s.reTask(w, r)
	if !ok {
		return
	}
	var body struct {
		EvidenceID int64  `json:"evidence_id"`
		Summary    string `json:"summary"`
	}
	if !reDecode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Summary) == "" || len(body.Summary) > 4000 {
		writeErr(w, 400, "摘要不能为空且不得超过 4000 字节")
		return
	}
	e, err := s.m.pg.REEvidence(id, body.EvidenceID)
	if err != nil {
		writeErr(w, 404, "证据不属于本任务")
		return
	}
	nodeID, err := t.Store.AddNode(db.KindFact, map[string]any{"summary": body.Summary, "confidence": "inferred", "source": "software_workspace_operator", "re_evidence_ids": []int64{e.ID}, "evidence": fmt.Sprintf("软件证据 #%d；session=%d；sample_sha256=%s；operation=%s；output_sha256=%s；tool_error=%t；truncated=%t。摘要为操作员解释，非自动行为验证。", e.ID, e.SessionID, e.SampleSHA, e.Operation, e.OutputSHA, e.IsError, e.Truncated)}, 5, "confirmed", "human", nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if root, err := t.Store.OriginFactID(); err == nil && root > 0 {
		_ = t.Store.Link(root, db.RelDerivedFrom, nodeID)
	}
	t.Notify()
	writeJSON(w, 201, map[string]any{"node_id": nodeID})
}

// softwareAnalysisTools uses the trusted run TaskID, never a model-supplied task.
func (s *Server) softwareAnalysisTools() []actool.CoreTool {
	return []actool.CoreTool{wrTool("software_analysis", "软件逆向受管样本、独占会话、函数/调用链/证据工作台。仅限当前 software_re 任务；不会安装 Provider。先 overview，按真实 Provider ID 与样本哈希 open，再 query/data；close 显式关闭。Provider 必须已经由操作员审核。",
		objSchema(map[string]any{
			"mode":       map[string]any{"type": "string", "enum": []string{"overview", "open", "query", "data", "evidence", "close"}},
			"sample_sha": strParam("已关联样本 SHA-256"), "provider_id": map[string]any{"type": "integer"}, "session_id": map[string]any{"type": "integer"}, "evidence_id": map[string]any{"type": "integer"},
			"operation": strParam("status/functions/decompile/xrefs/callgraph/types；data 模式也可 evidence"), "address": strParam("Provider 明确支持的地址，不得混淆 RVA/VA/文件偏移"), "query": strParam("有限查询关键词"), "before": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 256},
		}, "mode"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			info := agent.RunInfoFrom(ctx)
			t, ok := s.m.Task(fmt.Sprint(info.TaskID))
			if !ok || t.AnalysisDomain != db.AnalysisDomainSoftwareRE {
				return actool.Errorf("仅当前软件逆向任务可调用"), nil
			}
			if err := s.reInitialize(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var a struct {
				Mode       string `json:"mode"`
				SampleSHA  string `json:"sample_sha"`
				ProviderID int64  `json:"provider_id"`
				SessionID  int64  `json:"session_id"`
				EvidenceID int64  `json:"evidence_id"`
				Operation  string `json:"operation"`
				Address    string `json:"address"`
				Query      string `json:"query"`
				Before     int64  `json:"before"`
				Limit      int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var v any
			var err error
			switch a.Mode {
			case "overview":
				v, err = s.reOverviewData(info.TaskID)
			case "open":
				v, err = s.reOpen(ctx, info.TaskID, a.SampleSHA, a.ProviderID)
			case "query":
				v, err = s.reQuery(ctx, info.TaskID, a.SessionID, a.Operation, a.Address, a.Query, a.Limit)
			case "data":
				v, err = s.reData(info.TaskID, a.SessionID, a.Before, a.Operation)
			case "evidence":
				v, err = s.m.pg.REEvidence(info.TaskID, a.EvidenceID)
			case "close":
				err = s.reEnd(ctx, info.TaskID, a.SessionID, "close", false)
				v = map[string]any{"ok": err == nil}
			default:
				err = errors.New("mode 无效")
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(v)
		})}
}
