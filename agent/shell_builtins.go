package agent

import (
	"context"
	"crypto/md5" //nolint:gosec
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/tidwall/gjson"
)

// crossPlatformBashTool 装饰 SDK 的 Bash CoreTool，在保持 Name("Bash")、Description()、
// Prompt() 与权限审批钩子 100% 兼容的前提下，提供三层加速与三端统一兼容：
//  1. 进程内纯 Go 原语（Built-in Coreutils）：对无复杂管道/重定向的简单命令（cat/head/tail/
//     wc/grep/sort/uniq/echo/pwd/ls/mkdir/rm/cp/mv/base64/md5sum/sha256sum/jq）直接在 Go
//     内存中完成，零子进程开销、三端语法完全一致；
//  2. 外部安全二进制直调（Bypass PowerShell）：在 Windows 下调用 nmap/nuclei/ffuf/sqlmap/
//     httpx/subfinder/naabu/katana/curl.exe 等独立二进制且不含 Shell 复合元字符时，直接通过
//     exec.CommandContext (CreateProcess) 启动目标程序，彻底跳过 powershell.exe (.NET CLR) 冷启动；
//  3. Windows Shell 语法归一化与全局并发闸门：回退到底层 Shell 时自动将 /dev/null、裸 curl、
//     export VAR=val 归一化为 PowerShell 兼容形式，并受跨 Worker 全局进程信号量节流。
type crossPlatformBashTool struct {
	actool.CoreTool
	workDir  string
	extraEnv []string
}

// WrapCrossPlatformBash 包装默认工具切片中的 Bash 工具。
func WrapCrossPlatformBash(tools []actool.CoreTool, workDir string, extraEnv []string) []actool.CoreTool {
	out := make([]actool.CoreTool, len(tools))
	for i, tl := range tools {
		if tl != nil && tl.Name() == "Bash" {
			out[i] = &crossPlatformBashTool{
				CoreTool: tl,
				workDir:  workDir,
				extraEnv: extraEnv,
			}
		} else {
			out[i] = tl
		}
	}
	return out
}

// Call 在原行为之上追加一条「逃生通道转向」：一旦模型在 Bash 里写/跑 Python 脚本，
// 就在同一次工具结果里点名可替代的原生工具。回归证据：某安装 http_request 占全部
// 调用的 49.4%，而写脚本正是绕开专用工具的主要路径（见 agent/tool_steering.go）。
func (b *crossPlatformBashTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	res, err := b.call(ctx, in, tc)
	if err != nil {
		return res, err
	}
	if note := pythonScriptSteer(bashCommandOf(in)); note != "" {
		// 追加一个 text block；保留既有 text/非 text block、IsError 与 Extra。
		res.Content = append(append([]llm.ContentBlock(nil), res.Content...), llm.TextBlock(note))
	}
	return res, nil
}

// bashCommandOf 取回模型写的原始命令。
func bashCommandOf(in json.RawMessage) string {
	var p struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(in, &p)
	return p.Command
}

func (b *crossPlatformBashTool) call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	var payload map[string]any
	if err := json.Unmarshal(in, &payload); err != nil {
		return b.CoreTool.Call(ctx, in, tc)
	}
	cmdStr, _ := payload["command"].(string)
	cmdTrimmed := strings.TrimSpace(cmdStr)
	if cmdTrimmed == "" {
		return b.CoreTool.Call(ctx, in, tc)
	}
	runBg, _ := payload["run_in_background"].(bool)
	if runBg {
		if runtime.GOOS == "windows" {
			if norm := normalizeWindowsShellCommand(cmdTrimmed); norm != cmdTrimmed {
				payload["command"] = norm
				if nb, err := json.Marshal(payload); err == nil {
					in = nb
				}
			}
		}
		return b.CoreTool.Call(ctx, in, tc)
	}

	// 1. 尝试纯 Go 进程内原语（零子进程）
	if out, handled := b.tryExecBuiltin(cmdTrimmed); handled {
		return actool.Text(actool.Capture(tc, out)), nil
	}

	timeoutDur := 2 * time.Minute
	if tms, ok := payload["timeout"].(float64); ok && tms > 0 {
		timeoutDur = time.Duration(tms) * time.Millisecond
	}

	// 2. 尝试外部编译型工具直调（Windows 下绕过 powershell.exe 冷启动）
	if out, handled := b.tryExecDirectBinary(ctx, cmdTrimmed, timeoutDur); handled {
		return actool.Text(actool.Capture(tc, out)), nil
	}

	// 3. 回退底层 Shell 执行器（附带 Windows POSIX 兼容归一化与全局外部进程并发信号量保护）
	if runtime.GOOS == "windows" {
		if norm := normalizeWindowsShellCommand(cmdTrimmed); norm != cmdTrimmed {
			payload["command"] = norm
			if nb, err := json.Marshal(payload); err == nil {
				in = nb
			}
		}
	}
	release, err := AcquireExternalProc(ctx)
	if err != nil {
		return actool.Errorf("等待命令执行槽位超时: " + err.Error()), nil
	}
	defer release()
	return b.CoreTool.Call(ctx, in, tc)
}

func hasShellMeta(cmd string) bool {
	return strings.ContainsAny(cmd, "|;&><`$\n\r")
}

func (b *crossPlatformBashTool) resolvePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if b.workDir != "" {
		return filepath.Join(b.workDir, p)
	}
	return p
}

func (b *crossPlatformBashTool) tryExecBuiltin(cmd string) (string, bool) {
	if hasShellMeta(cmd) {
		return "", false
	}
	args, ok := splitShellWords(cmd)
	if !ok || len(args) == 0 {
		return "", false
	}
	prog := strings.ToLower(args[0])
	rest := args[1:]

	switch prog {
	case "pwd":
		if len(rest) == 0 {
			if b.workDir != "" {
				return b.workDir, true
			}
			wd, _ := os.Getwd()
			return wd, true
		}

	case "echo":
		noNewline := false
		if len(rest) > 0 && rest[0] == "-n" {
			noNewline = true
			rest = rest[1:]
		}
		s := strings.Join(rest, " ")
		if !noNewline {
			s += "\n"
		}
		return s, true

	case "cat":
		if len(rest) == 0 {
			return "", false
		}
		var sb strings.Builder
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				return "", false
			}
			raw, err := os.ReadFile(b.resolvePath(a))
			if err != nil {
				return fmt.Sprintf("cat: %s: %v", a, err), true
			}
			sb.Write(raw)
		}
		return clipUTF8(sb.String(), 64*1024), true

	case "head", "tail":
		linesCnt := 10
		var files []string
		for i := 0; i < len(rest); i++ {
			if (rest[i] == "-n" || rest[i] == "-Lines") && i+1 < len(rest) {
				if n, err := strconv.Atoi(rest[i+1]); err == nil && n > 0 {
					linesCnt = n
					i++
					continue
				}
			} else if strings.HasPrefix(rest[i], "-n") && len(rest[i]) > 2 {
				if n, err := strconv.Atoi(rest[i][2:]); err == nil && n > 0 {
					linesCnt = n
					continue
				}
			} else if strings.HasPrefix(rest[i], "-") {
				if n, err := strconv.Atoi(rest[i][1:]); err == nil && n > 0 {
					linesCnt = n
					continue
				}
				return "", false
			}
			files = append(files, rest[i])
		}
		if len(files) != 1 {
			return "", false
		}
		raw, err := os.ReadFile(b.resolvePath(files[0]))
		if err != nil {
			return fmt.Sprintf("%s: %s: %v", prog, files[0], err), true
		}
		lines := strings.Split(string(raw), "\n")
		if prog == "head" {
			if len(lines) > linesCnt {
				lines = lines[:linesCnt]
			}
		} else {
			if len(lines) > linesCnt {
				lines = lines[len(lines)-linesCnt:]
			}
		}
		return clipUTF8(strings.Join(lines, "\n"), 64*1024), true

	case "wc":
		if len(rest) == 2 && (rest[0] == "-l" || rest[0] == "-c" || rest[0] == "-w") {
			raw, err := os.ReadFile(b.resolvePath(rest[1]))
			if err != nil {
				return fmt.Sprintf("wc: %s: %v", rest[1], err), true
			}
			switch rest[0] {
			case "-l":
				return fmt.Sprintf("%d %s\n", strings.Count(string(raw), "\n"), rest[1]), true
			case "-c":
				return fmt.Sprintf("%d %s\n", len(raw), rest[1]), true
			case "-w":
				return fmt.Sprintf("%d %s\n", len(strings.Fields(string(raw))), rest[1]), true
			}
		}

	case "mkdir":
		var dirs []string
		for _, a := range rest {
			if a == "-p" {
				continue
			}
			if strings.HasPrefix(a, "-") {
				return "", false
			}
			dirs = append(dirs, a)
		}
		if len(dirs) == 0 {
			return "", false
		}
		for _, d := range dirs {
			if err := os.MkdirAll(b.resolvePath(d), 0o755); err != nil {
				return fmt.Sprintf("mkdir: %s: %v", d, err), true
			}
		}
		return "", true

	case "md5sum", "sha256sum":
		if len(rest) == 1 && !strings.HasPrefix(rest[0], "-") {
			raw, err := os.ReadFile(b.resolvePath(rest[0]))
			if err != nil {
				return fmt.Sprintf("%s: %s: %v", prog, rest[0], err), true
			}
			if prog == "md5sum" {
				s := md5.Sum(raw) //nolint:gosec
				return fmt.Sprintf("%s  %s\n", hex.EncodeToString(s[:]), rest[0]), true
			}
			s := sha256.Sum256(raw)
			return fmt.Sprintf("%s  %s\n", hex.EncodeToString(s[:]), rest[0]), true
		}

	case "base64":
		if len(rest) == 1 && !strings.HasPrefix(rest[0], "-") {
			raw, err := os.ReadFile(b.resolvePath(rest[0]))
			if err != nil {
				return fmt.Sprintf("base64: %s: %v", rest[0], err), true
			}
			return base64.StdEncoding.EncodeToString(raw) + "\n", true
		}
		if len(rest) == 2 && (rest[0] == "-d" || rest[0] == "--decode") {
			raw, err := os.ReadFile(b.resolvePath(rest[1]))
			if err != nil {
				return fmt.Sprintf("base64: %s: %v", rest[1], err), true
			}
			dec, err := decodeBase64Flexible(string(raw))
			if err != nil {
				return fmt.Sprintf("base64 decode error: %v", err), true
			}
			return string(dec), true
		}

	case "jq":
		var rawOut bool
		var positional []string
		for _, a := range rest {
			if a == "-r" || a == "-c" {
				rawOut = true
				continue
			}
			if strings.HasPrefix(a, "-") {
				return "", false
			}
			positional = append(positional, a)
		}
		if len(positional) == 2 {
			filter := strings.TrimPrefix(strings.TrimSpace(positional[0]), ".")
			raw, err := os.ReadFile(b.resolvePath(positional[1]))
			if err != nil {
				return fmt.Sprintf("jq: %s: %v", positional[1], err), true
			}
			if filter == "" {
				var obj any
				if json.Unmarshal(raw, &obj) == nil {
					pretty, _ := json.MarshalIndent(obj, "", "  ")
					return clipUTF8(string(pretty), 64*1024), true
				}
				return clipUTF8(string(raw), 64*1024), true
			}
			res := gjson.GetBytes(raw, filter)
			if rawOut && res.Type == gjson.String {
				return res.String() + "\n", true
			}
			return res.Raw + "\n", true
		}

	case "sort", "uniq":
		if len(rest) == 1 && !strings.HasPrefix(rest[0], "-") {
			raw, err := os.ReadFile(b.resolvePath(rest[0]))
			if err != nil {
				return fmt.Sprintf("%s: %s: %v", prog, rest[0], err), true
			}
			lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
			if prog == "sort" {
				sort.Strings(lines)
				return clipUTF8(strings.Join(lines, "\n")+"\n", 64*1024), true
			}
			var dedup []string
			for i, l := range lines {
				if i == 0 || l != lines[i-1] {
					dedup = append(dedup, l)
				}
			}
			return clipUTF8(strings.Join(dedup, "\n")+"\n", 64*1024), true
		}

	case "grep":
		var ignoreCase, lineNum bool
		var pos []string
		for _, a := range rest {
			switch a {
			case "-i":
				ignoreCase = true
			case "-n":
				lineNum = true
			case "-in", "-ni":
				ignoreCase = true
				lineNum = true
			case "-E", "-e":
				continue
			default:
				if strings.HasPrefix(a, "-") {
					return "", false
				}
				pos = append(pos, a)
			}
		}
		if len(pos) == 2 {
			pat := pos[0]
			if ignoreCase {
				pat = "(?i)" + pat
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return fmt.Sprintf("grep: invalid regex: %v", err), true
			}
			raw, err := os.ReadFile(b.resolvePath(pos[1]))
			if err != nil {
				return fmt.Sprintf("grep: %s: %v", pos[1], err), true
			}
			var matches []string
			for idx, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimRight(line, "\r")
				if re.MatchString(line) {
					if lineNum {
						matches = append(matches, fmt.Sprintf("%d:%s", idx+1, line))
					} else {
						matches = append(matches, line)
					}
				}
			}
			return clipUTF8(strings.Join(matches, "\n"), 64*1024), true
		}
	}

	return "", false
}

var directSecurityBinaries = map[string]bool{
	"nmap": true, "nuclei": true, "sqlmap": true, "ffuf": true,
	"httpx": true, "subfinder": true, "naabu": true, "katana": true,
	"feroxbuster": true, "gobuster": true, "dirsearch": true, "nikto": true,
	"hydra": true, "curl.exe": true, "camoufox-cli": true,
	"dig": true, "nslookup": true, "whois": true,
}

func (b *crossPlatformBashTool) tryExecDirectBinary(ctx context.Context, cmd string, timeout time.Duration) (string, bool) {
	if hasShellMeta(cmd) {
		return "", false
	}
	args, ok := splitShellWords(cmd)
	if !ok || len(args) == 0 {
		return "", false
	}
	binName := strings.ToLower(filepath.Base(args[0]))
	binNameNoExt := strings.TrimSuffix(binName, ".exe")
	if !directSecurityBinaries[binName] && !directSecurityBinaries[binNameNoExt] {
		return "", false
	}
	resolvedBin, err := exec.LookPath(args[0])
	if err != nil {
		return "", false
	}
	out, exitCode, runErr := runManagedExternalCommand(ctx, resolvedBin, args[1:], b.workDir, b.extraEnv, timeout)
	if runErr != nil {
		return "", false
	}
	if exitCode != 0 && out != "" {
		return fmt.Sprintf("%s\n[exit code: %d]", out, exitCode), true
	}
	return out, true
}

var (
	reWindowsLeadingCurl = regexp.MustCompile(`(^|[;|]\s*)curl(\s+)`)
	reWindowsExportVar   = regexp.MustCompile(`(?m)^(\s*)export\s+([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|[^\s;]+)`)
)

func normalizeWindowsShellCommand(cmd string) string {
	out := cmd
	out = strings.ReplaceAll(out, "2>/dev/null", "2>$null")
	out = strings.ReplaceAll(out, ">/dev/null", ">$null")
	out = strings.ReplaceAll(out, "/dev/null", "$null")
	out = reWindowsLeadingCurl.ReplaceAllString(out, "${1}curl.exe${2}")
	out = reWindowsExportVar.ReplaceAllString(out, "${1}$$env:${2}=${3}")
	return out
}

func splitShellWords(s string) ([]string, bool) {
	var args []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	hasToken := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSingle {
			if ch == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(ch)
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				inDouble = false
			} else if ch == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
			hasToken = true
		case '"':
			inDouble = true
			hasToken = true
		case ' ', '\t':
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		case '\\':
			if runtime.GOOS != "windows" && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				hasToken = true
			} else {
				cur.WriteByte(ch)
				hasToken = true
			}
		default:
			cur.WriteByte(ch)
			hasToken = true
		}
	}
	if inSingle || inDouble {
		return nil, false
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args, true
}
