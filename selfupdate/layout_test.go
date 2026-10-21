package selfupdate

// 版本化布局（pointer + versions/ + journal）的回归测试。
//
// 旧测试全部围绕 artex-rea.new/.old 这套文件替换协议，而这次改造的核心恰恰是
// "不再替换任何正在运行的文件"。所以下面这几条测的是新不变量：
//   - 指针切换/回滚只改 pointer.json，不动文件；
//   - ApplyNow 把暂存件装进 versions/<ver>/ 再切指针；
//   - 台账（journal）是跨进程的唯一事实源，Bootstrap 靠它累计失败次数并自动回滚。

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestLayout 造一个隔离的安装根目录，并用 ARTEX_INSTALL_ROOT 让所有经由
// ResolveLayout() 的入口（Bootstrap / Activate / Settle）都指向它。
func newTestLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	t.Setenv(InstallRootEnv, root)
	l := layoutFor(root, filepath.Join(root, entryStem+exeExt()))
	if err := EnsureLayoutDirs(l); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	return l
}

func TestPointerSwitchAndRollback(t *testing.T) {
	l := newTestLayout(t)

	EnsurePointer(l, "2.3.0")
	ptr, ok := ReadPointer(l)
	if !ok || ptr.Current != "2.3.0" {
		t.Fatalf("初始指针 = %+v（ok=%v），期望 current=2.3.0", ptr, ok)
	}

	// 回滚目标必须真的装在磁盘上，否则 RollbackTarget 会把记录清掉。
	if err := os.MkdirAll(filepath.Dir(CorePath(l, "2.3.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CorePath(l, "2.3.0"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SwitchPointer(l, "2.4.0"); err != nil {
		t.Fatalf("切指针: %v", err)
	}
	ptr, _ = ReadPointer(l)
	if ptr.Current != "2.4.0" || ptr.Previous != "2.3.0" {
		t.Fatalf("切换后指针 = %+v，期望 2.4.0 / 2.3.0", ptr)
	}

	if got := RollbackTarget(l); got != "2.3.0" {
		t.Fatalf("RollbackTarget = %q，期望 2.3.0", got)
	}
	if err := RollbackPointer(l); err != nil {
		t.Fatalf("回滚: %v", err)
	}
	ptr, _ = ReadPointer(l)
	if ptr.Current != "2.3.0" || ptr.Previous != "2.4.0" {
		t.Fatalf("回滚后指针 = %+v，期望 2.3.0 / 2.4.0（还能再滚回去）", ptr)
	}
}

func TestApplyNowInstallsVersionedCore(t *testing.T) {
	requireUnix(t)
	l := newTestLayout(t)
	EnsurePointer(l, "2.3.0")

	// 布置"已下载已校验"的暂存件。
	staged := StagedPath(l, "2.4.0")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, staged, "new", 0)
	sum, err := fileSHA256(staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteJournal(l, Journal{
		State:         StateStaged,
		From:          "2.3.0",
		To:            "2.4.0",
		StagedVersion: "2.4.0",
		StagedPath:    staged,
		SHA256:        sum,
	}); err != nil {
		t.Fatalf("写台账: %v", err)
	}

	if err := ApplyNow(l); err != nil {
		t.Fatalf("ApplyNow: %v", err)
	}

	core := CorePath(l, "2.4.0")
	if _, err := os.Stat(core); err != nil {
		t.Fatalf("新版本应被安装到 %s: %v", core, err)
	}
	ptr, ok := ReadPointer(l)
	if !ok || ptr.Current != "2.4.0" || ptr.Previous != "2.3.0" {
		t.Fatalf("安装后指针 = %+v（ok=%v）", ptr, ok)
	}
	j, _ := ReadJournal(l)
	if j.State != StateSwapped {
		t.Errorf("安装后台账状态 = %s，期望 %s", j.State, StateSwapped)
	}
	// 安装是"复制"而不是"搬运"：暂存件要留到确认成功再清。
	if _, err := os.Stat(staged); err != nil {
		t.Error("安装后暂存件应保留（Settle 确认成功后才清理）")
	}
}

func TestApplyNowRejectsTamperedStaging(t *testing.T) {
	requireUnix(t)
	l := newTestLayout(t)
	EnsurePointer(l, "2.3.0")

	staged := StagedPath(l, "2.4.0")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, staged, "new", 0)
	if err := WriteJournal(l, Journal{
		State:         StateStaged,
		To:            "2.4.0",
		StagedVersion: "2.4.0",
		StagedPath:    staged,
		SHA256:        "0000000000000000000000000000000000000000000000000000000000000000",
	}); err != nil {
		t.Fatalf("写台账: %v", err)
	}

	if err := ApplyNow(l); err == nil {
		t.Fatal("暂存件与台账摘要不一致时必须拒绝安装")
	}
	ptr, _ := ReadPointer(l)
	if ptr.Current != "2.3.0" {
		t.Errorf("拒绝安装后指针不应变化，实际 current=%s", ptr.Current)
	}
	j, _ := ReadJournal(l)
	if j.State != StateFailed {
		t.Errorf("台账应记为 %s，实际 %s", StateFailed, j.State)
	}
}

func TestBootstrapRollsBackAfterRepeatedVersionMismatch(t *testing.T) {
	l := newTestLayout(t)
	EnsurePointer(l, "2.3.0")
	// 回滚目标必须真实存在，否则 RollbackTarget 会拒绝回滚。
	if err := os.MkdirAll(filepath.Dir(CorePath(l, "2.3.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CorePath(l, "2.3.0"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SwitchPointer(l, "2.4.0"); err != nil {
		t.Fatalf("切指针: %v", err)
	}
	if err := WriteJournal(l, Journal{State: StateSwapped, From: "2.3.0", To: "2.4.0"}); err != nil {
		t.Fatalf("写台账: %v", err)
	}

	// 前 maxAttempts 次只累计，让新版有机会自己站稳。
	for i := 1; i <= maxAttempts; i++ {
		action, st := Bootstrap("2.3.0") // 实际起来的版本和目标版不一致
		if action != Continue {
			t.Fatalf("第 %d 次尝试期望 Continue，得到 %v", i, action)
		}
		if !st.Pending {
			t.Errorf("第 %d 次尝试状态应为 Pending", i)
		}
		if j, _ := ReadJournal(l); j.Attempts != i {
			t.Fatalf("第 %d 次尝试后 attempts=%d", i, j.Attempts)
		}
	}

	// 超限：回滚指针并重启。
	action, st := Bootstrap("2.3.0")
	if action != Restart {
		t.Fatalf("超过尝试上限时期望 Restart，得到 %v", action)
	}
	if !st.RolledBack {
		t.Error("状态应标记为 RolledBack")
	}
	ptr, _ := ReadPointer(l)
	if ptr.Current != "2.3.0" {
		t.Fatalf("应已回滚到 2.3.0，实际 current=%s", ptr.Current)
	}
	if j, _ := ReadJournal(l); j.State != StateRolledBack {
		t.Errorf("台账应记为 %s，实际 %s", StateRolledBack, j.State)
	}
}

func TestBootstrapIgnoresSmokeProcess(t *testing.T) {
	l := newTestLayout(t)
	EnsurePointer(l, "2.3.0")
	if err := WriteJournal(l, Journal{State: StateStaged, To: "2.4.0"}); err != nil {
		t.Fatalf("写台账: %v", err)
	}

	// 冒烟测试拉起的子进程必须直接放行：否则它会认为"有暂存件待安装"，
	// 又一次装一遍、又一次 exit 75，在守护循环里无限重启。
	t.Setenv(smokeEnv, "1")
	action, st := Bootstrap("2.3.0")
	if action != Continue || st != (State{}) {
		t.Fatalf("冒烟进程应直接 Continue，得到 action=%v state=%+v", action, st)
	}
	if j, _ := ReadJournal(l); j.State != StateStaged {
		t.Errorf("冒烟进程不应改动台账，实际 %s", j.State)
	}
}

func TestLegacyPathsAnchorAtInstallRoot(t *testing.T) {
	root := t.TempDir()
	// 模拟"已迁移"的现场：进程跑在 versions/<ver>/ 里。
	l := layoutFor(root, filepath.Join(root, layoutVersionsDir, "2.4.0", coreStem+exeExt()))
	p := l.legacyPaths()
	if p.Dir != root {
		t.Fatalf("旧协议路径应锚在安装根目录 %s，实际 %s", root, p.Dir)
	}
	if want := entryStem + ".old" + exeExt(); filepath.Base(p.Old) != want {
		t.Errorf("备份名 = %s，期望 %s", filepath.Base(p.Old), want)
	}
	if want := entryStem + ".new" + exeExt(); filepath.Base(p.New) != want {
		t.Errorf("暂存名 = %s，期望 %s", filepath.Base(p.New), want)
	}
}
