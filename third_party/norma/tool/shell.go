package tool

// Shell selection for the Bash / background-task / interactive-session execution
// paths (all three go through shellCmd below).
//
// Why this exists: the command corpus the model is prompted to write is POSIX
// (the tool is called Bash; the prompt says to chain with && and use pipes,
// heredocs, grep/sed). Before this file, Windows hard-mapped that corpus onto
// Windows PowerShell 5.1, which parses && differently (or not at all), has no
// grep/sed/head/tail, aliases curl/rm/cp to incompatible cmdlets, and decodes
// native output by console code page. The result was systematic tool errors on
// native Windows.
//
// The fix is a capability probe rather than a hard-coded platform branch:
//
//	explicit host path (Git Bash picked in the UI)   -> bash grammar
//	  else auto-detected POSIX shell (Git for Windows, MSYS2, Cygwin, busybox)
//	    else PowerShell 7 (pwsh)                      -> powershell grammar
//	      else Windows PowerShell 5.1                 -> powershell grammar (previous behaviour)
//
// Non-Windows platforms are untouched: shellCmd still returns exactly
// ("bash", ["-c"]), so Linux/macOS/Docker behaviour is byte-identical to before.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ShellFamily is the command grammar an executable understands, which decides
// both the argument flags and what the prompt may assume.
type ShellFamily string

const (
	FamilyBash       ShellFamily = "bash"       // POSIX sh grammar via `-c`
	FamilyBusybox    ShellFamily = "busybox"    // ash via `sh -c` on a busybox multi-call binary
	FamilyPwsh       ShellFamily = "pwsh"       // PowerShell 7 via `-Command`
	FamilyPowerShell ShellFamily = "powershell" // Windows PowerShell 5.1 via `-Command`
)

// Shell is one resolved command shell plus how it was chosen, so a host UI can
// show the operator which tier is live instead of guessing.
type Shell struct {
	Path   string      `json:"path"`
	Family ShellFamily `json:"family"`
	Source string      `json:"source"` // explicit | detect | fallback
	Reason string      `json:"reason,omitempty"`
}

const (
	// shellProbeMarker is what a successful probe must echo back.
	shellProbeMarker = "ART_NORMA_SHELL_OK"
	// shellResolveTTL bounds how long a resolved path may be reused before the
	// filesystem is re-checked. A path that disappears (Git uninstalled, portable
	// copy moved) therefore degrades on its own within one TTL, while an
	// explicitly saved setting takes effect immediately (SetShellPath resets it).
	shellResolveTTL = 30 * time.Second
	// shellProbeBudget is the ceiling for one probe exec — a hung shell must not
	// wedge the settings UI.
	shellProbeBudget = 8 * time.Second
)

// posixFlags and psFlags are the two grammars' "run this string" prefixes.
// bash uses plain -c (no -l/--login: profile scripts can print noise and slow
// every call, mirroring the -NoProfile intent already in the powershell branch).
var (
	posixFlags   = []string{"-c"}
	busyboxFlags = []string{"sh", "-c"}
	psFlags      = []string{"-NoProfile", "-NonInteractive", "-Command"}
)

// Injection points for tests: filesystem/PATH lookups only.
var (
	fileExists = func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	findExec = exec.LookPath
)

type shellState struct {
	mu       sync.Mutex
	explicit string
	cached   Shell
	cachedAt time.Time
}

var shells shellState

// SetShellPath records the host-chosen shell ("" = auto-detect) and forces the
// next resolution to re-run. Safe to call concurrently and repeatedly; every
// Bash call re-reads, so a config change applies without restarting or rebuilding
// agents.
func SetShellPath(path string) {
	shells.mu.Lock()
	shells.explicit = strings.TrimSpace(path)
	shells.cachedAt = time.Time{}
	shells.mu.Unlock()
}

// ShellPath returns the host-configured path ("" = auto-detect).
func ShellPath() string {
	shells.mu.Lock()
	defer shells.mu.Unlock()
	return shells.explicit
}

// CurrentShell returns the shell that shellCmd will use right now.
func CurrentShell() Shell { return resolveShell(runtime.GOOS, time.Now()) }

// resolveShell applies the chain above. For non-Windows it returns the historic
// answer without touching any config, so the feature cannot alter Linux/macOS
// behaviour even if a stale path is stored in the database. goos and now are
// parameters so the Windows branch and the cache are testable from any host.
func resolveShell(goos string, now time.Time) Shell {
	if goos != "windows" {
		return Shell{Path: "bash", Family: FamilyBash, Source: "platform"}
	}

	shells.mu.Lock()
	defer shells.mu.Unlock()
	if !shells.cachedAt.IsZero() && now.Sub(shells.cachedAt) < shellResolveTTL {
		return shells.cached
	}
	sh := pickWindowsShell(shells.explicit)
	shells.cached = sh
	shells.cachedAt = now
	return sh
}

// pickWindowsShell walks the degradation chain. Every step is a cheap stat or
// PATH lookup — no process is spawned here, so this stays on the hot path safely.
func pickWindowsShell(explicit string) Shell {
	if explicit != "" {
		if fileExists(explicit) {
			fam, reason := familyOfPath(explicit)
			return Shell{Path: explicit, Family: fam, Source: "explicit", Reason: reason}
		}
		// Fall through: a configured path that is gone degrades instead of
		// breaking every tool call.
	}
	for _, c := range detectWindowsShells() {
		return c
	}
	// Last tier = exactly the behaviour before this file existed.
	return Shell{Path: "powershell", Family: FamilyPowerShell, Source: "fallback",
		Reason: "no POSIX shell found; PowerShell does not understand the POSIX corpus the model writes"}
}

// DetectShells lists POSIX shells found on this machine, best first. Hosts use it
// both for auto-detection and to offer the operator a concrete choice.
func DetectShells() []Shell { return detectWindowsShells() }

func detectWindowsShells() []Shell {
	out := []Shell{}
	add := func(p, source string) {
		if p == "" || !fileExists(p) {
			return
		}
		fam, reason := familyOfPath(p)
		if fam != FamilyBash && fam != FamilyBusybox {
			return // only POSIX shells qualify here
		}
		for _, e := range out {
			// Compare separator-insensitively: an env var like ProgramFiles=C:/PROGRA~1
			// would otherwise yield the same shell twice via filepath.Join.
			if strings.EqualFold(slashPath(e.Path), slashPath(p)) {
				return
			}
		}
		out = append(out, Shell{Path: p, Family: fam, Source: source, Reason: reason})
	}

	// 1. bash/sh on PATH — minus the WSL launchers, which would silently put a
	//    VM/subsystem back in the loop (and do not see the Windows filesystem the
	//    same way) even though this deployment is explicitly no-WSL.
	for _, name := range []string{"bash", "sh"} {
		if p, err := findExec(name); err == nil && !isWslLauncher(p) {
			add(p, "detect:path")
		}
	}
	// 2. Standard Git for Windows / MSYS2 / Cygwin locations.
	for _, p := range knownShellPaths() {
		add(p, "detect:known")
	}
	// 3. Derive from git on PATH: <root>\cmd\git.exe -> <root>\bin\bash.exe.
	//    Covers custom install directories where the installer added git to PATH.
	if g, err := findExec("git"); err == nil && !isWslLauncher(g) {
		root := filepath.Dir(filepath.Dir(g)) // ...\Git\cmd -> ...\Git
		for _, rel := range []string{
			filepath.Join("bin", "bash.exe"),
			filepath.Join("usr", "bin", "bash.exe"),
		} {
			add(filepath.Join(root, rel), "detect:git")
		}
	}
	// 4. busybox-w32 single binary (no Git installed): native, no installer.
	if p, err := findExec("busybox"); err == nil && !isWslLauncher(p) {
		add(p, "detect:busybox")
	}
	return out
}

func knownShellPaths() []string {
	var out []string
	envs := []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "LOCALAPPDATA", "SystemDrive"}
	for _, k := range envs {
		base := strings.TrimSpace(os.Getenv(k))
		if base == "" {
			continue
		}
		for _, sub := range []string{
			filepath.Join("Git", "bin", "bash.exe"),
			filepath.Join("Git", "usr", "bin", "bash.exe"),
			filepath.Join("Programs", "Git", "bin", "bash.exe"),
			filepath.Join("Git", "bin", "sh.exe"),
		} {
			out = append(out, filepath.Join(base, sub))
		}
	}
	// Portable/unattended setups that put Git or MSYS2 on a fixed drive letter.
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Git\bin\bash.exe`,
		`C:\msys64\usr\bin\bash.exe`,
		`C:\cygwin64\bin\bash.exe`,
	} {
		out = append(out, p)
	}
	return out
}

// slashPath normalises separators for comparisons only (never for exec): the
// same file may be spelled with either separator depending on whether it came
// from an env var, a Join, or the operator's paste.
func slashPath(p string) string { return strings.ReplaceAll(p, "\\", "/") }

// baseName is filepath.Base that also treats backslash as a separator.
// filepath.Base only splits on '\' when running *on* Windows, so a Windows path
// handed to this function on another host would keep its whole string as the
// "file name" and every family match would silently fall through to the default.
// Making it separator-agnostic both fixes that and lets the Windows chain be
// unit-tested on Linux/macOS runners.
func baseName(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// familyOfPath maps an executable name to its grammar. Unknown names are assumed
// POSIX (that is what an operator selecting a "bash location" means), but the
// assumption is reported so a UI can surface it.
func familyOfPath(p string) (ShellFamily, string) {
	switch strings.ToLower(baseName(p)) {
	case "pwsh.exe", "pwsh":
		return FamilyPwsh, ""
	case "powershell.exe", "powershell":
		return FamilyPowerShell, ""
	case "busybox.exe", "busybox":
		return FamilyBusybox, ""
	case "bash.exe", "bash", "sh.exe", "sh", "bash_login":
		return FamilyBash, ""
	default:
		return FamilyBash, "unrecognized executable name; assumed POSIX `-c` semantics (verify with the probe)"
	}
}

// isWslLauncher reports whether p is the built-in WSL entry point rather than a
// real Windows-native shell.
func isWslLauncher(p string) bool {
	s := strings.ToLower(filepath.Clean(p))
	s = strings.ReplaceAll(s, "/", "\\")
	for _, frag := range []string{
		"\\windows\\system32\\bash.exe",
		"\\windows\\syswow64\\bash.exe",
		"\\windows\\system32\\wsl",
		"\\windowsapps\\",
	} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}

// flagsFor returns the "run this string" prefix for a family.
func flagsFor(fam ShellFamily) []string {
	switch fam {
	case FamilyBusybox:
		return busyboxFlags
	case FamilyPwsh, FamilyPowerShell:
		return psFlags
	default:
		return posixFlags
	}
}

// shellCmd returns the platform shell and the argument flags that precede the
// command string. On Windows the answer comes from the capability chain in this
// file (Git Bash / MSYS2 / busybox preferred, PowerShell as the documented
// degradation). Elsewhere it uses `bash -c`, unchanged from before.
func shellCmd() (string, []string) {
	sh := CurrentShell()
	return sh.Path, flagsFor(sh.Family)
}

// ProbeResult is the outcome of one usability test.
type ProbeResult struct {
	Shell   Shell  `json:"shell"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ProbeShell runs one trivial command through the given shell path and reports
// whether it actually works — presence of the file is not enough, the binary may
// be a WSL stub, a broken portable copy, or a different grammar entirely. An
// empty path probes the auto-detected choice.
func ProbeShell(ctx context.Context, path string) ProbeResult {
	sh := Shell{Path: strings.TrimSpace(path), Family: FamilyBash, Source: "explicit"}
	if sh.Path == "" {
		sh = CurrentShell()
	} else {
		sh.Family, sh.Reason = familyOfPath(sh.Path)
		if !fileExists(sh.Path) {
			return ProbeResult{Shell: sh, Error: "文件不存在或不可访问：" + sh.Path}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, shellProbeBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, sh.Path, append(flagsFor(sh.Family), probeCommand(sh.Family))...)
	// Probe with the ambient environment only: this is a local syntax test, it
	// must not depend on proxies, CA bundles or a working directory.
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	res := ProbeResult{Shell: sh, Output: text}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			res.Error = "执行超时（" + shellProbeBudget.String() + "），该 shell 可能卡在交互提示上"
		} else {
			res.Error = err.Error()
		}
	}
	lines := []string{}
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(strings.TrimRight(l, "\r")); l != "" {
			lines = append(lines, l)
		}
	}
	if !hasMarker(lines) {
		// Non-zero exit alone is not the diagnosis to report: what actually matters
		// is that the POSIX grammar did not work.
		if res.Error == "" {
			res.Error = "未回显探测标记：该文件不是可用的 POSIX shell，或其 profile 干扰了输出"
		} else {
			res.Error += "；且未回显探测标记（POSIX 语法不被支持）"
		}
		return res
	}
	res.OK = true
	for _, l := range lines {
		if l != shellProbeMarker {
			res.Version = l
			break
		}
	}
	return res
}

func hasMarker(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, shellProbeMarker) {
			return true
		}
	}
	return false
}

// probeCommand deliberately tests *grammar*, not just liveness: && chaining and
// parameter expansion are exactly what PowerShell 5.1 gets wrong, so a PowerShell
// binary pointed at here fails the probe instead of passing a "file exists" check.
//
// The version line must come from the shell's own builtins/variables — an explicit
// path is typically a *portable* Git that is not on PATH at all, so calling out to
// `bash`/`busybox` by name would print "command not found" as the version.
func probeCommand(fam ShellFamily) string {
	switch fam {
	case FamilyPwsh, FamilyPowerShell:
		return "Write-Output " + shellProbeMarker + "; $PSVersionTable.PSVersion.ToString()"
	case FamilyBusybox:
		// ash: no reliable version variable; the && grammar check is the point.
		return "echo " + shellProbeMarker + " && echo busybox-ash"
	default:
		return "echo " + shellProbeMarker + " && echo bash-${BASH_VERSION:-unknown}"
	}
}
