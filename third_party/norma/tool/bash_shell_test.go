package tool

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBashToolRunsThroughResolvedShell proves the Bash tool's synchronous path
// actually executes through shellCmd (rather than some hard-coded literal), using
// POSIX-only syntax that only a `-c` shell understands: $(( )) arithmetic, &&
// chaining and a pipe. That is exactly the corpus the model is prompted to write,
// so it is the behaviour the Git Bash work exists to protect.
func TestBashToolRunsThroughResolvedShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only syntax; the Windows chain is covered by shell_test.go")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash on this host")
	}
	t.Cleanup(func() { SetShellPath("") })

	out := callBashForTest(t, `echo $((6*7)) && printf 'a\nb\n' | wc -l`)
	if got := strings.TrimSpace(out); got != "42\n2" {
		t.Fatalf("bash output=%q, want %q（未按 POSIX 语法执行）", got, "42\n2")
	}
}

// The three execution paths (Bash foreground, background task, interactive
// session) all resolve their command line through shellCmd, so a config change
// must reach every one of them. This asserts it on the background path, which is
// a separate call site from runBashSync.
func TestBackgroundTaskUsesResolvedShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only syntax")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash on this host")
	}
	t.Cleanup(func() { SetShellPath("") })

	m := newTestManager(t)
	task, finished, output, err := m.RunForeground(context.Background(), SpawnSpec{
		Command:     "echo $((3*3))",
		Kind:        KindBash,
		Description: "shell resolution check",
	}, 20*time.Second)
	if err != nil {
		t.Fatalf("foreground task: %v (task=%v)", err, task != nil)
	}
	if !finished {
		t.Fatalf("task did not finish; output=%q", output)
	}
	if got := strings.TrimSpace(output); got != "9" {
		t.Fatalf("background path output=%q, want 9", got)
	}
}

// shellCmd must be the single source of truth for both the executable and the
// flags: bash family takes `-c`, powershell family takes `-NoProfile
// -NonInteractive -Command`. A drift here would silently send POSIX strings to
// PowerShell, which is the original bug.
func TestFlagsFollowResolvedFamily(t *testing.T) {
	t.Cleanup(func() { SetShellPath("") })

	for _, tt := range []struct {
		path      string
		wantFlags []string
	}{
		{"/bin/bash", []string{"-c"}},
		{`C:\Program Files\Git\bin\bash.exe`, []string{"-c"}},
		{`C:\Tools\busybox.exe`, []string{"sh", "-c"}},
		{`C:\Program Files\PowerShell\7\pwsh.exe`, []string{"-NoProfile", "-NonInteractive", "-Command"}},
		{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, []string{"-NoProfile", "-NonInteractive", "-Command"}},
	} {
		fam := mustFamily(tt.path)
		if fam == "" {
			t.Fatalf("familyOfPath(%q) returned empty family", tt.path)
		}
		flags := flagsFor(fam)
		if strings.Join(flags, "|") != strings.Join(tt.wantFlags, "|") {
			t.Errorf("flags for %s = %v, want %v", tt.path, flags, tt.wantFlags)
		}
	}
}

func mustFamily(path string) ShellFamily {
	fam, _ := familyOfPath(path)
	return fam
}

// callBashForTest exercises runBashSync: no task manager on the ToolContext means
// background execution is unavailable, which is exactly that path.
func callBashForTest(t *testing.T, command string) string {
	t.Helper()
	in, err := json.Marshal(map[string]any{"command": command, "timeout_ms": 20000})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewBash().Call(context.Background(), in, &ToolContext{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("bash call: %v", err)
	}
	if res.IsError {
		t.Fatalf("bash returned an error result: %s", res.Flatten())
	}
	return res.Flatten()
}
