package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	actool "github.com/Autumn-27/norma/tool"
)

// 本文件把「工具调用只能一次一个」这件事从宿主侧解掉，不动 SDK。
//
// ## 病根
//
// norma harness/streaming.go 的 streamExec 按每个工具的 IsConcurrencySafe(入参) 决定
// 能不能并行：安全工具之间可并行；不安全工具要**独占**（不能有别的工具在跑），而且
// 会把排在它后面的调用一起挡住（processQueue 遇到起不来的不安全工具直接 break，
// drain 也停在第一个未完成的不安全工具上）。
//
// 本仓的工具面按构造器分成两半：
//   - readTool / readExpTool 家族已经声明 Concurrent: true —— 本来就能并行；
//   - writeTool / writeExpTool 家族（insert_assets、record_fact、report_finding、
//     set_goals …）与直接 actool.Build 的探测工具（http_request、http_sequence、
//     http_fuzz、web_recon、net_probe、dns_probe、codec_crypto、extract_data、
//     binary_inspect、binary_lab、human_verification_flow），**一个都没声明**，
//     Build 的 fail-closed 默认把它们当不安全 —— 于是「记录一条事实」会把后面
//     所有读写一起挡住，「改一个文件」也会挡住一次 HTTP 探测。
//
// ## 做法：把「独占」从全局收窄到「资源键」
//
// 每个工具在 Call 里按自己声明的资源键加锁，从而可以诚实地对外声明
// IsConcurrencySafe == true——它与任何调用并行都不会产出错误结果，因为同键的
// 冲突已经由锁串起来了。键分四个命名空间：
//
//	""         全局独占：进程级/未知工具（Bash、shell_*、ExecuteExtraTool…）
//	graph      任务内探索图 + 资产图 + 目标/意图（读共享、写独占）
//	file:路径   单个文件（读共享、写独占）
//	mcp:服务    一个 MCP 服务（同服务串行、跨服务并行）
//
// 于是：写文件不再挡住记录事实、记录事实不再挡住抓 HTTP、抓 HTTP 不再挡住读文件；
// 只有「同一文件」「同一张图」「同一个 MCP 服务」「全局工具」这四种真正冲突的组合
// 才排队。未知写工具仍然退化成全局独占（fail-closed，等价于改造前的行为）。
//
// 与 SDK 侧的对应关系见 docs/工具并发与多文件补丁-落地说明.md。

// keyGlobal 是「全局独占」这个特殊键：拿到它的写锁就等于回到改造前的不安全工具。
const keyGlobal = ""

// keyGraph 是任务内图（探索图/资产图/目标意图/worker 控制）的共享键。
const keyGraph = "graph"

// keyFile / keyMCP 生成单文件键与 MCP 服务键。
func keyFile(pathKey string) string { return "file:" + pathKey }

func keyMCP(server string) string { return "mcp:" + server }

// nameSet 把一串工具名变成集合。用函数而不是多行 map 字面量：字面量的列对齐由
// gofmt 决定，手写容易与 gofmt 输出不一致（本仓 CI 会查 gofmt）。
func nameSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// keyLocks 是进程内按键分片的读写锁表。
//
// global 用来兜住「独占一切」的调用：这类调用拿 global 写锁，与所有持 global 读锁
// 的调用互斥；其余调用一律先拿 global 读锁，因此只被全局独占的工具挡住。
type keyLocks struct {
	global sync.RWMutex
	mu     sync.Mutex
	m      map[string]*keyEntry
}

type keyEntry struct {
	rw   sync.RWMutex
	refs int // 由 keyLocks.mu 保护；归零即回收，避免长跑进程无限堆积
}

var toolKeyLocks = &keyLocks{m: map[string]*keyEntry{}}

// lock 取单把键锁（write=true 独占，false 共享）。返回的函数必须调用一次。
func (l *keyLocks) lock(key string, write bool) func() {
	l.mu.Lock()
	e, ok := l.m[key]
	if !ok {
		e = &keyEntry{}
		l.m[key] = e
	}
	e.refs++
	l.mu.Unlock()
	if write {
		e.rw.Lock()
	} else {
		e.rw.RLock()
	}
	return func() {
		if write {
			e.rw.Unlock()
		} else {
			e.rw.RUnlock()
		}
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.m, key)
		}
		l.mu.Unlock()
	}
}

// lockKeys 按一次调用声明出来的读/写键取锁。同名键以写为准；多键按字典序获取
// （多文件补丁与其它编辑混跑时不会互相死锁）。空集合表示「与谁都不冲突」。
//
// 全局独占走 global 写锁并直接返回——此时其它键都被它包含，不必重复获取。
func lockKeys(reads, writes []string) func() {
	mode := make(map[string]bool, len(reads)+len(writes))
	for _, k := range reads {
		if _, ok := mode[k]; !ok {
			mode[k] = false
		}
	}
	for _, k := range writes {
		mode[k] = true
	}
	if len(mode) == 0 {
		return func() {}
	}
	if mode[keyGlobal] { // 全局写锁：包含一切，直接独占
		toolKeyLocks.global.Lock()
		return toolKeyLocks.global.Unlock
	}
	keys := make([]string, 0, len(mode))
	for k := range mode {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	toolKeyLocks.global.RLock()
	unlocks := make([]func(), 0, len(keys))
	for _, k := range keys {
		unlocks = append(unlocks, toolKeyLocks.lock(k, mode[k]))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
		toolKeyLocks.global.RUnlock()
	}
}

// fsPathKey 把工具入参里的路径规范化成锁键：相对路径按工作目录展开、Clean、
// Abs、尽量解析软链接，Windows 下不区分大小写。解析不出具体路径时返回空串，
// 由调用方退化成全局独占（fail-closed）。
func fsPathKey(tc *actool.ToolContext, raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		base := "."
		if tc != nil && tc.WorkingDir != "" {
			base = tc.WorkingDir
		}
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	// 软链接别名要归一到真实路径，否则 a.txt 与 link/to/a.txt 会被当成两个文件
	// 而真的并行写。文件还不存在时退一步解析父目录。
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	} else if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		p = filepath.Join(real, filepath.Base(p))
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// pathFieldOf 取模型这次调用要动的路径。SDK 的文件工具都用 file_path，
// LS/Grep 之类用 path；两个都认。
func pathFieldOf(in json.RawMessage) string {
	var v struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if len(in) > 0 {
		_ = json.Unmarshal(in, &v)
	}
	if v.FilePath != "" {
		return v.FilePath
	}
	return v.Path
}

// 文件类工具：读共享、写独占。
var fileReadTools = nameSet("Read")

var fileWriteTools = nameSet("Write", "Edit", "MultiEdit", "apply_patch")

// graphTools 是碰任务内「图」的工具：探索图（事实/意图/节点）、资产图（资产/企业/
// 范围/未测资产）、目标与约束、worker 控制。它们的写操作共享同一份进程内图状态，
// 因此写独占、读共享——与改造前「写工具全局独占」相比只放开了「跨命名空间」的并行。
var graphTools = nameSet(
	// 写
	"insert_assets", "add_company_scope", "add_task_scope",
	"record_fact", "report_finding", "expand_digest",
	"add_intent", "add_hint", "prove_goal", "goal_met",
	"set_goals", "set_constraints",
	"steer_work", "kill_work", "restart_work",
	// 读
	"graph_overview", "get_overview_slice", "node_detail",
	"list_facts", "list_findings", "list_goals",
	"list_assets", "list_companies", "list_untested_assets",
	"get_worker_output", "get_worker_trace", "list_worker_traces",
	"search_all_worker_traces",
)

// independentTools 是不碰进程内共享状态、只做网络/本地计算/写自己那一份产物的工具：
// 它们与谁都能并行。判据：每次调用自带 HTTP 客户端或纯计算；唯一的共享落点是流量库，
// 而流量库本来就是并发安全的（MITM 代理在用多协程往里写）。
var independentTools = nameSet(
	"http_request", "http_sequence", "http_fuzz", "web_recon",
	"net_probe", "dns_probe", "codec_crypto", "extract_data",
	"binary_inspect", "binary_lab",
	"human_verification", "human_verification_capabilities",
	"human_verification_solve", "human_verification_flow",
	"tactical_kb",
	"Sleep",
)

// mcpServerOf 从 mcp__<服务>__<工具> 里取服务名。
func mcpServerOf(name string) string {
	parts := strings.Split(name, "__")
	if len(parts) >= 2 {
		return parts[1]
	}
	return name
}

// toolAccessKeys 决定一次调用要占哪些资源键。
//
// 顺序有意义：先按名字认领（文件 → 图 → 独立工具 → MCP），其余只读工具完全并行，
// 其余写工具一律全局独占——**新加的工具默认落在最保守的一档**，要并行必须显式登记。
func toolAccessKeys(t actool.CoreTool, in json.RawMessage, tc *actool.ToolContext) (reads, writes []string) {
	name := t.Name()
	readOnly := t.IsReadOnly(in)
	switch {
	case name == "apply_patch":
		// 多文件补丁：它的两阶段写入由这里统一加锁，工具内部不再重复加锁。
		var p struct {
			Patches []struct {
				FilePath string `json:"file_path"`
			} `json:"patches"`
		}
		if len(in) > 0 {
			_ = json.Unmarshal(in, &p)
		}
		for _, item := range p.Patches {
			k := fsPathKey(tc, item.FilePath)
			if k == "" {
				return nil, []string{keyGlobal} // 解析不出路径 → 退化成全局独占
			}
			writes = append(writes, keyFile(k))
		}
		if len(writes) == 0 {
			return nil, []string{keyGlobal}
		}
		return nil, writes
	case fileReadTools[name], fileWriteTools[name]:
		k := fsPathKey(tc, pathFieldOf(in))
		if k == "" {
			return nil, []string{keyGlobal}
		}
		if fileReadTools[name] {
			return []string{keyFile(k)}, nil
		}
		return nil, []string{keyFile(k)}
	case graphTools[name]:
		if readOnly {
			return []string{keyGraph}, nil
		}
		return nil, []string{keyGraph}
	case independentTools[name]:
		return nil, nil
	case strings.HasPrefix(name, "mcp__"):
		k := keyMCP(mcpServerOf(name))
		if readOnly {
			return []string{k}, nil
		}
		return nil, []string{k}
	}
	if readOnly {
		return nil, nil // 其余只读工具：与谁都不冲突
	}
	return nil, []string{keyGlobal} // 未知写工具：全局独占（fail-closed）
}

// resourceLockedTool 让工具按资源键自我串行，并对外声明并发安全。
//
// 声明安全的理由不是「它不会互相影响」，而是「同键的影响已经被它自己的锁串起来了」。
// 其余行为（Name/Description/Schema/权限/只读标记）全部沿用原工具。
type resourceLockedTool struct {
	actool.CoreTool
}

func (r *resourceLockedTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (r *resourceLockedTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	reads, writes := toolAccessKeys(r.CoreTool, in, tc)
	if len(reads) == 0 && len(writes) == 0 { // 与谁都不冲突：直接跑，不加锁
		return r.CoreTool.Call(ctx, in, tc)
	}
	unlock := lockKeys(reads, writes)
	defer unlock()
	return r.CoreTool.Call(ctx, in, tc)
}

// lockResources 给一个工具套上资源键串行。AugmentTools 用它，因为那里是**所有**
// 工具的必经之路（内置 + 技能 + MCP + 宿主 + 自定义），一处覆盖整个工具面。
func lockResources(t actool.CoreTool) actool.CoreTool {
	if t == nil {
		return t
	}
	if _, wrapped := t.(*resourceLockedTool); wrapped {
		// 已经套过就原样返回：同一把 sync.RWMutex 不可重入，重复加锁会死锁。
		return t
	}
	return &resourceLockedTool{CoreTool: t}
}

// SDKDefaultTools 是 actool.DefaultTools() 在宿主侧的替代品，所有装配 agent 的
// 地方都应该用它而不是直接调 SDK：它在 SDK 默认工具集之上追加 apply_patch
// 多文件补丁工具（SDK 没有等价物，见 tools_patch.go）。
//
// 并发按资源键串行不在这里做——那是 AugmentTools 的职责，这样技能/MCP/宿主工具
// 也能一并覆盖。
func SDKDefaultTools() []actool.CoreTool {
	out := append([]actool.CoreTool{}, actool.DefaultTools()...)
	out = append(out, newApplyPatchTool())
	return out
}
