package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 工作空间目录大小的递归统计。
//
// 目录项本身的 os.FileInfo.Size() 只是目录记录占的几个扇区（Windows 上恒为 0），
// 用户在文件管理器里看到的「占用」是整棵子树的累计值，必须自己走一遍。直接每次
// 展开都全量走盘会让深目录把表格拖住，所以这里做三件事：
//
//  1. 递归累计 size / 文件数 / 子目录数，跳过符号链接（不跟随，避免环与越界）；
//  2. 单次统计有预算（时间 + 条目数），超了就返回已经量到的部分并标记 Partial，
//     宁可在界面上显示「≥ 12.3 MB」也不让请求卡死；
//  3. 结果按绝对路径缓存，写入/删除/上传之后由调用方失效对应路径及其祖先。

const (
	// wsMeasureBudget 是单次递归统计的时间上限。
	wsMeasureBudget = 400 * time.Millisecond
	// wsMeasureMaxEntries 是单次递归统计的条目上限，兜住超大目录。
	wsMeasureMaxEntries = 200_000
	// wsSizeTTL 是完整统计结果的缓存时长。
	wsSizeTTL = 15 * time.Second
	// wsSizePartialTTL 是「没量完」的结果缓存时长：短一些，好让下一次展开量完。
	wsSizePartialTTL = 2 * time.Second
	// wsMeasureWorkers 是同一层多个目录并发统计的并发度。
	wsMeasureWorkers = 4
)

// wsUsage 是一棵子树的占用统计。
type wsUsage struct {
	Size    int64
	Files   int
	Dirs    int
	Partial bool // 没量完（预算用尽或个别条目读不到）
	At      time.Time
}

var wsUsageCache sync.Map // abs path -> wsUsage

// wsUsageFor returns the cached usage for abs, measuring it when the entry is
// missing or stale.
func wsUsageFor(abs string) wsUsage {
	if v, ok := wsUsageCache.Load(abs); ok {
		u := v.(wsUsage)
		ttl := wsSizeTTL
		if u.Partial {
			ttl = wsSizePartialTTL
		}
		if time.Since(u.At) <= ttl {
			return u
		}
	}
	u := wsMeasure(abs)
	wsUsageCache.Store(abs, u)
	return u
}

// wsMeasure walks abs and accumulates its usage. Errors on individual entries are
// counted as Partial rather than aborting: one unreadable file should not blank
// out the whole number.
func wsMeasure(abs string) wsUsage {
	u := wsUsage{At: time.Now()}

	if fi, err := os.Stat(abs); err != nil {
		u.Partial = true
		return u
	} else if !fi.IsDir() {
		u.Size = fi.Size()
		u.Files = 1
		return u
	}

	root := filepath.Clean(abs)
	deadline := u.At.Add(wsMeasureBudget)
	seen := 0

	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 权限、占用中、路径过长……都按「没量到」处理。
			u.Partial = true
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		if d.IsDir() {
			u.Dirs++
		} else {
			if d.Type()&os.ModeSymlink != 0 {
				// 不跟随符号链接：既避免统计到工作区之外，也避免环。
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				u.Partial = true
				return nil
			}
			u.Size += info.Size()
			u.Files++
		}
		seen++
		if seen >= wsMeasureMaxEntries || time.Now().After(deadline) {
			u.Partial = true
			return fs.SkipAll
		}
		return nil
	})
	return u
}

// wsInvalidateUsage drops the cached usage of abs and every ancestor up to the
// work dir, so a write or delete anywhere below it is reflected on the way up.
func (s *Server) wsInvalidateUsage(abs string) {
	base := filepath.Clean(s.m.dir)
	p := filepath.Clean(abs)
	for {
		wsUsageCache.Delete(p)
		if p == base {
			return
		}
		parent := filepath.Dir(p)
		if parent == p || len(parent) < len(base) {
			return
		}
		p = parent
	}
}
