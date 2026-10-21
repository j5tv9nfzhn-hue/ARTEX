package agent

import "context"

// LiveDelta 是一条「还没落库」的模型增量：thinking / text / tool_use。
//
// 只走 SSE 给界面看，不写 activity 表——落库的仍是 capture 合并后的整块记录。
// 这样「思考时界面不动」的老问题被拆成两层：
//   - 界面：按帧刷新单行实时内容（本文件 + server 的 live 通道）；
//   - 历史：仍是整块 activity（回放、回滚、审计的口径不变）。
type LiveDelta struct {
	Kind      string
	Text      string
	Tool      string
	ToolUseID string
}

type liveDeltaKey struct{}

// WithLiveDelta 把接收器挂到 ctx 上。没挂（测试、非任务路径）时 emitLive 是 no-op。
func WithLiveDelta(ctx context.Context, fn func(LiveDelta)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, liveDeltaKey{}, fn)
}

func emitLive(ctx context.Context, d LiveDelta) {
	if fn, ok := ctx.Value(liveDeltaKey{}).(func(LiveDelta)); ok && fn != nil {
		fn(d)
	}
}
