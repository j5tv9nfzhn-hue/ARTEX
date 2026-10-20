package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/artex/binlab"
	actool "github.com/Autumn-27/norma/tool"
)

// binary_lab is the built-in, dependency-free analysis surface. It exists so
// that a software_re task never dead-ends when no external provider (IDA,
// Ghidra, REA) is installed. One tool, dispatched by mode, because the tool
// catalog is already large enough to degrade model selection.
//
// S0 implements info / audit / crypto. Later stages add funcs, disasm, xrefs,
// pseudo, structs, diff, dbg and dump behind the same name.
func (t *ToolSet) binaryLabTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "binary_lab",
		Description: "零依赖二进制静态分析工作台（按 mode 分发）：info=容器/架构/节区/导入导出/哈希；" +
			"audit=加壳与反调试分诊评分、停止线；crypto=已知算法常量与未知置换表扫描。" +
			"纯静态只读，不反汇编、不执行、不附加样本。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"enum":        []any{"info", "audit", "crypto"},
				"description": "分析模式；未实现的模式会明确报错而不是静默降级",
			},
			"file_path": map[string]any{
				"type":        "string",
				"description": "样本路径（相对当前工作目录或绝对路径）",
			},
		}, "required": []any{"mode", "file_path"}},
		ReadOnly: noStoreRo, Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Mode     string `json:"mode"`
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.FilePath) == "" {
				return actool.Errorf("file_path is required"), nil
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			switch mode {
			case "info", "audit", "crypto":
			case "":
				return actool.Errorf("mode is required: info|audit|crypto"), nil
			default:
				return actool.Errorf("unsupported mode " + a.Mode + "; implemented in this build: info, audit, crypto"), nil
			}
			if err := ctx.Err(); err != nil {
				return actool.Errorf(err.Error()), nil
			}

			img, err := binlab.Load(t.resolveLocalPath(a.FilePath))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}

			var payload any
			switch mode {
			case "info":
				payload = img
			case "audit":
				payload = img.Analyze()
			case "crypto":
				payload = binlab.Crypto(img)
			}

			out, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(actool.Capture(tc, string(out))), nil
		},
	})
}
