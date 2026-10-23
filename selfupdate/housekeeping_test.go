package selfupdate

// 磁盘卫生的回归测试（2026-10-23）。
//
// 起因：一台装了四代的机器上 versions/ 里躺着 2.3.0 / 2.3.1 / 2.4.0 / 2.4.0.1
// 四个 40MB 的二进制，updates/ 根下还有一个 40MB 的旧安装包，根目录另有一份
// 与版本目录完全相同的 core。三处都不是 bug，而是"清理逻辑的覆盖面"漏了：
//   - PruneVersions 只在 Settle（升级后首次启动）里跑，旁路安装永远不会触发；
//   - CleanupStaging 只遍历 staging/<ver>/ 目录，不认 updates/ 根下的散件；
//   - adoptBundledCore 只复制不退休，收完那份 core 永远留在根目录。
// 下面四条分别钉住这几条不变量。

import (
	"os"
	"path/filepath"
	"testing"
)

// mkVersion 造一个"装好了"的版本目录（InstalledVersions 只认含 core 的目录）。
func mkVersion(t *testing.T, l Layout, ver string) {
	t.Helper()
	p := CorePath(l, ver)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("建版本目录 %s: %v", ver, err)
	}
	if err := os.WriteFile(p, []byte("core-"+ver), 0o755); err != nil {
		t.Fatalf("写 core %s: %v", ver, err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// 四个版本、keep=3：删最旧的，current 与 previous 必须在。
func TestPruneVersionsKeepsCurrentAndPrevious(t *testing.T) {
	l := newTestLayout(t)
	for _, v := range []string{"2.3.0", "2.3.1", "2.4.0", "2.4.0.1"} {
		mkVersion(t, l, v)
	}
	if err := WritePointer(l, Pointer{Current: "2.4.0.1", Previous: "2.4.0"}); err != nil {
		t.Fatalf("写指针: %v", err)
	}

	PruneVersions(l, keepVersions)

	if exists(filepath.Dir(CorePath(l, "2.3.0"))) {
		t.Fatal("最旧的 2.3.0 应当被清理")
	}
	for _, v := range []string{"2.3.1", "2.4.0", "2.4.0.1"} {
		if !exists(CorePath(l, v)) {
			t.Fatalf("%s 不该被删（keep=%d，current/previous 受保护）", v, keepVersions)
		}
	}
}

// 指针读不出来时宁可一份都不删：否则可能把正在跑的版本删掉。
func TestPruneVersionsSkipsWhenPointerUnreadable(t *testing.T) {
	l := newTestLayout(t)
	for _, v := range []string{"2.3.0", "2.3.1", "2.4.0", "2.4.0.1"} {
		mkVersion(t, l, v)
	}
	_ = os.Remove(l.Pointer) // 指针缺失

	PruneVersions(l, keepVersions)

	for _, v := range []string{"2.3.0", "2.3.1", "2.4.0", "2.4.0.1"} {
		if !exists(CorePath(l, v)) {
			t.Fatalf("指针不可读时不该删任何版本，%s 却没了", v)
		}
	}
}

// updates/ 根下的 *.staged.* 散件要删；当前版本的 staging 目录要留。
func TestCleanupStagingRemovesLooseStagedAndForeignDirs(t *testing.T) {
	l := newTestLayout(t)
	if err := WritePointer(l, Pointer{Current: "2.4.0.1", Previous: "2.4.0"}); err != nil {
		t.Fatalf("写指针: %v", err)
	}

	looseEXE := filepath.Join(l.Updates, entryStem+".staged"+exeExt())
	looseSUM := looseEXE + ".sha256"
	if err := os.WriteFile(looseEXE, []byte("pkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(looseSUM, []byte("sum"), 0o644); err != nil {
		t.Fatal(err)
	}

	foreign := filepath.Join(l.Staging, "2.4.0")
	cur := filepath.Join(l.Staging, "2.4.0.1")
	for _, d := range []string{foreign, cur} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, entryStem+".staged"+exeExt()), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	CleanupStaging(l)

	if exists(looseEXE) || exists(looseSUM) {
		t.Fatal("updates/ 根下的 *.staged.* 散件应当被清理")
	}
	if exists(foreign) {
		t.Fatal("非当前版本的 staging 目录应当被清理")
	}
	if !exists(cur) {
		t.Fatal("当前版本的 staging 目录必须保留（确认成功前还要用）")
	}
}

// 收进 versions/ 之后，根目录那份 core 必须被退休。
func TestAdoptBundledCoreRetiresRootCopy(t *testing.T) {
	l := newTestLayout(t)
	rootCore := filepath.Join(l.Root, coreStem+exeExt())
	if err := os.WriteFile(rootCore, []byte("bundled-core"), 0o755); err != nil {
		t.Fatal(err)
	}

	adoptBundledCore(l, "2.4.0.1")

	got, err := os.ReadFile(CorePath(l, "2.4.0.1"))
	if err != nil || string(got) != "bundled-core" {
		t.Fatalf("core 应被收进 versions/2.4.0.1，实际 err=%v content=%q", err, got)
	}
	if exists(rootCore) {
		t.Fatal("收好之后根目录的 core 应当被删除或改名")
	}
	// 改名的情况也接受，但文件名必须带 .bundled- 前缀，交给下次启动清理。
	matches, _ := filepath.Glob(rootCore + bundledRetiredSuffix + "*")
	if len(matches) > 0 {
		t.Logf("根目录 core 被改名为 %s（下次启动清理）", filepath.Base(matches[0]))
	}
}

// 版本目录里已经有时（重复解压发行包），根目录那份同样要退休。
func TestAdoptBundledCoreRetiresDuplicateWhenAlreadyAdopted(t *testing.T) {
	l := newTestLayout(t)
	mkVersion(t, l, "2.4.0.1")
	rootCore := filepath.Join(l.Root, coreStem+exeExt())
	if err := os.WriteFile(rootCore, []byte("bundled-core"), 0o755); err != nil {
		t.Fatal(err)
	}

	adoptBundledCore(l, "2.4.0.1")

	if exists(rootCore) {
		t.Fatal("version 目录已存在时，根目录的重复 core 也应被退休")
	}
	got, _ := os.ReadFile(CorePath(l, "2.4.0.1"))
	if string(got) != "core-2.4.0.1" {
		t.Fatalf("已安装的 core 不该被覆盖，实际 %q", got)
	}
}

// 端到端：正常启动路径（无 journal）也要把多余版本收敛到 keep 份。
func TestBootstrapPrunesVersionsOnNormalStart(t *testing.T) {
	l := newTestLayout(t)
	for _, v := range []string{"2.3.0", "2.3.1", "2.4.0", "2.4.0.1"} {
		mkVersion(t, l, v)
	}
	if err := WritePointer(l, Pointer{Current: "2.4.0.1", Previous: "2.4.0"}); err != nil {
		t.Fatalf("写指针: %v", err)
	}

	if action, _ := Bootstrap("2.4.0.1"); action != Continue {
		t.Fatalf("正常启动应返回 Continue，得到 %v", action)
	}

	if exists(filepath.Dir(CorePath(l, "2.3.0"))) {
		t.Fatal("Bootstrap 启动时就该把最旧的版本清掉（不能只在 Settle 里做）")
	}
	if !exists(CorePath(l, "2.4.0.1")) || !exists(CorePath(l, "2.4.0")) {
		t.Fatal("current / previous 必须保留")
	}
}
