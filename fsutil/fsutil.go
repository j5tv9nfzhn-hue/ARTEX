// Package fsutil 提供平台自有写盘路径共用的小工具（批次 5 / #7–8）。
// 只有一个职责：原子写——让读者（前端文件树、agent 后续 Read、审阅者）
// 永远看不到写了一半的文件。参考 evidence/store.go 的 writeBody 实现。
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic 原子地把 data 写入 path：在目标同目录建临时文件（rename 必须
// 同卷）、写入并 fsync 后 rename 覆盖目标。Windows 上 os.Rename 走
// MoveFileEx(MOVEFILE_REPLACE_EXISTING)，可覆盖已存在文件；失败时目标保持不变、
// 临时文件被清理。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	discard := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(data); err != nil {
		discard()
		return err
	}
	// fsync 先行落盘，rename 之后的可见性才有意义（与 evidence/store.go 同策略）。
	if err := f.Sync(); err != nil {
		discard()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// CreateTemp 用 0600；需要可读权限的文件（技能、工作区）显式放开。
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
