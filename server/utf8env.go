package server

import (
	"os"
	"strings"
)

// childUTF8Env 返回给平台派生的子进程补加的 UTF-8 环境变量（批次 7 / #4–5）。
//
// 覆盖的场景：自定义工具的 Python 脚本（execPython）与经 PowerShell 执行的
// 自定义 command 工具。中文版 Windows 上，子进程默认按本地代码页（GBK）读写
// stdio——脚本把中文写成 GBK 字节后，Go 侧读回来的字符串不是合法 UTF-8，
// 进入 JSON/数据库时被替换成 U+FFFD（表现为「字符串乱码」）。
//
//	PYTHONUTF8=1        Python UTF-8 模式：stdin/stdout/stderr 与源码默认 UTF-8；
//	PYTHONIOENCODING   老版本 Python 的兜底（同上，仅 stdio）。
//
// 调用方应把结果放在 os.Environ() 之后、会话 env（tc.Env）之前，让显式会话
// 配置可以覆盖它。ARTEX_CHILD_UTF8=0 可整体关闭（排查编码问题时用）。
func childUTF8Env() []string {
	if childUTF8Disabled() {
		return nil
	}
	return []string{"PYTHONUTF8=1", "PYTHONIOENCODING=utf-8"}
}

func childUTF8Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ARTEX_CHILD_UTF8"))) {
	case "0", "false", "off", "no":
		return true
	default:
		return false
	}
}
