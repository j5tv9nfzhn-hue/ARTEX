//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// enableUTF8Console 把 Windows 控制台的输入/输出代码页切到 UTF-8（65001）。
//
// 背景（批次 7 / #4–5 编码审计）：Go 以 UTF-8 字节写 stdout/stderr，而中文版
// Windows 的控制台默认是 GBK（936）。直连控制台运行时，启动横幅与日志里的中文
// 会被按 GBK 解释成乱码。start.bat 已用 `chcp 65001` 处理「经脚本启动」的路径，
// 这里补上「直接运行 artex.exe / go run」的路径：
//  1. 本进程的标准输出、错误按 UTF-8 渲染；
//  2. 之后派生的子进程（powershell / cmd / python）经 GetConsoleOutputCP()
//     看到 UTF-8，PowerShell 在重定向管道里默认采用的输出编码随之变为 UTF-8，
//     从源头避免 GBK 字节进入 JSON、数据库与前端。
//
// 可用 ARTEX_UTF8_CONSOLE=0 关闭（老式终端或需要固定代码页时）。
func enableUTF8Console() {
	if !utf8ConsoleEnabled() {
		return
	}
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
}

func utf8ConsoleEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ARTEX_UTF8_CONSOLE"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}
