package selfupdate

// 单实例仲裁：同一安装目录只允许一个"真正对外服务"的进程。
//
// 为什么需要它：
//   - 之前靠 boot 里"端口被自己人占着就 terminateProcess 杀掉"来消解冲突。那不是
//     "不冲突"，是"冲突了就杀"——用户看到的是进程莫名消失、正在跑的 agent 被打断。
//   - 两个进程同时开同一个 data/ 下的 SQLite 也没有仲裁者。
//
// 现在用一把跨进程锁做唯一仲裁：先拿锁，再绑端口。旧实例退出后锁由内核回收，
// 新实例（更新重启、双击重开）自然接上 —— 不需要 PID 轮询，也没有竞态窗口。

import "time"

const (
	lockKindInstance = "instance" // 服务实例锁
	lockKindInstall  = "install"  // 更新安装锁
)

// LockState 是拿锁的结果。
type LockState int

const (
	// LockOwned 拿到了锁：没有别的实例在用。
	LockOwned LockState = iota
	// LockAbandoned 上一个持有者崩溃了，锁被内核回收，本次接管。
	LockAbandoned
	// LockBusy 另一个实例还活着，等到超时也没拿到。
	LockBusy
)

// InstallRoot 返回安装根目录：versions/、pointer.json、updates/ 与锁都挂在它下面。
func InstallRoot() (string, error) {
	l, err := ResolveLayout()
	if err != nil {
		return "", err
	}
	return l.Root, nil
}

// AcquireInstance 拿"同一安装目录只有一个服务实例"的锁。
//
// wait 是"等旧实例让出"的上限：正常退出、更新握手都在毫秒级；只有想抢占一个
// 正在服务的实例才会等到超时（那时默认行为是报告并退出，不抢占）。
func AcquireInstance(root string, wait time.Duration) (release func(), state LockState, err error) {
	return acquireNamedLock(root, lockKindInstance, wait)
}

// TryAcquireInstall 拿"同一安装目录只有一个更新安装者"的锁，不排队。
// 拿不到就说明另一个进程正在装（下载/换指针/回滚），调用方应当直接放弃。
func TryAcquireInstall(root string) (release func(), state LockState, err error) {
	return acquireNamedLock(root, lockKindInstall, 0)
}
