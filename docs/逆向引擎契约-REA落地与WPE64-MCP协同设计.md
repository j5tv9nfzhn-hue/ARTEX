# 逆向引擎契约（REA 优先）落地与 WPE64-MCP 协同设计（2026-10-19）

> 决策来源：本轮确认「选定单引擎 = REA 自研引擎优先深做；WPE64-MCP 需要全新设计并落地；
> 逆向与网安新增技能包与 MCP」。本文记录契约设计、落地文件与验证边界。

## 1. 逆向引擎契约落实（选定单引擎 = REA）

通用映射层（`REProviderConfig` + `reEvaluateCapabilities`）解决「工具怎么调、目录/schema 门控」；
本轮新增的深化契约层解决「**什么状态才算 ready、导出/配额/模块纪律是什么**」，防止
「通用工具目录存在 ≠ 引擎 ready」被误读。

### 1.1 契约分层（tier）

| tier | 含义 | 判定 |
|---|---|---|
| `unverified` | 通用门控未过 | `reviewed_static=false` / 未 discover / 映射缺失 |
| `basic` | 门控通过，契约核心操作未全部就绪 | 核心集任一未映射或 schema 不匹配 |
| `deep` | 核心操作全部就绪 | 可开展完整分析与导出 |

- REA 核心集：`status / functions / decompile / xrefs / callgraph`（**callgraph 计入核心**）。
- Ghidra / IDA 保持通用骨架（各自核心集见 `reContractCoreOps`），后续按同一框架深做。

### 1.2 本轮补齐的四项缺口（对应进度台账④）

| 缺口 | 落地 | 强制点 |
|---|---|---|
| 实时能力门控收口 | `REContractReport`（tier + 缺口清单） | `GET /api/re/providers/{id}/contract` |
| 完整导出 | `reExportBundle`：manifest + 分节数据，经既有 `reQuery` 通道重放 | `POST .../re/sessions/{session}/export`、工作台 `mode=export` |
| 清理/配额 | `REQuotaReport` + `POST /api/re/sweep`（confirmed 释放 interrupted 租约） | `GET .../re/quota`、工作台 `mode=quota` |
| 多模块 | 契约 notes + 技能手册纪律：modules 先行、module 列限定、`(module, address_kind, address)` 三元组身份 | 导出/查询的地址纪律 |

### 1.3 地址纪律（REA）

- functions/decompile/xrefs/callgraph/modules/imports/exports → **RVA**；strings → **file_offset**。
- 证据与导出必须标注 `address_kind`；禁止混用 ASLR 运行地址与不同版本 RVA。

## 2. WPE64-MCP × mitmproxy 协同

```
目标应用 ──┬── 代理感知流量 ──► 内置记录代理(go-mitmproxy :8788, traffic/) ──► SQLite 流量库
           └── Winsock/自定义帧 ──► WPE64 MCP（抓包/改包/重放/规则）
对齐基准：五元组 + 时间戳（|Δt| ≤ 2s 视为同一连接两侧视图）
```

### 2.1 WPE64-MCP 工具契约（本轮设计，14 工具）

`status` `list_adapters` `start_capture` `stop_capture` `list_packets` `get_packet`
`edit_packet` `send_packet` `replay_trace` `apply_rule` `list_rules` `clear_rules`
`save_trace` `load_trace`

命名规范 `mcp__wpe64__<tool>`，与既有 `mcp__ScopeSentry__*` / `mcp__browser__*` 一致；
已按 `builtinWPE64MCPTools` 预置工具缓存（离线预览，启用后以实时发现为准）。

### 2.2 REA-MCP 工具契约

与 `software_re_presets.go` rea 预设一一对应（`rea_open_sample` … `rea_list_strings`），
同样预置 `builtinReaMCPTools` 工具缓存。

## 3. 新技能包与 MCP（逆向 + 网安）

| 技能包 | 域 | MCP | 内容 |
|---|---|---|---|
| `skills/rea-mcp/`（+references/contract.md） | 逆向 | `rea` | 引擎契约操作手册：tier/地址纪律/多模块/导出/配额 |
| `skills/wpe64-mcp/`（+references/joint-playbook.md） | 网安 | `wpe64` | 封包实验手册 + WPE64×mitmproxy 联合取证 |
| `skills/mitmproxy-traffic/` | 网安 | —（内置代理） | 流量录制/解密取证/精确重放/证据绑定 |

配套：`skills/embed.go` go:embed 注册；`agent/skill_router.go` 三条技战术路由；
`agent/tool_contract.go` 别名（rea/wpe/wpe64/mitmproxy/…）；`agent/promptcatalog.go` 与
`agent/mainagent.go` 主动调用清单；`db/db.go` 播种 `rea`/`wpe64` MCP 行（默认禁用）与
技能可见性 V2（独立开关 `builtin_skill_visibility_v2`，只补新包不复活用户关闭的旧包）。

## 4. 落地文件清单（2026-10-19，源码阶段）

| 文件 | 动作 |
|---|---|
| `server/software_re_engine.go` | 新增：契约分级/配额/清理 |
| `server/software_re_export.go` | 新增：完整导出 |
| `server/software_re.go` | 修改：5 条路由 + 工作台 export/quota 模式 |
| `db/mcp_builtins.go` | 修改：rea/wpe64 工具缓存 + 播种清单 |
| `db/db.go` | 修改：rea/wpe64 MCP 行播种 + 技能可见性 V2 |
| `skills/{rea-mcp,wpe64-mcp,mitmproxy-traffic}/` | 新增：3 技能包（含 2 个 references） |
| `skills/embed.go` | 修改：go:embed 注册 |
| `agent/{skill_router,tool_contract,promptcatalog,mainagent}.go` | 修改：路由/别名/提示词接线 |

## 5. 验证边界（本轮遵循源码阶段口径）

- **已做**：源码落盘 + 逐文件回读核对；命名/契约对齐既有 `REOperation`/`MCPTool`/状态机（closed 终态）。
- **未做**：编译、gofmt、类型检查、单测、迁移、联调（沿用「不编译/测试/构建」授权边界）。
- **后续接线点**：reOpen 侧会话数硬拒绝（当前为报告阈值）；WPE64/REA 真实 MCP 联调与
  tools/list 刷新；逆向引擎单引擎验收（对照《云端全量编译测试与验收门禁清单》另行授权执行）。
