package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcpbuiltin"
	"github.com/Autumn-27/artex/mcphttp"
	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

// mcpClient is the shared surface of a connected MCP server (stdio, Streamable HTTP,
// or legacy SSE),
// so tools/list and cleanup are handled uniformly regardless of transport.
type mcpClient interface {
	Tools(context.Context) ([]actool.CoreTool, error)
	Close() error
}

// connectMCP 的四类传输配置错误通过 pgRefreshMCP 的 502 正文显示。
// Agent 组装、启动发现、ScopeSentry 同步和保存后发现只记录或丢弃错误。
// 因此这些验证消息中文化；stdio、http、sse 与 URL 为协议标识，保持原文。
const (
	errMCPStdioNoCommand      = "stdio 传输方式缺少命令"
	errMCPHTTPNoURL           = "http 传输方式缺少 URL"
	errMCPSSENoURL            = "sse 传输方式缺少 URL"
	errMCPUnknownTransportFmt = "未知的传输方式：%q"
)

// connectMCP dials one MCP server per its transport. Callers must Close the client.
func connectMCP(ctx context.Context, m *db.MCPServer) (mcpClient, error) {
	// env/header 值可能带 enc:v1: 密文（计划 #49）。拨号前在唯一出口解成明文；
	// 解不开（主密钥丢失/密文损坏）就明确失败，绝不把密文当明文塞进
	// Authorization 头——那只会得到一个原因离现场很远的 401。
	env, err := db.MCPEnvPlain(m.Env)
	if err != nil {
		return nil, fmt.Errorf("MCP %s：%w", m.Name, err)
	}
	switch m.Transport {
	case "stdio":
		// 内置 Go 服务器：command 是标记值 artex-builtin，工具由本进程提供，
		// 不 exec 任何东西、不依赖 Node/Python。名字必须在内置注册表里，
		// 否则明确报错（避免"看起来连上了、其实没有工具"）。
		if mcpbuiltin.IsBuiltin(m.Name) && (m.Command == mcpbuiltin.Command || m.Command == "") {
			return &builtinMCPClient{name: m.Name}, nil
		}
		if m.Command == "" {
			return nil, errors.New(errMCPStdioNoCommand)
		}
		cmd, args, stdioEnv := prepareStdioMCP(m.Command, jsonStrSlice(m.Args), env)
		return mcp.NewStdioClient(ctx, m.Name, cmd, stdioEnv, args...)
	case "http":
		if m.URL == "" {
			return nil, errors.New(errMCPHTTPNoURL)
		}
		// env map doubles as HTTP headers (e.g. Authorization).
		return mcphttp.New(ctx, m.Name, m.URL, env, m.Insecure)
	case "sse":
		if m.URL == "" {
			return nil, errors.New(errMCPSSENoURL)
		}
		return mcphttp.NewSSE(ctx, m.Name, m.URL, env, m.Insecure)
	case "builtin":
		// 进程内服务器（mcpbuiltin）：不出网、不启子进程、无外部运行时依赖。
		if !mcpbuiltin.IsBuiltin(m.Name) {
			return nil, fmt.Errorf("未知的内置 MCP 服务器：%q", m.Name)
		}
		return &builtinMCPClient{name: m.Name}, nil
	default:
		return nil, fmt.Errorf(trMsg(errMCPUnknownTransportFmt), m.Transport)
	}
}

// prepareStdioMCP normalizes the stdio command, arguments and environment across
// Windows / macOS / Linux so packaged single-binary builds can reliably spawn
// Node/npx-based MCP servers (such as the built-in Playwright browser MCP)
// without hanging on interactive prompts or missing PATH entries.
func prepareStdioMCP(command string, args []string, env map[string]string) (string, []string, map[string]string) {
	outEnv := make(map[string]string, len(env)+4)
	for k, v := range env {
		outEnv[k] = v
	}
	trimmedCmd := strings.TrimSpace(command)
	baseLower := strings.ToLower(filepath.Base(trimmedCmd))
	isNPX := baseLower == "npx" || baseLower == "npx.cmd" || baseLower == "npx.exe"

	outArgs := append([]string(nil), args...)
	if isNPX {
		hasYes := false
		for _, a := range outArgs {
			if a == "-y" || a == "--yes" {
				hasYes = true
				break
			}
		}
		if !hasYes && len(outArgs) > 0 {
			outArgs = append([]string{"-y"}, outArgs...)
		}
		if _, ok := outEnv["npm_config_yes"]; !ok {
			outEnv["npm_config_yes"] = "true"
		}
		if _, ok := outEnv["NPM_CONFIG_YES"]; !ok {
			outEnv["NPM_CONFIG_YES"] = "true"
		}
	}

	resolvedCmd, extraDirs := resolveStdioCommandPath(trimmedCmd)
	if len(extraDirs) > 0 {
		pathSep := string(os.PathListSeparator)
		curPath := outEnv["PATH"]
		if curPath == "" {
			curPath = outEnv["Path"]
		}
		if curPath == "" {
			curPath = os.Getenv("PATH")
		}
		prefix := strings.Join(extraDirs, pathSep)
		if curPath != "" {
			curPath = prefix + pathSep + curPath
		} else {
			curPath = prefix
		}
		outEnv["PATH"] = curPath
		if runtime.GOOS == "windows" {
			outEnv["Path"] = curPath
		}
	}
	return resolvedCmd, outArgs, outEnv
}

func resolveStdioCommandPath(command string) (string, []string) {
	if command == "" {
		return command, nil
	}
	if p, err := exec.LookPath(command); err == nil && p != "" {
		return p, []string{filepath.Dir(p)}
	}
	if filepath.IsAbs(command) {
		return command, nil
	}

	var candidateDirs []string
	if runtime.GOOS == "windows" {
		for _, envKey := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA", "APPDATA", "NVM_SYMLINK"} {
			val := strings.TrimSpace(os.Getenv(envKey))
			if val == "" {
				continue
			}
			switch envKey {
			case "ProgramFiles", "ProgramFiles(x86)":
				candidateDirs = append(candidateDirs, filepath.Join(val, "nodejs"))
			case "LOCALAPPDATA":
				candidateDirs = append(candidateDirs, filepath.Join(val, "Programs", "nodejs"), filepath.Join(val, "fnm_multishells"))
			case "APPDATA":
				candidateDirs = append(candidateDirs, filepath.Join(val, "npm"))
			case "NVM_SYMLINK":
				candidateDirs = append(candidateDirs, val)
			}
		}
	} else {
		candidateDirs = append(candidateDirs, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin")
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidateDirs = append(candidateDirs,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, ".nvm", "current", "bin"),
				filepath.Join(home, ".volta", "bin"),
			)
		}
	}

	exts := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(command) == "" {
		exts = []string{".cmd", ".exe", ".bat", ""}
	}

	var foundPath string
	var extraDirs []string
	seenDir := map[string]bool{}
	for _, dir := range candidateDirs {
		if dir == "" || seenDir[dir] {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			seenDir[dir] = true
			extraDirs = append(extraDirs, dir)
		}
		if foundPath == "" {
			for _, ext := range exts {
				cand := filepath.Join(dir, command+ext)
				if st, err := os.Stat(cand); err == nil && !st.IsDir() {
					foundPath = cand
					break
				}
			}
		}
	}
	if foundPath != "" {
		return foundPath, extraDirs
	}
	return command, extraDirs
}

// discoverAndCacheMCP connects to one MCP, lists its tools, and persists the tool
// names to mcp_tools_cache so the UI shows them without a live connection.
func (s *Server) discoverAndCacheMCP(ctx context.Context, m *db.MCPServer) error {
	managed, err := s.m.pg.REMCPManaged(m.ID)
	if err != nil {
		return err
	}
	if managed {
		return errors.New("逆向受管 MCP 请从软件分析工作台发现，避免抢占分析会话")
	}

	cl, err := connectMCP(ctx, m)
	if err != nil {
		return err
	}
	defer cl.Close()
	ts, err := cl.Tools(ctx)
	if err != nil {
		return err
	}
	tools := make([]db.MCPTool, 0, len(ts))
	for _, t := range ts {
		tools = append(tools, db.MCPTool{Name: t.Name(), Description: t.Description()})
	}
	if err := s.m.pg.SaveMCPTools(m.ID, tools); err != nil {
		return err
	}
	log.Printf(logT("[mcp] 从 %s 发现并缓存了 %d 个工具"), m.Name, len(tools))
	return nil
}

// discoverEmptyMCPsOnStartup fills the tool cache for any enabled MCP that has none
// yet (notably the seeded browser MCP on first run). Runs sequentially in one
// goroutine so we never spawn many stdio servers (npx) at once, and never blocks
// startup. Best-effort: a failure leaves the cache empty to retry next start.
func (s *Server) discoverEmptyMCPsOnStartup() {
	servers, err := s.m.pg.ListMCP()
	if err != nil {
		log.Printf(logT("[mcp] 启动时自动发现：读取列表失败：%v"), err)
		return
	}
	for _, m := range servers {
		managed, managedErr := s.m.pg.REMCPManaged(m.ID)
		if managedErr != nil || managed {
			continue
		}
		if !m.Enabled || len(m.Tools) > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
		if err := s.discoverAndCacheMCP(ctx, m); err != nil {
			log.Printf(logT("[mcp] 启动时自动发现 %s 失败：%v"), m.Name, err)
		}
		cancel()
	}
}
