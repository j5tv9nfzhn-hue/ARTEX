//go:build windows

package selfupdate

// Windows 用命名互斥量做单实例仲裁，比"比 PID / 比端口"可靠得多：
//   - 拿锁 = WaitForSingleObject；持有者退出，内核自动释放；
//   - 持有者崩溃 → 等待方拿到 WAIT_ABANDONED，说明"上一任是异常死的"，照样接管；
//   - 名字挂在 Local\ 命名空间下，同一台机器不同安装目录用不同哈希，互不干扰。
//
// 名字里带安装根目录的哈希，是为了"两个不同目录的 ARTEX 各跑各的"仍然成立。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func lockObjectName(root, kind string) (string, error) {
	clean := strings.ToLower(filepath.Clean(strings.TrimSpace(root)))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("安装目录未知，无法命名实例锁")
	}
	sum := sha256.Sum256([]byte(clean))
	return fmt.Sprintf(`Local\ARTEX-HYDRA-%s-%s`, kind, hex.EncodeToString(sum[:8])), nil
}

func acquireNamedLock(root, kind string, wait time.Duration) (func(), LockState, error) {
	name, err := lockObjectName(root, kind)
	if err != nil {
		return nil, LockBusy, err
	}
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, LockBusy, err
	}
	h, err := windows.CreateMutex(nil, false, ptr)
	if err != nil {
		return nil, LockBusy, fmt.Errorf("创建实例锁失败：%w", err)
	}
	event, err := windows.WaitForSingleObject(h, uint32(wait/time.Millisecond))
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, LockBusy, fmt.Errorf("等待实例锁失败：%w", err)
	}
	switch event {
	case uint32(windows.WAIT_TIMEOUT):
		_ = windows.CloseHandle(h)
		return nil, LockBusy, nil
	case windows.WAIT_OBJECT_0:
		return func() { _ = windows.ReleaseMutex(h); _ = windows.CloseHandle(h) }, LockOwned, nil
	case windows.WAIT_ABANDONED:
		return func() { _ = windows.ReleaseMutex(h); _ = windows.CloseHandle(h) }, LockAbandoned, nil
	default:
		_ = windows.CloseHandle(h)
		return nil, LockBusy, fmt.Errorf("等待实例锁返回未知状态 %d", event)
	}
}
