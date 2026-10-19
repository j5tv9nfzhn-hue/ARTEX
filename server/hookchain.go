// P1 hooks 插桩面：在既有工具生命周期钩（norma hook.Registry → guard/steerHooks）
// 之上提供一条「可插桩组合链」。默认没有任何注册槽位，buildHookChain 为恒等透传，
// 行为与现状逐字节一致（纯增量、默认 no-op、可零成本回滚——删除注册调用即回到原样）。
//
// 扩展方式（不 fork harness）：任意 init()/插件包调用 RegisterHookSlot 注册一个
// 具名 HookRunner 工厂；链按槽位名排序后依次包裹（外层=排序最前），每个工作意图
// 的 runIntent 现场构建。槽位契约：build 不得返回 typed-nil；钩实现只需实现
// harness.HookRunner 的 PreToolUse/PostToolUse/Stop 三个方法。
package server

import (
	"context"
	"sort"
	"sync"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// hookSlot is one named extension point registration.
type hookSlot struct {
	name  string
	note  string
	build func() harness.HookRunner
}

var (
	hookSlotsMu sync.RWMutex
	hookSlots   = map[string]hookSlot{}
)

// RegisterHookSlot registers (or replaces) a named hook slot. Call from
// init()/plugin bootstrap only; the chain is rebuilt per work run.
func RegisterHookSlot(name, note string, build func() harness.HookRunner) {
	if name == "" || build == nil {
		return
	}
	hookSlotsMu.Lock()
	defer hookSlotsMu.Unlock()
	hookSlots[name] = hookSlot{name: name, note: note, build: build}
}

// HookSlotInfo is the introspection shape for GET /api/protocol/hooks.
type HookSlotInfo struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// listHookSlots returns registered slots sorted by name (chain order).
func listHookSlots() []HookSlotInfo {
	hookSlotsMu.RLock()
	defer hookSlotsMu.RUnlock()
	names := make([]string, 0, len(hookSlots))
	for n := range hookSlots {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]HookSlotInfo, 0, len(names))
	for _, n := range names {
		out = append(out, HookSlotInfo{Name: n, Note: hookSlots[n].note})
	}
	return out
}

// buildHookChain wraps inner with every registered slot (sorted by name,
// outermost first). Empty registry → returns inner unchanged.
func buildHookChain(inner harness.HookRunner) harness.HookRunner {
	hookSlotsMu.RLock()
	slots := make([]hookSlot, 0, len(hookSlots))
	for _, n := range sortedKeys() {
		slots = append(slots, hookSlots[n])
	}
	hookSlotsMu.RUnlock()

	chain := inner
	for i := len(slots) - 1; i >= 0; i-- {
		head := slots[i].build()
		if head == nil {
			continue
		}
		chain = chainHooks{head: head, tail: chain}
	}
	return chain
}

func sortedKeys() []string {
	keys := make([]string, 0, len(hookSlots))
	for k := range hookSlots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// chainHooks runs head before tail: a head block wins immediately; otherwise
// the call continues down the chain. PostToolUse broadcasts to the whole chain.
type chainHooks struct {
	head harness.HookRunner
	tail harness.HookRunner
}

func (c chainHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if c.head != nil {
		blocked, msg, out := c.head.PreToolUse(ctx, name, input)
		if blocked {
			return blocked, msg, out
		}
	}
	if c.tail != nil {
		return c.tail.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (c chainHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if c.tail != nil {
		c.tail.PostToolUse(ctx, name, input, result, isErr)
	}
	if c.head != nil {
		c.head.PostToolUse(ctx, name, input, result, isErr)
	}
}

func (c chainHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if c.head != nil {
		prevent, blocking, msg := c.head.Stop(ctx, messages)
		if prevent || len(blocking) > 0 {
			return prevent, blocking, msg
		}
	}
	if c.tail != nil {
		return c.tail.Stop(ctx, messages)
	}
	return false, nil, ""
}
