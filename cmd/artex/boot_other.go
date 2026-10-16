//go:build !windows

package main

// bootBeforeStart 在非 Windows 平台上不做任何自举处理。
//
// 端口占用自举依赖 Windows 的 IP Helper API（GetExtendedTcpTable）：Linux/macOS 上
// 既没有这个能力，也不是本项目的发布目标（release 只产出 Windows 二进制），因此保持
// 直通，让端口冲突由后续 ListenAndServe 按原有方式报错。
//
// 数据库不需要自举：存储是进程内打开的单个 SQLite 文件，没有服务要拉起、没有端口要等。
//
// 保留这个空实现而不是给调用点加构建标签，是为了让 main.go 的启动流程与平台无关
// ——平台差异全部收敛在本文件与 boot_windows.go 之间。
func bootBeforeStart(addr, proxy, exeDir string) error { return nil }
