package mcpbuiltin

// 进程内 MCP 服务器：全部用 Go 实现，不拉起子进程、不依赖 Python/Node/外部包。
//
// 为什么自己做而不是包装社区 MCP：
//   1. 分发成本 —— ARTE 是单二进制，装一个 MCP 就要 Node/Python/容器，等于把交付复杂度翻倍；
//   2. 供应链 —— 公开 MCP 服务器里 41% 无认证、36.7% 有 SSRF、43% 的 CVE 是 shell 注入，
//      包装得越多，攻击面越大；
//   3. 可控 —— 每个工具的输入输出、超时、并发、截断都能按我们的证据链来定。
//
// 命名一律用 artex- 前缀，不与任何上游项目同名，避免"仿官方"的歧义。
// 依赖外部引擎（Ghidra/IDA/x64dbg/Frida）的能力不在这里实现，仍走 REProvider 与
// stdio MCP 预设；这里只做"纯 Go 能做完"的那一层。

import (
	"sort"
	"strings"

	"github.com/Autumn-27/norma/mcp"
)

// Command 是内置服务器在 mcp_servers.command 里的标记值。
//
// 为什么不用 transport='builtin'：mcp_servers 表上有
// CHECK (transport IN ('stdio','http','sse'))，改约束要重建表（含外键与触发器），
// 对一个装着用户数据的库来说风险大于收益。于是约定：transport 仍是 stdio
// （语义上就是"本进程、不出网"），command 固定为 artex-builtin，
// connectMCP 在看到这个组合时直接返回进程内客户端，不尝试 exec 任何东西。
const Command = "artex-builtin"

// Server 是一个内置 MCP 服务器的完整定义。
type Server struct {
	Name    string // mcp 服务器名（同时决定工具前缀 mcp__<Name>__）
	Title   string
	Domain  string // pentest | reverse
	Summary string
	Tools   []mcp.InProcessTool
}

// All 返回全部内置服务器。顺序即界面上展示的顺序。
func All() []Server {
	return []Server{reconServer(), webScanServer(), binScanServer()}
}

// Names 返回全部内置服务器名（小写）。
func Names() []string {
	out := make([]string, 0, len(All()))
	for _, s := range All() {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// ByName 按名字取服务器定义，大小写不敏感。
func ByName(name string) (Server, bool) {
	for _, s := range All() {
		if strings.EqualFold(s.Name, strings.TrimSpace(name)) {
			return s, true
		}
	}
	return Server{}, false
}

// Tools 返回某个内置服务器的工具集；未知服务器返回 nil。
func Tools(name string) []mcp.InProcessTool {
	if s, ok := ByName(name); ok {
		return s.Tools
	}
	return nil
}

// IsBuiltin 判断某个 mcp_servers 名字是否由本进程实现。
func IsBuiltin(name string) bool {
	_, ok := ByName(name)
	return ok
}
