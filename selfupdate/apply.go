package selfupdate

// 安装（activate）、健康确认、回滚与清理。
//
// 与旧实现的根本区别：**安装不再替换正在运行的文件**。
// 新版核心被放进 versions/<ver>/，切换只是原子改写 pointer.json，
// 因此不存在「Windows 不允许覆盖运行中的 exe」这一类问题，
// 也不需要"谁来换装"这个角色——任何进程都能安全地完成安装。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Activate 是安装入口：读台账里暂存好的版本，安装并切换指针。
func Activate() error {
	l, err := ResolveLayout()
	if err != nil {
		return err
	}
	return ApplyNow(l)
}

// ApplyNow 把台账里记录的暂存件安装成 versions/<to>/ 并切换指针。
//
// 幂等：重复调用（例如上次安装到一半进程被杀）会重做一遍并收敛到同一结果。
func ApplyNow(l Layout) error {
	j, ok := ReadJournal(l)
	if !ok || strings.TrimSpace(j.To) == "" || strings.TrimSpace(j.StagedPath) == "" {
		return errors.New("没有待安装的更新（升级台账缺失或尚未暂存）")
	}
	if err := EnsureLayoutDirs(l); err != nil {
		return err
	}

	got, err := fileSHA256(j.StagedPath)
	if err != nil {
		msg := "暂存件不可读：" + err.Error()
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateFailed
			jj.LastError = msg
			jj.Detail = "暂存的新版本已不可用，请重新检查更新"
		})
		return errors.New(msg)
	}
	if j.SHA256 != "" && !strings.EqualFold(got, j.SHA256) {
		msg := "暂存件与台账摘要不一致（安装前被改动），已拒绝安装"
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateFailed
			jj.LastError = msg
		})
		logUpdate(l, "%s", msg)
		return errors.New(msg)
	}

	core, err := InstallCore(l, j.To, j.StagedPath)
	if err != nil {
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateFailed
			jj.LastError = err.Error()
		})
		logUpdate(l, "安装新版本失败：%v", err)
		return err
	}

	// 冒烟：装好的核心必须能在当前系统上真正执行。
	// 这一步挡掉"下载完整但架构不对/缺依赖"这类问题，避免把指针切到一个跑不起来的东西上。
	if err := smokeTest(core); err != nil {
		_ = os.Remove(core)
		msg := "新版本无法在当前系统运行：" + err.Error()
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateFailed
			jj.LastError = msg
			jj.Detail = "新版本冒烟测试未通过，已放弃安装，仍在使用原版本"
		})
		logUpdate(l, "%s", msg)
		return errors.New(msg)
	}

	if err := SwitchPointer(l, j.To); err != nil {
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateFailed
			jj.LastError = err.Error()
		})
		return err
	}

	UpdateJournal(l, func(jj *Journal) {
		jj.State = StateSwapped
		jj.Mode = "pointer"
		jj.SwappedAt = time.Now().Unix()
		jj.Attempts = 0
		jj.LastError = ""
		jj.Detail = "新版本已安装并切换指针，重启后生效"
	})
	logUpdate(l, "安装完成：%s → versions/%s（指针已切换，重启生效）", j.To, versionKey(j.To))
	return nil
}

// Settle 确认新版本真的起来了：进程活着不够，还要"版本对 + 接口通"。
//
// 由 main 在 HTTP 监听成功后延迟调用。判定不通过时把台账置为 failed，
// 下一次启动的 Bootstrap 会累计失败次数并在超限后自动回滚。
func Settle(version, addr string) error {
	l, err := ResolveLayout()
	if err != nil {
		return err
	}
	j, ok := ReadJournal(l)
	if !ok {
		return nil
	}
	switch j.State {
	case StateSwapped, StateApplying:
	default:
		return nil // 不是升级后的启动，无事可做
	}

	if err := HealthProbe(addr, j.To); err != nil {
		msg := "新版本启动后健康检查未通过：" + err.Error()
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateFailed
			jj.LastError = msg
			jj.Detail = "新版本已启动但未通过健康检查，可回滚到上一版本"
		})
		logUpdate(l, "%s", msg)
		return errors.New(msg)
	}

	UpdateJournal(l, func(jj *Journal) {
		jj.State = StateConfirmed
		jj.ConfirmedAt = time.Now().Unix()
		jj.Attempts = 0
		jj.LastError = ""
		jj.Detail = "升级完成"
	})
	logUpdate(l, "新版本 %s 已确认稳定（版本号核对与健康检查均通过）", version)

	CleanupStaging(l)
	PruneVersions(l, keepVersions)
	CleanupLegacy(l)
	return nil
}

// HealthProbe 轮询本机 /api/health，要求 200 且版本匹配。
func HealthProbe(addr, wantVersion string) error {
	url := healthURL(addr)
	want := strings.TrimPrefix(strings.TrimSpace(wantVersion), "v")
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	client := &http.Client{Timeout: 3 * time.Second}
	for {
		lastErr = probeOnce(client, url, want)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func probeOnce(c *http.Client, url, want string) error {
	resp, err := c.Get(url)
	if err != nil {
		return fmt.Errorf("无法访问 %s：%w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s 返回 HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("读取健康检查响应失败：%w", err)
	}
	var h struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("健康检查响应格式异常：%w", err)
	}
	if !h.OK {
		return errors.New("健康检查返回 ok=false")
	}
	got := strings.TrimPrefix(strings.TrimSpace(h.Version), "v")
	if want != "" && got != want {
		return fmt.Errorf("运行中的版本是 %s，期望 %s", got, want)
	}
	return nil
}

// healthURL 把监听地址转成本机探活地址。
// ":8787" / "0.0.0.0:8787" 这类只能监听不能连接，统一换回 127.0.0.1。
func healthURL(addr string) string {
	addr = strings.TrimSpace(addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr + "/api/health"
		}
		return "http://127.0.0.1:8787/api/health"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]", "*":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/health"
}

// Rollback 优先走指针回滚（瞬时、不动文件），没有指针时退回旧版换装。
func Rollback() error {
	l, err := ResolveLayout()
	if err != nil {
		return err
	}
	if target := RollbackTarget(l); target != "" {
		if err := RollbackPointer(l); err != nil {
			return err
		}
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateRolledBack
			jj.Detail = "已回滚到 " + target
			jj.LastError = ""
			jj.Attempts = 0
		})
		CleanupStaging(l)
		return nil
	}
	return rollback(l.legacyPaths())
}

// HasBackup 报告是否存在可回滚的版本（指针优先，兼容旧版 .old 备份）。
func HasBackup() bool {
	l, err := ResolveLayout()
	if err != nil {
		return false
	}
	if RollbackTarget(l) != "" {
		return true
	}
	return hasLegacyBackup(l)
}

// CleanupStaging 清掉"不是当前版本"的暂存目录与残留安装包。
// 当前版本的暂存件保留，直到确认成功（便于失败后重试）。
//
// 两类垃圾，缺一不可：
//   - updates/staging/<ver>/  —— 其它版本的暂存目录；
//   - updates/*.staged.*      —— 散落在 updates 根下的安装包与校验文件。
//
// 第二类只在"旧协议遗留 / 手工放置"时出现（安装流程自身写的是 staging/<ver>/）：
// 它们既不是目录，也不在旧协议名单（.new / .failed / .swap / .upgrade.json）里，
// 原先没有任何一条清理逻辑会碰它们，放进去就永远留在盘上。
func CleanupStaging(l Layout) {
	ptr, _ := ReadPointer(l)
	if entries, err := os.ReadDir(l.Staging); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if versionKey(ptr.Current) == e.Name() {
				continue
			}
			if err := os.RemoveAll(filepath.Join(l.Staging, e.Name())); err != nil {
				logUpdate(l, "清理暂存目录 %s 失败（下次再试）：%v", e.Name(), err)
			}
		}
	}
	cleanupLooseStaged(l)
}

// cleanupLooseStaged 删除 updates/ 根下散落的 artex-rea.staged* 文件。
//
// 这些是"已经装完、包还留着"的典型残留：安装流程只认 staging/<ver>/，
// 装在根目录的散件从此无人过问。删不掉（被杀软/索引器短暂占用）只记日志。
func cleanupLooseStaged(l Layout) {
	entries, err := os.ReadDir(l.Updates)
	if err != nil {
		return
	}
	prefix := strings.ToLower(stagedLoosePrefix)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(strings.ToLower(e.Name()), prefix) {
			continue
		}
		target := filepath.Join(l.Updates, e.Name())
		if err := os.Remove(target); err != nil {
			if !os.IsNotExist(err) {
				logUpdate(l, "清理残留安装包 %s 失败（下次再试）：%v", e.Name(), err)
			}
			continue
		}
		logUpdate(l, "已清理残留安装包 %s", e.Name())
	}
}

// CleanupLegacy 清理旧协议（v2.2.2 及更早）留下的升级残留。
//
// 只删"确定属于升级过程"的文件，并且**容忍失败**：Windows 上残留的正在运行的
// .new.exe/.old.exe 删不掉是常态，删不掉就留着下次再试，绝不阻断启动。
func CleanupLegacy(l Layout) {
	p := l.legacyPaths()
	dir := p.Dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if !strings.HasPrefix(name, entryStem) {
			continue
		}
		if !strings.Contains(name, ".new") && !strings.Contains(name, ".failed") &&
			!strings.Contains(name, ".swap") && !strings.Contains(name, bundledRetiredSuffix) &&
			!strings.HasSuffix(name, ".upgrade.json") {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if err := os.Remove(target); err != nil {
			if !os.IsNotExist(err) {
				logUpdate(l, "清理升级残留 %s 失败（可能正在运行或被占用，稍后会再试）：%v", e.Name(), err)
			}
			continue
		}
		logUpdate(l, "已清理升级残留 %s", e.Name())
	}
}

// legacyPaths 返回"旧协议文件在安装根目录下"的路径集合。
//
// 必须锚在根目录而不是可执行文件所在目录：迁移到版本化布局之后进程跑在
// versions/<ver>/ 里，而旧协议的 .new / .old / upgrade.json 全留在根目录。
// 不做这一步，回滚与残留清理会去看一个空的版本目录，表现为"备份明明在却报没有"。
func (l Layout) legacyPaths() Paths {
	root := filepath.Clean(l.Root)
	if root == "" || root == "." {
		if p, err := ResolvePaths(); err == nil {
			return p
		}
		return Paths{}
	}
	// 入口就在根目录（尚未迁移）：按它的真实名字推导，兼容更早的 artex[.exe]。
	if l.Entry != "" && filepath.Clean(filepath.Dir(l.Entry)) == root {
		return pathsFor(filepath.Clean(l.Entry))
	}
	// 已经跑在 versions/<ver>/ 里：旧协议的文件属于根目录下的入口。
	return pathsFor(filepath.Join(root, entryStem+exeExt()))
}

// renameRetry 带退避的重命名：杀毒软件与索引器会短暂持有句柄，
// 瞬时失败不等于永久失败（这是 Windows 上最常见的"有时成功有时 Access Denied"）。
func renameRetry(from, to string) error {
	delays := []time.Duration{0, 200 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond}
	var lastErr error
	for _, d := range delays {
		if d > 0 {
			time.Sleep(d)
		}
		if err := os.Rename(from, to); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// legacySwapSafe 是旧协议的一次性换装：唯一备用名 + 重试 + 清理失败不阻断。
//
// 旧实现用固定名 <exe>.old 且"清理失败即返回错误"，于是只要有一轮 .old 被锁住，
// 之后每一次升级都会卡死在同一行。这里把两个问题一起修掉：
//   - 备用名带时间戳，永不复用；
//   - 旧备份清不掉只告警，不阻断本次换装。
func legacySwapSafe(p Paths) error { return swap(p) }

// swap 用"唯一备用名"完成一次旧协议换装，返回时新版本已在 p.Current。
//
// 两个重命名之间会有一个极短的窗口期，期间 p.Current 不存在；
// 因此第二步行不通时必须把备用名挪回来，绝不能让用户只剩一个 .old-<ts>。
func swap(p Paths) error {
	emptyBackups(p)
	aside := fmt.Sprintf("%s.old-%d", p.Current, time.Now().UnixNano())
	if err := renameRetry(p.Current, aside); err != nil {
		return fmt.Errorf("备份当前版本失败：%w", err)
	}
	if err := renameRetry(p.New, p.Current); err != nil {
		if rerr := renameRetry(aside, p.Current); rerr != nil {
			return fmt.Errorf("安装新版本失败 (%v)，恢复当前版本也失败：%w", err, rerr)
		}
		return fmt.Errorf("安装新版本失败：%w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// legacyBackupPrefix 是旧协议备份文件名前缀（含路径）。备用名形如
// artex-rea.exe.old-1729478400123456789。
func legacyBackupPrefix(p Paths) string { return p.Current + ".old" }

// legacyBackups 列出全部旧协议备份，最新的在前。
//
// 兼容两代命名：固定名 p.Old（v2.2.2 及更早留下）与带时间戳的 p.Current+".old-<ns>"。
func legacyBackups(p Paths) []string {
	var out []string
	if _, err := os.Stat(p.Old); err == nil {
		out = append(out, p.Old)
	}
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return out
	}
	prefix := strings.ToLower(filepath.Base(legacyBackupPrefix(p)))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if !strings.HasPrefix(name, prefix+"-") || !strings.HasPrefix(name, prefix) {
			continue
		}
		out = append(out, filepath.Join(p.Dir, e.Name()))
	}
	// 名字里的纳秒时间戳定长，字典序即时间序；固定名 .old 排在最后当作最旧。
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// latestBackup 返回最近一次换装留下的备份。
func latestBackup(p Paths) (string, bool) {
	all := legacyBackups(p)
	if len(all) == 0 {
		return "", false
	}
	return all[0], true
}

// emptyBackups 在换装前清掉旧备份，避免每升一次级就在目录里堆一个 exe。
//
// 删不掉（被占用）只告警：这正是旧实现卡死的地方——清理失败绝不能阻断升级。
func emptyBackups(p Paths) {
	for _, path := range legacyBackups(p) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[update] 旧备份 %s 仍被占用，跳过清理（不阻断本次升级）：%v", filepath.Base(path), err)
		}
	}
}
