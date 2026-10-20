# Harness 多域升级路线图（长期任务）

> 目标：把 Harness 从「只能做逆向/渗透」的工作台，升级成「渗透/逆向 + 软件开发 + 办公 + 通用工作」的统一工作台。
> 硬约束：**不损坏现有渗透与逆向链路**；不新增第二套审批入口；能力变化必须在 UI 可见。

---

## 一、现状盘点（都是代码事实，不是设想）

| 事实 | 位置 | 对升级的含义 |
|---|---|---|
| Harness 会话跑的是 `ChatAgent`，定位是**纯助理**：SDK 基础工具（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash/Sleep）+ 该 agent 的 Skills/MCP，**不注入渗透图/任务上下文** | `agent/chat.go:19-24` | 开发/办公能力天然长在这一层，**渗透链路（MainAgent/Planner/Worker）完全不用碰** |
| `ChatAgent.workDir = s.m.dir`（数据目录） | `server/server.go:631/772` | 没有"项目"概念；要开发必须引入**会话级工作根** |
| `conversations` 表只有 id/agent_key/title/llm_profile_id/pinned_at | `db/schema_sqlite.sql` | 没有 mode、没有 project/root 字段 |
| 权限模式只有 `plan / edit / auto`，且是前端 state | `web/src/lib/harness/types.ts:9`、`use-harness.ts:117` | 域的能力边界目前无处表达 |
| 右栏只有 变更 / 计划 / 文件 三页签；变更靠时间线里扫 diff | `web/src/components/harness/review-rail.tsx:1-7` | 注释自己写着"后端目前没有 `/conversations/{id}/changes`" → 这是 Phase 1 的第一块后端 |
| 已有文件 API：`/api/workspace/list|read|write|mkdir|delete|backup` | `server/server.go:877-883` | 可复用，但根是数据目录，需要"项目根"版本 |
| 迁移是幂等的加列（SQLite 用 pragma 检查、PG 用 `IF NOT EXISTS`） | `db/open_sqlite.go:105-138`、`db/commands.go:187-196` | Phase 0 加列是低风险、有现成路径的事 |
| `TodoWrite` / `WebFetch` 是 SDK 里 **opt-in** 的工具（不在 DefaultTools） | norma `tool/registry.go:56-70` | 开发/办公域按需开启，默认不改变现有会话面貌 |

**最重要的一条**：渗透/逆向走的是 `agent/mainagent.go`、`agent/planner.go`、`agent/tools*.go`、`agent/promptcatalog.go` 这条独立链路，与 `ChatAgent` 无耦合。**只要这条链路一行不动，升级就不可能破坏它。**

---

## 二、目标形态：域（Domain）驱动

同一套会话/审批/回滚/时间线基础设施，按**域**装配不同能力：

| 域 | 定位 | 执行引擎 | 默认工具面 |
|---|---|---|---|
| `general` | 问答、写作、整理、轻量脚本 | ChatAgent | 现状基础工具（行为不变，作为默认值） |
| `dev` | 项目开发：读码、改码、构建、测试、调试、git | ChatAgent（+ 子代理） | 基础工具 + TodoWrite + WebFetch + `run_task` + `git_*` + 项目根文件树 |
| `office` | 文档/表格/PPT/PDF/邮件/调研 | ChatAgent | 基础工具 + `doc_*` / `sheet_*` / `pdf_*` + 产物预览 |
| `pentest` | 渗透/逆向 | **既有 MainAgent/Planner/Worker**（不改） | 既有 tools.go 全家桶（不改） |

**关键设计**：`pentest` 域不是把渗透能力塞进 ChatAgent，而是 Harness 里"打开/派工到任务"的**路由入口**。域只决定「谁执行、看得到什么」，不重写任何执行逻辑。

### 分层

```
┌ Web ─ 域切换 · 项目面板 · 运行面板 · 产物预览 · 审批卡（唯一入口）
├ API ─ conversations(mode, workspace) · projects · changes · runs · artifacts
├ Domain Registry（Go，单一真值源）─ 域 → prompt key / 工具白名单 / 能力位 / 默认权限
├ Execution ─ ChatAgent（general/dev/office）  |  MainAgent/Planner/Worker（pentest，冻结）
└ Runtime ─ 工作根 · 文件快照 · 进程 · git · 产物
```

---

## 三、分阶段路线

### Phase 0 — 地基：域与会话工作根（不动任何执行逻辑）

**后端**
- `conversations` 加列：`mode TEXT NOT NULL DEFAULT 'general'`、`workspace_root TEXT NOT NULL DEFAULT ''`、`workspace_kind TEXT NOT NULL DEFAULT 'data'`
  - SQLite：`db/schema_sqlite.sql` + `db/open_sqlite.go` 迁移表；PG：`db/commands.go`
  - 默认 `general` = 今天的行为，老会话零变化
- `db/conversation.go`：读写新列
- `GET/PATCH /api/conversations/{id}`：改 mode / workspace_root（校验：必须在"允许根"清单内）
- `GET /api/conversations/{id}/changes`：先返回空实现 + 明确 `unsupported`，让前端不再靠扫时间线

**前端**
- 会话列表/页头显示域徽标；新建会话时选域
- 「项目」选择器（选择/新建目录），显示当前工作根
- 右栏"变更"改为调新 API（有就渲染，没有就空态说明）

**验收**：老会话打开、发消息、看时间线、回滚全部与今天逐字节一致；新会话可选域但不改变执行结果。

---

### Phase 1 — dev 域 MVP：能真的改代码

**后端**
- `agent/domain.go`：域注册表（prompt key、工具白名单、能力位、默认权限模式）
- `ChatAgent` 按域装配工具（默认面不变，新增项都是 opt-in）
- `ChatAgent.workDir` 改为会话级 `workspace_root`
- `projects` 模块：`GET /api/projects/tree|file`（限定在允许根内）、`POST /api/projects`（新建/登记）
- `changes` 模块：git 仓库走 `git diff`（含 untracked 的 `--no-index` 兜底），非 git 走文件快照哈希对比
- 文件快照：每轮开始前对工作根做轻量快照（路径+mtime+size+hash），供"变更"与整轮回滚

**前端**
- 顶部域切换（dev/general/office/pentest 路由）
- 右栏"文件"接项目根；"变更"显示真实 diff（已有 `DiffBlock`）
- 单文件回滚、整轮回滚（复用现有 rollback 语义）

**验收**：在真实仓库里完成「读 → 改 → 跑构建 → 看 diff → 回滚一个文件」闭环。

---

### Phase 2 — dev 深化：构建/测试/调试回路

**后端模块 `runtask`**
- `run_task`：按项目配置自动识别（package.json scripts / go.mod / Makefile / pyproject / Cargo.toml），白名单命令 + 超时 + 输出裁剪 + 退出码 + 结构化摘要
- `git_*`：status/diff/log/show 只读自动；commit/branch/checkout/reset 走审批
- 后台进程：长任务（dev server、watch）后台运行 + 日志轮询（复用 `Sleep`）
- 失败归因：把 stderr 里最常见的 30 类错误映射成"下一步建议"（缺依赖/端口占用/权限/版本不符）

**前端**：运行面板（运行中/历史/日志折叠）、"失败 → 让模型修"一键、命令白名单可见

**验收**：跑测试 → 红 → 模型读日志 → 改代码 → 复跑 → 绿，全过程在时间线可审计。

---

### Phase 3 — office 域：文档/表格/PPT/PDF

**后端模块 `docgen` / `sheet` / `pdfkit`**
- `doc_create(structured JSON → docx/xlsx/pptx)`、`doc_read`、`sheet_query`（CSV/公式/透视）、`pdf_extract`（文本/表格/页）
- 模板库：周报、会议纪要、需求文档、预算表、汇报 PPT
- 产物落 `artifacts/`，带 manifest（类型/页数/大小/来源活动）

**前端**：产物卡片 + 就地预览（docx/xlsx/pptx/pdf 已有预览能力）+ 下载/另存

**验收**：一句话产出「带图表的周报 xlsx + 一页 docx」，可下载可预览，产物与对话活动双向可追溯。

---

### Phase 4 — 跨域：技能分域、子代理、模板

- Skills/MCP 按域可见（现在是全局），避免 dev 会话里混进渗透技能
- 会话模板（脚手架、周报、渗透任务），一键带出预置 prompt + 工具面
- 子代理：dev 域 explore/plan/implement 三个只读/写角色（复用 Worker 的机制但**不接渗透图**）
- 验收：一个跨文件需求自动拆解 → 并行子代理 → 汇总 diff

---

### Phase 5 — 治理：预算、审计、模型策略

- 域级预算/限额、域级审计报表（按域看工具失败率、成本、时长）
- 域级模型策略（弱模型不给重工具面）
- 会话级 checkpoint 增强：整轮文件回滚

---

## 四、数据模型变更总表（只加不删）

| 表 | 列 | 阶段 | 默认值 | 说明 |
|---|---|---|---|---|
| conversations | `mode` | P0 | `general` | 域 |
| conversations | `workspace_root` | P0 | `''` | 会话工作根（空=沿用数据目录） |
| conversations | `workspace_kind` | P0 | `data` | data/project |
| settings | `workspace_allowed_roots` | P0 | `[]` | 允许登记的项目根（安全边界） |
| 新表 `projects` | id/name/root/created_at | P1 | — | 项目登记 |
| 新表 `run_tasks` | id/conversation_id/cmd/status/exit/output | P2 | — | 构建/测试运行记录 |
| 新表 `artifacts` | id/conversation_id/kind/path/meta | P3 | — | 产物清单 |

---

## 五、必须守住的不变式

1. **渗透链路冻结**：`agent/mainagent.go`、`agent/planner.go`、`agent/tools*.go`、`agent/promptcatalog.go`、`server/orchestration.go` 的渗透部分不改；只在"路由"处新增分支。
2. **审批唯一入口**：写文件/执行命令/越界操作，全部复用现有 permission 与 proposal 机制，不新开第二个审批面。
3. **拒绝必须可见**：越界根、未登记项目、危险命令，一律回明确理由 + 建议，不做静默失败。
4. **迁移只加列**：不删列、不改语义；每次升级后老库直接可用。
5. **每期独立可回退**：一个阶段一个 commit，revert 不影响其他阶段。

---

## 六、每阶段回归清单

```
go build ./... && go vet ./agent ./server
go test ./agent ./server
npx tsc --noEmit && npm test && biome check <改动文件>
渗透冒烟：新建渗透任务 → 派 worker → 工具调用→ 落库 → 时间线可见
Harness 冒烟：新建会话 → 发消息 → 改文件 → 看 diff → 回滚
```

---

## 七、Phase 0 立即可开工的任务拆解（等你点头即执行）

1. `db/schema_sqlite.sql`：conversations 增 3 列
2. `db/open_sqlite.go`：迁移表加 3 条（pragma 幂等）
3. `db/commands.go`：PG `ADD COLUMN IF NOT EXISTS` ×3
4. `db/conversation.go`：`Conversation` 结构体 + CRUD
5. `server/conversations.go`：PATCH 支持 mode/workspace_root；`changes` 空实现
6. `web/src/lib/types.ts` + `api.ts`：类型与调用
7. harness 前端：域徽标 + 项目选择器 + 右栏接新接口

预计 1 个 commit，约 12 个文件，**不改任何执行逻辑**。
