# ARTEX 源码复核报告

本文件记录对 ARTEX 源码做过的一轮**只读复核**结论。所有条目都回到当前源码逐条核对，
不做推测性描述；行号以本仓库当前提交为准，随代码演进可能位移。

## 范围与方法

- **范围**：仅复核、不作修改。逐条对照源码定位缺陷所在的具体分支与语句。
- **方法**：以"社区 Bug 清单"为线索，逐条回源码验证是否**真的存在**（`已确认`）、
  还是仅**在特定数据形态下才成立**（`潜在`）。
- **未纳入**：架构、可维护性、性能等非缺陷项；本轮也不做可利用性分析。

## 风险等级说明

本次复核**未评定风险等级**（CVSS 或内部分级）。下方所有条目一律标注为
`未知（未评定）`：复核只确认了"代码里确实存在这个缺陷/这个形态"，
但**没有**评估其触发前提是否可达、影响面多大、是否需要更高权限前置条件。
在补齐可利用性评估之前，任何"高/中/低"的结论都属于臆测，故不给出。

## 已核实条目（共 7 条）

### 1. 工具表读取失败时 fail-open，被禁用的高危工具重新可用

| 项 | 内容 |
| --- | --- |
| 位置 | `server/assembly.go:239-244`（`agent.ToolResolve` 的 `pg.ListTools()` 分支） |
| 风险等级 | 未知（未评定） |
| 核实结论 | **已确认** |

`ToolResolve` 用来按数据库里的 `tools` 行过滤/覆盖工具集（禁用、以及"绑定到哪些 agent"）。
当 `pg.ListTools()` 返回错误时，这里直接 `log.Printf` 后 `return tools` —— 把**未经过滤的
基础工具集**原样交还给 agent。也就是说：数据库抖动/权限异常的那一刻，管理员在 UI 上
禁用的高危工具会被重新放行，且只在服务端日志里留一行"按代码默认放行"。

- 建议方向：改为 fail-closed（返回空集或只保留非 shell 类工具），并把降级动作显式暴露到
  前端/健康检查里，而不是只写日志。

### 2. LLM 客户端未设超时，429 响应体无界读取

| 项 | 内容 |
| --- | --- |
| 位置 | `agent/provider.go:419`（构造 `&http.Client{Transport: ...}` 时无 `Timeout`）；`agent/provider.go:367`（429 分支 `io.ReadAll(resp.Body)`） |
| 风险等级 | 未知（未评定） |
| 核实结论 | **已确认** |

两处叠在同一个客户端上：

1. `quotaAwareHTTPClient` 返回的 `http.Client` 没有 `Timeout`。上游网关挂死（只建连不响应）时，
   请求会一直挂到 TCP/中间层自己超时，占着一个 worker 与一次任务配额。
2. 429 分支为了识别"额度耗尽"文案会把整个响应体读进内存，且**没有大小上限**。
   异常网关/代理返回一个超大响应体即可把内存打满。

- 建议方向：给 client 设总超时（并在 SSE 场景用 per-request context 而非全局 Timeout），
  429 分支改用 `io.LimitReader` 截断后再匹配文案。

### 3. CORS 未放行 PATCH 与 Authorization

| 项 | 内容 |
| --- | --- |
| 位置 | `server/server.go:3883-3884` |
| 风险等级 | 未知（未评定） |
| 核实结论 | **已确认** |

`Access-Control-Allow-Methods` 只有 `GET,POST,PUT,DELETE,OPTIONS`，
`Access-Control-Allow-Headers` 只有 `Content-Type`。而 API 里实际存在大量 `PATCH` 路由
（如 `PATCH /api/tasks/{id}`、`PATCH /api/task-categories/{id}`、`PATCH /api/exploration/findings/{id}`），
并且鉴权走 `Authorization: Bearer`。

后果：当前端与后端不同源（独立域名/独立端口/反代拆分）时，浏览器的预检会拦掉 `PATCH` 与
带 `Authorization` 的请求——表现为"某些操作在跨域部署下静默 405/预检失败"，而在同源部署下完全正常。

- 建议方向：补齐 `PATCH` 到 methods、`Authorization` 到 headers，并考虑 `Expose-Headers`。

### 4. `Redact` 对 keyword/value 形式的 DSN 失效，密码会进日志

| 项 | 内容 |
| --- | --- |
| 位置 | `config/config.go:181-192`；唯一调用点在 `db/db.go:153` |
| 风险等级 | 未知（未评定） |
| 核实结论 | **已确认** |

`Redact` 用 `url.Parse` 解析后只对 `u.User` 的密码段打码。但 `ARTEX_PG_DSN` 允许
`host=... user=... password=... dbname=...` 这种 keyword/value 形式（pgx 原生支持）：
这种串被 `url.Parse` 当作不透明路径，`u.User` 为空，于是**原样返回**。
而 `db/db.go:153` 把它拼进了连库失败的 `fmt.Errorf`，最终落到服务端日志与前端日志页。

后果：连库失败时（恰恰是用户最常贴日志求助的时刻），明文数据库密码可能出现在日志里。

- 建议方向：对非 URL 形式的 DSN 用正则脱敏 `password=...` / `passfile=...`，
  或在入口处统一把 keyword/value 转成 URL 再脱敏。

### 5. 工作区上传直接 `os.Create`：同名静默覆盖 + 失败留半截文件

| 项 | 内容 |
| --- | --- |
| 位置 | `server/workspace.go:268-281`（`saveUpload`，`os.Create` 在 `:274`） |
| 风险等级 | 未知（未评定） |
| 核实结论 | **已确认** |

上传落盘没有"检查是否已存在"，也不做临时文件 + 原子 rename：
`os.Create` 会立刻截断目标文件。于是

- 同名上传**静默覆盖**，没有任何提示；
- 传输中途失败/连接断开时，磁盘上留下的是**被截断的半截文件**，而接口只是返回错误，
  原文件已经不可恢复。

对"工作区里放脚本/字典/证据"这种用法，这是数据丢失级的问题，而不是体验问题。

- 建议方向：先写 `*.part` 再 `os.Rename`；已存在时显式返回 409 或要求覆盖标志。

### 6. 命令列表的 `COUNT(*)` 与数据查询口径不一致（潜在）

| 项 | 内容 |
| --- | --- |
| 位置 | `db/commands.go:96`（计数）对比 `db/commands.go:102-113`（取数） |
| 风险等级 | 未知（未评定） |
| 核实结论 | **潜在**（依赖数据形态，未构造出稳定复现） |

分页总数走 `SELECT COUNT(*) FROM activity u <where>`（单表），
而列表数据走 `LEFT JOIN activity r ON r.tool_use_id = u.tool_use_id AND r.kind='tool_result'`。
`activity.tool_use_id` 上**没有唯一约束**（`db/schema.sql` 只有普通索引
`idx_act_tool_call`），因此同一个 `tool_use_id` 只要出现多条 `tool_result`，
JOIN 结果就会行翻倍：分母按 `tool_use` 行数算，分子按 JOIN 行数返回。

- 触发前提：同一工具调用产生多条结果记录（重试/回放/上游重复写入）。未在实机稳定复现，
  故记为"潜在"。
- 建议方向：计数改为与被分页的结果集同源（`COUNT(DISTINCT u.id)` 或子查询包一层）。

### 7. 工具调用统计 `COUNT(*)` 建立在 JOIN 结果上（潜在）

| 项 | 内容 |
| --- | --- |
| 位置 | `db/commands.go:56-63`（`ToolStats`） |
| 风险等级 | 未知（未评定） |
| 核实结论 | **潜在**（与第 6 条同源） |

`ToolStats` 同样 `LEFT JOIN` 后聚合：

```sql
SELECT COALESCE(NULLIF(u.tool,''),'-') AS tool, COUNT(*) AS total,
       COUNT(*) FILTER (WHERE COALESCE(r.is_error,false)) AS errors
FROM activity u
LEFT JOIN activity r ON r.tool_use_id = u.tool_use_id AND r.kind = 'tool_result'
```

`COUNT(*)` 计的是 JOIN 后的行数，一多对时会**虚增**工具调用次数与错误数
（错误数还依赖"每条结果都带 `is_error`"这一前提）。

- 建议方向：`COUNT(DISTINCT u.id)` 与 `COUNT(DISTINCT u.id) FILTER (WHERE r.is_error)`。

## 未纳入本表的条目

复核过程中另有若干"社区 Bug 清单"条目在本版本源码中**找不到对应实现**
（`device-agent`、`intercept` 审批 CAS、`gzip 对 304`），属于衍生版自加或已被重构掉，
故不列入，避免留下无法核实结论的记录。

## 附：本轮复核环境

- 代码版本：本仓库当前提交（`main`）。
- 运行环境：Linux/amd64，PostgreSQL 16，Go 1.26.3。
- 复核方式：源码定位 + 编译/启动/接口实测；**未对任何外部目标发起测试**。
