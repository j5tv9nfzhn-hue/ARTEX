# artex-dsh

ARTEX-REA 的 [DSH（DeepSeek Harness）](https://github.com/deepseek-ai/dsh) 原生插件。
它把 ARTEX 的样本分诊、资产图与验证能力直接挂成 DSH 的工具，不用绕
`mcp__artex__*` 这层命名。

## 为什么不用官方 MCP 桥

`@deepseek-ai/dsh-mcp-client` 只桥 **Tools**（Resources / Prompts 官方标注 deferred），
而且工具名会变成 `mcp__artex__pe_info`。这个插件：

- 工具名是干净的 `artex_binscan` / `artex_recon` / `artex_task`；
- 把 ARTEX 的多步工序收成一个动作（`artex_binscan(kind=pe)` → `pe_info`），少一轮往返；
- 档位没开时给的是「去 系统 → MCP 打开 L2」这种能照做的话，而不是 `unknown tool`；
- 挂一段常驻提示（`ctx.systemPrompt`）和一个 `/artex` 斜杠命令（不支持的环境会自动跳过）。

## 安装

```bash
# 1. 先在 ARTEX 控制台 系统 → MCP → 对外接入：打开开关，复制 token
export ARTEX_MCP_TOKEN="artex_mcp_……"      # Windows: setx ARTEX_MCP_TOKEN "…"

# 2. 安装插件（本地目录或已发布的包名）
dsh plugin --profile web add ./artex-dsh

# 3. 确认配置里出现了 artex 行
dsh --profile web --dump-config | grep -A4 artex

# 4. 重启
dsh web
```

`cordis.patch.yml` 默认接 `http://127.0.0.1:8787/mcp`；改端点就改这里的 `config.url`，
或用环境变量 `ARTEX_MCP_URL`。

## 工具一览

| 工具 | 作用 | 依赖档位 |
|---|---|---|
| `artex_binscan` | 样本静态分诊：identify / pe / strings / entropy / ioc | L0 |
| `artex_assets` | 查资产图，避免重复扫描 | L1 |
| `artex_findings` | 查历史漏洞与证据 | L1 |
| `artex_tasks` | 列任务 | L1 |
| `artex_kb` | 战术知识库：打法卡片 / 业务不变量 / 历史结论 | L1 |
| `artex_recon` | DNS / 端口 / TLS / 技术栈验证 | L2 |
| `artex_web` | 原始请求 / FUZZ / 目录 / 参数 / JWT / SQLi 判定 | L2 |
| `artex_task` | 把攻坚委派成 ARTEX 后台任务 | L3 |
| `artex_task_status` | 查任务进度 | L3 |
| `artex_task_pause` / `artex_task_resume` | 暂停 / 恢复任务 | L3 |

默认只有 L0+L1；L2、L3 要在 ARTEX 控制台里显式打开。

## 注意

- 插件本身不做鉴权，也不缓存 token：每次调用都带 `Authorization: Bearer`。
- 没提供「取消任务」：取消等于删任务，请到 ARTEX 控制台操作；这里只有暂停/恢复。
- 本插件按 DSH 2026-08 文档的 Cordis 插件形态编写（`name` / `inject=['tools']` /
  `apply(ctx, config)` + `ctx.tools.register(defineTool(...))`）。若你的 DSH 版本
  API 有变，`dsh --profile web --dump-config` 与 `dsh plugin list` 是最快的验证点。
