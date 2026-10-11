//go:build !windows

package main

// bootBeforeStart 在非 Windows 平台上不做任何自举处理。
//
// 端口占用自举依赖 Windows 的 IP Helper API 与服务控制管理器；
// 内置 PostgreSQL 拉起依赖 pg_ctl.exe。Linux/macOS 上既没有这些能力，
// 也不是本项目的发布目标（release 只产出 Windows 二进制），因此保持直通，
// 让端口冲突和数据库不可用由后续 NewManager / ListenAndServe 按原有方式报错。
//
// 保留这个空实现而不是给调用点加构建标签，是为了让 main.go 的启动流程
// 与平台无关——平台差异全部收敛在本文件与 boot_windows.go 之间。
func bootBeforeStart(addr, proxy, exeDir string) error { return nil }
