package agent

import "context"

// 上下文窗口用量要显示成「已用 / 总量」，但总量是模型属性（llm_profile.context_window_k），
// capture 层只看得见 usage 事件、看不见模型配置。用 ctx 把它带进来：
// 各 agent 本来就有 compactionWindow()，在开跑前挂上即可，capture 只负责读取。
type contextWindowKey struct{}

// WithContextWindow 把「本轮模型上下文窗口（token）」挂到 ctx 上。
// window <= 0 视为未知，capture 不会写 usage 的窗口信息，前端据此不显示容量环。
func WithContextWindow(ctx context.Context, window int) context.Context {
	if window <= 0 {
		return ctx
	}
	return context.WithValue(ctx, contextWindowKey{}, window)
}

func contextWindowFrom(ctx context.Context) int {
	if v, ok := ctx.Value(contextWindowKey{}).(int); ok {
		return v
	}
	return 0
}
