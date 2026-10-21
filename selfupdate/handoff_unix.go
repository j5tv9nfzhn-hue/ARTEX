//go:build !windows

package selfupdate

// Unix 上用 exec 直接替换进程映像，而不是再 fork 一个子进程。
//
// 原因是信号与退出码必须由同一个 PID 承担：守护脚本 start.sh 只记着它拉起的那个
// PID（`"$BIN" &` 得到的 $!），停止时也只 kill 那个 PID。如果入口再 fork 一个
// 核心进程，SIGTERM 只打到入口，核心会带着监听端口变成孤儿，下一次启动就得靠
// 端口自举去把它杀掉——那是能避则避的一整类问题。
//
// exec 成功后本函数不会返回：进程映像被核心程序取代，退出码由内核直接交回给
// 守护脚本，语义与"从来没有这一层"完全一致。
import (
	"os"
	"syscall"
)

func runHandoff(target string, l Layout) (int, error) {
	env := append(os.Environ(), InstallRootEnv+"="+l.Root, handoffEnv+"=1")
	argv := append([]string{target}, os.Args[1:]...)
	if err := syscall.Exec(target, argv, env); err != nil {
		return 1, err
	}
	return 0, nil // 走不到：exec 成功即替换映像
}
