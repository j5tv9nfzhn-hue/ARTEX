package agent

import (
	"encoding/json"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// deferredSystem builds an agent's system-prompt segments and cache boundary from
// its DeferredInfo. When globally-available MCP tools are present, their names + a
// "prefer core tools" instruction render into a <available-deferred-tools> block
// placed as the LAST system-prompt segment, with DynamicBoundary set so the whole
// (session-fixed) system prompt — including the block — is cached (design doc
// §2.1 / C1). Skill-gated MCP names are NOT in this block; they surface when their
// skill loads. Without a global block the single plain segment is still placed
// inside the boundary: norma only sends cache_control when boundary>0, so
// returning 0 here would leave the default path re-billing the entire system
// prompt at full price on every request. 无块时也整段缓存的依据：worker 的
// system 虽混入 per-intent 数据，但在单次 run 内恒定（跨轮复用命中），
// planner/chat 的 system 跨轮/跨唤醒稳定；Anthropic 缓存区间内每轮复用，
// 首轮 1.25x write 一次即回本。boundary=len(system) 命中 SDK 的“b>=len →
// 整段一个块”分支。
func deferredSystem(sysText string, def DeferredInfo) (system []string, boundary int) {
	sysText += def.FindingGuidance
	block := actool.RenderDeferredToolsBlock(def.GlobalNames)
	if block == "" {
		system = []string{sysText}
		boundary = len(system) // b >= len → 整段 system 一个缓存块
		return system, boundary
	}
	system = []string{sysText, block}
	boundary = len(system) // b >= len → whole system prompt cached (SDK guard)
	return system, boundary
}

// seedUnlockFromHistory replays prior Skill() invocations in the conversation so
// their skill-gated MCPs are re-unlocked on a resumed session (design doc C2). The
// main agent builds a fresh session each turn; its in-memory unlock set would
// otherwise reset, leaving the model able to see a skill-revealed tool name yet
// unable to call it. No-op when unlockSkill is nil (no deferred tools).
func seedUnlockFromHistory(msgs []llm.Message, unlockSkill func(string)) {
	if unlockSkill == nil {
		return
	}
	for _, m := range msgs {
		for _, b := range m.ToolUses() {
			if b.Name != "Skill" {
				continue
			}
			var in struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(b.Input, &in) == nil && in.Name != "" {
				unlockSkill(in.Name)
			}
		}
	}
}
