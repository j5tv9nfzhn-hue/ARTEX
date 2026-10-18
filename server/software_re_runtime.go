package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/tidwall/gjson"
)

const reMaxOutput = 1 << 20

type reLiveSession struct {
	mu        sync.Mutex
	clientMu  sync.Mutex
	closeOnce sync.Once
	life      context.Context
	client    mcpClient
	tools     map[string]actool.CoreTool
	cancel    context.CancelFunc
	provider  db.REProvider
	key       any
}

func (v *reLiveSession) shutdown() {
	v.cancel()
	v.clientMu.Lock()
	cl := v.client
	v.clientMu.Unlock()
	if cl != nil {
		v.closeOnce.Do(func() { _ = cl.Close() })
	}
}
func (v *reLiveSession) setClient(cl mcpClient) error {
	v.clientMu.Lock()
	defer v.clientMu.Unlock()
	if err := v.life.Err(); err != nil {
		_ = cl.Close()
		return err
	}
	v.client = cl
	return nil
}

type reCatalogTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

func (s *Server) reInitialize() error {
	s.reOnce.Do(func() {
		_, s.reInitErr = s.m.pg.Exec(`UPDATE re_sessions SET state='interrupted',message='服务重启：外部分析状态未知，确认关闭后才能释放租约',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE state IN ('opening','ready','busy','closing')`)
		if s.ctx != nil && s.ctx.Done() != nil {
			go func() {
				<-s.ctx.Done()
				s.reLive.Range(func(key, value any) bool { v := value.(*reLiveSession); go v.shutdown(); return true })
			}()
		}
	})
	return s.reInitErr
}
func (s *Server) reProviderLock(id int64) *sync.Mutex {
	v, _ := s.reLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func (s *Server) reMCP(id int64) (*db.MCPServer, error) {
	ms, err := s.m.pg.ListMCP()
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if m.ID == id {
			if !m.Enabled {
				return nil, errors.New("MCP 已禁用")
			}
			return m, nil
		}
	}
	return nil, errors.New("MCP 不存在")
}
func reCatalog(tools []actool.CoreTool) (string, string) {
	out := make([]reCatalogTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, reCatalogTool{t.Name(), t.Description(), t.InputSchema()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	raw, _ := json.Marshal(out)
	hash := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(hash[:])
}
func reOperation(config db.REProviderConfig, name string) (db.REOperation, error) {
	op, ok := config.Operations[name]
	if !ok || op.Tool == "" {
		return op, fmt.Errorf("未映射操作 %s；请先读取实际工具目录并审核配置", name)
	}
	return op, nil
}
func reValidateConfig(p db.REProvider) error {
	if p.MCPID <= 0 || strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 {
		return errors.New("Provider 名称或 MCP 无效")
	}
	if p.Kind != "rea" && p.Kind != "ghidra" && p.Kind != "ida" {
		return errors.New("Provider 类型必须为 rea、ghidra 或 ida")
	}
	if p.Config.Mode != "open" && p.Config.Mode != "attach" {
		return errors.New("mode 必须为 open 或 attach")
	}
	allowed := map[string]bool{
		"open": true, "close": true, "identity": true, "status": true,
		"functions": true, "decompile": true, "xrefs": true, "callgraph": true,
		"types": true, "modules": true, "imports": true, "exports": true, "strings": true,
	}
	for name, op := range p.Config.Operations {
		if !allowed[name] || op.Tool == "" {
			return fmt.Errorf("操作映射无效：%s", name)
		}
		if op.AddressKind != "" && op.AddressKind != "rva" && op.AddressKind != "va" && op.AddressKind != "file_offset" && op.AddressKind != "provider" {
			return errors.New("address_kind 必须为 rva/va/file_offset/provider")
		}
		if len(op.Columns) > 16 {
			return errors.New("字段映射超过上限")
		}
		needsAddrKind := name == "functions" || name == "xrefs" || name == "callgraph" || name == "modules" || name == "imports" || name == "exports" || name == "strings"
		if op.RowsPath != "" && op.AddressKind == "" && needsAddrKind {
			return errors.New("函数/模块/符号/字符串/关系归一化必须指定 address_kind")
		}
	}
	return nil
}
func reValidateCatalog(p db.REProvider, tools []actool.CoreTool) error {
	have := map[string]actool.CoreTool{}
	for _, t := range tools {
		have[t.Name()] = t
	}
	for name, op := range p.Config.Operations {
		t := have[op.Tool]
		if t == nil {
			return fmt.Errorf("操作 %s 指向不存在的工具 %s", name, op.Tool)
		}
		if req, ok := t.InputSchema()["required"].([]any); ok {
			for _, key := range req {
				if k, ok := key.(string); ok && k != "" {
					if _, exists := op.Input[k]; !exists {
						return fmt.Errorf("操作 %s 映射的工具 %s 缺少必填参数 %s", name, op.Tool, k)
					}
				}
			}
		}
	}
	return nil
}

// Templates substitute typed values, never shell syntax. Unknown placeholders fail closed.
func reRender(value any, vars map[string]any) (any, error) {
	switch v := value.(type) {
	case string:
		if x, ok := vars[v]; ok {
			return x, nil
		}
		if strings.Contains(v, "{{") || strings.Contains(v, "}}") {
			return nil, fmt.Errorf("未知模板或不支持内嵌模板：%s", v)
		}
		return v, nil
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			r, err := reRender(x, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, x := range v {
			r, err := reRender(x, vars)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	default:
		return value, nil
	}
}
func reCallRaw(ctx context.Context, live *reLiveSession, session db.RESession, name string, vars map[string]any) (db.REEvidence, string, error) {
	e := db.REEvidence{Operation: name}
	op, err := reOperation(live.provider.Config, name)
	if err != nil {
		return e, "", err
	}
	e.Tool = op.Tool
	if err := live.life.Err(); err != nil {
		return e, "", err
	}
	vars["{{provider_session}}"] = live.key
	input, err := reRender(op.Input, vars)
	if err != nil {
		return e, "", err
	}
	if input == nil {
		input = map[string]any{}
	}
	e.Input, err = json.Marshal(input)
	if err != nil {
		return e, "", err
	}
	tool := live.tools[op.Tool]
	if tool == nil {
		return e, "", errors.New("工具未出现在当前连接的目录中")
	}
	// Required-property check catches stale/incorrect adapters early. Provider remains
	// authoritative for its complete JSON Schema (including refs and nested constraints).
	if args, ok := input.(map[string]any); ok {
		if req, ok := tool.InputSchema()["required"].([]any); ok {
			for _, key := range req {
				if k, ok := key.(string); ok {
					if _, exists := args[k]; !exists {
						return e, "", fmt.Errorf("工具 %s 缺少必要参数 %s", op.Tool, k)
					}
				}
			}
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	stop := context.AfterFunc(live.life, cancel)
	defer stop()
	if err := callCtx.Err(); err != nil {
		return e, "", err
	}
	res, callErr := tool.Call(callCtx, e.Input, &actool.ToolContext{})
	output := res.Flatten()
	e.IsError = res.IsError || callErr != nil
	if callErr != nil {
		e.Diagnostic = callErr.Error()
	}
	sum := sha256.Sum256([]byte(output))
	e.OutputSHA = hex.EncodeToString(sum[:])
	e.Truncated = len(output) > reMaxOutput
	if e.Truncated {
		e.Output = strings.ToValidUTF8(output[:reMaxOutput], "�")
	} else {
		e.Output = output
	}
	if callErr != nil {
		return e, output, callErr
	}
	if res.IsError {
		return e, output, errors.New("Provider 返回工具错误；查看证据原文")
	}
	if e.Truncated {
		return e, output, errors.New("Provider 输出超过 1 MiB；请缩小查询范围，当前结果不做归一化")
	}
	return e, output, nil
}
func reCall(ctx context.Context, live *reLiveSession, session db.RESession, name string, vars map[string]any) (db.REEvidence, string, error) {
	e, output, err := reCallRaw(ctx, live, session, name, vars)
	if len(e.Input) == 0 {
		e.Input = json.RawMessage(`{}`)
	}
	if err != nil {
		e.IsError = true
		e.Diagnostic = err.Error()
	}
	if e.OutputSHA == "" {
		h := sha256.Sum256([]byte(output))
		e.OutputSHA = hex.EncodeToString(h[:])
		e.Output = output
	}
	sum := sha256.Sum256([]byte(e.Output))
	e.RetainedSHA = hex.EncodeToString(sum[:])
	return e, output, err
}
func reEntityKey(parts ...string) string { raw, _ := json.Marshal(parts); return string(raw) }
func reNormalize(operation string, op db.REOperation, output, sampleSHA string, limit int) []db.REEntity {
	out := make([]db.REEntity, 0)
	if op.RowsPath == "" || !gjson.Valid(output) {
		return out
	}
	result := gjson.Get(output, op.RowsPath)
	if !result.IsArray() {
		return out
	}
	rows := result.Array()
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for _, row := range rows {
		get := func(name string) string {
			path := op.Columns[name]
			if path == "" {
				return ""
			}
			v := row.Get(path)
			if !v.Exists() {
				return ""
			}
			if v.Type == gjson.Number {
				return v.Raw
			}
			return v.String()
		}
		module := get("module")
		if module == "" {
			module = "sample:" + sampleSHA
		}
		v := db.REEntity{Kind: operation, Name: get("name"), Module: module, Address: get("address"), AddressKind: op.AddressKind}
		payload := map[string]any{"row": json.RawMessage(row.Raw), "normalized_from": "operator-reviewed field mapping"}
		switch operation {
		case "functions", "exports", "strings":
			if v.Address == "" {
				continue
			}
			v.Identity = reEntityKey(module, v.AddressKind, v.Address, v.Name)
		case "modules", "imports":
			if v.Name == "" && v.Address == "" {
				continue
			}
			v.Identity = reEntityKey(module, v.AddressKind, v.Name, v.Address)
		case "xrefs", "callgraph":
			source, target := get("source"), get("target")
			if source == "" || target == "" {
				continue
			}
			payload["source"], payload["target"] = source, target
			v.Identity = reEntityKey(module, v.AddressKind, source, target)
		case "types":
			if v.Name == "" {
				continue
			}
			v.Identity = reEntityKey(module, "type", v.Name)
		default:
			continue
		}
		v.Payload, _ = json.Marshal(payload)
		if len(v.Payload) > 64<<10 {
			continue
		}
		out = append(out, v)
	}
	return out
}
func (s *Server) reSamplePath(sha string) (string, error) {
	if len(sha) != 64 {
		return "", errors.New("样本 SHA-256 无效")
	}
	if _, err := hex.DecodeString(sha); err != nil || sha != strings.ToLower(sha) {
		return "", errors.New("样本 SHA-256 无效")
	}
	root, err := s.reStoreRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sha[:2], sha+".bin"), nil
}
func (s *Server) reStoreRoot() (string, error) {
	work, err := filepath.Abs(s.m.dir)
	if err != nil {
		return "", err
	}
	// Outside the editable workspace so workspaceWrite cannot silently replace samples.
	work, err = filepath.EvalSymlinks(work)
	if err != nil {
		return "", err
	}
	if filepath.Dir(work) == work {
		return "", errors.New("工作区根目录不能为文件系统根目录")
	}
	root := filepath.Join(filepath.Dir(work), filepath.Base(work)+"-re-store")
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("样本存储目录不能是符号链接")
	}
	return root, nil
}
func reVerifyFile(ctx context.Context, path, want string) error {
	li, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !li.Mode().IsRegular() || li.Mode()&os.ModeSymlink != 0 {
		return errors.New("样本路径必须为非符号链接的普通文件")
	}
	di, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !di.IsDir() || di.Mode()&os.ModeSymlink != 0 {
		return errors.New("样本分区不能为符号链接")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(li, info) || !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return errors.New("样本不是普通文件或超过 128 MiB")
	}
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	var count int64
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, e := f.Read(buf)
		if n > 0 {
			count += int64(n)
			if count > 128<<20 {
				return errors.New("样本超出读取上限")
			}
			_, _ = hash.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != want {
		return errors.New("样本内容与登记哈希不一致，已阻止打开")
	}
	return nil
}
func (s *Server) reOpen(ctx context.Context, taskID int64, sha string, providerID int64) (*db.RESession, error) {
	if err := s.reInitialize(); err != nil {
		return nil, err
	}
	if !s.m.pg.REHasSample(taskID, sha) {
		return nil, errors.New("样本未关联本任务")
	}
	lock := s.reProviderLock(providerID)
	if !lock.TryLock() {
		return nil, errors.New("Provider 正忙")
	}
	defer lock.Unlock()
	p, err := s.m.pg.REProvider(providerID)
	if err != nil {
		return nil, err
	}
	if !p.Config.ReviewedStatic || p.Discovery != "catalog_verified" {
		return nil, errors.New("请先审核静态操作映射并完成工具目录发现；目录就绪不代表引擎支持此样本")
	}
	if capReport := reEvaluateCapabilities(*p); len(capReport.MissingTools) > 0 || len(capReport.SchemaMismatches) > 0 {
		return nil, errors.New(capReport.GateReason)
	}
	m, err := s.reMCP(p.MCPID)
	if err != nil {
		return nil, err
	}
	if m.Transport != "stdio" {
		return nil, errors.New("当前只支持与服务器共享样本路径的本地 stdio Provider；远程传输需要显式样本同步适配")
	}
	path, err := s.reSamplePath(sha)
	if err != nil {
		return nil, err
	}
	if err = reVerifyFile(ctx, path, sha); err != nil {
		return nil, err
	}
	if p.Config.Mode == "open" {
		if _, err = reOperation(p.Config, "open"); err != nil {
			return nil, err
		}
		if _, err = reOperation(p.Config, "close"); err != nil {
			return nil, err
		}
	} else {
		if _, err = reOperation(p.Config, "identity"); err != nil || p.Config.IdentityHashPath == "" {
			return nil, errors.New("附加现有数据库必须配置 identity 工具与 identity_hash_path，用真实 SHA-256 匹配样本")
		}
	}
	session, err := s.m.pg.RECreateSession(taskID, sha, *p)
	if err != nil {
		return nil, fmt.Errorf("无法取得 Provider 独占租约（可能有活动或待人工释放的会话）：%w", err)
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	life, cancel := context.WithCancel(parent)
	requestStop := context.AfterFunc(ctx, cancel)
	defer requestStop()
	live := &reLiveSession{life: life, cancel: cancel, tools: map[string]actool.CoreTool{}, provider: *p}
	live.mu.Lock()
	defer live.mu.Unlock()
	s.reLive.Store(session.ID, live)
	timer := time.AfterFunc(90*time.Second, cancel)
	cl, err := connectMCP(life, m)
	if err != nil {
		timer.Stop()
		live.shutdown()
		s.reLive.Delete(session.ID)
		_ = s.m.pg.RETransition(session.ID, "opening", "interrupted", err.Error())
		return nil, err
	}
	if err = live.setClient(cl); err != nil {
		timer.Stop()
		live.shutdown()
		s.reLive.Delete(session.ID)
		_ = s.m.pg.RETransition(session.ID, "opening", "interrupted", err.Error())
		return nil, err
	}
	tools, err := cl.Tools(life)
	timer.Stop()
	if err == nil {
		err = life.Err()
	}
	if err == nil {
		_, hash := reCatalog(tools)
		if hash != p.CatalogHash {
			err = errors.New("实际工具 schema 已变化；重新发现并审核后再打开")
		}
	}
	if err != nil {
		live.shutdown()
		s.reLive.Delete(session.ID)
		_ = s.m.pg.RETransition(session.ID, "opening", "interrupted", err.Error())
		return nil, err
	}
	for _, t := range tools {
		live.tools[t.Name()] = t
	}
	// Cancellation may arrive in the short interval between row creation and
	// publication of its cancellation handle. Recheck the lease before target I/O.
	if err = s.m.pg.RETransition(session.ID, "opening", "opening", ""); err != nil {
		live.shutdown()
		s.reLive.Delete(session.ID)
		return nil, err
	}
	vars := map[string]any{"{{sample_path}}": path, "{{sample_sha}}": sha, "{{address}}": "", "{{query}}": "", "{{limit}}": 100}
	operation := "open"
	if p.Config.Mode == "attach" {
		operation = "identity"
	}
	e, output, callErr := reCall(ctx, live, *session, operation, vars)
	if callErr == nil && p.Config.Mode == "attach" {
		if !gjson.Valid(output) || gjson.Get(output, p.Config.IdentityHashPath).String() != sha {
			callErr = errors.New("当前分析数据库的样本哈希不匹配；拒绝附加")
			e.IsError = true
		}
	}
	if callErr == nil && p.Config.SessionKeyPath != "" {
		key := gjson.Get(output, p.Config.SessionKeyPath)
		if !gjson.Valid(output) || !key.Exists() {
			callErr = errors.New("配置的 Provider 会话键未出现在响应中")
			e.IsError = true
		} else {
			decoder := json.NewDecoder(strings.NewReader(key.Raw))
			decoder.UseNumber()
			if err = decoder.Decode(&live.key); err != nil {
				callErr = errors.New("Provider 会话键不是有效 JSON 值")
				e.IsError = true
			}
		}
	}
	next := "ready"
	if callErr != nil {
		next = "interrupted"
		e.IsError = true
		e.Diagnostic = callErr.Error()
	}
	_, saveErr := s.m.pg.REAppendResult(*session, "opening", next, e, nil)
	if saveErr != nil || callErr != nil {
		live.shutdown()
		s.reLive.Delete(session.ID)
		if saveErr != nil {
			_ = s.m.pg.RETransition(session.ID, "opening", "interrupted", saveErr.Error())
			return nil, saveErr
		}
		_ = s.m.pg.RETransition(session.ID, "interrupted", "interrupted", callErr.Error())
		return nil, callErr
	}
	return s.m.pg.RESession(taskID, session.ID)
}
func (s *Server) reQuery(ctx context.Context, taskID, sessionID int64, operation, address, query string, limit int) (map[string]any, error) {
	switch operation {
	case "status", "functions", "decompile", "xrefs", "callgraph", "types", "modules", "imports", "exports", "strings":
	default:
		return nil, errors.New("只允许已审核的静态查询操作")
	}
	if len(address) > 256 || len(query) > 2000 {
		return nil, errors.New("地址或查询过长")
	}
	if limit < 1 || limit > 256 {
		limit = 100
	}
	session, err := s.m.pg.RESession(taskID, sessionID)
	if err != nil {
		return nil, err
	}
	v, ok := s.reLive.Load(sessionID)
	if !ok {
		return nil, errors.New("没有本进程持有的连接，请检查会话与重启状态")
	}
	live := v.(*reLiveSession)
	if !live.mu.TryLock() {
		return nil, errors.New("会话正在执行其他操作")
	}
	defer live.mu.Unlock()
	if _, err = reOperation(live.provider.Config, operation); err != nil {
		return nil, err
	}
	if err = s.m.pg.RETransition(sessionID, "ready", "busy", ""); err != nil {
		return nil, err
	}
	// Disabling/deleting a configured MCP takes effect before each new query.
	if _, err = s.reMCP(live.provider.MCPID); err != nil {
		_ = s.m.pg.RETransition(sessionID, "busy", "interrupted", err.Error())
		return nil, err
	}
	path, err := s.reSamplePath(session.SampleSHA)
	if err != nil {
		_ = s.m.pg.RETransition(sessionID, "busy", "interrupted", err.Error())
		return nil, err
	}
	vars := map[string]any{"{{sample_path}}": path, "{{sample_sha}}": session.SampleSHA, "{{address}}": address, "{{query}}": query, "{{limit}}": limit}
	e, output, callErr := reCall(ctx, live, *session, operation, vars)
	next := "ready"
	if callErr != nil {
		next = "interrupted"
	}
	entities := []db.REEntity{}
	if callErr == nil {
		entities = reNormalize(operation, live.provider.Config.Operations[operation], output, session.SampleSHA, limit)
	}
	id, saveErr := s.m.pg.REAppendResult(*session, "busy", next, e, entities)
	if saveErr != nil {
		_ = s.m.pg.RETransition(sessionID, "busy", "interrupted", saveErr.Error())
		return nil, saveErr
	}
	if callErr != nil {
		_ = s.m.pg.RETransition(sessionID, "interrupted", "interrupted", callErr.Error())
	}
	return map[string]any{"evidence_id": id, "is_error": e.IsError, "truncated": e.Truncated, "normalized_count": len(entities), "state": next}, nil
}
func (s *Server) reEnd(ctx context.Context, taskID, sessionID int64, action string, confirmed bool) error {
	session, err := s.m.pg.RESession(taskID, sessionID)
	if err != nil {
		return err
	}
	if session.State == "closed" || session.State == "failed" {
		return nil
	}
	v, has := s.reLive.Load(sessionID)
	if action == "cancel" || action == "release" {
		if action == "release" && !confirmed {
			return errors.New("必须人工确认外部分析已经停止，才能释放独占租约")
		}
		if has {
			live := v.(*reLiveSession)
			live.cancel()
			go live.shutdown()
		}
		next := "interrupted"
		message := "已请求取消；外部状态未知，确认停止后才能释放租约"
		if action == "release" {
			next = "closed"
			message = "操作员确认外部关闭并释放租约"
			s.reLive.Delete(sessionID)
		}
		return s.m.pg.RETransition(sessionID, session.State, next, message)
	}
	if action != "close" {
		return errors.New("不支持的会话操作")
	}
	if !has {
		return errors.New("连接已丢失；请先确认外部分析关闭，再人工释放租约")
	}
	live := v.(*reLiveSession)
	if !live.mu.TryLock() {
		return errors.New("会话忙；可请求取消，不能并发关闭")
	}
	defer live.mu.Unlock()
	if _, err = reOperation(live.provider.Config, "close"); err != nil {
		return err
	}
	if err = s.m.pg.RETransition(sessionID, "ready", "closing", ""); err != nil {
		return err
	}
	e, _, callErr := reCall(ctx, live, *session, "close", map[string]any{"{{sample_sha}}": session.SampleSHA})
	next := "closed"
	if callErr != nil {
		next = "interrupted"
	}
	_, err = s.m.pg.REAppendResult(*session, "closing", next, e, nil)
	live.shutdown()
	s.reLive.Delete(sessionID)
	if err != nil {
		_ = s.m.pg.RETransition(sessionID, "closing", "interrupted", err.Error())
		return err
	}
	return callErr
}

func reID(raw string) int64 { id, _ := strconv.ParseInt(raw, 10, 64); return id }
