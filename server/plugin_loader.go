// plugin_loader.go — 插件加载器（P2：插件体系·加载器与 manifest）。
// 纯增量：插件根目录为空时注册表恒空，既有工具/执行路径行为不变，可零成本回滚。
//
// 能力（与 Harness 对标条款「插件加载器」对齐）：
//  1. 发现：扫描 plugins 根目录下的 */plugin.json（manifest：id/name/version/type/
//     description/tools/exec 等字段），manifest 校验失败即跳过并记录，不影响存量。
//  2. 内置适配：type=internal 的插件声明的工具名逐一校验存在于内置工具注册表
//     （由 SetBuiltinToolNames 注入的列表），校验通过即绑定为内置别名（只读绑定，
//     不重复注册、不改变既有工具行为）。
//  3. 外部进程：type=external 的插件按 manifest.exec（exec_windows 优先）拉起子进程，
//     以「一行一报文」JSON-RPC over stdio 协议握手：
//     → {"jsonrpc":"2.0","id":1,"method":"plugin.initialize","params":{"id":...}}
//     ← {"jsonrpc":"2.0","id":1,"result":{"name":...,"version":...,"tools":[...]}}
//     → {"jsonrpc":"2.0","id":2,"method":"plugin.call_tool","params":{"name":...,"args":{...}}}
//     ← {"jsonrpc":"2.0","id":2,"result":{...}} 或 {"error":{...}}
//     任何握手/通信失败只把该插件标记为 failed（fail-soft），服务主体不受影响。
//  4. 生命周期：Load / Enable / Disable / Reload / CallTool / List，全程加锁；
//     Disable 即终止子进程并摘除工具绑定，Reload = Disable+Load。
//
// 进程通信用 json.Decoder 流式解析（非 bufio.Scanner），规避 >64KB 报文行长上限。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// PluginTool 声明一个插件提供的工具（manifest.tools[]）。
type PluginTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// PluginManifest 是 plugins/<dir>/plugin.json 的结构。
type PluginManifest struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Type        string       `json:"type"` // internal | external
	Description string       `json:"description,omitempty"`
	Author      string       `json:"author,omitempty"`
	Tools       []PluginTool `json:"tools"`
	Exec        []string     `json:"exec,omitempty"`
	ExecWindows []string     `json:"exec_windows,omitempty"`
}

// PluginState 是插件生命周期状态。
type PluginState string

const (
	PluginLoaded   PluginState = "loaded"
	PluginDisabled PluginState = "disabled"
	PluginFailed   PluginState = "failed"
)

// PluginStatus 是注册表对外快照。
type PluginStatus struct {
	Manifest PluginManifest `json:"manifest"`
	State    PluginState    `json:"state"`
	Error    string         `json:"error,omitempty"`
	Root     string         `json:"root"`
}

// pluginRuntime 是单个已装载插件的运行时（外部进程时持有 stdio 通道）。
type pluginRuntime struct {
	mu       sync.Mutex
	manifest PluginManifest
	root     string
	state    PluginState
	err      string

	cmd    *exec.Cmd
	stdin  jsonWriter    // 向子进程写请求
	stdout *json.Decoder // 从子进程读响应
	nextID int64
	pend   map[int64]chan pluginResp
}

// pluginResp 是子进程单行响应（result/error 二选一）。
type pluginResp struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// jsonWriter 抽象写端口，便于测试替换。
type jsonWriter interface {
	WriteJSON(v any) error
	Close() error
}

type pipeWriter struct {
	f *os.File
}

func (p *pipeWriter) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = p.f.Write(b)
	return err
}

func (p *pipeWriter) Close() error { return p.f.Close() }

// PluginRegistry 是插件注册表（线程安全）。
type PluginRegistry struct {
	mu       sync.RWMutex
	plugins  map[string]*pluginRuntime
	builtin  map[string]bool
	root     string
	loadOnce sync.Once
}

// SharedPlugins 是进程级共享注册表。Server 的 /api/plugins 系列即挂在此表上；
// 单测直接构造私有 NewPluginRegistry()，不污染共享表。
var sharedPlugins = NewPluginRegistry()

// SharedPlugins 返回进程级插件注册表。
func SharedPlugins() *PluginRegistry { return sharedPlugins }

// NewPluginRegistry 空注册表。
func NewPluginRegistry() *PluginRegistry {
	return &PluginRegistry{plugins: map[string]*pluginRuntime{}, builtin: map[string]bool{}}
}

// SetBuiltinToolNames 注入内置工具名单（internal 插件工具名须在名单内）。
func (r *PluginRegistry) SetBuiltinToolNames(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builtin = map[string]bool{}
	for _, n := range names {
		r.builtin[n] = true
	}
}

// SetRoot 设定插件根目录（默认 <cwd>/plugins）。
func (r *PluginRegistry) SetRoot(root string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.root = root
}

func (r *PluginRegistry) pluginRoot() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.root != "" {
		return r.root
	}
	return "plugins"
}

// Scan 扫描插件根目录并装载全部有效 manifest。返回装载/失败计数。
// 幂等：已存在的插件先 Disable 再按 manifest 重载。
func (r *PluginRegistry) Scan(ctx context.Context) (loaded, failed int, err error) {
	root := r.pluginRoot()
	entries, e := os.ReadDir(root)
	if e != nil {
		if os.IsNotExist(e) {
			return 0, 0, nil // 无插件目录 = 恒空注册表，正常
		}
		return 0, 0, e
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		mf, e := readManifest(filepath.Join(root, ent.Name(), "plugin.json"))
		if e != nil {
			failed++
			continue
		}
		if e := r.loadManifest(ctx, mf, filepath.Join(root, ent.Name())); e != nil {
			failed++
			continue
		}
		loaded++
	}
	return loaded, failed, nil
}

func readManifest(path string) (PluginManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return PluginManifest{}, err
	}
	var mf PluginManifest
	if err := json.Unmarshal(b, &mf); err != nil {
		return PluginManifest{}, fmt.Errorf("manifest 解析失败 %s: %w", path, err)
	}
	if mf.ID == "" || mf.Name == "" {
		return PluginManifest{}, fmt.Errorf("manifest %s 缺少 id/name", path)
	}
	if mf.Type != "internal" && mf.Type != "external" {
		return PluginManifest{}, fmt.Errorf("manifest %s type 非法: %q", path, mf.Type)
	}
	if mf.Type == "external" && len(mf.Exec) == 0 && len(mf.ExecWindows) == 0 {
		return PluginManifest{}, fmt.Errorf("manifest %s external 缺少 exec", path)
	}
	for _, t := range mf.Tools {
		if t.Name == "" {
			return PluginManifest{}, fmt.Errorf("manifest %s 存在无名工具", path)
		}
	}
	return mf, nil
}

// loadManifest 校验并装载单个 manifest。
func (r *PluginRegistry) loadManifest(ctx context.Context, mf PluginManifest, root string) error {
	r.mu.Lock()
	if old, ok := r.plugins[mf.ID]; ok {
		r.mu.Unlock()
		_ = r.disableOne(mf.ID)
		r.mu.Lock()
		_ = old
	}
	rt := &pluginRuntime{manifest: mf, root: root, state: PluginFailed, pend: map[int64]chan pluginResp{}}

	if mf.Type == "internal" {
		if len(r.builtin) > 0 {
			for _, t := range mf.Tools {
				if !r.builtin[t.Name] {
					rt.err = fmt.Sprintf("内置工具名 %q 不在注册表", t.Name)
					r.plugins[mf.ID] = rt
					r.mu.Unlock()
					return fmt.Errorf("%s", rt.err)
				}
			}
		}
		rt.state = PluginLoaded
		r.plugins[mf.ID] = rt
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	// external：拉起子进程并握手
	if err := rt.start(ctx); err != nil {
		r.mu.Lock()
		rt.err = err.Error()
		rt.state = PluginFailed
		r.plugins[mf.ID] = rt
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	rt.state = PluginLoaded
	r.plugins[mf.ID] = rt
	r.mu.Unlock()
	return nil
}

// start 拉起外部插件进程并完成 initialize 握手。
func (rt *pluginRuntime) start(ctx context.Context) error {
	argv := rt.manifest.Exec
	if runtime.GOOS == "windows" && len(rt.manifest.ExecWindows) > 0 {
		argv = rt.manifest.ExecWindows
	}
	if len(argv) == 0 {
		return fmt.Errorf("manifest 无可用 exec")
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// 不用 exec.CommandContext 绑定 cctx：cctx 只约束下面的握手，
	// 一旦 start 返回 cancel 就会连同子进程一起杀掉（后续 CallTool 直接 EOF）。
	// 进程生命周期由 stop()/Disable 显式管理。
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = rt.root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// 插件 stderr 并入进程日志，防子进程因管道满阻塞。
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动插件进程失败: %w", err)
	}

	rt.mu.Lock()
	rt.cmd = cmd
	rt.stdin = &pipeWriter{f: stdin.(*os.File)}
	if f, ok := stdin.(*os.File); ok {
		rt.stdin = &pipeWriter{f: f}
	} else {
		// os/exec 返回 io.WriteCloser；退化为适配器。
		rt.stdin = &wcWriter{w: stdin}
	}
	rt.stdout = json.NewDecoder(stdout)
	rt.nextID = 0
	rt.mu.Unlock()

	// initialize 握手（10s 内必须应答）。
	var result struct {
		Name    string       `json:"name"`
		Version string       `json:"version"`
		Tools   []PluginTool `json:"tools"`
	}
	if _, err := rt.call(cctx, "plugin.initialize", map[string]any{"id": rt.manifest.ID}); err != nil {
		_ = rt.stop()
		return fmt.Errorf("initialize 握手失败: %w", err)
	}
	_ = result // 握手结果以 manifest 为准，子进程工具表可增量扩展（后续版本）。
	return nil
}

type wcWriter struct {
	w interface {
		Write([]byte) (int, error)
		Close() error
	}
}

func (w *wcWriter) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.w.Write(append(b, '\n'))
	return err
}
func (w *wcWriter) Close() error { return w.w.Close() }

// call 发一次请求并等响应（json.Decoder 流式读，规避行长上限）。
func (rt *pluginRuntime) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	rt.mu.Lock()
	rt.nextID++
	id := rt.nextID
	ch := make(chan pluginResp, 1)
	rt.pend[id] = ch
	w := rt.stdin
	dec := rt.stdout
	rt.mu.Unlock()

	if w == nil || dec == nil {
		return nil, fmt.Errorf("插件进程未启动")
	}
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := w.WriteJSON(req); err != nil {
		return nil, err
	}

	type readResult struct {
		resp pluginResp
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		// 顺序读：应答与请求 id 匹配才回传；不匹配（插件乱序）直接丢弃。
		// 每轮重新声明 resp，避免上一行的 Error 指针/Result 残留到下一行。
		for {
			var resp pluginResp
			if err := dec.Decode(&resp); err != nil {
				done <- readResult{err: err}
				return
			}
			if resp.ID == id {
				done <- readResult{resp: resp}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		if r.resp.Error != nil {
			return nil, fmt.Errorf("plugin error %d: %s", r.resp.Error.Code, r.resp.Error.Message)
		}
		return r.resp.Result, nil
	}
}

// stop 终止外部进程。
func (rt *pluginRuntime) stop() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.stdin != nil {
		_ = rt.stdin.Close()
	}
	if rt.cmd != nil && rt.cmd.Process != nil {
		_ = rt.cmd.Process.Kill()
		_, _ = rt.cmd.Process.Wait()
	}
	rt.cmd = nil
	rt.stdin = nil
	rt.stdout = nil
	return nil
}

// CallTool 对 external 插件发 call_tool；internal 插件返回绑定信息（调用走既有工具面）。
func (r *PluginRegistry) CallTool(ctx context.Context, id, tool string, args json.RawMessage) (json.RawMessage, error) {
	r.mu.RLock()
	rt, ok := r.plugins[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("插件 %q 不存在", id)
	}
	rt.mu.Lock()
	state, typ := rt.state, rt.manifest.Type
	rt.mu.Unlock()
	if state != PluginLoaded {
		return nil, fmt.Errorf("插件 %q 当前状态 %s，不可调用", id, state)
	}
	if typ == "internal" {
		// 内置绑定：调用方应直接走既有工具执行面；此处只回执绑定关系。
		return json.Marshal(map[string]any{
			"bound": true,
			"note":  "internal 工具经既有工具执行面调用，本端点仅校验绑定",
			"tool":  tool,
		})
	}
	return rt.call(ctx, "plugin.call_tool", map[string]any{"name": tool, "args": args})
}

// Enable 重新装载一个 disabled/failed 插件。
func (r *PluginRegistry) Enable(ctx context.Context, id string) error {
	r.mu.RLock()
	rt, ok := r.plugins[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("插件 %q 不存在", id)
	}
	rt.mu.Lock()
	mf, root := rt.manifest, rt.root
	rt.mu.Unlock()
	return r.loadManifest(ctx, mf, root)
}

// Disable 停用插件（终止 external 进程、internal 解绑）。
func (r *PluginRegistry) Disable(id string) error { return r.disableOne(id) }

func (r *PluginRegistry) disableOne(id string) error {
	r.mu.Lock()
	rt, ok := r.plugins[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("插件 %q 不存在", id)
	}
	_ = rt.stop()
	rt.mu.Lock()
	rt.state = PluginDisabled
	rt.err = ""
	rt.mu.Unlock()
	return nil
}

// Reload = Disable + Enable。
func (r *PluginRegistry) Reload(ctx context.Context, id string) error {
	_ = r.Disable(id)
	return r.Enable(ctx, id)
}

// List 返回全部插件快照。
func (r *PluginRegistry) List() []PluginStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PluginStatus, 0, len(r.plugins))
	for _, rt := range r.plugins {
		rt.mu.Lock()
		out = append(out, PluginStatus{Manifest: rt.manifest, State: rt.state, Error: rt.err, Root: rt.root})
		rt.mu.Unlock()
	}
	return out
}

// Get 返回单个插件快照。
func (r *PluginRegistry) Get(id string) (PluginStatus, bool) {
	r.mu.RLock()
	rt, ok := r.plugins[id]
	r.mu.RUnlock()
	if !ok {
		return PluginStatus{}, false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return PluginStatus{Manifest: rt.manifest, State: rt.state, Error: rt.err, Root: rt.root}, true
}

// ---------- HTTP 面（全部 additive，挂在既有 mux 上） ----------

// pluginsList GET /api/plugins。
func (s *Server) pluginsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"plugins": SharedPlugins().List()})
}

// pluginsGet GET /api/plugins/{id}。
func (s *Server) pluginsGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, ok := SharedPlugins().Get(id)
	if !ok {
		writeErr(w, 404, "plugin not found: "+id)
		return
	}
	writeJSON(w, 200, st)
}

// pluginsScan POST /api/plugins/scan。
func (s *Server) pluginsScan(w http.ResponseWriter, r *http.Request) {
	loaded, failed, err := SharedPlugins().Scan(r.Context())
	if err != nil {
		writeErr(w, 500, "scan failed: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"loaded": loaded, "failed": failed})
}

// pluginsEnable POST /api/plugins/{id}/enable。
func (s *Server) pluginsEnable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := SharedPlugins().Enable(r.Context(), id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if st, ok := SharedPlugins().Get(id); ok {
		writeJSON(w, 200, st)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

// pluginsDisable POST /api/plugins/{id}/disable。
func (s *Server) pluginsDisable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := SharedPlugins().Disable(id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if st, ok := SharedPlugins().Get(id); ok {
		writeJSON(w, 200, st)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

// pluginsReload POST /api/plugins/{id}/reload。
func (s *Server) pluginsReload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := SharedPlugins().Reload(r.Context(), id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if st, ok := SharedPlugins().Get(id); ok {
		writeJSON(w, 200, st)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

// pluginsCall POST /api/plugins/{id}/call，body={"tool":...,"args":{...}}。
func (s *Server) pluginsCall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if body.Tool == "" {
		writeErr(w, 400, "tool 必填")
		return
	}
	res, err := SharedPlugins().CallTool(r.Context(), id, body.Tool, body.Args)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var data any
	if err := json.Unmarshal(res, &data); err != nil {
		data = string(res)
	}
	writeJSON(w, 200, map[string]any{"result": data})
}
