//go:build windows

package selfupdate

// Windows 没有 execve，入口只能派生核心并等它结束，再把退出码原样交回去。
//
// 这里必须**前台等待**：退出码 75 是与 start.bat 之间的重启协议，入口如果
// detach 或吃掉退出码，一键更新就会"看起来成功、其实没换版本"。
// 而 core 位于 versions/<ver>/ 下，程序自己的端口自举认得这个路径
// （见 cmd/artex/boot_windows.go 的 isOwnedProcess），
// 即使入口被强杀留下了孤儿 core，下一次启动也会先把它收掉。
import (
	"errors"
	"os"
	"os/exec"
)

func runHandoff(target string, l Layout) (int, error) {
	cmd := exec.Command(target, os.Args[1:]...)
	cmd.Dir = l.Root
	cmd.Env = append(os.Environ(), InstallRootEnv+"="+l.Root, handoffEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), err
	}
	return 1, err
}
