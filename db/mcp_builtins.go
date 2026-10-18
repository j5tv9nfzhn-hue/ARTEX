package db

import (
	"encoding/json"
	"fmt"
	"strings"
)

// builtinMCPStaticTools 定义内置 MCP 服务器（ScopeSentry 与 browser）的标准工具集。
//
// 为什么在数据库初始化时预置到 mcp_tools_cache：
//  1. ScopeSentry（HTTP）与 browser（Playwright stdio）默认以 enabled=false 播种，
//     启动时的 discoverEmptyMCPsOnStartup 仅对已启用服务器发起实时连接；
//  2. 若不预置工具缓存，前端 /system/mcp 页面在未启用或离线打包分发状态下会一直显示
//     「尚未发现工具」，用户无法在启用/授权前预览这两个内置 MCP 包含哪些工具；
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

var builtinBrowserMCPTools = []MCPTool{
	{Name: "mcp__browser__browser_navigate", Description: "导航浏览器到指定 URL 并等待页面加载"},
	{Name: "mcp__browser__browser_navigate_back", Description: "回退到当前标签页的上一访问页面"},
	{Name: "mcp__browser__browser_snapshot", Description: "采集当前页面的可访问性 DOM 快照与元素引用（ref）"},
	{Name: "mcp__browser__browser_click", Description: "点击页面上的指定目标元素（按 ref 或定位器）"},
	{Name: "mcp__browser__browser_type", Description: "在指定输入框或可编辑元素中输入文本（可选提交）"},
	{Name: "mcp__browser__browser_hover", Description: "将鼠标悬停在页面指定元素上"},
	{Name: "mcp__browser__browser_drag", Description: "在两个页面元素之间执行拖放操作"},
	{Name: "mcp__browser__browser_select_option", Description: "在下拉选择框（select）中选择指定选项"},
	{Name: "mcp__browser__browser_press_key", Description: "在当前页面或聚焦元素上按下指定键盘按键"},
	{Name: "mcp__browser__browser_wait_for", Description: "等待指定文本出现、消失或等待指定时长"},
	{Name: "mcp__browser__browser_take_screenshot", Description: "对当前视口、整页或指定元素进行截图"},
	{Name: "mcp__browser__browser_pdf_save", Description: "将当前页面导出保存为 PDF 文件"},
	{Name: "mcp__browser__browser_file_upload", Description: "向文件选择控件上传一个或多个本地文件"},
	{Name: "mcp__browser__browser_fill_form", Description: "批量填写页面表单中的多个字段"},
	{Name: "mcp__browser__browser_handle_dialog", Description: "接受或取消页面的 alert/confirm/prompt 弹窗"},
	{Name: "mcp__browser__browser_evaluate", Description: "在当前页面上下文中执行 JavaScript 表达式并返回结果"},
	{Name: "mcp__browser__browser_console_messages", Description: "读取页面控制台（console）输出的日志与错误消息"},
	{Name: "mcp__browser__browser_network_requests", Description: "列出当前页面发起的 HTTP/XHR/Fetch 网络请求记录"},
	{Name: "mcp__browser__browser_tabs", Description: "列出、新建、切换或关闭浏览器标签页"},
	{Name: "mcp__browser__browser_resize", Description: "调整浏览器窗口视口宽高尺寸"},
	{Name: "mcp__browser__browser_close", Description: "关闭当前浏览器页面并释放会话资源"},
	{Name: "mcp__browser__browser_install", Description: "安装或修复 Playwright 所需的受管浏览器内核"},
}

// seedBuiltinMCPServersAndTools 幂等确保内置的 browser 与 ScopeSentry MCP 具备正确的
// 启动参数及初始工具缓存，使打包分发后开箱即可看到完整内置工具集并能非交互启动。
func (d *DB) seedBuiltinMCPServersAndTools() error {
	// 1. 修正旧库中 browser MCP 缺少 "-y" 的默认 npx 参数（避免 stdio 下 npx 等待 y/n 确认卡死）。
	if v, _, _ := d.GetSetting("browser_mcp_npx_yes_v1"); v != "done" {
		var id int64
		var cmd, rawArgs string
		err := d.QueryRow(`SELECT id, COALESCE(command,''), COALESCE(args,'[]') FROM mcp_servers WHERE name='browser'`).Scan(&id, &cmd, &rawArgs)
		if err == nil && id > 0 && strings.EqualFold(strings.TrimSpace(cmd), "npx") {
			var args []string
			if json.Unmarshal([]byte(rawArgs), &args) == nil && len(args) > 0 {
				hasYes := false
				for _, a := range args {
					if a == "-y" || a == "--yes" {
						hasYes = true
						break
					}
				}
				if !hasYes && args[0] == "@playwright/mcp" {
					args = append([]string{"-y"}, args...)
					if b, merr := json.Marshal(args); merr == nil {
						_, _ = d.Exec(`UPDATE mcp_servers SET args=$1 WHERE id=$2`, string(b), id)
					}
				}
			}
		}
		_ = d.SetSetting("browser_mcp_npx_yes_v1", "done")
	}

	// 2. 为 ScopeSentry 与 browser 预置初始工具缓存（仅当该 server 的 mcp_tools_cache 为空时写入）。
	builtinCatalogs := []struct {
		serverName string
		tools      []MCPTool
	}{
		{serverName: "ScopeSentry", tools: builtinScopeSentryTools},
		{serverName: "browser", tools: builtinBrowserMCPTools},
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
