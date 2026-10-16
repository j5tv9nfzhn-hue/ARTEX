//go:build !windows

package main

// enableUTF8Console 在非 Windows 平台无需处理：Unix 终端与管道本身就是 UTF-8。
func enableUTF8Console() {}
