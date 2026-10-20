package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePorcelainZ(t *testing.T) {
	// 真实 -z 输出：普通改动 + 未跟踪 + 重命名（新名在前，旧名随后一个 token）
	raw := "M  src/a.go\x00?? notes.md\x00R  src/new.go\x00src/old.go\x00D  gone.txt\x00"
	got := parsePorcelainZ(raw)
	want := []convChange{
		{Path: "gone.txt", Status: "D"},
		{Path: "notes.md", Status: "??"},
		{Path: "src/a.go", Status: "M"},
		{Path: "src/new.go", Status: "R"},
	}
	if len(got) != len(want) {
		t.Fatalf("条目数 %d want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Path != want[i].Path || got[i].Status != want[i].Status {
			t.Fatalf("第 %d 项 %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestCountDiffLines(t *testing.T) {
	patch := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,2 +1,3 @@\n-old\n+new\n+extra\n same\n"
	if add, del := countDiffLines(patch); add != 2 || del != 1 {
		t.Fatalf("add=%d del=%d want 2/1", add, del)
	}
}

// gitRepo 建一个临时 git 仓库并提交一个文件，返回仓库根。
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境没有 git，跳过")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "init")
	return dir
}

func TestGitChangeFlow(t *testing.T) {
	root := gitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if !isGitRoot(ctx, root) {
		t.Fatal("临时仓库应被识别为 git 工作根")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := gitCmd(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	files := parsePorcelainZ(out)
	if len(files) != 2 {
		t.Fatalf("应有 2 个变更，实际 %+v", files)
	}
	byPath := map[string]convChange{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if byPath["a.txt"].Status != "M" || byPath["b.txt"].Status != "??" {
		t.Fatalf("状态解析错误: %+v", byPath)
	}

	patch, _, err := gitCmd(ctx, root, "diff", "HEAD", "--unified=3", "--", "a.txt")
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	if !strings.Contains(patch, "-old") || !strings.Contains(patch, "+new") {
		t.Fatalf("diff 内容不符合预期:\n%s", patch)
	}
	if add, del := countDiffLines(patch); add != 1 || del != 1 {
		t.Fatalf("add=%d del=%d want 1/1", add, del)
	}

	// 回滚一个已跟踪文件：内容应恢复且不再出现在 status 里。
	if _, errOut, err := gitCmd(ctx, root, "checkout", "HEAD", "--", "a.txt"); err != nil {
		t.Fatalf("checkout: %v %s", err, errOut)
	}
	rest, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(rest) != "old\n" {
		t.Fatalf("回滚后内容应为 old，实际 %q", string(rest))
	}
}
