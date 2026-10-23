//go:build !windows

package main

// enableUTF8Console 在非 Windows 平台无需处理：Unix 终端与管道本身就是 UTF-8。
func enableUTF8Console() {}

// consoleAttached 在非 Windows 平台恒为 false：Unix 由 start.sh 负责「Ctrl-C 停止」，
// 不需要、也不该在退出前停几秒。
func consoleAttached() bool { return false }
