// compaction_layers.go — 分层 compaction 的策略面与可观测面（P2：降本提速）。
// 纯增量：只读描述既有四层压缩机制 + 观察器链工具，不改动任何一层的执行路径；
// 未启用时行为与现状完全一致，可零成本回滚。
//
// ARTEX 会话/上下文压缩共三层（自外向内）：
//
//	L1 工具输出裁剪   : 单条工具结果上限（agentcore 工具输出裁剪），挡最大头的 token 膨胀。
//	L2 会话窗口压缩   : norma agent-core compaction.Config（compactionConfig(windowTokens)），
//	                   按角色（worker/planner/main_agent/chat）各自解析上下文窗口。
//	L3 冷图摘要压缩   : Compactor（compaction.go：minor/major → digest），压缩探索图
//	                   冷节点块，供规划器低成本回看；生命周期经 CompactionEvent 上报宿主。
//
// 降本提速的成因：L1 挡掉绝大部分无效 token；L2 控制活动上下文在窗口内；
// L3 让规划器不必重读全部历史探索。本文件把三层显式化，便于运维/调试/后续分层调参。
package agent

import "fmt"

// LayerKind 是压缩层标识。
type LayerKind string

const (
	LayerToolClip    LayerKind = "tool_output_clip"
	LayerWindow      LayerKind = "conversation_window"
	LayerGraphDigest LayerKind = "cold_graph_digest"
)

// LayerPlan 是单层压缩的策略快照。
type LayerPlan struct {
	Layer   LayerKind      `json:"layer"`
	Role    string         `json:"role,omitempty"`
	Enabled bool           `json:"enabled"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// LayerPlanFor 返回某角色在给定上下文窗口（token）下的分层压缩计划。
// windowTokens<=0 表示取默认窗口（与 compactionConfig 的语义一致）。
// 每一层都对应真实存在的执行路径，登记的是「实际会不会跑」，不是设想。
func LayerPlanFor(role string, windowTokens int) []LayerPlan {
	win := windowTokens
	if win <= 0 {
		win = defaultWindowK * 1000
	}
	return []LayerPlan{
		{
			Layer:   LayerToolClip,
			Role:    role,
			Enabled: true,
			Detail:  map[string]any{"note": "单条工具结果裁剪，挡 token 膨胀大头"},
		},
		{
			Layer:   LayerWindow,
			Role:    role,
			Enabled: true,
			Detail:  map[string]any{"window_tokens": win, "note": "norma agent-core 会话窗口压缩"},
		},
		{
			Layer:   LayerGraphDigest,
			Role:    role,
			Enabled: true,
			Detail:  map[string]any{"note": "冷图摘要 Compactor（minor/major → digest），生命周期走 CompactionEvent"},
		},
	}
}

// Describe 渲染人类可读的分层计划（每层一行）。
func DescribeLayerPlan(plan []LayerPlan) string {
	out := ""
	for _, p := range plan {
		state := "启用"
		if !p.Enabled {
			state = "旁路"
		}
		out += fmt.Sprintf("[%s] %s（%s）", p.Layer, state, p.Role)
		if w, ok := p.Detail["window_tokens"]; ok {
			out += fmt.Sprintf(" window=%v", w)
		}
		out += "\n"
	}
	return out
}

// ChainCompactionObserver 把多个 CompactionEvent 观察器串成一条链（逐个调用，单个
// panic 不影响后续——压缩观察是旁路可观测，不允许反噬执行路径）。
// 典型用法：cmp.SetObserver(agent.ChainCompactionObserver(hostObserver, traceObserver))。
func ChainCompactionObserver(observers ...func(CompactionEvent)) func(CompactionEvent) {
	return func(ev CompactionEvent) {
		for _, fn := range observers {
			if fn == nil {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				fn(ev)
			}()
		}
	}
}
