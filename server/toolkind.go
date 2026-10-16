package server

// 工具 kind 常量（批次 7 / #174）。此前 "command"/"script"/"http"/"shell" 这四个
// 字面量在本包内散落 10+ 处（HTTP API、host 工具、装配过滤各写一遍），一次拼写
// 漂移就会静默丢工具。集中为常量后，kind 集合只有一个定义点；它们同时是数据库
// 列值与前端契约（web/src/app/(main)/system/tools/page.tsx 的 kind 下拉）。
//
// 注意：不要与 MCP transport（"stdio"/"http"）、URL scheme（"http"/"https"）混用，
// 那些是不同语义的字符串，本文件不覆盖。
const (
	toolKindCommand = "command" // 渲染命令模板，经 Bash 底层执行
	toolKindScript  = "script"  // 仅 Python：临时文件 + stdin JSON + TOOL_* env
	toolKindHTTP    = "http"    // 原生 HTTP 请求（可带代理）
	toolKindShell   = "shell"   // 仅环境声明（写入 Bash 描述），不可执行
)

// toolKindKnown 报告 kind 是否属于允许写入的四种之一。
func toolKindKnown(kind string) bool {
	switch kind {
	case toolKindCommand, toolKindScript, toolKindHTTP, toolKindShell:
		return true
	default:
		return false
	}
}
