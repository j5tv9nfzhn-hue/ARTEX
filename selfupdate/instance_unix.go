//go:build !windows

package selfupdate

// Unix 用 flock(2) 做单实例仲裁：锁跟着打开的文件描述符走，
// 持有者进程一死内核就释放，不会有"僵尸锁文件"要人手动删。
//
// 锁文件放在 updates/ 下并按安装根目录哈希命名，互不干扰。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func lockFilePath(root, kind string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(root))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("安装目录未知，无法命名实例锁")
	}
	sum := sha256.Sum256([]byte(clean))
	return filepath.Join(clean, layoutUpdatesDir, kind+"-"+hex.EncodeToString(sum[:6])+".lock"), nil
}

func acquireNamedLock(root, kind string, wait time.Duration) (func(), LockState, error) {
	p, err := lockFilePath(root, kind)
	if err != nil {
		return nil, LockBusy, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, LockBusy, fmt.Errorf("创建实例锁目录失败：%w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, LockBusy, fmt.Errorf("打开实例锁失败：%w", err)
	}
	fd := int(f.Fd())
	deadline := time.Now().Add(wait)
	for {
		flockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if flockErr == nil {
			release := func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = f.Close()
			}
			return release, LockOwned, nil
		}
		if !errors.Is(flockErr, unix.EWOULDBLOCK) && !errors.Is(flockErr, unix.EAGAIN) {
			_ = f.Close()
			return nil, LockBusy, fmt.Errorf("获取实例锁失败：%w", flockErr)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, LockBusy, nil
		}
		time.Sleep(120 * time.Millisecond)
	}
}
