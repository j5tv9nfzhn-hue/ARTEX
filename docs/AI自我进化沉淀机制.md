# AI 自我进化沉淀机制（任务终态 / 删除 / 手动提炼 / 审阅工作台与动态路由闭环）

> 状态：源码阶段。本文记录实现范围、触发时机、写入门控、前端审阅工作台、动态路由注入与验收边界；未在本机执行编译、测试、启动或数据库迁移，统一交由后续云端验收。

## 1. 与既有「缺口触发」的关系

Git 记录中的 `91db3c0 feat(skills): 落实自主沉淀的触发条件、门控接线与审阅流程` 已经落地了三件事：

- `skill_usage.found=false` 的未命中缺口（阈值 3）作为触发信号；
- `create_skill` / `update_skill_file` 的人工审阅门控（默认开启）；
- `propose_skill` / `list_skill_proposals` / `review_skill_proposal` 审阅流程。

在此基础上，本轮完成了从**产物采集 → 提议暂存/在线修订审阅 → 落盘授权 → 新任务 Turn-1 动态路由命中**的完整自主进化闭环：

1. **多源产物采集**：不仅采集探索图中的目标、意图、事实、确认漏洞、活动流与 3-Strike 止损信号，还采集**独立漏洞复测记录（`finding_retests`）**与**软件逆向静态分析证据（`re_samples` / `re_sessions` / `re_evidence`）**。
2. **三类触发入口**：任务进入终态（`done`/`failed`/`timeout`）异步沉淀、任务删除前同步沉淀、以及通过任务详情页顶部「沉淀技能」按钮 / 软件逆向工作台「提炼逆向复盘技能」按钮 / `/system/skills` 手动提炼栏 / 平台工具（`distill_task_skill`）按任务 ID 手动触发沉淀。
3. **审阅工作台、在线修订、批量审阅与跨任务合并**：`/system/skills` 页面新增「提议审阅」抽屉，支持按状态筛选（待审阅/已批准/已否决/全部）、查看审计依据与质量检查告警、**在批准前直接在线修订 `SKILL.md` 正文**并一键落盘生效、**多选批量批准/批量否决**、**多选合并提炼为 `merged-*` 通用专家技能**（自动去重合并各章节并可选归档原待审提议），以及从技能缺口弹窗一键预填新建技能。
4. **平台工具绑定与门控修正**：修正 `propose_skill` 使其即使在 `skill_review_gate=false` 时也始终走暂存队列（符合工具契约）；将 `list_skill_gaps` / `propose_skill` / `list_skill_proposals` / `review_skill_proposal` / `distill_task_skill` 纳入默认绑定与存量库迁移。
5. **新任务动态路由闭环与 `tactical_kb` 专项召回**：
   - `agent.BuildTacticalRoutingHint` 自动解析 `Skill` 工具描述中的 `<available_skills>` 清单，当新任务描述命中已沉淀的 `case-web-*` / `case-re-*` / `merged-*` 技能或自定义技能关键词时，在 Turn-1 `<tactical_skill_mcp_routing>` 中主动推荐加载对应历史沉淀技能。
   - `tactical_kb` 新增 `mode="case_skills"` 模式，支持在渗透中途按关键词专项检索所有已生效的历史任务复盘技能，结构化返回「已验证路径」「漏洞/复测/逆向证据」与「3-Strike 止损经验」。

## 2. 触发时机

| 时机 | 实现位置 | 执行方式 |
|---|---|---|
| 任务进入终态 `done` / `failed` / `timeout` | `Manager.SetTaskStatus` / `SetTaskStatusGuarded` → `fireTaskTerminal` | 异步 goroutine（45s 上限），不阻塞状态落库、通知与前端刷新 |
| 用户删除任务 | `Manager.DeleteTask` 在级联删除**之前** → `fireTaskDeleted` | 同步执行（30s 上限），因为级联删除后图、活动、证据已不存在 |
| 手动/Agent 指定任务提炼 | `POST /api/skills/distill` 或平台工具 `distill_task_skill` → `Server.DistillTaskSkill` | 同步执行，支持显式指定 `apply` 覆盖默认落盘策略 |

钩子只在 `Server.New` 中安装一次；生命周期写入路径本身不依赖 Server。测试或无头 Manager 没有安装钩子时行为不变。

## 3. 收集什么

`server/skill_curator.go` 只读取已落库的结构化记录，不调用 LLM，不生成或执行新命令：

- 目标（goals）与任务描述/目标；
- 意图（intents）：完成态用于「有效路径」，blocked / exhausted / stopped / deleted 用于「失败、误区与止损」；
- 事实（facts）：summary、confidence（observed / inferred）、evidence 摘要；
- 漏洞（findings）：severity、名称、类型、summary、证据摘要，按严重度排序；
- 漏洞复测记录（`finding_retests`）：复测结论（`fixed` / `reproduced` / `inconclusive`）、验证摘要与补丁说明；
- 软件逆向产物（`re_samples` / `re_sessions` / `re_evidence`）：样本名称与 SHA-256、静态分析工具、诊断结论与关键提取项；
- 活动（activity）：最多 3000 条，用于统计工具调用次数、失败次数、命令/URL 示例与 3-Strike 熔断信号；
- 工具调用账本与技能账本：本任务实际加载过的技能。

输出是一份确定性 Markdown 技能，包含：

1. `何时使用`（目标、关键词、分析域）；
2. `已验证的有效路径`（完成意图及其产出事实/漏洞数量）；
3. `关键事实与证据`；
4. `已确认漏洞与可利用点`；
5. `漏洞复测与修复验证`（存在复测记录时生成）；
6. `逆向样本与静态分析证据`（存在软件逆向样本或静态证据时生成）；
7. `工具与命令模式`；
8. `失败、误区与止损`；
9. `人工提示与决策`；
10. `复用步骤`；
11. `适用边界与验证要求`。

## 4. 命名与幂等

- 技能名：`case-web-<taskID>` / `case-re-<taskID>`，符合 `^[a-z][a-z0-9-]{0,63}$`。
- 提议 id 由 `sha256(skill|file|content)` 派生：同一任务在终态与删除两个时机生成相同内容时只保留一条，不会重复。
- 已存在同名技能文件：
  - 内容完全一致 → 跳过；
  - 内容不同 → 使用内容哈希后缀生成新版本名，绝不覆盖用户已有/已修改的技能。
- 同一进程内用任务 id 去重，避免终态与删除钩子并发写同一任务。

## 5. 写入门控与审阅 API

沿用既有 `skill_review_gate`：

| 设置 | 默认 | 行为 |
|---|---:|---|
| `skill_auto_distill` | `true` | 任务终态/删除时自动生成复盘技能 |
| `skill_review_gate` | `true` | 技能写入需要人工审阅；自动沉淀生成 `pending` 提议，不落盘 |
| `skill_auto_distill_apply` | `false` | 在审阅门控开启时，是否让 curator 生成的提议直接落盘 |

新增 HTTP 接口：

- `GET /api/skills/proposals?status=pending|applied|rejected|all&limit=50`：返回提议列表、各状态计数（`counts`）及当前门控开关状态。
- `POST /api/skills/proposals/{id}/review`：批准（`decision="approve"`，支持附带修订后的 `content` 先更新 `pending` 提议并重新执行质量校验，再原子写盘与绑定默认 Agent 可见性）或否决（`decision="reject"`，留档不落盘）。
- `POST /api/skills/proposals/batch-review`：按 `{"ids":[...],"decision":"approve|reject","note":"..."}` 批量批准或批量否决多条待审提议。
- `POST /api/skills/proposals/merge`：按 `{"ids":[...],"name":"merged-...","description":"...","apply":bool,"reject_sources":bool}` 将 ≥2 条提议去重合并为通用技能，并可自动否决归档原待审提议。
- `GET /api/skills/gaps?min_misses=3`：返回过滤掉已落盘技能后的结构化未命中技能缺口。
- `POST /api/skills/distill`：按 `{"task_id": <id>, "apply": <bool>}` 手动提炼指定任务的复盘技能。

## 6. 敏感值脱敏

沉淀内容只使用摘要与短示例，不写原始响应正文。进入技能前统一经过 `curatorRedact`：

- `password/passwd/pwd/secret/token/api_key/authorization/cookie/session/private_key` 等键值；
- `Authorization: Bearer …` 与 `Cookie:` / `Set-Cookie:` 请求响应头；
- URL 内嵌凭据（`https://user:pass@host`）；
- PEM 私钥块（`-----BEGIN ... PRIVATE KEY-----`）；
- 常见 API Key 前缀（`sk-`、`ssk-`、`ghp_`、`xox*`、`AKIA*` 等）与 JWT 形状。

这是模式化脱敏，不等于完整的秘密扫描；原始敏感数据仍以任务证据库为准，技能文件不应作为凭据存储。

## 7. 边界与未完成项

- 不自动执行、不安装、不改写目标；只生成 Markdown 技能文本。
- 不调用 LLM 归纳，因此不会产生「模型二次编造」；代价是表达偏模板化，复杂攻击链可在 `/system/skills` 提议审阅抽屉中人工在线修订后再批准落盘。
- 任务删除时同步沉淀会增加删除请求的耗时上限（30s）；超时/失败只记日志，不阻断删除。
- 未在本机编译、运行测试、启动服务或迁移数据库；需要在云端执行 `go test ./...`、前端构建与隔离库验收。

## 8. 相关代码

- `server/skill_curator.go` / `server/skill_curator_test.go`：收集（含复测与逆向证据）、生成、脱敏、幂等、手动与自动沉淀。
- `server/task_skill_hooks.go`：Manager 生命周期钩子。
- `server/skill_review_tools.go` / `server/skill_review_test.go`：门控、审阅工具、`distill_task_skill` 与 HTTP 审阅/沉淀接口。
- `server/platform_tools.go` / `server/orchestration.go` / `server/server.go`：平台工具默认绑定、存量库一次性迁移与路由挂载。
- `db/skill_proposals.go` / `db/skill_proposals_test.go`：待审阅提议查询（含 `all`、状态统计、批准前正文修订与时间戳导出）。
- `agent/skill_router.go` / `agent/skill_router_test.go`：`<available_skills>` 动态解析与 `case-web-*` / `case-re-*` 复盘技能 Turn-1 路由注入。
- `web/src/app/(main)/system/skills/page.tsx`：技能提议审阅工作台、在线修订、手动任务提炼与缺口一键建技能。
- `web/src/app/(main)/system/settings/page.tsx`：AI 自我进化沉淀设置卡片。
