package selfupdate

// 跨进程升级台账。
//
// 旧实现的"上一次升级到底怎么样了"只存在于内存（server.SetBootUpdateState）与
// 控制台日志里：进程一退出全丢，SSE 一断就只剩前端那个永不超时的"正在重启"。
// 台账把这件事落盘，于是：
//   - 下一个进程无论新旧都能读到"上次做到哪一步、为什么失败"；
//   - 前端可以轮询 /api/update/journal，失败时把原因摆到用户脸上；
//   - 崩溃计数（attempts）不再依赖一个只用于换装的 marker 文件。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Autumn-27/artex/fsutil"
)

// JournalState 是升级状态机的状态，直接用于 HTTP 与前端展示。
//
// 名字刻意不叫 State：bootstrap.go 里的 State 是"本次启动发生了什么"的结构体，
// 两者语义不同，混用会让调用点难以阅读。
type JournalState string

const (
	StateIdle       JournalState = "idle"        // 没有进行中的升级
	StateStaging    JournalState = "staging"     // 正在下载/校验（进度仍走 SSE）
	StateStaged     JournalState = "staged"      // 已下载校验，等待安装
	StateApplying   JournalState = "applying"    // 正在安装核心并切换指针
	StateSwapped    JournalState = "swapped"     // 已切换，等待新版本启动并确认
	StateConfirmed  JournalState = "confirmed"   // 新版本已启动且健康检查通过
	StateFailed     JournalState = "failed"      // 失败（含健康检查未通过）
	StateRolledBack JournalState = "rolled-back" // 已回滚
)

// Journal 是一次升级的完整记录。
type Journal struct {
	State         JournalState `json:"state"`
	Mode          string       `json:"mode,omitempty"` // pointer（版本化布局）
	From          string       `json:"from,omitempty"` // 升级前版本
	To            string       `json:"to,omitempty"`   // 目标版本（带 v）
	StagedVersion string       `json:"staged_version,omitempty"`
	StagedPath    string       `json:"staged_path,omitempty"`
	SHA256        string       `json:"sha256,omitempty"`
	Attempts      int          `json:"attempts"` // 切换后已尝试启动的次数
	RequestedAt   int64        `json:"requested_at,omitempty"`
	SwappedAt     int64        `json:"swapped_at,omitempty"`
	ConfirmedAt   int64        `json:"confirmed_at,omitempty"`
	LastError     string       `json:"last_error,omitempty"`
	Detail        string       `json:"detail,omitempty"` // 面向用户的一句话
	ByPID         int          `json:"by_pid,omitempty"`
}

// journalMu 串行化同一进程内的读改写；跨进程靠 install 时的单写者约定
// （只有服务进程与它派生的安装步骤会写，且都先经过 journal 状态判断）。
var journalMu sync.Mutex

// ReadJournal 读取台账；不存在或损坏时 ok=false（损坏即视为没有，不阻断启动）。
func ReadJournal(l Layout) (Journal, bool) {
	b, err := os.ReadFile(l.Journal)
	if err != nil {
		return Journal{}, false
	}
	var j Journal
	if json.Unmarshal(b, &j) != nil {
		return Journal{}, false
	}
	if j.State == "" {
		j.State = StateIdle
	}
	return j, true
}

// WriteJournal 原子写台账。
func WriteJournal(l Layout, j Journal) error {
	if err := os.MkdirAll(filepath.Dir(l.Journal), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := fsutil.WriteFileAtomic(l.Journal, b, 0o644); err != nil {
		return fmt.Errorf("写入升级台账失败：%w", err)
	}
	return nil
}

// UpdateJournal 读改写一次台账，返回写入后的内容。
// 写失败只记日志：台账是"可观测性"，绝不能因为写不了它而让升级本身失败。
func UpdateJournal(l Layout, fn func(*Journal)) Journal {
	journalMu.Lock()
	defer journalMu.Unlock()

	j, ok := ReadJournal(l)
	if !ok {
		j = Journal{State: StateIdle}
	}
	fn(&j)
	if err := WriteJournal(l, j); err != nil {
		log.Printf("[update] %v", err)
	}
	return j
}

// logUpdate 同时写标准日志与 logs/update.log。
//
// 落盘这一半是关键：换装/重启期间控制台可能已经关了、或用户根本没有看控制台，
// 而文件会留在原地，事后能查。
func logUpdate(l Layout, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[update] %s", msg)
	if err := os.MkdirAll(filepath.Dir(l.Log), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(l.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().Format(time.RFC3339), os.Getpid(), msg)
}

// JournalSnapshot 给 HTTP 层用的只读快照：台账 + 指针 + 已安装版本。
// 任何一步失败都返回"尽力而为"的结果，绝不因此让接口 500。
func JournalSnapshot() (map[string]any, error) {
	l, err := ResolveLayout()
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"root":    l.Root,
		"pointer": PointerSummary(l),
	}
	j, ok := ReadJournal(l)
	if !ok {
		out["state"] = string(StateIdle)
		out["journal"] = nil
		return out, nil
	}
	out["state"] = string(j.State)
	out["journal"] = j
	out["last_error"] = j.LastError
	out["detail"] = j.Detail
	out["staged_version"] = j.StagedVersion
	out["staged_path"] = j.StagedPath
	out["attempts"] = j.Attempts
	if j.To != "" {
		out["to"] = j.To
	}
	return out, nil
}
