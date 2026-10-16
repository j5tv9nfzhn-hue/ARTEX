<div align="center">

# ARTEX 简体中文版

**由 LLM 多 Agent 自主执行渗透测试的系统**（Go 后端 + Next.js 前端）

界面、报告、摘要、对话及后端消息**统一使用简体中文**，本仓库不再提供其他语言界面。界面语言属于浏览器本地偏好；报告、摘要、对话与后端消息的输出语言由服务端设置管理，固定为简体中文。历史遗留的其他语言配置一律回退为简体中文。


[![ci](https://github.com/j5tv9nfzhn-hue/ARTEX/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/j5tv9nfzhn-hue/ARTEX/actions/workflows/ci.yml) [![detections](https://github.com/j5tv9nfzhn-hue/ARTEX/actions/workflows/detections.yml/badge.svg?branch=main)](https://github.com/j5tv9nfzhn-hue/ARTEX/actions/workflows/detections.yml) [![license: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

</div>

---

> ## 🚨 安全与滥用警告：请先阅读
>
> **仅允许在自有或明确授权的环境中，以学习、防御和检测为目的使用。**
>
> ARTEX 可自主组织侦察、工具执行和多阶段测试，滥用可能造成严重损害。
>
> ### 关于本项目的来源与真实事件
>
> - **国内原作者：** ARTEX 由国内开发者 **Autumn-27** 创建并开源（[Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)），Agent 能力依赖其 **[Autumn-27/norma](https://github.com/Autumn-27/norma)** SDK。本仓库是该项目的韩语本地化分支 `jiwoochris/artex-ko` 的简体中文分支。
> - **已发生的真实滥用事件：** 2026 年 10 月，韩国多家媒体（经由本项目韩语分支的安全告知记录）报道称，ARTEX 被用于针对韩国境内金融机构的个人信息窃取攻击，涉嫌相关材料已被调查机关获取，相关刑事调查在进行中。该事件是本项目被滥用的公开记录，**本仓库仅转述并保留该记录，不代表对其细节的独立核实**。同一时期亦有面向其他地区金融机构的类似指控报道。保留这段记录的唯一目的，是让防御方理解自主 AI 攻击的真实后果，并据此建立检测与阻断能力。
> - **本仓库的立场：** 本仓库不提供任何攻击方法、目标清单或利用建议，只保留技术说明、防御指南与检测规则。任何以本仓库内容为依据对未授权目标实施扫描、探测或利用的行为，由行为人自行承担全部法律责任。
>
> - **未经授权的使用可能构成犯罪。** 没有所有权或明确书面授权，不得对任何系统扫描、探测或利用漏洞。在中华人民共和国大陆地区，未经授权侵入、非法控制计算机信息系统，可能触犯《刑法》第二百八十五条、第二百八十六条；同时涉及《网络安全法》《数据安全法》《个人信息保护法》。跨境使用还需遵守行为地与数据主体所在地的法律。
> - **不要将他人的真实服务作为实验目标。** 请使用自有、本地隔离的 OWASP Juice Shop、DVWA 等有意设计的测试环境；真实目标必须事先取得书面许可，并约定范围、时间窗口与数据处理方式。
> - **从防御视角阅读。** 本仓库提供[自主 AI 防御与检测指南](docs/defense.md)、指标、规则与加固清单。
>
> 若不同意下方[使用限制与免责声明](#许可证与免责声明)，请勿下载或使用。

ARTEX 最初由国内开发者 Autumn-27 创建并以 AGPL-3.0 开源；`jiwoochris/artex-ko` 是其韩语本地化分支；**本仓库是该韩语分支的简体中文分支**。上游在 2026-10 记录为无法公开访问，链接保留用于来源归属，不保证远端当前可用。

系统将目标分解为意图，执行真实工具，将资产与发现写入图结构。Next.js 静态前端可内嵌到 Go 单二进制，数据存储于 SQLite（单文件，无需数据库服务）。

## 使用范围与法律告知

**仅对自有或获得明确书面授权的目标使用。** 超出授权范围的扫描、探测、利用可能违法。

- 中华人民共和国大陆地区：《刑法》第二百八十五条（非法侵入计算机信息系统罪）、第二百八十六条（破坏计算机信息系统罪）；《网络安全法》第二十一条；《数据安全法》；《个人信息保护法》。非法获取、买卖、提供公民个人信息还可能触犯《刑法》第二百五十三条之一。
- 测试中收集、暴露的个人信息受《个人信息保护法》约束，遵循最小必要原则。即使已获测试许可，也应谨慎处理访问、保存、删除与销毁。
- 跨境测试还需同时满足行为地、数据主体所在地及数据出境的相关合规要求。
- 使用目的应为学习、研究与隔离实验。对真实系统操作前，取得书面许可，并约定目标、方法、时间窗口与数据处置方式。

详见[许可证与免责声明](#许可证与免责声明)。使用本工具即表示已阅读并接受相关条件。

## 分支沿革与简体中文重构

### 分支沿革

```
Autumn-27/ARTEX（国内原作者，中文原创）
        └─ jiwoochris/artex-ko（韩语本地化分支）
                └─ j5tv9nfzhn-hue/ARTEX（本仓库 · 简体中文分支）
```

本仓库 **fork 自韩语分支** `jiwoochris/artex-ko`，并在其基础上完成中文重构：移除全部韩文内容，界面与产物统一为简体中文。

### 重构说明

本分支将项目自有的韩文文案、注释、提示和说明统一为简体中文，而非只替换少量页面或设置默认 locale。

- **中文唯一：** 界面、报告、事实摘要、最终报告、对话、系统消息与后端日志文案均为简体中文；界面不再提供语言选择器，英文与西班牙语词库一并移除。
- **技术语义保留：** 保留内部推理提示词的角色、判断与工具行为；原有中文行为正文不作无关改写。
- **协议不变：** API/JSON 字段、工具名、枚举、SQL、命令、payload、URL、原始日志及用户证据不因翻译改变。
- **语言回退：** 历史遗留的其他语言代码（`ko`/`en`/`es`）与未知值统一回退为简体中文，不再产生韩文或外语产物。
- **防回归：** 源码文本、词库、脚本、文档及静态 UI 通过残留语言检查；格式占位符、错误码与审批解析有回归测试。

详细边界见[贡献指南](CONTRIBUTING.md#本地化原则)，上游同步见[维护手册](MAINTAINING.md)。

## ARTEX 60 秒动态展示

<p align="center">
  <a href="showreel/ARTEX-Showreel-60s.mp4">
    <img src="showreel/poster.jpg" width="960" alt="点击封面查看或下载 ARTEX 60 秒动态展示视频">
  </a><br>
  <a href="showreel/ARTEX-Showreel-60s.mp4">查看 / 下载完整 60 秒 MP4</a> ·
  <a href="showreel/README.md">交互播放器、内容简介与制作说明</a>
</p>

这是一支 60 秒、1920 × 1080、60 fps 的无缝循环动态设计短片：一条分析线从纸面铅笔线演化为蓝图几何，再成为黑色全息空间中的光束，串起 ARTEX 的授权安全测试流程。影片含 120 BPM 原创配乐与自动同步音效；交互式播放器、完整分镜和可复现导出步骤见 [`showreel/`](showreel/)。

> **Artex modified open-source reel, full motion graphic + original score included.**
>
> It coded every frame (HyperFrames + GSAP), wrote the background music in Python and synced all sound effects automatically.
>
> The audit panel and visualization art in “Creating” scene is real generated output: Artex exported assessment logs rendered inside DaVinci Resolve on the same day.
>
> Details ⤵️

## 快速开始（Windows 二进制）

本项目只构建并发布 **Windows** 可执行程序（amd64 与 arm64），不提供 Linux/macOS 二进制，也不发布 Docker 镜像。

### 方式一：下载发布包

到 [Releases](https://github.com/j5tv9nfzhn-hue/ARTEX/releases) 下载 `artex-<版本>-windows-<架构>.zip`，解压后得到：

- `artex.exe` —— 程序本体（前端已内嵌）
- `start.bat` —— 启动监护脚本（页面一键更新依赖它重启进程）
- `skills/`、`config.example.json`、`README.md`

```bat
copy config.example.json config.json   & rem 可选：指定数据库文件位置，留空自动创建 artex.db
start.bat                              & rem → http://localhost:8787
```

首次访问 `http://localhost:8787/setup` 初始化管理员密码；设置令牌打印在启动日志中。

### 方式二：源码构建

前置依赖：Go 1.26（以 `go.mod` 为准）、Node.js 22 LTS。

```bat
cd web && npm ci && npm run build:static && cd ..
rm -rf server/webui/dist && mkdir -p server/webui/dist
cp -a web/out/. server/webui/dist/
CGO_ENABLED=0 go build -tags embedui -o artex.exe ./cmd/artex
copy config.example.json config.json
start.bat
```

或直接使用打包脚本（自动完成前端构建、内嵌与 zip 打包）：

```bash
./build.sh --release          # 三端（Linux/macOS/Windows）amd64/arm64，产出 dist/artex-<版本>-<os>-<arch>.zip
```

- `npm ci` 需要 devDependencies，例如 `@tailwindcss/postcss`。若环境为 `NODE_ENV=production`，使用 `npm ci --include=dev`，避免构建时缺包。
- 正式启动使用 `start.bat`，不要直接运行 `artex.exe` —— 监护脚本负责在一键更新或回滚退出后重新拉起进程。
- 数据库文件位置通过 `config.json` 的 `database.path` 或环境变量 `ARTEX_SQLITE_PATH` 配置（留空则在配置文件旁自动创建 `artex.db`），见下节。

## 配置

数据库只有 SQLite：一个数据库文件，无需安装、无需服务。可在 `config.json` 中指定位置，`ARTEX_SQLITE_PATH` 优先：

```json
{
  "database": { "path": "" },
  "language": "zh"
}
```

`database.path` 留空时，首次启动会在 `config.json` 旁自动创建 `artex.db`；完整注释与示例见 [config.example.json](config.example.json)。

LLM 可在环境变量或“LLM 设置”页面配置，相关环境变量包括 `ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`ARTEX_LLM_PROVIDER`、`ARTEX_LLM_MODEL`、`ARTEX_LLM_BASE_URL`、`ARTEX_LLM_PROXY`。OpenAI 兼容供应商可使用自己的 Base URL。

成本与效率相关的运行设置（新建任务默认 Token 预算 `default_task_token_budget`、模型价目表 `price_table`、LLM 重试策略 `llm_retry_policy`）在「系统配置」页填写，说明与默认值见[成本效率优化实施记录](docs/成本效率优化-实施记录.md)。

每个任务的 Worker 并发数在系统设置中配置，默认 3。`-addr` 控制 Web/API 地址，`-proxy` 控制流量记录代理，例如 `./start.sh -addr :8787 -proxy :8788`；默认 Web/API 仅绑定 `127.0.0.1:8787`。

输出语言固定为**简体中文**。服务端配置的优先级为 **`ARTEX_LANG` > `config.json.language` > `zh`**，数据库中的运行时输出语言设置在服务启动后优先。控制台日志使用环境/文件默认语言，浏览器界面语言通过本地 cookie/localStorage 保存，两者不是同一个配置，但取值一致。

### 模型选择与输出语言

输出语言依赖 `agent/prompt.go` 的 `langDirective()` 提示约束，而不是对模型回复作字符级硬过滤。命令、代码、工具名与原始证据不应被翻译。

- 选择能力合适的模型，结构化字段、短摘要仍可能受角色和上下文影响而混入英文。可在任务中明确要求用简体中文生成自然语言内容。
- 韩语分支曾在隔离沙箱比较 `claude-opus-4-8`、`gpt-4o` 与 `gpt-4o-mini` 的输出语言表现，记录了不同角色的语言漂移。该历史比较不是当前中文输出的模型端到端验证，不能当作质量保证。
- 推理型模型可能把较小的响应预算用于内部推理，导致最终回复为空。应按模型实际要求设置足够的 `max_tokens`。
- 韩语分支记录 `gpt-4o` 的响应上限为 16,384，而兼容请求可能默认填写 32,768，从而发生 `400 (max_tokens is too large)`。遇到该错误时核对供应商和模型限制，并在 LLM profile 中设置合适上限；不要假设所有兼容模型具有相同限制。

### 首次运行保护（设置令牌与绑定地址）

管理员用户名固定为 `ARTEX`，首次通过 `/setup` 创建密码。

- 无密码时，启动日志打印设置令牌。`/setup` 和 `POST /api/auth/init` 的 `setup_token` 必须匹配，错误尝试限流。令牌不通过公开状态接口返回。
- 无人值守部署可设置 `ARTEX_SETUP_TOKEN` 固定令牌，或用至少八字符的 `ARTEX_ADMIN_PASSWORD` 直接初始化。
- 默认仅监听回环地址。开放到其他设备前先完成初始化，并优先部署 HTTPS；非回环且未初始化时会有启动警告。
- 修改密码会使旧 JWT 会话在数秒内失效。若恢复泄露的管理员账户，还需检查自定义工具、MCP、LLM 与通知配置。
- 忘记管理员密码时，可在本机运行仓库根目录的 `reset-password.sh`：脚本定位 `artex`/`artex.exe`（或回退 `go run ./cmd/artex`）调用 `-reset-password` 重写密码哈希，无需数据库服务与 psql；也可直接运行 `artex -reset-password`。
- CORS 允许列表为 `ARTEX_CORS_ORIGINS`，以逗号分隔；内嵌 UI 同源通常无需填写，开发默认允许 `http://localhost:5173`。

### 反向代理部署（HTTPS / 只开放 443）

Web、API、SSE 共用后端端口。生产环境可只公开 443，将 8787 留在内部；同源 SSE 通常无需设置 `NEXT_PUBLIC_SSE_BASE`。

**必须关闭 SSE 代理缓冲**，否则浏览器连接成功但活动流持续等待。Nginx 示例见 [README.zh.md](README.zh.md#反向代理部署https--只开放-443)。

## 系统架构

系统 = **Go 单二进制后端（内嵌 Next.js 静态前端）+ SQLite 单文件 + [norma SDK](https://github.com/Autumn-27/norma) 多智能体内核**。关键设计：**双图数据底座**（探索图 + 资产图，经锚点互连）、**事件驱动闭环**（图变化唤醒 Planner）与**成本与可靠性控制面**（缓存 / 预算 / 通知 / 记账）。以下用六个视角完整展开，可直接按序阅读。

### ① 全景架构：一图纵览（六层 + 外部依赖）

```mermaid
flowchart TB
  subgraph L1["① 访问与界面层"]
    direction LR
    BR["浏览器 · 作战台单页"]
    FED["Next.js 静态产物（go:embed 内嵌）<br/>仪表盘 / 任务 / 发现 / 资产覆盖图<br/>探索图谱 / 流量 / 审批 / 设置 · ⌘K"]
  end
  subgraph L2["② 服务接入层 · server"]
    direction LR
    API["REST /api + SSE 事件流"]
    AUTH["JWT 鉴权 · 审计事件（CallID）"]
  end
  subgraph L3["③ 编排核心层 · server"]
    direction LR
    MGR["Manager<br/>任务生命周期"]
    ENG["Engine<br/>规划循环 + N 执行器"]
    SCH["Scheduler<br/>触发器 · 心跳"]
    INV["Intercept<br/>审批门"]
    NOT["Notify<br/>六渠道通知"]
  end
  subgraph L4["④ 智能体层 · agent"]
    direction LR
    GOALS["goals<br/>目标分解"]
    PLAN["Planner<br/>唯一意图创建者"]
    WORK["Worker ×N<br/>单意图执行"]
    OTHER["Reporter · Retester · SideQuestion<br/>报告 · 复测 · 旁路问答"]
  end
  subgraph L5["⑤ 内核层 · norma SDK"]
    direction LR
    HK["harness 会话循环<br/>提示缓存断点 · 重试策略"]
    CP["压缩面<br/>micro / auto / 冷摘要"]
    TL["工具调度<br/>域工具 + 默认工具 + MCP + Skills"]
  end
  subgraph L6["⑥ 数据与存储层"]
    direction LR
    SQL["SQLite 单文件<br/>双图 · 台账 · 配置 · 检查点"]
    DISK["磁盘产物<br/>blobs · runs · 快照"]
  end
  subgraph L7["⑦ 外部依赖"]
    direction LR
    LLM["LLM 供应商<br/>多配置 · 角色回退链"]
    MCPE["MCP 服务器 · 技能包"]
    TGT["授权目标环境"]
  end
  BR -->|"① HTTPS / SSE"| API
  API --> MGR
  MGR -->|"② 起停任务"| ENG
  SCH -->|"③ 触发"| ENG
  ENG -->|"④ 派意图"| PLAN
  PLAN -->|"⑤ frontier 队列"| WORK
  WORK -->|"⑥ 调用工具"| TL
  WORK -->|"高风险工具"| INV
  INV -.->|"批准后继续"| WORK
  PLAN --> HK
  WORK --> HK
  OTHER --> HK
  HK -->|"⑦ 模型调用"| LLM
  TL -->|"⑧ 实际动作"| TGT
  TL -.-> MCPE
  PLAN --> SQL
  WORK --> SQL
  ENG --> SQL
  NOT --> SQL
  ENG -->|"发现 / 终态"| NOT
  TL -.->|"产物落盘"| DISK
  classDef c1 fill:#2F80ED33,stroke:#2F80ED
  classDef c2 fill:#27AE6033,stroke:#27AE60
  classDef c3 fill:#F2994A33,stroke:#F2994A
  classDef c4 fill:#9B51E033,stroke:#9B51E0
  classDef c5 fill:#00A3A333,stroke:#00A3A3
  classDef c6 fill:#6B728033,stroke:#6B7280
  classDef c7 fill:#D3545433,stroke:#D35454
  class BR,FED c1
  class API,AUTH c2
  class MGR,ENG,SCH,INV,NOT c3
  class GOALS,PLAN,WORK,OTHER c4
  class HK,CP,TL c5
  class SQL,DISK c6
  class LLM,MCPE,TGT c7
```

| 层 | 职责 | 代码入口 |
| --- | --- | --- |
| **界面** | 作战台单页 + 7 套主题预设 + ⌘K 命令面板 | `web/src/app/`、`web/src/styles/presets/` |
| **接入** | REST + SSE + JWT；审计事件以 `CallID` 贯穿 | `server/server.go` |
| **编排** | 任务生命周期、规划循环、触发器/心跳、审批门、通知引擎 | `server/manager.go`、`server/engine.go`、`server/scheduler.go` |
| **智能体** | 目标分解、意图规划（唯一创建者）、执行器、报告/复测/旁路问答 | `agent/planner.go`、`agent/worker.go`、`server/orchestration.go` |
| **内核** | 会话循环、提示缓存、上下文压缩、工具调度；MCP 与技能接入 | norma SDK（`agent/assembly.go` 接线） |
| **数据** | 双图、台账、配置、检查点；SQLite 单文件（modernc 纯 Go 驱动，无 CGO） | `db/schema_sqlite.sql`、`db/` |
| **观测与出口** | Token 台账与计价、进度指标、审计指标、六渠道通知 | `llmrec/`、`server/pricing.go`、`metrics/`、`notify/` |

支持子系统作为横切能力接入以上各层：记录型 MITM 流量代理与 DNS/HTTP 富化、证据存储（blobs）、工具审批门（guard / intercept）、通知路由（Markdown · HTML · Telegram · 飞书 · 钉钉 · Webhook）、检查点/回滚，以及内核侧的技能（Skills）、MCP 与记忆/报告能力。

### ② 任务运行闭环：从触发到收尾

引擎是事件驱动的闭环：图一变就唤醒 Planner，Planner 派意图，Worker 领意图执行并写回，写回又触发下一轮——直到目标被证明（`prove_goal`）。预算闸、审批门与收尾通知都挂在这条闭环上。

```mermaid
sequenceDiagram
  autonumber
  participant TR as 触发源（人工 / 调度器 / 心跳 / 图变化）
  participant EN as Engine（任务循环）
  participant PL as Planner
  participant QU as frontier 意图队列
  participant WK as Worker
  participant GT as 审批门 Intercept
  participant DB as 双图 + activity + 台账
  participant NT as 通知引擎
  TR->>EN: 唤醒（去抖动合并）
  EN->>EN: 预算核算 / 心跳空转跳过判定
  EN->>PL: 启动一轮规划（新会话 + 共享 todolist）
  PL->>DB: 读态势（紧凑注入，工具按需查全量）
  PL->>QU: 派 0..N 个意图（带资产锚点）
  Note over PL,QU: 无新方向时派 0 个并结束本轮
  WK->>QU: 领取一条意图
  WK->>DB: 取初始资产与过程线索
  WK->>GT: 调用高风险工具
  GT-->>NT: 等待人工审批（推送审批事件）
  GT-->>WK: 批准 / 拒绝后继续
  WK->>DB: 写回 fact / asset / finding + 每步 activity
  DB-->>EN: 图变化（再次唤醒，闭环）
  EN->>NT: 发现 / 任务终态（完成 / 失败 / 超时 / 暂停）
  Note over EN,PL: 超预算 → 自动暂停；证明目标 → 收尾 settle
  Note over WK: 运行中可接收人工引导（steer）与终止（kill）
```

### ③ 数据底座：双图 + 存储拓扑

“目标是什么”和“测到了什么程度”分别存储，经**锚点**双向追溯。Planner 只读态势、只在有未覆盖方向时派意图；Worker 只领一条意图、把新资产/事实/发现写回两图后结束。

```mermaid
flowchart LR
  subgraph DOM["领域模型"]
    direction TB
    subgraph EG["探索图 · 每任务独立（推进链）"]
      direction TB
      G["goal 目标"] -->|spawns| I1["intent 意图"]
      I1 -->|yields| F1["fact 事实"]
      F1 -->|derived_from| I2["intent 下一步"]
      I2 -->|proves| FD["finding 发现"]
      H["hint 提示"]
    end
    subgraph AG["资产图 · 全局共享（真值库）"]
      direction TB
      RD["root_domain"] --> SD["subdomain"]
      SD --> SV["service"]
      SV --> EP["endpoint"]
    end
    AN["锚点 exploration_anchors<br/>意图/事实/发现 ↔ 资产 双向追溯"]
  end
  subgraph STO["SQLite 单文件（modernc · 无 CGO · 启动幂等迁移）"]
    direction TB
    S1["运行态：tasks · intents · activity<br/>checkpoints · compaction_state"]
    S2["双图：exploration_nodes / edges / anchors<br/>assets / companies / task_scope"]
    S3["账本与配置：llm_usage / llm_records · settings<br/>llm_profiles · credentials / mcp_env"]
  end
  subgraph FSG["磁盘产物"]
    direction TB
    D1["blobs：流量与证据"]
    D2["runs：会话工作目录"]
  end
  I1 -. 锚定 .-> AN
  FD -. 锚定 .-> AN
  AN -. 绑定 .-> SD
  AN -. 绑定 .-> EP
  EG ==> S2
  AG ==> S2
  STO -.-> FSG
```

锚点机制同时支撑**资产测试覆盖度**与**资产覆盖图**（范围内资产 + 已测高亮）。

### ④ 协作机制：过程共享 + 跨轮 todolist

两条自主性机制让多 Worker、跨轮次的探索保持连贯：

- **过程级信息共享**：有价值的观察未必写成正式 fact。`search_all_worker_traces(q)` / `list_worker_traces` / `get_worker_trace(intent_id, step_ids)` 让 Worker 能检索同任务其他意图的执行过程（自动排除自己），信息以“执行过程”为粒度流动，边界不变。
- **跨轮共享 todolist**：串行利用链（发现注入点 → 取凭据 → 横向 → 提权）存在前置依赖，不能一次并行派发。todolist 按任务持久化，每轮只对“前置已完成、依赖 fact 已存在”的下一步派意图，并随进展更新状态——事件驱动 + 无状态会话下依然稳定推进、不重复、不错序。

```mermaid
flowchart TB
  subgraph SHARE["过程级信息共享（同一任务的 worker 之间）"]
    direction LR
    WA["worker A · 意图 #12"] -->|"每步 activity"| ACT[("activity 过程库")]
    WB["worker B · 意图 #34"] -->|"每步 activity"| ACT
    ACT -->|"1) search_all_worker_traces(q)"| WC["worker C · 意图 #56"]
    WC -->|"2) get_worker_trace(intent_id, step_ids)"| ACT
    ACT -.->|"返回命中步骤（排除自己）"| WC
  end
  subgraph TODO["跨轮共享 todolist（按任务持久化）"]
    direction LR
    T1["1 注入点 [完成]"] --> T2["2 取凭据 [进行中]"]
    T2 --> T3["3 横向 [待前置]"]
    T3 --> T4["4 提权 [待前置]"]
  end
  R1["第 1 轮唤醒 · 派意图①"] --> T1
  R2["第 2 轮（①产出 fact）· 派意图②"] --> T2
```

### ⑤ 成本与可靠性控制面

每一层都刻意收敛“重复计费”的可能性：输入侧瘦身、过程侧守门、统一观测与出口。

```mermaid
flowchart LR
  subgraph IN["输入侧瘦身（每轮重复计费部分）"]
    direction TB
    C1["提示缓存断点<br/>system 整段 ephemeral 缓存"]
    C2["工具输出上限 12k 字符<br/>超限落盘 + 指针"]
    C3["注入瘦身<br/>planner ≤16KB · worker ≤12KB<br/>资产 30 条紧凑视图"]
    C4["心跳空转跳过<br/>有在跑意图且无新变动不启轮"]
  end
  subgraph RUN["过程侧守门"]
    direction TB
    R1["意图重试 ≤4 次<br/>指数退避 3→24s · 硬失败 2 次"]
    R2["触发器间隔 ≥60s"]
    R3["任务 Token 预算闸<br/>每轮收尾核算 → 超限自动暂停"]
    R4["上下文压缩<br/>micro / auto / 冷摘要 800tok"]
  end
  subgraph OBSV["观测与出口"]
    direction TB
    O1["Token 台账 + 价格表<br/>按任务 / 模型 / 角色计价"]
    O2["进度指标<br/>首发现耗时 · 产出速率 · 等待审批"]
    O3["通知六渠道<br/>发现 / 任务终态 / 等待审批"]
    O4["审计与指标<br/>AuditEntry.CallID · /api/metrics"]
  end
  IN --> RUN --> OBSV
```

对应落点：提示缓存 `agent/deferred.go`；预算闸 `server/engine.go` + `tasks.token_budget`；计价 `server/pricing.go`（设置 `price_table`）；通知 `notify/`；配置项与完整清单见[成本效率优化实施记录](docs/成本效率优化-实施记录.md)。

### ⑥ 单二进制部署与交付

```mermaid
flowchart LR
  PKG["发布包<br/>artex.exe · start.bat · skills/ · config.example.json"] --> BIN["单进程<br/>内嵌前端 + REST/SSE + 引擎 + 记录代理"]
  BIN --> DATA["数据目录<br/>SQLite 单文件 · blobs/ · runs/ · 日志"]
  USER["浏览器"] -->|"http://127.0.0.1:8787（或反向代理 HTTPS）"| BIN
  BIN -->|"模型调用"| PLLM["LLM 供应商（Anthropic / OpenAI 形态）"]
  BIN -->|"授权范围内"| PTGT["测试目标"]
```

发布产物、编译标志与运行时文件落点详见[构建与产物](docs/构建与产物.md)。

## 防御与检测资料

- [自主 AI 防御与检测指南](docs/defense.md)：行为差异、可观察指纹、入口加固、WAF/SIEM/认证日志、响应清单及通报渠道。
- [部署用检测规则](detections/README.md)：Sigma 主机/日志规则、关联规则与 Suricata 网络规则。
- [ATT&CK 覆盖率](detections/attack/README.md)：仅来自实际规则标签，不添加推测技术。
- [CSV/MISP 指标](detections/indicators/README.md)：每项都注明来源、对应规则和局限。
- [只读主机 triage](detections/triage/README.md)：无需额外依赖，检查主机、端口、代理与数据库结构等本地线索（数据库检查仍针对上游 PostgreSQL 布局，本分支 SQLite 暂未覆盖）；结果是分类线索，不是攻击归属证明。
- [可复现检测测试](detections/tests/README.md)：规则、层、指标和 triage 自测遵循同一贡献契约。

新增规则请遵循[检测规则与测试贡献契约](CONTRIBUTING.md#检测规则与测试贡献)，不得将泛化行为误称 ARTEX 专属指纹。

## 项目文档

- [开发总清单](docs/开发总清单.md)：`计划.md` 198 条（除 97–130 反检测外）的全面落实执行表与批次状态。
- [修复台账](docs/修复台账.md)：198 条的核实结果、PostgreSQL → SQLite 迁移记录与本轮落地条目。
- [成本效率优化实施记录](docs/成本效率优化-实施记录.md)：省 token、产出可见、操作顺手的改动、配置项与落点。
- [norma SDK 优化补丁清单](docs/norma-SDK优化补丁清单.md)：需在 SDK 上游立项的 6 项优化建议。
- [架构分层](docs/架构分层.md)：后端分层现状、边界约定与本轮新增模块的层级归属。
- [构建与产物](docs/构建与产物.md)：构建入口、编译标志、发布包内容与运行时文件落点。
- [UTF-8 全链路审计](docs/utf8-audit.md)：编码风险点（控制台代码页、子进程 stdio）与修复、回退开关。
- [XSS 全链路审计](docs/xss-audit.md)：前端注入面、后端凭据掩码与 `Cache-Control` 加固记录。
- [漏洞多流量证据](docs/finding-traffic-evidence.md)：关联流量的工具、API、存储、导出与归档设计。

## 开发

```bash
./dev.sh  # 后端 :8787、记录代理 :8788、前端 :5173
```

- 后端：`go run ./cmd/artex`，无 `embedui` tag 时不内嵌前端。
- 前端：`cd web && npm run dev`，`/api` 反向代理到后端。
- Mock 预览：`cd web && NEXT_PUBLIC_MOCK=1 npm run dev`，不需要真实后端，不进行真实扫描。
- 回归：`go test ./...`；DB 测试自动使用包内临时 SQLite 库，无需外部数据库；个别上游 PostgreSQL 专属用例显式跳过并注明原因。
- 前端：在 `web` 执行 `npm test`、`npm run check`、`npm run build:static`。
- 语言与文档：`python3 -I scripts/check-language.py --source`、构建后 `--web`、`python3 -I scripts/check-doc-links.py`。

更多开发说明见 [README.zh.md](README.zh.md#开发)及[贡献指南](CONTRIBUTING.md)。分支变更见 [CHANGELOG.md](CHANGELOG.md)。

## 许可证与免责声明

### 开源许可证与分支声明

项目使用 **GNU Affero General Public License v3.0（AGPL-3.0）**，全文见 [LICENSE](LICENSE)。可使用、修改、分发，但衍生作品须遵循相同许可。若修改后通过网络服务提供给用户，应向这些用户提供对应完整源码。

- **上游原创：** 国内开发者 Autumn-27 的 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)，以 AGPL-3.0 开源。
- **本仓库的 fork 关系：** 本仓库 fork 自韩语分支 [jiwoochris/artex-ko](https://github.com/jiwoochris/artex-ko)，并在其上完成简体中文重构；**韩语内容已从本仓库移除**。
- **归属：** 本仓库保留上游及韩语分支原作者的署名与 AGPL-3.0 许可，不因移除某一语言版本而改变版权归属。

> 开源许可证本身不限制使用目的。以下使用限制和免责声明是原作者的额外使用告知，不应混同为修改了 AGPL 条款。

### 使用限制

- 用于阅读源码、学习研究及自有隔离环境验证。
- 没有所有权或明确书面授权，不对网站、线上服务或联网系统扫描、探测、利用、攻击。
- 严禁非法侵入、数据盗窃、DoS 及其他破坏或犯罪活动。
- 遵守适用国家/地区的网络安全、数据保护与计算机犯罪法律；在中华人民共和国大陆地区包括《网络安全法》《数据安全法》《个人信息保护法》及《刑法》第二百八十五条、第二百八十六条等。

### 免责声明

软件按“现状（AS IS）”提供，不作明示或默示保证。原作者与贡献者不承担因使用本工具产生的直接或间接损害、数据丢失、系统损坏或法律争议责任。下载、安装、使用表示已阅读并理解相关告知。

**使用行为的全部法律责任与后果由使用者本人承担。**

## 原始项目与来源

- **国内原作者 / 上游原创：** [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)（中文原创，AGPL-3.0）。
- **韩语分支（本仓库的 fork 源）：** [jiwoochris/artex-ko](https://github.com/jiwoochris/artex-ko)。
- **Agent SDK：** [Autumn-27/norma](https://github.com/Autumn-27/norma)。
- 上游中文技术原稿：[README.zh.md](README.zh.md)。
- 上游演示：[artex-demo.vercel.app](https://artex-demo.vercel.app/)。

上述远端在 2026-10 被记录为无法公开访问；保留链接用于来源归属与历史对照，不保证当前可访问，也不以此断言永久删除。
