# 外部接入：扫描、一键配置与「同时在线」约束

> 2026-10-21 修订：**外部客户端接入不再拉起任何后台进程**。
> stdio 只剩一条职责——把会话透明转发给正在运行的 ARTEX 控制台。
> 因此规则可以一句话说清：**ARTEX 控制台与客户端同时在线，就能用；不在一方，就明确报错。**

## 1. 两种接法

| 接法 | 端点 | 谁承载 | 前提 |
|---|---|---|---|
| **HTTP（推荐）** | `http://127.0.0.1:8787/mcp` | 控制台进程 | 控制台在线 |
| **stdio 透明转发** | `artex-rea.exe mcp serve --stdio --addr http://127.0.0.1:8787` | 客户端子进程只做代理 | 控制台在线 |
| ~~stdio 本地服务~~ | ~~`--auto` / `--attach` / `headless`~~ | ~~客户端旁另起一个实例~~ | **已删除** |

两种接法拿到的工具面完全一样：档位由控制台里的开关决定（默认 `L0,L1`，
L2 主动探测 / L3 任务编排都要显式打开）。

### 1.1 为什么 stdio 改成"透明转发"

旧实现有三条互相牵连的副作用，任何一条都会让排查变难：

1. `mcp serve` 无条件 `db.DSN()` + `db.Open()` —— 客户端的工作目录（往往就是桌面）
   会被新建出 `artex.db`、`artex.db-wal`、`artex.db-shm` 三件套；
2. `--auto` / `ensure` 会在客户端旁边 **detached 拉起**一个 headless 实例
   （默认 `127.0.0.1:8799`，空闲 10 分钟自退）；
3. 拉不起来时"回落本地 stdio"，于是又回到第 1 条，静默多出一个空库。

现在的实现只有三步：探活 → 报错或连上 → 转发。

```
artex mcp serve --stdio --addr http://127.0.0.1:8787
   ├─ Probe(控制台)            零副作用，只问 server/discover
   ├─ 不通 → stderr 说明 + 退出码 1（不建库、不降级、不起进程）
   └─ 通   → ProxyStdio(stdin/stdout ⇄ /mcp)
```

### 1.2 一键配置写进去的形态

TOML 方言（Codex）：

```toml
[mcp_servers.artex]
command = "C:\ARTEX程序\artex-rea.exe"
args = ["mcp", "serve", "--stdio", "--addr", "http://127.0.0.1:8787"]
env = { ARTEX_MCP_TOKEN = "<控制台「外部接入」页显示的 token>" }
```

JSON 方言（Cursor / Claude Code / VS Code / Gemini 等）默认走 HTTP 直连更省事：

```json
{ "mcpServers": { "artex": {
  "url": "http://127.0.0.1:8787/mcp",
  "headers": { "Authorization": "Bearer <token>" }
} } }
```

只有客户端不支持 HTTP 时才用 stdio；此时**地址与 token 必须一起写进配置**，
否则它会在自己的当前目录重新开一个空库——这正是要根除的形态。

### 1.3 三条"不再"

- **不再建库**：`mcp serve` 不 import `db`，不碰 SQLite，不写任何文件；
- **不再拉进程**：`mcpd.go` / `mcpbridge.go`（headless、detached、Job Object 脱逃、
  空闲自退、`ensure` 子命令）整组删除；
- **不再静默降级**：控制台不在线就是失败，退出码 1，stderr 写明"先启动控制台"。

## 2. 扫描：只读、固定路径、四态

`GET /api/harness` 扫的是本机已安装的 harness（32 家，编进二进制）：

- 状态：`linked`（配置里已有 artex）/ `configured`（有配置没 artex）/ `installed`（装了没配）/ `absent`
- `stale`：配置在、但既没有可执行文件也没有安装目录 → 多半是卸载残留
- 路径先展开 `~` / `$VAR` / `%VAR%` / `<profile>`；展开失败的仍然展示，标 `resolved=false`

不做全盘遍历，不执行客户端二进制（`--version` 会挂住）。

## 3. 一键配置：plan → diff → apply

```
POST /api/harness/plan   只算不写，返回每个文件的 diff
POST /api/harness/apply  备份 → 临时文件 → rename → 回读 sha256 → 回扫状态
```

- 五种方言：`mcpServers` JSON / JSONC（文本插入，保留注释）/ TOML 表 / YAML 映射 / Cordis 插件（DSH）
- 落盘前自检：JSON、JSONC 会再解析一遍，解不开就放弃写入并给出原因
- 护栏：只允许写用户目录或请求里显式给出的项目目录
- 分级：A 级（官方文档已核）可自动写；B 级只读探测并标「需实机确认」；C 级只给片段

## 4. 已知边界

- B 级 14 家的路径来自社区，写入按钮灰着，等实机确认
- 控制台版本过旧（没有 `/mcp` 路由）时，探活会失败并提示——stdio 不会再退回本地服务
- `task_pause` / `task_resume` 依赖控制台内存里的任务表，控制台重启后需要重新查询
- **控制台重启期间**：客户端工具调用会失败（连接被拒），客户端重连即可；
  这是"同时在线"约束的直接后果，属于预期行为
- token 轮换后，旧配置里的 `ARTEX_MCP_TOKEN` 立即失效，需要重新一键配置或手改

## 5. 指挥员模型：外部 AI 用 L3 做什么

L3 不是「更多工具」，而是一套**下达 / 观察 / 干预**的指挥接口。外部 AI 不直接操作目标，
它把目标拆成可验证的子目标交给 ARTEX，然后读证据。

| 动词 | 工具 | 说明 |
|---|---|---|
| 下达 | `task_create` | description 写范围与授权边界，goal 写验证目标，analysis_domain 选 web_security / software_re |
| 观察 | `artex_status` | **全局态势**：活动任务、运行/排队/暂停/结束计数、并发上限、任务列表 |
| 观察 | `task_status` | **单任务详情**：进度、资产计数、最近发现 |
| 干预 | `task_steer` | `mode=hint` 写提示节点（下一轮规划读到）；`mode=intent` 插一条高优先级意图 |
| 干预 | `task_pause` / `task_resume` | 叫停与继续，不删任何数据 |

### 5.1 为什么需要 `artex_status`

实测：外部 AI 连上 ARTEX 后，被问「ARTEX 能做什么 / 现在在干什么」时，会去翻本机源码和文档
——因为它没有一个权威的「我面前这个东西现在是什么状态」的入口。`artex_status` 就是这个入口；
`mcpInstructions()` 里也明写了「不要读源码，调这个」。

### 5.2 `task_steer` 的两种落点

- `hint`（默认）：`Store.AddNode(KindHint, …, source="human")`，等价于控制台里的「提示」命令。
  不改变优先级，planner 下一轮规划时读到。
- `intent`：`Store.AddIntent(summary, priority=9, source="human")`，等价于人工插队，planner 优先处理。

两者都不暂停任务；要停下用 `task_pause`。

### 5.3 边界

- 两个新工具都是 L3，只存在于**控制台进程**。stdio 转发拿到的就是控制台的工具面，
  因此 L3 是否可见，完全看控制台的档位开关。
- `task_steer` 只对活着的任务有效；控制台重启后任务表需要重新加载。
- 指挥员模型的前提是 instructions 生效：客户端必须真的读取 `initialize.instructions`（绝大多数支持）。
