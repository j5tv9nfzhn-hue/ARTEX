package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/artex/mcpserver"
)

// 能力注册表：一个能力，一份实现，两个投影。
//
// 起因是 Codex 的「单内核、多入口」：内核只写一遍，入口按信任等级分层。
// ARTEX 之前是两份真相——HTTP 一份 handler、MCP 一份 mcpXxx 适配器，
// 两边各自 decode 参数、各自 shaping 输出，改一边另一边静默漂移。
//
// 这个注册表是第一步（S2）：先把「哪些能力存在、各自属于哪一档、暴露不暴露」
// 收敛成一张表，MCP 投影从表里生成。HTTP 投影暂时保持原样（S5 才逐步接过来），
// 因为「行为零变化」比「结构漂亮」重要。
//
// 两条硬规则：
//  1. Expose 零值是 false —— 没有显式声明暴露的能力，永远不会出现在 tools/list。
//  2. 禁区能力不是「记得别写」，而是压根不进注册表（见 deniedCapabilities）。

// defaultMaxResultChars 是单次工具结果的软上限。
//
// 为什么是 40,000：Claude Code 默认 25,000 token 硬顶、纯文本超过 50,000 字符会落盘，
// 而中文字符密度高、JSON 又带转义，40k 字符是个保守的中间值。
// 超过就落盘 + 头尾预览，模型还能自己用 L0 工具去读全文。
const defaultMaxResultChars = 40000

// Capability 是注册表里的一行。
type Capability struct {
	ID    string         // 稳定标识，形如 "mcp.list_assets"
	Title string         // 人类可读名字
	Tier  mcpserver.Tier // 档位
	// Expose 决定它进不进 AI 投影。零值 false = 不暴露（fail-closed）。
	Expose bool
	Tool   mcpserver.Tool // 唯一实现（含 schema 与 handler）
}

// CapabilityManifest 是给 `GET /api/capabilities` 与前端对账用的视图。
type CapabilityManifest struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Tier        string `json:"tier"`
	MCPName     string `json:"mcp_name,omitempty"`
	Exposed     bool   `json:"exposed"`
	ReadOnly    bool   `json:"read_only"`
	Destructive bool   `json:"destructive"`
	Idempotent  bool   `json:"idempotent"`
	OpenWorld   bool   `json:"open_world"`
	Reason      string `json:"reason,omitempty"`
}

// Registry 按插入顺序保存能力；顺序即 tools/list 的顺序。
type Registry struct {
	caps []Capability
}

func NewRegistry() *Registry { return &Registry{caps: make([]Capability, 0, 32)} }

// Add 追加一个能力，并给它套上结果上限的装饰。
//
// 装饰放在注册表这一层，是为了 L0/L1/L2/L3 四条来源共用同一条输出纪律：
// 任何一个工具都不该把 25 万字符直接怼给宿主。
func (r *Registry) Add(c Capability) {
	if c.Tool.Handler != nil {
		c.Tool.Handler = capResult(c.Tool.Handler, defaultMaxResultChars)
	}
	r.caps = append(r.caps, c)
}

// Tools 生成 MCP 投影：只要 Expose 且档位不超过 maxTier。
func (r *Registry) Tools(maxTier mcpserver.Tier) []mcpserver.Tool {
	out := make([]mcpserver.Tool, 0, len(r.caps))
	for _, c := range r.caps {
		if !c.Expose || c.Tier > maxTier {
			continue
		}
		out = append(out, c.Tool)
	}
	return out
}

// Exposed 生成 manifest 视图（含禁区条目，方便前端解释「为什么没有那个接口」）。
func (r *Registry) Manifest() []CapabilityManifest {
	out := make([]CapabilityManifest, 0, len(r.caps))
	for _, c := range r.caps {
		out = append(out, CapabilityManifest{
			ID:          c.ID,
			Title:       firstNonEmpty(c.Title, c.Tool.Title, c.Tool.Name),
			Tier:        c.Tier.String(),
			MCPName:     c.Tool.Name,
			Exposed:     c.Expose,
			ReadOnly:    c.Tool.ReadOnly,
			Destructive: c.Tool.Destructive,
			Idempotent:  c.Tool.Idempotent || c.Tool.ReadOnly,
			OpenWorld:   c.Tool.OpenWorld,
		})
	}
	return out
}

// Denied 返回永不可暴露的能力清单（只用于展示与审计，不参与 tools/list）。
func Denied() []CapabilityManifest { return append([]CapabilityManifest(nil), deniedCapabilities...) }

// deniedCapabilities 是「结构上进不去」的那一类。
//
// 它们在代码里本来就没有对应的工具实现，列在这里是为了让前端/审计能回答
// 「外部 AI 为什么调不到 rotate token」这个问题——答案是「没有这个工具」，
// 而不是「有个工具但是被拦了」。
var deniedCapabilities = []CapabilityManifest{
	{ID: "mcp.token.rotate", Title: "轮换 MCP token", Reason: "改自己的暴露面 = 提权"},
	{ID: "mcp.tiers.set", Title: "修改暴露档位", Reason: "同上"},
	{ID: "llm.config", Title: "LLM 供应商与 Key", Reason: "直接读密钥"},
	{ID: "intercept.rule", Title: "拦截与审批规则", Reason: "关掉自己的审批"},
	{ID: "task.delete", Title: "删除任务", Reason: "销毁证据"},
	{ID: "task.archive.delete", Title: "删除归档", Reason: "销毁证据"},
	{ID: "retention.execute", Title: "执行保留策略", Reason: "批量删数据"},
	{ID: "update.apply", Title: "安装更新 / 回滚", Reason: "换掉二进制"},
	{ID: "shell", Title: "任意命令执行", Reason: "等于把主机交出去"},
	{ID: "file.write", Title: "任意文件写入", Reason: "同上"},
}

// buildRegistry 按固定顺序装配全部能力。
//
// 顺序必须与 mcpBuild 历史行为一致：L0 内置 → L1 → L2(recon) → L2(webscan) → L3。
// tools/list 的顺序会出现在客户端的 UI 里，也会进 golden 测试，别随手调。
func buildRegistry(owner *Server, guard *mcpserver.FileGuard) *Registry {
	r := NewRegistry()
	add := func(tools []mcpserver.Tool) {
		for _, t := range tools {
			r.Add(Capability{
				ID:     "mcp." + t.Name,
				Title:  firstNonEmpty(t.Title, t.Name),
				Tier:   t.Tier,
				Expose: true,
				Tool:   t,
			})
		}
	}
	add(mcpserver.BuiltinTools("artex-binscan", mcpserver.L0, guard))
	add(owner.mcpL1Tools())
	add(mcpserver.BuiltinTools("artex-recon", mcpserver.L2, nil))
	add(mcpserver.BuiltinTools("artex-webscan", mcpserver.L2, nil))
	add(owner.mcpL3Tools())
	return r
}

// mcpHostHints 是追加到 instructions 末尾的宿主约束提示。
//
// 每个宿主都强加了超时与输出上限（Codex 工具 60 s、Claude Code 25k token），
// 写在 instructions 里，比埋在某个工具的 description 里更容易被读进去。
func mcpHostHints() string {
	return strings.Join([]string{
		"",
		"【宿主约束提示】",
		"- 单次工具调用请控制在 50 秒内：需要更久时缩小范围或调小 timeout_ms，不要硬扛。",
		"- 长任务用 task_create 拿句柄再 task_status 轮询，不要用一次 tools/call 等到底。",
		"- 结果超过 4 万字符会自动落盘并只回摘要与路径，需要全文时再用 L0 工具去读。",
	}, "\n")
}

// capResult 把超大结果落盘，只回「头 + 尾 + 路径」。
//
// 不做硬截断：硬截断会让模型以为结果就这么多，然后给出错误结论。
// 落盘 + 明说「中间省略了多少」才不会误导。
func capResult(h func(context.Context, json.RawMessage) (string, error), limit int) func(context.Context, json.RawMessage) (string, error) {
	if limit <= 0 {
		limit = defaultMaxResultChars
	}
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		out, err := h(ctx, raw)
		if err != nil {
			return out, err
		}
		runes := []rune(out)
		if len(runes) <= limit {
			return out, nil
		}
		dir := filepath.Join(os.TempDir(), "artex-mcp-out")
		name := fmt.Sprintf("result-%d.txt", time.Now().UnixNano())
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return spillFailed(out, limit), nil
		}
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			return spillFailed(out, limit), nil
		}
		head := string(runes[:limit/2])
		tail := string(runes[len(runes)-limit/2:])
		return fmt.Sprintf("%s\n\n…（中间省略 %d 字符；完整结果已落盘：%s）\n\n%s",
			head, len(runes)-limit, path, tail), nil
	}
}

// spillFailed 是落盘失败时的兜底：仍然明确说「被截断了」，绝不假装完整。
func spillFailed(out string, limit int) string {
	runes := []rune(out)
	return string(runes[:limit]) + fmt.Sprintf("\n\n…（输出共 %d 字符，超过 %d 上限且落盘失败，此处为硬截断）", len(runes), limit)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
