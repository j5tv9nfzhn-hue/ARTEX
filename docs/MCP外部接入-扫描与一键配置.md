# 外部接入：扫描、一键配置与 L3

面向两个问题：**别的 harness 怎么挂上 ARTEX**，以及**挂上以后 L3 为什么能跑在「没有常驻进程」的前提下**。

## 1. 三种接法

| 接法 | 端点 | 需要控制台常驻 | 档位 | 谁承载 |
|---|---|---|---|---|
| HTTP | `http://127.0.0.1:8787/mcp` | 是 | L0–L3 | 控制台进程 |
| stdio（`--auto`） | `artex-rea.exe mcp serve --stdio --auto` | 仅 L3 需要 | L0–L3 | 本进程跑 L0–L2，L3 转发控制台 |
| stdio（不带 `--auto`） | `artex-rea.exe mcp serve --stdio` | 不需要 | L0–L2 | 本进程 |

`--auto` 是**一键配置默认写进去的形态**：

```toml
[mcp_servers.artex]
command = "C:\\ARTEX程序\\artex-rea.exe"
args = ["mcp", "serve", "--stdio", "--auto"]
```

不带 `--auto` 时 L3 会被静默丢掉（设计如此：那时进程里没有引擎）。带上以后，L3 工具照常出现在 `tools/list`，调用时转发给控制台。

## 2. 为什么 L3 只转发给控制台，不转发给 headless

`task_create` 在 headless 实例上会往数据库插一行，然后**没有任何人去跑**——任务永远停在 `pending`。这是最坏的失败形态：看起来成功了。

引擎、调度、审批都在控制台进程（8787）里，所以：

- 控制台在跑 → 转发过去，任务真的会被跑起来；
- 控制台没跑 → **明确报错**，并说明「L3 需要控制台在运行」，不拉起 headless、不降级。

`artex mcp serve --stdio --auto` 的档位默认跟随控制台设置（`mcp_server_tiers`），可用 `--tiers` 覆盖。

## 3. 扫描：只读、固定路径、四态

`GET /api/harness` 扫的是本机已安装的 harness（32 家，编进二进制）：

- 状态：`linked`（配置里已有 artex）/ `configured`（有配置没 artex）/ `installed`（装了没配）/ `absent`
- `stale`：配置在、但既没有可执行文件也没有安装目录 → 多半是卸载残留
- 路径先展开 `~` / `$VAR` / `%VAR%` / `<profile>`；展开失败的仍然展示，标 `resolved=false`

不做全盘遍历，不执行客户端二进制（`--version` 会挂住）。

## 4. 一键配置：plan → diff → apply

```
POST /api/harness/plan   只算不写，返回每个文件的 diff
POST /api/harness/apply  备份 → 临时文件 → rename → 回读 sha256 → 回扫状态
```

- 五种方言：`mcpServers` JSON / JSONC（文本插入，保留注释）/ TOML 表 / YAML 映射 / Cordis 插件（DSH）
- 落盘前自检：JSON、JSONC 会再解析一遍，解不开就放弃写入并给出原因
- 护栏：只允许写用户目录或请求里显式给出的项目目录
- 分级：A 级（官方文档已核）可自动写；B 级只读探测并标「需实机确认」；C 级只给片段

## 5. 已知边界

- B 级 14 家的路径来自社区，写入按钮灰着，等实机确认
- 控制台版本过旧（没有 `/mcp` 路由）时，L3 转发会报「该地址返回 HTML，不是 MCP 端点」——升级控制台即可
- `task_pause` / `task_resume` 依赖控制台内存里的任务表，控制台重启后需要重新查询


## 6. 指挥员模型：外部 AI 用 L3 做什么

L3 不是「更多工具」，而是一套**下达 / 观察 / 干预**的指挥接口。外部 AI 不直接操作目标，
它把目标拆成可验证的子目标交给 ARTEX，然后读证据。

| 动词 | 工具 | 说明 |
|---|---|---|
| 下达 | `task_create` | description 写范围与授权边界，goal 写验证目标，analysis_domain 选 web_security / software_re |
| 观察 | `artex_status` | **全局态势**：活动任务、运行/排队/暂停/结束计数、并发上限、任务列表 |
| 观察 | `task_status` | **单任务详情**：进度、资产计数、最近发现 |
| 干预 | `task_steer` | `mode=hint` 写提示节点（下一轮规划读到）；`mode=intent` 插一条高优先级意图 |
| 干预 | `task_pause` / `task_resume` | 叫停与继续，不删任何数据 |

### 6.1 为什么需要 `artex_status`

实测：外部 AI 连上 ARTEX 后，被问「ARTEX 能做什么 / 现在在干什么」时，会去翻本机源码和文档
——因为它没有一个权威的「我面前这个东西现在是什么状态」的入口。`artex_status` 就是这个入口；
`mcpInstructions()` 里也明写了「不要读源码，调这个」。

### 6.2 `task_steer` 的两种落点

- `hint`（默认）：`Store.AddNode(KindHint, …, source="human")`，等价于控制台里的「提示」命令。
  不改变优先级，planner 下一轮规划时读到。
- `intent`：`Store.AddIntent(summary, priority=9, source="human")`，等价于人工插队，planner 优先处理。

两者都不暂停任务；要停下用 `task_pause`。

### 6.3 边界

- 两个新工具都是 L3，只存在于**控制台进程**（需要编排器 `s.m.pg`）。headless 与本地 stdio 都拿不到。
- `task_steer` 只对活着的任务有效；控制台重启后任务表需要重新加载。
- 指挥员模型的前提是 instructions 生效：客户端必须真的读取 `initialize.instructions`（绝大多数支持）。
