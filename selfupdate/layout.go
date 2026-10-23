package selfupdate

// 安装布局（P2 落点）：把「进程身份」与「文件身份」解耦。
//
// 旧实现把两者绑在一起——谁是"当前版本"由"正在跑的那个 exe 文件名"决定，
// 于是出现了三类事故：暂存件 artex-rea.new.exe 被当作正式程序启动、
// 固定名 .old 被锁死后永久阻断升级、以及换装必须由"要被替换的那个进程"自己完成。
//
// 现在磁盘结构是：
//
//	<root>/
//	├── artex-rea.exe                入口（也可能是任意历史版本，它只负责转发）
//	├── pointer.json                 {"current":"2.3.0","previous":"2.2.2"}  原子写
//	├── versions/<ver>/artex-rea-core.exe   真正的核心程序，升级只往这里放
//	├── updates/journal.json         跨进程升级台账
//	├── updates/staging/<ver>/      下载与校验的暂存区（名字带 .staged，不是正式程序）
//	└── logs/update.log             追加式升级日志（进程退出也留得住）
//
// 由此得到三条不变量：
//  1. 升级不再替换任何**正在运行**的文件，只新增 versions/<ver>/ 并原子改指针；
//  2. 回滚 = 把指针指回上一个版本，不需要搬运文件；
//  3. 任何一步失败都只落在 journal 与日志里，可被下一个进程、下一个页面读懂。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
)

const (
	// InstallRootEnv 由入口进程在转发（handoff）时设置，保证核心进程与入口
	// 对"安装根目录"的判断完全一致，不依赖任何路径猜测。
	//
	// 常量本身定义在 config 包里：config.BaseDir() 也要读它（否则核心程序会把
	// data/ 解析进 versions/<ver>/）。两处共用同一个常量，避免字符串写歪一个。
	InstallRootEnv = config.InstallRootEnv

	// handoffEnv 标记"我是被入口转发起起来的"，仅用于日志与诊断。
	handoffEnv = "ARTEX_SELFUPDATE_HANDOFF"

	// keepVersions 是 versions/ 下保留的历史版本数（含当前与上一个）。
	// 每个版本约一个二进制的体积，3 个足以覆盖"升级失败回滚 + 再回滚"。
	keepVersions = 3

	layoutVersionsDir = "versions"
	layoutUpdatesDir  = "updates"
	layoutStagingDir  = "staging"
	layoutLogsDir     = "logs"

	pointerName   = "pointer.json"
	journalName   = "journal.json"
	updateLogName = "update.log"

	entryStem = "artex-rea"
	coreStem  = "artex-rea-core"

	// bundledRetiredSuffix 是"已收进 versions/、但根目录那份一时删不掉"时的改名后缀。
	// Windows 上被进程占用的 exe 删不掉是常态，改名几乎总能成功；改完由
	// CleanupLegacy 在下次启动时按升级残留删掉，不会永远留着。
	bundledRetiredSuffix = ".bundled-"

	// stagedLoosePrefix 匹配 updates/ 根下散落的安装包（artex-rea.staged.exe、
	// artex-rea.staged.exe.sha256）。它们不在 staging/<ver>/ 里，既不是目录、
	// 也不在旧协议残留名单上——三套清理逻辑原先都不认，于是永远留在盘上。
	stagedLoosePrefix = entryStem + ".staged"
)

// Layout 是一次升级涉及的全部路径，全部挂在安装根目录下。
type Layout struct {
	Root     string // 安装根目录
	Entry    string // 当前运行的可执行文件（入口或核心）
	Versions string // versions/
	Pointer  string // pointer.json
	Updates  string // updates/
	Staging  string // updates/staging/
	Journal  string // updates/journal.json
	Log      string // logs/update.log
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ResolveLayout 按当前可执行文件推导安装布局。
func ResolveLayout() (Layout, error) {
	exe, err := os.Executable()
	if err != nil {
		return Layout{}, fmt.Errorf("无法确定可执行文件位置：%w", err)
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	return layoutFor(installRoot(exe), exe), nil
}

// installRoot 返回安装根目录。
//
// 判定顺序（每一步都只看确定的事实，不猜）：
//  1. 环境变量 ARTEX_INSTALL_ROOT —— 入口转发时显式写入，优先级最高；
//  2. 可执行文件位于 <root>/versions/<ver>/ 下 —— 向上两层；
//  3. 否则就是可执行文件所在目录（历史行为，保证 v2.2.2 及更早的安装可用）。
func installRoot(exe string) string {
	if v := strings.TrimSpace(os.Getenv(InstallRootEnv)); v != "" {
		return filepath.Clean(v)
	}
	dir := filepath.Dir(exe)
	if filepath.Base(filepath.Dir(dir)) == layoutVersionsDir {
		return filepath.Dir(filepath.Dir(dir))
	}
	if filepath.Base(dir) == layoutVersionsDir {
		return filepath.Dir(dir)
	}
	return dir
}

func layoutFor(root, exe string) Layout {
	return Layout{
		Root:     root,
		Entry:    exe,
		Versions: filepath.Join(root, layoutVersionsDir),
		Pointer:  filepath.Join(root, pointerName),
		Updates:  filepath.Join(root, layoutUpdatesDir),
		Staging:  filepath.Join(root, layoutUpdatesDir, layoutStagingDir),
		Journal:  filepath.Join(root, layoutUpdatesDir, journalName),
		Log:      filepath.Join(root, layoutLogsDir, updateLogName),
	}
}

// EnsureLayoutDirs 建好台账、日志、版本目录。失败只返回错误，不 panic。
func EnsureLayoutDirs(l Layout) error {
	for _, dir := range []string{l.Versions, l.Staging, filepath.Dir(l.Log)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败：%w", dir, err)
		}
	}
	return nil
}

// versionKey 把版本号净化成目录名：只保留字母数字与 . _ -，去掉前导 v。
//
// 版本号会拼进路径，所以这里必须拒绝一切路径分隔符与 ..（哪怕上游已经校验过一次，
// 纵深防御也不能只做在一处）。
func versionKey(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" || strings.Contains(out, "..") {
		return "unknown"
	}
	return out
}

// CorePath 返回某个版本的核心程序路径。
func CorePath(l Layout, version string) string {
	return filepath.Join(l.Versions, versionKey(version), coreStem+exeExt())
}

// StagedPath 返回某个版本的暂存件路径。
//
// 文件名固定为 artex-rea.staged[.exe]：带 .staged 是为了让"被误启动"能在启动守卫里
// 一眼认出来（见 RefuseUnsafeExecutable），带 .exe 是为了 Windows 上能真正执行冒烟测试。
func StagedPath(l Layout, version string) string {
	return filepath.Join(l.Staging, versionKey(version), entryStem+".staged"+exeExt())
}

// InstallCore 把校验过的暂存件安装成 versions/<ver>/artex-rea-core[.exe]。
//
// 先写 .part 再 rename：任何时刻目标路径要么不存在、要么是完整文件，
// 不存在"半个可执行文件"的中间态。
func InstallCore(l Layout, version, src string) (string, error) {
	dst := CorePath(l, version)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("创建版本目录失败：%w", err)
	}
	if err := copyFileAtomic(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开源文件失败：%w", err)
	}
	defer in.Close()

	tmp := dst + ".part-" + strconv.Itoa(os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("复制文件失败：%w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("写入磁盘失败：%w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭临时文件失败：%w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("安装文件失败：%w", err)
	}
	_ = os.Chmod(dst, 0o755)
	return nil
}

// InstalledVersions 列出 versions/ 下已安装的版本目录名（已排序，新的在前）。
func InstalledVersions(l Layout) []string {
	entries, err := os.ReadDir(l.Versions)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(l.Versions, e.Name(), coreStem+exeExt())); err != nil {
			continue // 目录不完整（安装中断），不算一个版本
		}
		out = append(out, e.Name())
	}
	sort.Slice(out, func(i, j int) bool {
		c, ok := CompareVersions(out[i], out[j])
		if !ok {
			return out[i] > out[j]
		}
		return c > 0
	})
	return out
}

// PruneVersions 只保留最近 keep 个版本，并且永远不删当前版本与回滚目标。
//
// 删除失败只记日志：旧版本被某个进程锁住（Windows 上正在运行的 exe 不能删）是常见情况，
// 它不该影响任何东西——下次启动会再试一次。
func PruneVersions(l Layout, keep int) {
	if keep < 2 {
		keep = 2
	}
	// 指针读不出来就一个字都不删：不知道谁是 current 时，任何一次删除都可能
	// 删掉正在跑的那个版本。宁可多占一份 40MB，也不要在坏指针上赌。
	ptr, ok := ReadPointer(l)
	if !ok || strings.TrimSpace(ptr.Current) == "" {
		return
	}
	all := InstalledVersions(l)
	if len(all) <= keep {
		return
	}
	protected := map[string]bool{
		versionKey(ptr.Current):  true,
		versionKey(ptr.Previous): true,
	}
	// 再加一道保险：真正在跑的那个二进制所在的版本目录。指针被手改过、或指针与
	// 实际启动的版本暂时不一致时，靠它还兜得住（Windows 上删运行中的 exe 会失败，
	// 但会刷一屏失败日志，也容易在杀掉进程后的下一轮真的删掉）。
	if v := runningVersionDir(l); v != "" {
		protected[v] = true
	}
	removed := 0
	for i, ver := range all {
		if i < keep || protected[ver] {
			continue
		}
		dir := filepath.Join(l.Versions, ver)
		if err := os.RemoveAll(dir); err != nil {
			logUpdate(l, "清理旧版本 %s 失败（不影响运行，下次再试）：%v", ver, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		logUpdate(l, "已清理 %d 个旧版本目录，保留 %d 个", removed, len(all)-removed)
	}
}

// runningVersionDir 返回"当前这个进程"的二进制所在的版本目录名。
//
// 只在可执行文件确实位于 <root>/versions/<ver>/ 下时返回，否则返回空串
// （入口在根目录、go run、开发构建都属于后者）。
func runningVersionDir(l Layout) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	dir := filepath.Clean(filepath.Dir(exe))
	if !strings.EqualFold(filepath.Clean(filepath.Dir(dir)), filepath.Clean(l.Versions)) {
		return ""
	}
	return filepath.Base(dir)
}

// Handoff 是入口进程的转发：如果 pointer.json 指向的核心程序不是自己，就执行它。
//
// 返回值 ok=true 表示"已经交给核心程序执行"，调用方必须立刻带着这个退出码结束进程。
// 之所以用"复制入口 + 指针"而不是符号链接：Windows 建符号链接需要管理员或开发者模式，
// 目录 junction 又只对本地卷有效；而一个普通的指针文件任何环境都读得了。
func Handoff() (int, bool) {
	l, err := ResolveLayout()
	if err != nil {
		return 0, false
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, false
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	ptr, ok := ReadPointer(l)
	if !ok || strings.TrimSpace(ptr.Current) == "" {
		return 0, false
	}
	target := CorePath(l, ptr.Current)
	if filepath.Clean(target) == filepath.Clean(exe) {
		return 0, false // 自己就是那个核心，继续转交会递归
	}
	if _, err := os.Stat(target); err != nil {
		logUpdate(l, "指针指向的版本 %s 不存在：%v，先按当前可执行文件启动", ptr.Current, err)
		return 0, false
	}
	logUpdate(l, "控制权转交：%s → %s", filepath.Base(exe), target)

	// 监护循环：入口 exe 本来就是父进程（在等 core 退出），把"等"改成"等 + 按需重启"，
	// 就不再需要 start.sh / start.bat 在外面再套一层循环。
	// 只有"孩子写了重启意图"才重新拉起：用户 Ctrl-C 出去的进程不会被复活。
	var restarts []time.Time
	for {
		code, err := runHandoff(target, l)
		if err != nil {
			logUpdate(l, "转交执行结束（退出码 %d）：%v", code, err)
		}
		if code != ExitRestart {
			return code, true
		}
		intent, wanted := ConsumeRestartIntent(l)
		if !wanted {
			logUpdate(l, "子进程以 %d 退出但没有重启意图（用户中断或崩溃），不再拉起", code)
			return code, true
		}
		if !allowRestart(&restarts) {
			logUpdate(l, "重启过于频繁（%s 内上限 %d 次），停止自动拉起；详见 logs/update.log",
				handoffRestartWindow, handoffMaxRestarts)
			return code, true
		}
		// 安装与回滚都只改指针，每一轮都重新读，才能真正"换成新版本"。
		if next, ok := ReadPointer(l); ok && strings.TrimSpace(next.Current) != "" {
			candidate := CorePath(l, next.Current)
			if filepath.Clean(candidate) != filepath.Clean(target) {
				if _, err := os.Stat(candidate); err == nil {
					logUpdate(l, "按重启意图（%s）改跑 versions/%s", intent.Reason, versionKey(next.Current))
					target = candidate
				}
			}
		}
		logUpdate(l, "按重启意图（%s，目标 %s）重新拉起核心进程", intent.Reason, orUnknown(intent.To))
	}
}

// RefuseUnsafeExecutable 是启动守卫：拒绝从"升级过程中的临时名字"启动。
//
// 这条守卫直接封死了一类真实事故：暂存件 artex-rea.new.exe 被当成正式程序启动后，
// 它按 os.Executable() 推出了属于自己的一套 marker/暂存路径，等于在同一台机器上
// 出现了"第二个安装"，既不会被现有逻辑认领，又会和正式进程抢同一批端口。
func RefuseUnsafeExecutable() error {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	lower := strings.ToLower(exe)
	base := strings.ToLower(filepath.Base(exe))
	for _, bad := range []string{".new", ".old", ".failed", ".swap", ".part", ".staged", ".bak", ".tmp"} {
		if strings.Contains(base, bad) {
			return fmt.Errorf(
				"检测到从升级临时文件启动（%s）：这不是一个可用的安装。\n"+
					"请用 start.bat / start.sh 启动正式程序（artex-rea%s）；"+
					"若上次升级失败，请查看 logs/update.log 与 updates/journal.json，或删除该残留文件后重启。",
				exe, exeExt())
		}
	}
	if strings.Contains(lower, string(filepath.Separator)+layoutStagingDir+string(filepath.Separator)) {
		return fmt.Errorf("检测到从暂存区启动（%s）：请改用正式程序启动。", exe)
	}
	return nil
}
