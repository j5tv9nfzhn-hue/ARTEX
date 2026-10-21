package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/mcpbuiltin"
)

// builtinMCPStaticTools 定义内置 MCP 服务器（ScopeSentry / rea / wpe64）的标准工具集。
//
// 为什么在数据库初始化时预置到 mcp_tools_cache：
//  1. ScopeSentry（HTTP）等服务默认以 enabled=false 播种，
//     启动时的 discoverEmptyMCPsOnStartup 仅对已启用服务器发起实时连接；
//  2. 若不预置工具缓存，前端 /system/mcp 页面在未启用或离线打包分发状态下会一直显示
//     「尚未发现工具」，用户无法在启用/授权前预览内置 MCP 包含哪些工具；
//  3. 预置仅在对应服务器的 mcp_tools_cache 为空时幂等写入；一旦用户配置并启用服务或
//     点击「刷新」完成实时发现，SaveMCPTools 会用远端实际返回的列表覆盖本缓存。
var builtinScopeSentryTools = []MCPTool{
	{Name: "mcp__ScopeSentry__list_projects", Description: "按标签分组获取 ScopeSentry 项目树（含项目 ObjectID）"},
	{Name: "mcp__ScopeSentry__list_projects_data", Description: "分页查询 ScopeSentry 项目列表，支持按名称搜索"},
	{Name: "mcp__ScopeSentry__get_project", Description: "获取指定 ScopeSentry 项目的详细配置与范围信息"},
	{Name: "mcp__ScopeSentry__create_project", Description: "在 ScopeSentry 中新建资产测绘项目"},
	{Name: "mcp__ScopeSentry__list_tasks", Description: "分页查询 ScopeSentry 扫描任务列表与运行状态"},
	{Name: "mcp__ScopeSentry__get_task", Description: "获取指定 ScopeSentry 扫描任务的详情与进度"},
	{Name: "mcp__ScopeSentry__list_scan_templates", Description: "列出 ScopeSentry 可用的扫描流水线模板"},
	{Name: "mcp__ScopeSentry__get_scan_template", Description: "获取指定扫描模板的模块与插件配置详情"},
	{Name: "mcp__ScopeSentry__list_plugin_modules", Description: "列出 ScopeSentry 扫描流水线支持的模块名称"},
	{Name: "mcp__ScopeSentry__list_plugins", Description: "查询可用扫描插件列表（含 hash 与默认参数）"},
	{Name: "mcp__ScopeSentry__create_scan_template", Description: "按模块与插件组合创建新的 ScopeSentry 扫描模板"},
	{Name: "mcp__ScopeSentry__create_scan_task", Description: "指定目标、节点与模板下发 ScopeSentry 扫描任务"},
	{Name: "mcp__ScopeSentry__list_assets", Description: "按类型、DSL 搜索表达式与项目过滤分页查询 ScopeSentry 资产库"},
	{Name: "mcp__ScopeSentry__count_assets", Description: "统计满足 DSL 与过滤条件的 ScopeSentry 资产总数"},
	{Name: "mcp__ScopeSentry__get_asset_detail", Description: "获取单条 ScopeSentry 资产或漏洞记录的完整详情"},
	{Name: "mcp__ScopeSentry__add_asset_tag", Description: "为指定 ScopeSentry 资产添加分类标签"},
	{Name: "mcp__ScopeSentry__list_nodes", Description: "列出 ScopeSentry 在线扫描节点及其负载状态"},
}

// builtinReaMCPTools 预置 REA (Reverse Engineering Assistant) 引擎 MCP 的标准
// 工具集，命名与 server/software_re_presets.go 的 rea 预设契约一致，便于启用
// 前在 /system/mcp 预览，并作为 software_re REProvider 的绑定候选。
var builtinReaMCPTools = []MCPTool{
	{Name: "mcp__rea__rea_open_sample", Description: "打开样本并创建静态分析会话（返回 session_id）"},
	{Name: "mcp__rea__rea_close_sample", Description: "关闭样本会话并释放引擎侧资源"},
	{Name: "mcp__rea__rea_session_status", Description: "查询 REA 会话状态、架构与加载模块摘要"},
	{Name: "mcp__rea__rea_list_functions", Description: "按关键词分页列出函数（RVA、名称、所属模块）"},
	{Name: "mcp__rea__rea_decompile", Description: "按 RVA 或符号反编译函数伪代码"},
	{Name: "mcp__rea__rea_list_xrefs", Description: "列出地址的交叉引用（from/to 与引用类型）"},
	{Name: "mcp__rea__rea_callgraph", Description: "导出函数调用图边（caller/callee）"},
	{Name: "mcp__rea__rea_list_types", Description: "列出类型/结构体定义（名称、模块）"},
	{Name: "mcp__rea__rea_list_modules", Description: "列出多模块清单（模块名、基址）"},
	{Name: "mcp__rea__rea_list_imports", Description: "列出导入表（符号、RVA、来源库）"},
	{Name: "mcp__rea__rea_list_exports", Description: "列出导出表（符号、RVA、模块）"},
	{Name: "mcp__rea__rea_list_strings", Description: "按关键词列出字符串索引（值、file_offset、区段）"},
}

// builtinWPE64MCPTools 预置 WPE64 封包实验 MCP 的标准工具集（抓包/改包/重放/
// 规则）。WPE64 与 REA 内置 mitmproxy 流量记录代理协同：记录代理负责 HTTP(S)
// 解密取证，WPE64 负责 Winsock 层截获、编辑与重放，见 skills/wpe64-mcp 与
// skills/mitmproxy-traffic。启用前仅作工具目录预览；实际工具以启用后实时发现为准。
var builtinWPE64MCPTools = []MCPTool{
	{Name: "mcp__wpe64__status", Description: "查询 WPE64 引擎状态、驱动可用性与当前抓包/规则计数"},
	{Name: "mcp__wpe64__list_adapters", Description: "枚举网卡、进程与套接字（进程名、PID、五元组）"},
	{Name: "mcp__wpe64__start_capture", Description: "开始抓包（进程/端口/协议/方向/截断长度过滤）"},
	{Name: "mcp__wpe64__stop_capture", Description: "停止抓包并固化 trace（返回 trace 标识）"},
	{Name: "mcp__wpe64__list_packets", Description: "分页列出数据包（序号、时间、方向、五元组、长度）"},
	{Name: "mcp__wpe64__get_packet", Description: "读取单包十六进制与解析字段（头/载荷）"},
	{Name: "mcp__wpe64__edit_packet", Description: "编辑待发包（十六进制补丁或字段替换，长度校验）"},
	{Name: "mcp__wpe64__send_packet", Description: "单包注入发送（须显式授权目标）"},
	{Name: "mcp__wpe64__replay_trace", Description: "按时序/变速/区段重放 trace（speed/loop/range）"},
	{Name: "mcp__wpe64__apply_rule", Description: "添加持续改包规则（match→replace，长度保持或修正）"},
	{Name: "mcp__wpe64__list_rules", Description: "列出当前生效的改包规则"},
	{Name: "mcp__wpe64__clear_rules", Description: "清除全部或指定改包规则"},
	{Name: "mcp__wpe64__save_trace", Description: "导出 trace 文件到指定路径"},
	{Name: "mcp__wpe64__load_trace", Description: "加载既有 trace 文件用于重放或编辑"},
}

// seedBuiltinMCPServersAndTools 幂等确保内置的 ScopeSentry 等 MCP 具备正确的启动参数
// 及初始工具缓存，使打包分发后开箱即可看到完整内置工具集；同时清理已下线的
// browser（Chromium / Playwright）条目。
func (d *DB) seedBuiltinMCPServersAndTools() error {
	// 1. 下线内置 browser（npx @playwright/mcp）MCP：它驱动 Chromium，而无头 Chromium
	//    会在 sec-ch-ua 暴露 HeadlessChrome，是最容易被拦的形态。浏览器自动化统一
	//    走 skills/camoufox（Camoufox / Firefox 内核）。升级安装时清掉旧库里的
	//    默认行与工具缓存——只删「仍是出厂配置」的那一条，用户自定义过的同名服务器
	//    命令行不同，不会被误删。
	if v, _, _ := d.GetSetting("browser_mcp_retired_v1"); v != "done" {
		var id int64
		var cmd, rawArgs string
		err := d.QueryRow(`SELECT id, COALESCE(command,''), COALESCE(args,'[]') FROM mcp_servers WHERE name='browser'`).Scan(&id, &cmd, &rawArgs)
		if err == nil && id > 0 && strings.EqualFold(strings.TrimSpace(cmd), "npx") {
			var args []string
			stock := strings.Contains(rawArgs, "@playwright/mcp")
			if json.Unmarshal([]byte(rawArgs), &args) == nil {
				for _, a := range args {
					if a == "@playwright/mcp" {
						stock = true
					}
				}
			}
			if stock {
				_, _ = d.Exec(`DELETE FROM mcp_tools_cache WHERE server_id=$1`, id)
				_, _ = d.Exec(`DELETE FROM mcp_servers WHERE id=$1`, id)
			}
		}
		_ = d.SetSetting("browser_mcp_retired_v1", "done")
	}

	// 2. 为 ScopeSentry / rea / wpe64 预置初始工具缓存（仅当该 server 的 mcp_tools_cache 为空时写入）。
	type catalog struct {
		serverName string
		tools      []MCPTool
	}
	builtinCatalogs := []catalog{
		{serverName: "ScopeSentry", tools: builtinScopeSentryTools},
		{serverName: "rea", tools: builtinReaMCPTools},
		{serverName: "wpe64", tools: builtinWPE64MCPTools},
	}
	// Go 内置服务器（mcpbuiltin）：工具清单直接从代码定义生成，避免「代码改了、
	// 缓存里的预览清单没改」的两处手抄漂移。启用后实时发现会覆盖本缓存。
	for _, srv := range mcpbuiltin.All() {
		tools := make([]MCPTool, 0, len(srv.Tools))
		for _, t := range srv.Tools {
			tools = append(tools, MCPTool{
				Name:        "mcp__" + srv.Name + "__" + t.Name,
				Description: t.Description,
			})
		}
		builtinCatalogs = append(builtinCatalogs, catalog{serverName: srv.Name, tools: tools})
	}

	for _, item := range builtinCatalogs {
		var serverID int64
		if err := d.QueryRow(`SELECT id FROM mcp_servers WHERE name=$1`, item.serverName).Scan(&serverID); err != nil || serverID <= 0 {
			continue
		}
		var count int
		if err := d.QueryRow(`SELECT COUNT(*) FROM mcp_tools_cache WHERE server_id=$1`, serverID).Scan(&count); err != nil || count > 0 {
			continue
		}
		for _, tool := range item.tools {
			if _, err := d.Exec(`
INSERT INTO mcp_tools_cache(server_id, tool_name, description)
VALUES ($1, $2, $3)
ON CONFLICT (server_id, tool_name) DO NOTHING`,
				serverID, tool.Name, tool.Description); err != nil {
				return fmt.Errorf("seed mcp tools for %s (%s): %w", item.serverName, tool.Name, err)
			}
		}
	}
	return nil
}
