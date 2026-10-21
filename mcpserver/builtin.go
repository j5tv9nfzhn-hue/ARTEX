package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/mcpbuiltin"
)

// BuiltinTools 把 mcpbuiltin 的进程内工具投影成对外的 MCP 工具。
//
// 复用而不是重写：同一份实现既服务控制台内部的 agent，也服务外部 harness，
// 行为、截断、错误话术只有一套。差异只在两点：
//   - 档位由调用方指定（bin.go → L0，recon.go/web.go → L2）；
//   - L0 的 bin 工具带 path 参数，投影时套一层 FileGuard。
func BuiltinTools(server string, tier Tier, guard *FileGuard) []Tool {
	src := mcpbuiltin.Tools(server)
	out := make([]Tool, 0, len(src))
	for _, t := range src {
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
			Tier:        tier,
			ReadOnly:    true,
			Handler:     guardHandler(t.Handler, guard),
		})
	}
	return out
}

// guardHandler 在每个调用前校验 path 参数（mcpbuiltin 的样本类工具都用这个字段名）。
func guardHandler(h func(ctx context.Context, args json.RawMessage) (string, error), guard *FileGuard) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		if guard != nil {
			var probe struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &probe); err == nil && probe.Path != "" {
				if err := guard.Check(probe.Path); err != nil {
					return "", fmt.Errorf("拒绝读取: %v", err)
				}
			}
		}
		return h(ctx, raw)
	}
}

// WithReadOnly 把一组工具整体标成只读（L1 查询类工具用）。
func WithReadOnly(tools []Tool) []Tool {
	for i := range tools {
		tools[i].ReadOnly = true
	}
	return tools
}
