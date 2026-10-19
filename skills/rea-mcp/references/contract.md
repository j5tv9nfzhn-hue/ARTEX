# REA 契约操作映射与地址纪律（references）

> 本手册是 `rea-mcp` 技能的深读附件：完整操作映射、模板变量、导出与配额边界。
> 权威定义见 `server/software_re_presets.go`（rea 预设）与 `server/software_re_engine.go`（深化契约）。

## 1. 会话生命周期

| 契约操作 | REA 工具 | 输入模板变量 | 说明 |
|---|---|---|---|
| open | `rea_open_sample` | `sample_path`, `sample_sha` | 创建静态会话，返回 `session_id`（SessionKeyPath=session_id） |
| close | `rea_close_sample` | `provider_session`, `sample_sha` | 关闭并释放引擎侧资源 |
| status | `rea_session_status` | `provider_session` | 会话状态、架构与模块摘要 |

模板变量由平台渲染（typed values，非 shell 语法）；未知占位符 fail closed。

## 2. 查询操作

| 契约操作 | REA 工具 | 输入 | RowsPath | 行字段映射 |
|---|---|---|---|---|
| functions | `rea_list_functions` | session_id, query, limit | `items` | name / rva / module |
| decompile | `rea_decompile` | session_id, address, symbol | —（正文） | — |
| xrefs | `rea_list_xrefs` | session_id, address, limit | `items` | kind / from / to / module |
| callgraph | `rea_callgraph` | session_id, address, limit | `edges` | label / caller / callee / module |
| types | `rea_list_types` | session_id, query, limit | `items` | name / module |
| modules | `rea_list_modules` | session_id, limit | `items` | name / base(rva) / module |
| imports | `rea_list_imports` | session_id, query, limit | `items` | symbol / rva / library |
| exports | `rea_list_exports` | session_id, query, limit | `items` | symbol / rva / module |
| strings | `rea_list_strings` | session_id, query, limit | `items` | value / offset(file_offset) / section |

## 3. 地址纪律（硬约束）

- **RVA**：functions / decompile / xrefs / callgraph / modules / imports / exports。
- **file_offset**：strings。
- 证据与导出必须标注 `address_kind`；跨 kind 的地址**不得**直接比较或拼接。
- 版本/构建变化后 RVA 失效：以 sample_sha 为锚，重新查询而非复用旧地址。

## 4. 多模块纪律

1. 开会话后先 `modules` 建立清单（模块名 + 基址）。
2. 函数/符号查询结果的 `module` 列用于分节；跨模块关系写成 `caller@modA → callee@modB`。
3. 同一函数名在多模块出现时，identity 必须是 `(module, address_kind, address)` 三元组，不得只用名字。

## 5. 完整导出

- 默认节区：`status, functions, modules, imports, exports, strings`。
- 追加节区：`decompile / xrefs / callgraph / types` 需显式 `address`（export 请求体一步）。
- 配额：`max_export_sections=12`、`max_export_bytes=4MiB`、`limit ≤ 256`（默认 100）。
- manifest 记录 task_id / session_id / sample_sha / provider kind / tier / exported_at。
- 失败节区以 `is_error + diagnostic` 记录，不中断整包；这与「受阻记录」语义一致。

## 6. 配额与清理

| 项 | 默认值 | 强制点 |
|---|---|---|
| 任务活动会话 | ≤ 4 | 报告（reOpen 硬拒绝为后续接线点） |
| 单 Provider 活动会话 | ≤ 2 | 报告 |
| 导出节区/字节 | 12 / 4 MiB | 导出侧强制 |
| 滞留租约清理 | 批量 ≤ 200 | `POST /api/re/sweep`，需 `confirmed=true` |

`interrupted` = 外部引擎状态未知；只有 control/close 或确认后的 sweep 才能置为 `closed` 终态。
