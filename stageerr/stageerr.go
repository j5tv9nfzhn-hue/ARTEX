// Package stageerr 统一平台自有边界的错误包装（批次 5 / #6「半路不知名错误」）：
// 在错误文本里补上发生阶段与动作的中文说明，同时用 %w 保留底层原文，便于日志检索
// 与界面展示。只用于平台控制的边界（工具执行包装、技能/工作区落盘、hook 解析），
// 不改造 norma SDK 内部的英语错误。
package stageerr

import "fmt"

// 阶段常量，与 reliability 面板的 stage 分类保持一致。
const (
	StageToolExec     = "tool_exec"     // 工具执行
	StageToolParse    = "tool_parse"    // 入参解析
	StageToolValidate = "tool_validate" // 入参结构校验
	StageHook         = "hook"          // 钩子/拦截
	StagePersist      = "persist"       // 磁盘落盘
)

// Wrap 把底层错误包成「动作（stage=…）：原文」。err 为 nil 时返回 nil。
func Wrap(stage, action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s（stage=%s）：%w", action, stage, err)
}

// Msgf 生成带阶段前缀的新错误（不包裹底层 error）。
func Msgf(stage, format string, args ...any) error {
	return fmt.Errorf("（stage=%s）"+format, append([]any{stage}, args...)...)
}

// Tag 给既有错误信息追加阶段标签，不改变其为普通 error 的事实。
func Tag(stage, msg string) string {
	return fmt.Sprintf("%s（stage=%s）", msg, stage)
}
