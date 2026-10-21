package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// smokeEnv 让被冒烟测试拉起的子进程直接跳过 Bootstrap 与全部启动逻辑。
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action 是 Bootstrap 给 main 的指令。
type Action int

const (
	// Continue：照常启动 server。
	Continue Action = iota
	// Restart：立刻以 ExitRestart 退出，让守护脚本重新拉起。
	Restart
)

// State 描述本次启动时的升级状态，供 /api/update/check 如实告诉前端
// "上一次升级是成功了还是被回滚了"。台账（journal）是持久的事实源，
// 这里只携带"本次启动刚刚发生了什么"。
type State struct {
	Pending     bool   // 升级已切换，尚未确认稳定
	RolledBack  bool   // 本次启动刚刚执行过自动回滚
	FailedStage bool   // 暂存件校验/冒烟未通过，已丢弃
	Detail      string // 面向用户的一句话说明
}

// Bootstrap 在 main 的最开头运行，必须在任何监听端口、打开数据库之前调用。
//
// 版本化布局（pointer）下的局面：
//
//	① 台账 applying  → 上次安装到一半被打断：重做一次（幂等），然后重启
//	② 台账 staged    → 已下载校验但还没安装（例如自动下载后用户自己重启了）：安装并重启
//	③ 台账 swapped   → 本次就是切换后的启动：累计尝试次数，交给 Settle 判定健康
//	④ 其它           → 正常启动
//
// 旧协议（v2.2.2 及更早）留下的 artex-rea.new / upgrade.json 仍按原逻辑处理一次，
// 保证从老版本升上来的机器不会卡住。
func Bootstrap(version string) (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}

	l, lerr := ResolveLayout()
	if lerr == nil {
		_ = EnsureLayoutDirs(l)
		adoptBundledCore(l, version)
		EnsurePointer(l, version)
	}

	// 旧协议兼容必须排在清理之前。v2.2.2 只认 artex-rea.new + upgrade.json，
	// 而 CleanupLegacy 会把 .new 当"升级残留"删掉——顺序反了，用户升级到一半
	// 重启一次，暂存件就凭空消失，界面上什么都看不到。
	if p, err := ResolvePaths(); err == nil {
		if _, err := os.Stat(p.New); err == nil {
			return applyStaged(p)
		}
		if m, ok := readMarker(p.Marker); ok {
			return confirmOrRollback(p, m)
		}
	}

	if lerr != nil {
		log.Printf("[update] 跳过自举：%v", lerr)
		return Continue, State{}
	}

	if j, ok := ReadJournal(l); ok {
		switch j.State {
		case StateApplying:
			logUpdate(l, "上次安装未完成（%s → %s），本次启动重试", orUnknown(j.From), orUnknown(j.To))
			if err := ApplyNow(l); err != nil {
				logUpdate(l, "重试安装失败：%v", err)
				return Continue, State{FailedStage: true, Detail: "新版本安装未完成：" + err.Error()}
			}
			return Restart, State{Pending: true}
		case StateStaged:
			logUpdate(l, "检测到已暂存但未安装的 %s，正在安装", orUnknown(j.To))
			if err := ApplyNow(l); err != nil {
				logUpdate(l, "安装暂存版本失败：%v", err)
				return Continue, State{FailedStage: true, Detail: "新版本安装未完成：" + err.Error()}
			}
			return Restart, State{Pending: true}
		case StateSwapped:
			return confirmPointerAttempt(l, j, version)
		}
	}

	// 到这里说明既没有待换装的旧协议暂存件，也没有未完成的台账，
	// 才轮到清理上一轮留下的 .failed/.swap/.new 残留。
	CleanupLegacy(l)
	return Continue, State{}
}

// adoptBundledCore 把发行包自带的 artex-rea-core[.exe] 收进 versions/<当前版本>/。
//
// 发行包是"入口 + core"两件套：入口（artex-rea）永远待在根目录只做转发，
// 真正干活的是 core。首次启动时把 core 收进版本目录，此后升级就只是
// "新增 versions/<新版本>/ + 改 pointer.json"，不再替换任何正在运行的文件。
//
// 找不到 core（旧发行包、`go run`、开发构建）就静默返回：没有它，
// 入口仍然按旧方式在本目录启动，行为与改造前一致。
func adoptBundledCore(l Layout, version string) {
	v := versionKey(version)
	if v == "unknown" || v == "dev" {
		return
	}
	dst := CorePath(l, v)
	if _, err := os.Stat(dst); err == nil {
		return // 已经收好了
	}
	src := filepath.Join(l.Root, coreStem+exeExt())
	if filepath.Clean(src) == filepath.Clean(l.Entry) {
		return // 自己就是 core，不用再收
	}
	if _, err := os.Stat(src); err != nil {
		return
	}
	if err := copyFileAtomic(src, dst); err != nil {
		logUpdate(l, "把发行包自带的 core 收进 versions/%s 失败（下次启动再试）：%v", v, err)
		return
	}
	logUpdate(l, "已把发行包自带的 core 收进 versions/%s", v)
}

// confirmPointerAttempt 处理"指针已切换后的启动"：累计尝试次数，超限自动回滚。
//
// 与旧实现的关键差别：这里同时核对**实际运行的版本号**与目标版本。
// 旧实现只看"进程活过 30 秒"，于是"指针指错但进程起来了"会被判成升级成功。
func confirmPointerAttempt(l Layout, j Journal, version string) (Action, State) {
	j.Attempts++

	if !sameVersion(j.To, version) {
		j.LastError = fmt.Sprintf("已切换到 %s，但实际启动的版本是 %s", orUnknown(j.To), orUnknown(version))
		if j.Attempts > maxAttempts {
			// 回滚目标取自指针，而不是台账——台账里没有 previous 字段，
			// 而指针才是"上一版本是哪个"的权威来源。
			prev := ""
			if pt, ok := ReadPointer(l); ok {
				prev = pt.Previous
			}
			if err := RollbackPointer(l); err != nil {
				logUpdate(l, "版本不一致且回滚失败：%v", err)
				j.State = StateFailed
				j.Detail = "新版本未能生效，且回滚失败：" + err.Error()
				_ = WriteJournal(l, j)
				return Continue, State{Detail: j.Detail}
			}
			UpdateJournal(l, func(jj *Journal) {
				jj.State = StateRolledBack
				jj.Attempts = 0
				jj.Detail = fmt.Sprintf("新版本未能生效，已回滚到 %s", orUnknown(prev))
			})
			logUpdate(l, "连续 %d 次启动都不是目标版本，已回滚，退出以重启（exit %d）", maxAttempts, ExitRestart)
			return Restart, State{RolledBack: true, Detail: j.Detail}
		}
		_ = WriteJournal(l, j)
		logUpdate(l, "%s（第 %d/%d 次尝试）", j.LastError, j.Attempts, maxAttempts)
		return Continue, State{Pending: true, Detail: j.LastError}
	}

	_ = WriteJournal(l, j)
	logUpdate(l, "新版本 %s 启动中（第 %d/%d 次尝试），稳定运行并通过健康检查后确认",
		version, j.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// SettleDelay 是判定"新版本活下来了"所需的运行时长。
const SettleDelay = 30 * time.Second

// applyStaged 是旧协议的一次性换装：校验通过就换装，失败就丢弃。
//
// 换装本身走 swap（唯一备用名 + 重试 + 清理失败不阻断），
// 修掉了"固定名 .old 被锁死后永久无法升级"的老问题。
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] 暂存的新版本未通过校验，已丢弃，继续运行当前版本：%v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "新版本验证失败，已丢弃： " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] 换装失败，继续运行当前版本：%v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "替换版本失败： " + err.Error()}
	}

	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] 写升级标记失败（失去自动回滚能力）：%v", err)
	}
	if l, err := ResolveLayout(); err == nil {
		// 顺手把刚换上的新二进制装成 versions/<To>/artex-rea-core 并切指针：
		// 这样"从旧协议升上来"的机器一次启动就进入版本化布局，下一次升级直接走
		// 新增目录 + 原子改指针，不再替换任何正在运行的文件。
		// 失败只是少了一条捷径，不影响本次升级本身。
		if strings.TrimSpace(m.To) != "" {
			if _, cerr := InstallCore(l, m.To, p.Current); cerr != nil {
				logUpdate(l, "把新版本装入 versions/%s 失败（不影响本次升级）：%v", versionKey(m.To), cerr)
			} else if serr := SwitchPointer(l, m.To); serr != nil {
				logUpdate(l, "切换版本指针失败（不影响本次升级）：%v", serr)
			}
		}
		UpdateJournal(l, func(jj *Journal) {
			jj.State = StateSwapped
			jj.Mode = "legacy"
			jj.From = m.From
			jj.To = m.To
			jj.SwappedAt = time.Now().Unix()
			jj.Attempts = 0
			jj.Detail = "旧协议换装完成，等待重启确认"
		})
		logUpdate(l, "旧协议换装完成：%s → %s", orUnknown(m.From), orUnknown(m.To))
	}
	log.Printf("[update] 已换装到 %s，退出以重启（exit %d）", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback 处理旧协议"换装后的启动"：累计尝试次数，超限则把旧版换回来。
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			log.Printf("[update] 新版本连续 %d 次启动失败，且回滚失败：%v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "新版本启动失败，回滚也失败： " + err.Error()}
		}
		log.Printf("[update] 新版本连续 %d 次启动失败，已回滚到 %s，退出以重启（exit %d）",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("新版本启动失败，已回滚到旧版本 (%s)", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] 更新升级标记失败：%v", err)
	}
	log.Printf("[update] 新版本启动中（第 %d/%d 次尝试），稳定运行后将确认升级", m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// verifyStaged 校验暂存件：先比对 SHA256，再真正把它拉起来跑一次。
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("读取校验和失败：%w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("计算校验和失败：%w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 不匹配（下载内容损坏或被篡改）")
	}
	return smokeTest(p.New)
}

// smokeTest 用一个"什么都不做就退出"的参数拉起新二进制，确认它在本机真的能执行。
//
// 优先 -self-test（语义明确：只做启动自检，不碰端口/数据库/网络）；
// 旧版本不认识这个参数时会以 2 退出，此时回落到 -h。
// 这条路径同时挡掉：下载截断、架构选错（exec format error）、缺动态库。
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("授予执行权限失败：%w", err)
	}
	if err := runSmoke(bin, "-self-test"); err == nil {
		return nil
	} else if !isUnknownFlag(err) {
		return err
	}
	return runSmoke(bin, "-h")
}

// isUnknownFlag 判断冒烟失败是不是"旧版本不认识 -self-test"。
func isUnknownFlag(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "flag provided but not defined") || strings.Contains(msg, "unknown flag")
}

func runSmoke(bin, flag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, flag)
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("冒烟测试超时（新二进制未响应）")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("冒烟测试失败（%s）：%v：%s", flag, err, snippet)
	}
	return nil
}

// rollback 把旧协议的备份换回来（保留 Paths 入参，便于回归测试直接构造路径）。
func rollback(p Paths) error {
	backup, ok := latestBackup(p)
	if !ok {
		return fmt.Errorf("没有可回滚的备份（%s 或 %s.old-* 均不存在）", p.Old, p.Current)
	}
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := renameRetry(p.Current, failed); err != nil {
		return fmt.Errorf("移动失败版本失败：%w", err)
	}
	if err := renameRetry(backup, p.Current); err != nil {
		return fmt.Errorf("恢复旧版本失败（备份仍在 %s）：%w", filepath.Base(backup), err)
	}
	return nil
}

// settle 清除旧协议标记。
//
// 生产路径已改用 Settle(version, addr)（带版本号核对与健康检查），
// 这个小函数保留给既有回归测试直接调用，语义与旧版一致：只在标记存在时删除它。
func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] 清除升级标记失败：%v", err)
		return
	}
	log.Printf("[update] 新版本运行稳定，升级完成（上一版本保留为 %s）", p.Old)
}

// hasLegacyBackup 报告旧协议是否留有可回滚的备份。
func hasLegacyBackup(l Layout) bool {
	_, ok := latestBackup(l.legacyPaths())
	return ok
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知版本"
	}
	return s
}

// sameVersion 比较两个版本号，忽略前导 v 与首尾空白。
func sameVersion(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	return a != "" && a == b
}
