//go:build !windows

package main

// guardProcessTree 在 Linux / macOS 等非 Windows 平台上保持无侵入返回。
//
// 三端进程树收口分工：
//   - Windows：由 jobobject_windows.go 的 Kill-On-Close Job Object 统一收口；
//   - Linux / macOS：norma SDK 的 Bash 工具与 agent/proc_limiter_unix.go 均为子进程
//     启用独立进程组（Setpgid: true），在超时或取消时向 -pgid 发送 SIGKILL 整组回收；
//     守护脚本 start.sh 捕获 INT/TERM 并转发给主进程，配合上下文取消链完成退出清理。
func guardProcessTree() error { return nil }
