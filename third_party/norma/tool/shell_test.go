package tool

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withFakeShellEnv swaps the two lookup injection points for the duration of a
// test so the whole Windows chain is deterministic on any host OS.
func withFakeShellEnv(t *testing.T, files map[string]bool, path map[string]string) {
	t.Helper()
	oldFiles, oldFind := fileExists, findExec
	fake := make(map[string]bool, len(files))
	for k, v := range files {
		fake[slashPath(k)] = v
	}
	fileExists = func(p string) bool { return fake[slashPath(p)] }
	findExec = func(name string) (string, error) {
		if p, ok := path[strings.ToLower(name)]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { fileExists, findExec = oldFiles, oldFind })
}

func TestFamilyOfPath(t *testing.T) {
	for _, tt := range []struct {
		path string
		want ShellFamily
	}{
		{`C:\Program Files\Git\bin\bash.exe`, FamilyBash},
		{`/usr/bin/sh`, FamilyBash},
		{"busybox.exe", FamilyBusybox},
		{"pwsh.exe", FamilyPwsh},
		{"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe", FamilyPowerShell},
		{"C:\\Tools\\customshell.exe", FamilyBash}, // unknown → assumed POSIX
	} {
		if got, _ := familyOfPath(tt.path); got != tt.want {
			t.Errorf("familyOfPath(%q)=%q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestIsWslLauncher(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{`C:\Windows\System32\bash.exe`, true},
		{`C:\WINDOWS\sysnative\bash.exe`, false}, // not a WSL entry point we block
		{`C:\Windows\System32\wsl.exe`, true},
		{`C:\Program Files\WindowsApps\CanonicalGroupLimited.Ubuntu_1.0\x64\bash.exe`, true},
		{`C:\Program Files\Git\bin\bash.exe`, false},
		{`/bin/bash`, false},
	} {
		if got := isWslLauncher(tt.path); got != tt.want {
			t.Errorf("isWslLauncher(%q)=%v, want %v", tt.path, got, tt.want)
		}
	}
}

// The WSL launcher must never be selected: this deployment is explicitly
// no-WSL, and System32\bash.exe would quietly put a Linux VM back in the loop.
func TestDetectSkipsWSLBash(t *testing.T) {
	gitBash := `C:\Program Files\Git\bin\bash.exe`
	withFakeShellEnv(t,
		map[string]bool{gitBash: true, `C:\Windows\System32\bash.exe`: true},
		map[string]string{"bash": `C:\Windows\System32\bash.exe`},
	)
	t.Setenv("ProgramFiles", `C:\Program Files`)
	got := detectWindowsShells()
	if len(got) != 1 || slashPath(got[0].Path) != slashPath(gitBash) {
		t.Fatalf("detected=%+v, want only the Git for Windows bash", got)
	}
	if got[0].Family != FamilyBash {
		t.Fatalf("family=%q, want bash", got[0].Family)
	}
}

func TestDetectDeduplicatesSamePathFromMultipleSources(t *testing.T) {
	git := `C:\Program Files\Git\cmd\git.exe`
	bash := `C:\Program Files\Git\bin\bash.exe`
	withFakeShellEnv(t,
		map[string]bool{bash: true, git: true},
		map[string]string{"bash": bash, "git": git},
	)
	seen := map[string]int{}
	for _, s := range detectWindowsShells() {
		seen[strings.ToLower(slashPath(s.Path))]++
	}
	if n := seen[strings.ToLower(slashPath(bash))]; n > 1 {
		t.Fatalf("bash path listed %d times, want deduped to 1", n)
	}
}

// PATH bash wins over the known-location scan (a user-installed bash earlier in
// PATH is the one they meant).
func TestDetectPrefersPathBash(t *testing.T) {
	pathBash := `D:\Tools\Git\bin\bash.exe`
	withFakeShellEnv(t,
		map[string]bool{pathBash: true, `C:\Program Files\Git\bin\bash.exe`: true},
		map[string]string{"bash": pathBash},
	)
	t.Setenv("ProgramFiles", `C:\Program Files`)
	got := detectWindowsShells()
	if len(got) == 0 || slashPath(got[0].Path) != slashPath(pathBash) {
		t.Fatalf("first candidate=%+v, want %s", got, pathBash)
	}
	if got[0].Source != "detect:path" {
		t.Fatalf("source=%q, want detect:path", got[0].Source)
	}
}

func TestDetectFindsBusybox(t *testing.T) {
	bb := `D:\Tools\busybox.exe`
	withFakeShellEnv(t, map[string]bool{bb: true}, map[string]string{"busybox": bb})
	got := detectWindowsShells()
	if len(got) != 1 || got[0].Family != FamilyBusybox {
		t.Fatalf("detected=%+v, want busybox", got)
	}
	if flags := flagsFor(got[0].Family); len(flags) != 2 || flags[0] != "sh" {
		t.Fatalf("busybox flags=%v, want [sh -c]", flags)
	}
}

func TestPickWindowsShellChain(t *testing.T) {
	explicit := `E:\Portable\Git\bin\bash.exe`
	auto := `C:\Program Files\Git\bin\bash.exe`

	t.Run("explicit wins", func(t *testing.T) {
		withFakeShellEnv(t, map[string]bool{explicit: true, auto: true}, map[string]string{})
		got := pickWindowsShell(explicit)
		if got.Source != "explicit" || slashPath(got.Path) != slashPath(explicit) {
			t.Fatalf("got=%+v, want the explicit path", got)
		}
	})

	// The degradation route that matters most: a saved path that has since been
	// moved/uninstalled must fall through, not break every tool call.
	t.Run("missing explicit degrades to detection", func(t *testing.T) {
		withFakeShellEnv(t, map[string]bool{auto: true}, map[string]string{})
		got := pickWindowsShell(explicit)
		if got.Source != "detect:known" || slashPath(got.Path) != slashPath(auto) {
			t.Fatalf("got=%+v, want detected Git bash", got)
		}
	})

	// Last tier is byte-identical to the pre-feature behaviour.
	t.Run("nothing found falls back to powershell", func(t *testing.T) {
		withFakeShellEnv(t, map[string]bool{}, map[string]string{})
		got := pickWindowsShell("")
		if got.Family != FamilyPowerShell || got.Path != "powershell" || got.Source != "fallback" {
			t.Fatalf("got=%+v, want powershell fallback", got)
		}
		if !strings.Contains(got.Reason, "PowerShell") {
			t.Fatalf("fallback must explain itself, got reason=%q", got.Reason)
		}
	})

	t.Run("pwsh preferred over powershell via detection", func(t *testing.T) {
		// pwsh is not a POSIX shell, so it must NOT appear in DetectShells;
		// it is only reachable as an explicit choice.
		withFakeShellEnv(t, map[string]bool{`C:\ps\pwsh.exe`: true},
			map[string]string{"pwsh": `C:\ps\pwsh.exe`})
		if got := detectWindowsShells(); len(got) != 0 {
			t.Fatalf("detect must ignore pwsh, got %+v", got)
		}
		got := pickWindowsShell(`C:\ps\pwsh.exe`)
		if got.Family != FamilyPwsh {
			t.Fatalf("family=%q, want pwsh", got.Family)
		}
	})
}

// Non-Windows must be untouched, even with a stale path stored.
func TestNonWindowsIgnoresConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows host")
	}
	t.Cleanup(func() { SetShellPath("") })
	SetShellPath(`C:\Program Files\Git\bin\bash.exe`)
	got := resolveShell("linux", time.Now())
	if got.Path != "bash" || got.Family != FamilyBash || got.Source != "platform" {
		t.Fatalf("non-windows resolution=%+v, want unchanged bash", got)
	}
	sh, flags := shellCmd()
	if sh != "bash" || len(flags) != 1 || flags[0] != "-c" {
		t.Fatalf("shellCmd()=%q %v, want bash [-c]", sh, flags)
	}
}

func TestSetShellPathInvalidatesCache(t *testing.T) {
	gitBash := `C:\Program Files\Git\bin\bash.exe`
	withFakeShellEnv(t, map[string]bool{gitBash: true}, map[string]string{})
	t.Cleanup(func() { SetShellPath("") })

	t0 := time.Now()
	_ = resolveShell("windows", t0)
	SetShellPath(gitBash)
	got := resolveShell("windows", t0.Add(time.Second)) // inside TTL, but config changed
	if got.Source != "explicit" {
		t.Fatalf("source=%q, want explicit immediately after SetShellPath", got.Source)
	}

	// Without a config change the cached answer is reused inside the TTL.
	second := resolveShell("windows", t0.Add(2*time.Second))
	if second.Path != got.Path {
		t.Fatalf("cache not reused: %q vs %q", second.Path, got.Path)
	}
}

// ProbeShell must accept a real POSIX shell and reject a binary that only looks
// like one — the point of the probe is grammar capability, not existence.
func TestProbeShellAcceptsRealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell path not guaranteed on windows runners")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this host")
	}
	res := ProbeShell(context.Background(), bash)
	if !res.OK {
		t.Fatalf("probe of %s failed: %+v", bash, res)
	}
	if res.Version == "" {
		t.Fatal("probe should surface the version line")
	}
}

func TestProbeShellRejectsNonShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX binaries")
	}
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no /usr/bin/false on this host")
	}
	res := ProbeShell(context.Background(), falseBin)
	if res.OK {
		t.Fatalf("`false` must not pass the probe: %+v", res)
	}
	if res.Error == "" {
		t.Fatal("failed probe must carry a reason")
	}
}

func TestProbeShellMissingFile(t *testing.T) {
	res := ProbeShell(context.Background(), filepath.Join(t.TempDir(), "nope", "bash.exe"))
	if res.OK || !strings.Contains(res.Error, "不存在") {
		t.Fatalf("got=%+v, want a clear missing-file error", res)
	}
}
