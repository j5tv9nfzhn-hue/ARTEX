package selfupdate

// 重启意图：把"要重启"从裸退出码升级成可观测的意图文件。
//
// 之前 exit 75 同时承担两件事："我是更新要的重启"和"上层脚本请拉起我"。
// 没有启动脚本时，用户 Ctrl-C 的 0 与更新的 75 都只是退出码，没人能判断
// "这个进程该不该再起来"。现在退出前先写 updates/restart.request，
// 写清是谁要的、为什么、目标版本；入口监护循环只在这份意图存在时才重新拉起。
//
// 意图文件是**一次性的**：被消费后立刻删除，所以"用户中断"永远不会被误拉起。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/fsutil"
)

const (
	restartIntentName = "restart.request"
	// relaunchEnv 标记"我是自重启拉起来的"，只用于日志与诊断。
	relaunchEnv = "ARTEX_SELFUPDATE_RELAUNCH"

	// handoffMaxRestarts / handoffRestartWindow 是入口监护循环的防抖窗口：
	// 窗口内连续重启超过上限就不再拉起，避免新版一崩就无限重启刷日志。
	handoffMaxRestarts   = 5
	handoffRestartWindow = 5 * time.Minute
)

// RestartIntent 记录"这一次重启是谁要的"，同时留给下一次启动做诊断。
type RestartIntent struct {
	Reason string `json:"reason"`         // apply | rollback | bootstrap
	From   string `json:"from,omitempty"` // 触发时的当前版本
	To     string `json:"to,omitempty"`   // 目标版本
	At     int64  `json:"at"`             // 写意图的时间
	PID    int    `json:"pid,omitempty"`  // 谁写的
}

func restartIntentPath(l Layout) string { return filepath.Join(l.Updates, restartIntentName) }

// WriteRestartIntent 原子写重启意图；失败只记日志，不阻塞退出流程。
func WriteRestartIntent(l Layout, reason string) {
	if err := EnsureLayoutDirs(l); err != nil {
		logUpdate(l, "写重启意图前建目录失败：%v", err)
		return
	}
	j, _ := ReadJournal(l)
	in := RestartIntent{Reason: reason, From: j.From, To: j.To, At: time.Now().Unix(), PID: os.Getpid()}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return
	}
	b = append(b, '\n')
	if err := fsutil.WriteFileAtomic(restartIntentPath(l), b, 0o644); err != nil {
		logUpdate(l, "写重启意图失败：%v", err)
	}
}

// ConsumeRestartIntent 读取并删除意图：读得到才算"真的要重启"，读不到就是普通退出。
func ConsumeRestartIntent(l Layout) (RestartIntent, bool) {
	p := restartIntentPath(l)
	b, err := os.ReadFile(p)
	if err != nil {
		return RestartIntent{}, false
	}
	var in RestartIntent
	if err := json.Unmarshal(b, &in); err != nil {
		// 坏文件当没写过，删掉避免下次误判。
		_ = os.Remove(p)
		return RestartIntent{}, false
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		logUpdate(l, "重启意图删除失败（%v），下次启动可能多拉起一次", err)
	}
	return in, true
}

// HandoffChild 判断当前进程是不是入口 exe 转交出来的核心进程。
// 是的话"重启"由入口的监护循环负责，本进程只负责带上退出码。
func HandoffChild() bool { return os.Getenv(handoffEnv) != "" }

// RelaunchedChild 判断当前进程是不是自重启拉起来的（诊断用）。
func RelaunchedChild() bool { return os.Getenv(relaunchEnv) != "" }

// CanRestartSelf 报告"装完之后进程能不能自己回到运行态"。
//
// 以前这件事等同于"有没有 start.sh / start.bat 在监护"，于是没有脚本的用户
// 会被直接挡住 400。现在进程自己会拉起新进程重开（ExitForRestart → Relaunch），
// 脚本降级为可选兜底；唯一确实不能自拉的是容器内 —— 那由 InDocker 单独判断。
func CanRestartSelf() bool { return !InDocker() }

// ExitForRestart 写重启意图，然后按"谁在托管我"选择重启方式：
//
//   - 入口监护循环带起来的（HandoffChild）或 start.sh / start.bat 带起来的（Supervised）
//     → 返回 ExitRestart，交给上层；
//   - 两者都不是（用户直接双击 exe）→ 自己拉起新进程后返回 0。
//
// 这条分支就是"不依赖 BAT 也能更新"的全部秘密：重启器从脚本搬进了进程自己。
func ExitForRestart(reason string) int {
	l, err := ResolveLayout()
	if err != nil {
		return ExitRestart
	}
	WriteRestartIntent(l, reason)
	if HandoffChild() || Supervised() {
		// 引导程序只有自己带托管循环时才会把新版本拉起来。用旧版 zip 装出来的引导程序
		// 没有这个循环，它收到 75 会跟着退出，于是用户看到的就是“更新完成、窗口消失、什么都不打开”。
		// Bootstrap 里的 RefreshOuter 已把根目录的引导程序换成当前版本：动过它
		// 就说明正在跑的那个是旧版，这里主动把新的拉起来。
		if OuterRefreshed() && !Supervised() {
			if err := relaunchOuter(l); err != nil {
				logUpdate(l, "旧引导程序不会托管重启，主动拉起也失败：%v（reason=%s）", err, reason)
			} else {
				logUpdate(l, "旧引导程序不会托管重启，已主动拉起新引导程序后退出（reason=%s）", reason)
				return ExitRestart
			}
		}
		logUpdate(l, "以退出码 %d 把重启交给上层（reason=%s）", ExitRestart, reason)
		return ExitRestart
	}
	if err := Relaunch(); err != nil {
		logUpdate(l, "自重启失败（%v），退回退出码 %d 让上层处理", err, ExitRestart)
		return ExitRestart
	}
	logUpdate(l, "已自行拉起新进程并退出（reason=%s）", reason)
	return 0
}

// Relaunch 用同样的参数拉起一个新的自己，父进程随后正常退出。
//
// 新进程会先抢单实例锁、再开库：父进程要等 defer 里的 Close 把库关掉，
// 才会在退出时释放锁，所以两个版本不会同时写同一个 data/。
func Relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	l, lerr := ResolveLayout()
	if lerr != nil {
		return lerr
	}
	env := append(os.Environ(), relaunchEnv+"=1", InstallRootEnv+"="+l.Root)
	return spawnDetached(exe, os.Args[1:], env, l.Root)
}

// relaunchOuter 把根目录的引导程序以独立控制台拉起来（带着原样的命令行参数）。
func relaunchOuter(l Layout) error {
	outer := OuterPath(l)
	if _, err := os.Stat(outer); err != nil {
		return err
	}
	env := append(os.Environ(), InstallRootEnv+"="+l.Root, relaunchEnv+"=1")
	return spawnDetachedConsole(outer, os.Args[1:], env, l.Root)
}

// allowRestart 做滑动窗口防抖：窗口内达到上限就返回 false。
func allowRestart(recent *[]time.Time) bool {
	now := time.Now()
	kept := (*recent)[:0]
	for _, at := range *recent {
		if now.Sub(at) <= handoffRestartWindow {
			kept = append(kept, at)
		}
	}
	*recent = kept
	if len(kept) >= handoffMaxRestarts {
		return false
	}
	*recent = append(*recent, now)
	return true
}
