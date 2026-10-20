package agent

import (
	"context"
	"sync"
)

// ConvLedger 是一条会话（控制台聊天 / Harness 工作台）在**执行中**的 token 账本。
//
// 与任务级预算的分工（server/engine.go checkTokenBudget）：
//   - 任务预算在每次 run 收尾后读 llm_usage 累计值，超过就暂停整个任务，等人工恢复；
//   - 会话预算在**每一条用量事件到达时**就地结算，超过上限立刻终止本轮，
//     不需要人工干预，也不会让一整轮 agent 先跑完再停。
//
// 计量口径与任务预算一致：input+output，缓存读/写不计入（db.TaskTokenTotal 同口径）。
// 缓存读按 0.1x 计价、数量级远大于常规输入，计入会让「预算」与实际花费脱钩。
type ConvLedger struct {
	mu        sync.Mutex
	convID    int64
	limit     int64  // <=0 表示不限
	mode      string // off | warn | enforce
	baseline  int64  // 本轮开始前已结算的历史用量（kind=result 行汇总）
	turn      int64  // 本轮内累计（SDK 的 usage 事件是本轮累计值，取最大值）
	exhausted bool
	stopped   bool
	stop      func()
}

// NewConvLedger 建立一个会话账本。baseline 是历史已用（来自 db.ConvTokenTotal），
// limit<=0 表示不限，mode 为空按 enforce 处理。
func NewConvLedger(convID, limit int64, mode string, baseline int64) *ConvLedger {
	if mode == "" {
		mode = "enforce"
	}
	if baseline < 0 {
		baseline = 0
	}
	return &ConvLedger{convID: convID, limit: limit, mode: mode, baseline: baseline}
}

// SetStop 绑定「终止本轮」的回调（run 的 cancel）。只在 enforce 且越线时调用一次。
func (l *ConvLedger) SetStop(stop func()) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.stop = stop
	l.mu.Unlock()
}

// Observe 接收一条 usage 事件。SDK 上报的是**本轮累计用量**（不是增量），
// 所以这里取最大值而不是加法累计，否则会指数级放大。
func (l *ConvLedger) Observe(inTokens, outTokens int) {
	if l == nil {
		return
	}
	turn := int64(inTokens) + int64(outTokens)
	if turn < 0 {
		turn = 0
	}
	l.mu.Lock()
	if turn > l.turn {
		l.turn = turn
	}
	used := l.baseline + l.turn
	shouldStop := l.limit > 0 && l.mode == "enforce" && used >= l.limit && !l.stopped
	var stop func()
	if shouldStop {
		l.stopped = true
		l.exhausted = true
		stop = l.stop
	}
	l.mu.Unlock()
	if shouldStop && stop != nil {
		stop()
	}
}

// Used 返回本轮开始前 baseline + 本轮累计。
func (l *ConvLedger) Used() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.baseline + l.turn
}

// Exhausted 报告本轮是否因预算被终止。
func (l *ConvLedger) Exhausted() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exhausted
}

// Tier 给出当前档位：ok(<70%) / watch(70–85%) / tight(85–100%) / exhausted(>=100%)。
// limit<=0 或 mode=off 时恒为 ok——不配置就等于不降级，和以前行为一致。
func (l *ConvLedger) Tier() string {
	if l == nil {
		return "ok"
	}
	l.mu.Lock()
	limit := l.limit
	mode := l.mode
	used := l.baseline + l.turn
	l.mu.Unlock()
	return convBudgetTier(used, limit, mode)
}

// convBudgetTier 是档位判定的唯一实现，服务端视图和运行中账本共用，避免两处阈值漂移。
func convBudgetTier(used, limit int64, mode string) string {
	if limit <= 0 || mode == "off" {
		return "ok"
	}
	if used >= limit {
		return "exhausted"
	}
	switch {
	case used*100 >= limit*85:
		return "tight"
	case used*100 >= limit*70:
		return "watch"
	default:
		return "ok"
	}
}

type convLedgerKey struct{}

// WithConvLedger 把账本挂到 run 的 ctx 上；capture 循环每次收到 usage 就结算一次。
func WithConvLedger(ctx context.Context, l *ConvLedger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, convLedgerKey{}, l)
}

// ConvLedgerFrom 取出当前 run 的账本（没有则 nil）。
func ConvLedgerFrom(ctx context.Context) *ConvLedger {
	l, _ := ctx.Value(convLedgerKey{}).(*ConvLedger)
	if l == nil {
		return nil
	}
	return l
}

// ConvBudgetTier 对外暴露档位判定，让 server 侧的只读视图与运行中的账本共用同一套阈值。
func ConvBudgetTier(used, limit int64, mode string) string {
	return convBudgetTier(used, limit, mode)
}
