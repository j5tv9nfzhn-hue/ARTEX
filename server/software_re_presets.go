package server

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
)

// REEnginePreset describes an operator-reviewable static contract template for a
// specific reverse-engineering MCP engine family (Ghidra, IDA Pro, or REA).
// Loading a preset never marks ReviewedStatic=true automatically: the operator
// must still discover the live MCP catalog and review the mapping before opening
// a session.
type REEnginePreset struct {
	Kind        string              `json:"kind"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Config      db.REProviderConfig `json:"config"`
}

// RECapabilityReport summarizes the live readiness and capability gate of a
// configured REProvider against its discovered MCP tool catalog.
type RECapabilityReport struct {
	ProviderID         int64    `json:"provider_id"`
	Kind               string   `json:"kind"`
	Mode               string   `json:"mode"`
	ReviewedStatic     bool     `json:"reviewed_static"`
	Discovery          string   `json:"discovery"`
	CatalogCount       int      `json:"catalog_count"`
	CoreReady          bool     `json:"core_ready"`
	GateReason         string   `json:"gate_reason"`
	ReadyOperations    []string `json:"ready_operations"`
	MissingTools       []string `json:"missing_tools,omitempty"`
	SchemaMismatches   []string `json:"schema_mismatches,omitempty"`
	UnmappedOperations []string `json:"unmapped_operations,omitempty"`
}

var reAllStaticOperations = []string{
	"status",
	"functions",
	"decompile",
	"xrefs",
	"callgraph",
	"types",
	"modules",
	"imports",
	"exports",
	"strings",
}

func reEnginePresets() []REEnginePreset {
	return []REEnginePreset{
		{
			Kind:        "ghidra",
			Title:       "Ghidra MCP 标准只读静态契约",
			Description: "适用于本地 stdio Ghidra Headless/GUI MCP 服务：支持二进制导入/打开、函数表、反编译伪代码、交叉引用、调用图、类型结构、多模块/段区、导入导出表与字符串索引。",
			Config: db.REProviderConfig{
				Mode:           "open",
				ReviewedStatic: false,
				SessionKeyPath: "session_id",
				Operations: map[string]db.REOperation{
					"open": {
						Tool:  "ghidra_open_program",
						Input: map[string]any{"file_path": "{{sample_path}}", "sha256": "{{sample_sha}}", "read_only": true},
					},
					"close": {
						Tool:  "ghidra_close_program",
						Input: map[string]any{"session_id": "{{provider_session}}", "sha256": "{{sample_sha}}"},
					},
					"status": {
						Tool:  "ghidra_program_info",
						Input: map[string]any{"session_id": "{{provider_session}}"},
					},
					"functions": {
						Tool:        "ghidra_list_functions",
						Input:       map[string]any{"session_id": "{{provider_session}}", "filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "functions",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "entry_point", "module": "program"},
					},
					"decompile": {
						Tool:        "ghidra_decompile_function",
						Input:       map[string]any{"session_id": "{{provider_session}}", "address": "{{address}}", "name": "{{query}}"},
						AddressKind: "va",
					},
					"xrefs": {
						Tool:        "ghidra_list_xrefs",
						Input:       map[string]any{"session_id": "{{provider_session}}", "address": "{{address}}", "limit": "{{limit}}"},
						RowsPath:    "xrefs",
						AddressKind: "va",
						Columns:     map[string]string{"name": "type", "address": "from_address", "source": "from_address", "target": "to_address", "module": "program"},
					},
					"callgraph": {
						Tool:        "ghidra_function_callgraph",
						Input:       map[string]any{"session_id": "{{provider_session}}", "address": "{{address}}", "limit": "{{limit}}"},
						RowsPath:    "edges",
						AddressKind: "va",
						Columns:     map[string]string{"name": "callee_name", "address": "caller", "source": "caller", "target": "callee", "module": "program"},
					},
					"types": {
						Tool:     "ghidra_list_data_types",
						Input:    map[string]any{"session_id": "{{provider_session}}", "filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath: "types",
						Columns:  map[string]string{"name": "name", "module": "category"},
					},
					"modules": {
						Tool:        "ghidra_list_memory_blocks",
						Input:       map[string]any{"session_id": "{{provider_session}}", "limit": "{{limit}}"},
						RowsPath:    "blocks",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "start", "module": "program"},
					},
					"imports": {
						Tool:        "ghidra_list_imports",
						Input:       map[string]any{"session_id": "{{provider_session}}", "filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "imports",
						AddressKind: "va",
						Columns:     map[string]string{"name": "symbol", "address": "address", "module": "library"},
					},
					"exports": {
						Tool:        "ghidra_list_exports",
						Input:       map[string]any{"session_id": "{{provider_session}}", "filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "exports",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "address", "module": "program"},
					},
					"strings": {
						Tool:        "ghidra_list_strings",
						Input:       map[string]any{"session_id": "{{provider_session}}", "filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "strings",
						AddressKind: "va",
						Columns:     map[string]string{"name": "value", "address": "address", "module": "program"},
					},
				},
			},
		},
		{
			Kind:        "ida",
			Title:       "IDA Pro / Hex-Rays MCP 标准只读静态契约",
			Description: "适用于已打开样本 IDB 的 IDA MCP 服务（默认 attach 模式，通过输入文件 SHA-256 强制核验身份）：支持 Hex-Rays 反编译、函数与段枚举、交叉引用、调用链、导入导出与字符串查询。",
			Config: db.REProviderConfig{
				Mode:             "attach",
				ReviewedStatic:   false,
				IdentityHashPath: "input_file_sha256",
				Operations: map[string]db.REOperation{
					"identity": {
						Tool:  "ida_get_metadata",
						Input: map[string]any{},
					},
					"status": {
						Tool:  "ida_get_metadata",
						Input: map[string]any{},
					},
					"functions": {
						Tool:        "ida_list_functions",
						Input:       map[string]any{"filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "functions",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "ea", "module": "segment"},
					},
					"decompile": {
						Tool:        "ida_decompile_function",
						Input:       map[string]any{"address": "{{address}}", "name": "{{query}}"},
						AddressKind: "va",
					},
					"xrefs": {
						Tool:        "ida_list_xrefs",
						Input:       map[string]any{"address": "{{address}}", "limit": "{{limit}}"},
						RowsPath:    "xrefs",
						AddressKind: "va",
						Columns:     map[string]string{"name": "type", "address": "from", "source": "from", "target": "to", "module": "segment"},
					},
					"callgraph": {
						Tool:        "ida_get_callgraph",
						Input:       map[string]any{"address": "{{address}}", "limit": "{{limit}}"},
						RowsPath:    "edges",
						AddressKind: "va",
						Columns:     map[string]string{"name": "callee_name", "address": "caller", "source": "caller", "target": "callee", "module": "segment"},
					},
					"types": {
						Tool:     "ida_list_local_types",
						Input:    map[string]any{"filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath: "types",
						Columns:  map[string]string{"name": "name", "module": "ordinal"},
					},
					"modules": {
						Tool:        "ida_list_segments",
						Input:       map[string]any{"limit": "{{limit}}"},
						RowsPath:    "segments",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "start_ea", "module": "class"},
					},
					"imports": {
						Tool:        "ida_list_imports",
						Input:       map[string]any{"filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "imports",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "ea", "module": "module"},
					},
					"exports": {
						Tool:        "ida_list_exports",
						Input:       map[string]any{"filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "exports",
						AddressKind: "va",
						Columns:     map[string]string{"name": "name", "address": "ea", "module": "ordinal"},
					},
					"strings": {
						Tool:        "ida_list_strings",
						Input:       map[string]any{"filter": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "strings",
						AddressKind: "va",
						Columns:     map[string]string{"name": "value", "address": "ea", "module": "type"},
					},
				},
			},
		},
		{
			Kind:        "rea",
			Title:       "REA 逆向分析助手 MCP 标准只读静态契约",
			Description: "适用于 REA (Reverse Engineering Assistant) 本地 stdio MCP 服务：支持多架构二进制静态会话打开/关闭、函数与反编译、调用关系、模块/导入导出与字符串查询。",
			Config: db.REProviderConfig{
				Mode:           "open",
				ReviewedStatic: false,
				SessionKeyPath: "session_id",
				Operations: map[string]db.REOperation{
					"open": {
						Tool:  "rea_open_sample",
						Input: map[string]any{"path": "{{sample_path}}", "sha256": "{{sample_sha}}"},
					},
					"close": {
						Tool:  "rea_close_sample",
						Input: map[string]any{"session_id": "{{provider_session}}", "sha256": "{{sample_sha}}"},
					},
					"status": {
						Tool:  "rea_session_status",
						Input: map[string]any{"session_id": "{{provider_session}}"},
					},
					"functions": {
						Tool:        "rea_list_functions",
						Input:       map[string]any{"session_id": "{{provider_session}}", "query": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "items",
						AddressKind: "rva",
						Columns:     map[string]string{"name": "name", "address": "rva", "module": "module"},
					},
					"decompile": {
						Tool:        "rea_decompile",
						Input:       map[string]any{"session_id": "{{provider_session}}", "address": "{{address}}", "symbol": "{{query}}"},
						AddressKind: "rva",
					},
					"xrefs": {
						Tool:        "rea_list_xrefs",
						Input:       map[string]any{"session_id": "{{provider_session}}", "address": "{{address}}", "limit": "{{limit}}"},
						RowsPath:    "items",
						AddressKind: "rva",
						Columns:     map[string]string{"name": "kind", "address": "from", "source": "from", "target": "to", "module": "module"},
					},
					"callgraph": {
						Tool:        "rea_callgraph",
						Input:       map[string]any{"session_id": "{{provider_session}}", "address": "{{address}}", "limit": "{{limit}}"},
						RowsPath:    "edges",
						AddressKind: "rva",
						Columns:     map[string]string{"name": "label", "address": "caller", "source": "caller", "target": "callee", "module": "module"},
					},
					"types": {
						Tool:     "rea_list_types",
						Input:    map[string]any{"session_id": "{{provider_session}}", "query": "{{query}}", "limit": "{{limit}}"},
						RowsPath: "items",
						Columns:  map[string]string{"name": "name", "module": "module"},
					},
					"modules": {
						Tool:        "rea_list_modules",
						Input:       map[string]any{"session_id": "{{provider_session}}", "limit": "{{limit}}"},
						RowsPath:    "items",
						AddressKind: "rva",
						Columns:     map[string]string{"name": "name", "address": "base", "module": "name"},
					},
					"imports": {
						Tool:        "rea_list_imports",
						Input:       map[string]any{"session_id": "{{provider_session}}", "query": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "items",
						AddressKind: "rva",
						Columns:     map[string]string{"name": "symbol", "address": "rva", "module": "library"},
					},
					"exports": {
						Tool:        "rea_list_exports",
						Input:       map[string]any{"session_id": "{{provider_session}}", "query": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "items",
						AddressKind: "rva",
						Columns:     map[string]string{"name": "symbol", "address": "rva", "module": "module"},
					},
					"strings": {
						Tool:        "rea_list_strings",
						Input:       map[string]any{"session_id": "{{provider_session}}", "query": "{{query}}", "limit": "{{limit}}"},
						RowsPath:    "items",
						AddressKind: "file_offset",
						Columns:     map[string]string{"name": "value", "address": "offset", "module": "section"},
					},
				},
			},
		},
	}
}

// reEvaluateCapabilities inspects a provider's configured operation mappings
// against its discovered MCP catalog (tool names and required input properties)
// without claiming that catalog presence alone proves target binary compatibility.
func reEvaluateCapabilities(p db.REProvider) RECapabilityReport {
	var cat []reCatalogTool
	_ = json.Unmarshal(p.Catalog, &cat)
	byName := make(map[string]reCatalogTool, len(cat))
	for _, t := range cat {
		byName[t.Name] = t
	}

	report := RECapabilityReport{
		ProviderID:         p.ID,
		Kind:               p.Kind,
		Mode:               p.Config.Mode,
		ReviewedStatic:     p.Config.ReviewedStatic,
		Discovery:          p.Discovery,
		CatalogCount:       len(cat),
		ReadyOperations:    []string{},
		UnmappedOperations: []string{},
	}

	opNames := make([]string, 0, len(p.Config.Operations))
	for name := range p.Config.Operations {
		opNames = append(opNames, name)
	}
	sort.Strings(opNames)

	for _, name := range opNames {
		op := p.Config.Operations[name]
		if op.Tool == "" {
			continue
		}
		ct, ok := byName[op.Tool]
		if !ok {
			report.MissingTools = append(report.MissingTools, fmt.Sprintf("%s -> %s", name, op.Tool))
			continue
		}
		mismatch := false
		if req, ok := ct.Schema["required"].([]any); ok {
			for _, key := range req {
				if k, ok := key.(string); ok && k != "" {
					if _, exists := op.Input[k]; !exists {
						report.SchemaMismatches = append(report.SchemaMismatches, fmt.Sprintf("%s(%s): 缺少必填参数 %s", name, op.Tool, k))
						mismatch = true
					}
				}
			}
		}
		if !mismatch {
			report.ReadyOperations = append(report.ReadyOperations, name)
		}
	}

	for _, opName := range reAllStaticOperations {
		if _, ok := p.Config.Operations[opName]; !ok {
			report.UnmappedOperations = append(report.UnmappedOperations, opName)
		}
	}

	readySet := make(map[string]bool, len(report.ReadyOperations))
	for _, name := range report.ReadyOperations {
		readySet[name] = true
	}

	hasLifecycle := false
	if p.Config.Mode == "open" {
		hasLifecycle = readySet["open"] && readySet["close"]
	} else if p.Config.Mode == "attach" {
		hasLifecycle = readySet["identity"] && p.Config.IdentityHashPath != ""
	}
	hasCoreAnalysis := readySet["functions"] || readySet["decompile"] || readySet["xrefs"] || readySet["modules"]

	switch {
	case !p.Config.ReviewedStatic:
		report.GateReason = "尚未勾选人工静态审核（reviewed_static=false）"
	case p.Discovery != "catalog_verified":
		report.GateReason = "尚未完成工具目录发现与校验（需执行 discover 并通过校验）"
	case len(report.MissingTools) > 0:
		report.GateReason = "存在未出现在实际 MCP 目录中的映射工具：" + report.MissingTools[0]
	case len(report.SchemaMismatches) > 0:
		report.GateReason = "操作映射缺少 MCP Schema 必填参数：" + report.SchemaMismatches[0]
	case !hasLifecycle:
		if p.Config.Mode == "open" {
			report.GateReason = "open 模式必须同时就绪 open 与 close 生命周期工具"
		} else {
			report.GateReason = "attach 模式必须就绪 identity 工具并配置 identity_hash_path"
		}
	case !hasCoreAnalysis:
		report.GateReason = "至少需就绪一项核心静态分析操作（functions / decompile / xrefs / modules）"
	default:
		report.CoreReady = true
		report.GateReason = "静态契约与目录 schema 门控已就绪；实际样本架构支持仍以会话打开时引擎反馈为准"
	}
	return report
}
