// Package mcpserver 把 ARTEX 的能力以 Model Context Protocol 服务器形态对外提供。
//
// 与 mcpbuiltin 相反：mcpbuiltin 是 ARTEX 作为 MCP **客户端**去调用进程内的
// 内置服务器；本包是 **服务端**，讲 JSON-RPC 2.0，向 Codex / OpenCode / Cursor /
// Claude Code / VS Code / Gemini CLI / DSH 这些外部 harness 暴露 tools / resources /
// prompts 三个能力面，让它们把 ARTEX 的侦察、Web 验证、二进制分诊与知识库当成
// 自己的工具使用。
//
// 三条设计约束（与 docs/移除Harness工作台-与ARTEX-MCP对外接入-方案.md 一致）：
//
//  1. 只出「手」不出「刀」：不暴露 shell、文件写入、删除类能力。
//  2. 分档开放：L0 离线分析 / L1 知识库只读 / L2 主动探测 / L3 任务编排，
//     由调用方（server 包）按设置决定放行哪些档位，本包只做过滤。
//  3. 无状态：2026-07-28 规范以无状态为核心，这里不维护会话 id，
//     一次请求即可完成 tools/call，stdio 与 Streamable HTTP 复用同一份实现。
package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ProtocolVersion 是本服务器首选并对外宣称的 MCP 规范版本。
const ProtocolVersion = "2026-07-28"

// supportedProtocols 是能够原样回显的历史版本集合；客户端带其它版本时我们回自己的。
var supportedProtocols = map[string]bool{
	"2026-07-28": true,
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

// Tier 是工具的能力档位。数字越大越接近「会产生副作用」，默认只开前两档。
type Tier int

const (
	// L0 离线分析：只读本地文件，不出网、不碰库。
	L0 Tier = iota
	// L1 知识库只读：查 assets / findings / companies / tasks，不写。
	L1
	// L2 主动探测：出网做 DNS/TCP/TLS/HTTP 验证，只判定不利用。
	L2
	// L3 任务编排：建任务、查进度、暂停，需要进程内 Manager 才注册。
	L3
)

// String 返回档位代号（L0..L3），用于设置项与界面。
func (t Tier) String() string {
	switch t {
	case L0:
		return "L0"
	case L1:
		return "L1"
	case L2:
		return "L2"
	case L3:
		return "L3"
	}
	return "L?"
}

// Label 返回档位的中文名，用于界面展示。
func (t Tier) Label() string {
	switch t {
	case L0:
		return "离线分析"
	case L1:
		return "知识库只读"
	case L2:
		return "主动探测"
	case L3:
		return "任务编排"
	}
	return "未知"
}

// ParseTier 解析档位代号，大小写与空白不敏感。
func ParseTier(s string) (Tier, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "L0":
		return L0, true
	case "L1":
		return L1, true
	case "L2":
		return L2, true
	case "L3":
		return L3, true
	}
	return L0, false
}

// Tool 是一个对外暴露的 MCP 工具。
type Tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	Tier        Tier
	// ReadOnly 会作为 annotations.readOnlyHint 透出，客户端据此决定是否要求确认。
	ReadOnly bool
	// Destructive / Idempotent / OpenWorld 对应 MCP 的另外三个注解位。
	//
	// 为什么必须显式给：Codex 的默认审批模式把「缺省」当作最危险解释
	// （destructiveHint / openWorldHint 缺省 = true），没注解的工具每调一次都要审批；
	// Claude Code 的 plan 模式只自动放行 readOnlyHint=true 的工具。
	// 所以宁可写死，也不要留空。
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
	// Handler 收到模型传来的原始 JSON 对象（可能是 {} 或 null 归一后的 {}）。
	Handler func(ctx context.Context, args json.RawMessage) (string, error)
}

// Resource 是一个只读资源（artex://findings 之类）。
type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

// PromptArgument 是提示模板的一个参数。
type PromptArgument struct {
	Name        string
	Description string
	Required    bool
}

// Prompt 是一个提示模板。
type Prompt struct {
	Name        string
	Title       string
	Description string
	Arguments   []PromptArgument
}

// AuditEvent 是一次工具调用的审计记录。server 包把它写进 tool_usage 账本，
// 与内部控制台调用同一套计量口径。
type AuditEvent struct {
	Tool     string
	Tier     Tier
	Args     json.RawMessage
	Duration time.Duration
	Err      error
	Client   string
	Remote   string
}

// Config 是创建一个 MCP 服务器实例所需的全部输入。
type Config struct {
	Name         string
	Title        string
	Version      string
	Instructions string

	Tools []Tool

	// Tiers 是放行的档位；为空表示只放行 L0。
	Tiers []Tier
	// AllowTools 非空时只放行其中的工具名（与 Tiers 取交）。
	AllowTools []string
	// DenyTools 永远不放行。
	DenyTools []string

	Resources    []Resource
	ReadResource func(ctx context.Context, uri string) (mimeType, text string, err error)

	Prompts   []Prompt
	GetPrompt func(ctx context.Context, name string, args map[string]string) (string, error)

	// ToolTimeout 是单次工具调用的墙钟上限；0 表示不额外设限（由调用方 ctx 决定）。
	ToolTimeout time.Duration

	Audit func(AuditEvent)
}
