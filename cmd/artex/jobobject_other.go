//go:build !windows

package main

// guardProcessTree 在非 Windows 平台是空实现。
//
// Windows 之外的进程树由 SDK（norma）在每条命令结束/中断时按 ctx 终止进程组负责；
// 「父进程被强杀后子进程变孤儿」这一类问题在 Linux/macOS 需要 session/PDEATHSIG
// 之类的额外机制，本分支当前部署面是 Windows，暂不扩面（保持行为不变）。
func guardProcessTree() error { return nil }
