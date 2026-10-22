package selfupdate

// 引导程序（根目录的 artex-rea[。exe]）自我刷新。
//
// 为什么需要它：发布包里引导程序和核心是同一个二进制，引导程序只做三件事：
// 读 pointer.json → 启动 versions/<ver>/ 的核心 → 核心以 75 退出时再启动一次。
// 但 in-app 更新只替换 versions/<ver>/下的文件与指针，**根目录的引导程序从来不会被替换**。
// 而“核心退出 75 后由谁拉起新版本”的托管循环是后来才加进引导程序的，
// 于是用旧 zip 装出来的环境会出现：更新成功、核心退出、而没有任何进程把新版本打开。
//
// 这里在每次核心启动时比对两个文件的字节，不一致就把自己复制过去。
// Windows 不允许覆盖运行中的 exe，但允许重命名，所以顺序是：
// 先复制成 .new → 把旧引导程序改名为 .old-<ts> → 把 .new 改名为正式版本。
// 全程 best-effort：任何一步失败只记日志，不影响本次启动。

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// outerBackupPrefix 是替换引导程序时留下的备份前缀，下次启动清掉。
const outerBackupPrefix = entryStem + ".old-"

// outerRefreshed 记录本次启动是否真的动过根目录的引导程序。
// ExitForRestart 用它判断“外层是否可信”：动过说明跑着的那个是旧版本。
var outerRefreshed atomic.Bool

// OuterPath 返回安装根目录的引导程序路径。
func OuterPath(l Layout) string {
	return filepath.Join(l.Root, entryStem+exeExt())
}

// OuterRefreshed 报告本次启动是否把根目录的引导程序换成了当前版本。
func OuterRefreshed() bool { return outerRefreshed.Load() }

// RefreshOuter 用当前运行的核心二进制刷新安装根目录的引导程序。
func RefreshOuter(l Layout) {
	cleanupOuterBackups(l)

	self, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, e := filepath.EvalSymlinks(self); e == nil {
		self = resolved
	}
	// 直接双击引导程序时本进程就在根目录，没有“外层比核心旧”这回事。
	if strings.EqualFold(filepath.Dir(self), filepath.Clean(l.Root)) {
		return
	}

	outer := OuterPath(l)
	if _, err := os.Stat(outer); err != nil {
		return // 开发态（go run）或包里没带引导程序
	}
	selfSum, err := fileSHA256(self)
	if err != nil {
		return
	}
	outerSum, err := fileSHA256(outer)
	if err != nil {
		return
	}
	if selfSum == outerSum {
		return // 已经是同一个二进制
	}

	tmp := outer + ".new"
	if err := copyFile(self, tmp); err != nil {
		logUpdate(l, "刷新引导程序失败（复制新文件）：%v", err)
		return
	}
	backup := filepath.Join(l.Root, outerBackupPrefix+time.Now().Format("20060102-150405"))
	if err := os.Rename(outer, backup); err != nil {
		_ = os.Remove(tmp)
		logUpdate(l, "刷新引导程序失败（旧文件被占用）：%v", err)
		return
	}
	if err := os.Rename(tmp, outer); err != nil {
		_ = os.Rename(backup, outer)
		_ = os.Remove(tmp)
		logUpdate(l, "刷新引导程序失败（写入新文件）：%v", err)
		return
	}

	outerRefreshed.Store(true)
	// 旧引导程序可能还在运行，删不掉就留给下次启动。
	_ = os.Remove(backup)
	logUpdate(l, "已将根目录引导程序刷新为当前版本（旧文件 %s）", filepath.Base(backup))
}

// cleanupOuterBackups 清掉上次替换留下的旧引导程序。
func cleanupOuterBackups(l Layout) {
	entries, err := os.ReadDir(l.Root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), outerBackupPrefix) {
			continue
		}
		_ = os.Remove(filepath.Join(l.Root, e.Name()))
	}
}

// copyFile 把 src 完整复制到 dst（部分文件系统不支持 rename 跨目录时用的）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
