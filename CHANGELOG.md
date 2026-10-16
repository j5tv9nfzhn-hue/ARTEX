# 变更日志


记录本分支在上游 ARTEX 基础上增加的功能与维护改动，格式参考 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)。上游 0.3.x 及更早版本的发布历史与贡献者署名保留在 [CHANGELOG.zh.md](CHANGELOG.zh.md)，每项改动的详细依据可查看 Git 历史。

## [Unreleased] · 分支改动

### 安全

- **阻止首次运行时管理员账户被远程抢先初始化。** 原来未设置密码的实例可被先访问 `POST /api/auth/init` 的人初始化并获取 JWT（私密报告者：Dangel）。现在必须提供仅在服务端控制台显示的 `setup_token`，错误尝试有速率限制；新增 `ARTEX_SETUP_TOKEN`、`ARTEX_ADMIN_PASSWORD` 环境变量和 `/setup` 令牌输入框。
- **默认仅绑定 127.0.0.1。** `artex -addr` 默认由 `:8787` 改为 `127.0.0.1:8787`，Compose 使用 `${ARTEX_BIND:-127.0.0.1}`。需要其他设备连接时须显式设置 `-addr :8787` 或 `ARTEX_BIND=0.0.0.0`，并先完成初始化。无密码、非回环绑定会打印警告。
- **修改密码后废除旧 token。** JWT 签名密钥从密码哈希派生，UI、`reset-password.sh` 或直接数据库修改均使旧会话失效；升级后需要重新登录，UI 改密响应包含新 token，当前浏览器可继续使用。
- **CORS 从全部允许改为允许列表。** 根据 `ARTEX_CORS_ORIGINS` 返回跨域响应头，开发默认允许 `http://localhost:5173`。新增服务端八字符最小密码限制，以及带客户端地址的初始化、登录失败日志。

### 本地化（i18n）

- **本次将项目自有韩文统一重构为简体中文。** 覆盖 Go 后端消息、状态、工具摘要、模型输出指令、Next.js 页面与演示数据、注释、部署脚本、配置说明、文档和 GitHub 模板。
- **默认语言改为 zh。** 正式支持 zh/en/es，旧 ko 配置、cookie 和本地偏好归一为 zh；`web/messages/ko.json` 保留为与中文词库一致的兼容副本，不再提供新的韩文 UI 选项。
- **统一 Agent 输出语言。** 保留行为提示词、角色职责、命令和证据语义，以 `langDirective` 明确报告、事实摘要、最终总结和对话的输出语言；自定义收尾提示也追加选定语言约束。
- **同步中文消息协议。** 审批摘要解析与后端格式一致，保持工具名及 `pending_id`；Skill 已存在提示、通知、默认回退和语言切换测试同步更新，保留供应商返回的底层诊断原文。
- **增加语言回归检查。** `check-language.py` 扫描项目自有文本及静态 HTML 的韩文字母/音节，允许中文；新增 Python 和前端语言、审批回归测试并接入 CI。
- **保留既有多语言基础设施。** 原分支引入 next-intl、消息键提取、后端词库和输出语言设置，这些功能继续使用；新增消息须同步 zh/en/es 与 ko 兼容副本，不应重新硬编码到页面。

### 界面与主题

- **新增 7 套主题预设。** 在浅色/深色/跟随系统之上提供「默认、琥珀终端、粗野主义、深空指挥室、矩阵绿、柔和流行、柑橘」，偏好保存在浏览器本地；预设样式各自独立成文件。
- **字体自托管。** Geist、Space Grotesk、JetBrains Mono 随包分发，不请求外部字体服务，离线可用。
- **统一严重度色彩契约。** 收敛为 critical 红 → high 橙 → medium 琥珀 → low 灰蓝（修正旧版亮度倒挂），徽章、圆点、条带与图表色（亮/暗两套 hex）集中在一个事实源，页面不再各写一套。
- **命令面板与界面打磨。** 新增 ⌘K / Ctrl+K 命令面板（跳转、操作、搜索，⌘J 保留为旧版别名），以及侧边栏自定义、空状态、动效、活动流圆点、发现告警、LLM 配置徽章与 Agent 备用链可视化等组件。探索图重绘（小地图、节点类型配色与图例、节点进入动效、自适应缩放）；仪表盘新增可靠性面板（工具错误率、崩溃重放成功率、中断恢复率与按阶段分布）。

### 成本与效率（批次 E）

- **自动注入瘦身。** planner 每轮态势与 worker 首消息概览收紧条数并加字节硬上限（16KB / 12KB），摘要正文与触发消息限长；工具取数路径保持全量。工具输出进入历史的字符上限由 30000 收紧到 12000，超限全文落盘、可按需重读。planner 心跳空转跳过：心跳到点但无触发消息、无边沿变更且仍有在跑意图时，不再白烧一整轮规划 LLM。
- **system prompt 整段缓存。** 原来只有静态前缀进 Anthropic 缓存；现在整段 system（含延迟披露工具块）作为缓存块跨轮复用。
- **任务级 Token 预算硬闸。** 新增 `default_task_token_budget` 与任务级预算：累计输入+输出达到上限时自动暂停任务并在活动流留痕，恢复后继续累计。
- **LLM 重试可配置、意图重跑收敛。** 新增 `llm_retry_policy`（connect / empty / stream / breaker / intent 五层的次数与间隔），单 profile 可覆盖三层；`model_error` 收场的意图重跑限制为 4 次（硬失败 2 次）、指数退避。
- **价格表与费用视图。** 新增 `price_table`（模型名子串 → 单价，USD/百万 token），用量与成本视图按当前价格读时计算；不做内置价目，由用户自填。
- **Token 归因与进度指标。** 每次 LLM 调用按任务 / 意图 / worker / 配置 / 模型落账；任务新增首个发现用时、每小时产出速率与等待审批数指标（列表显示「等待审批」徽章）。
- **触发器与通知的成本护栏。** 定时触发器最小间隔 60 秒（创建/更新校验，存量按 60 秒调度并告警）；「报告撰写」触发器默认关闭；通知新增任务状态变化与等待审批事件（存量渠道 opt-in，新建渠道默认开启）。
- **steer 纠偏与诚实回滚文案。** 运行中的意图可直接接收人工纠偏，不暂停、不重跑；检查点回滚与一键回退的文案明确「不撤销目标侧副作用、数据库结构不回退」。

### 功能与可靠性

- **LLM「测试连接」独立轻量探测。** 只发一次最小补全（不带工具、系统提示与流式），成功判据包含协议结构校验；失败诊断内联 HTTP 状态码与供应商原文。
- **模型配置声明输入模态。** LLM 配置可声明 text / image / audio / pdf，路由时对不支持的附件显式报错、绝不静默丢弃；未声明时按模型名保守推断。
- **Agent LLM 备用链。** 每个 Agent 可在主配置之外配置有序备用链，主配置不可用时按序切换，界面展示备用链。
- **共享基础包与审计关联。** 新增 metrics / stageerr / fsutil 三个基础包（工具调用与错误按阶段计数、错误文案统一分阶段、文件写入通用工具）；guard 审计条目携带拦截记录的 ToolUseID（`call_id`），审计与审批可配对。
- **任务环境快照与检查点。** 任务准入时探测运行环境（OS / shell / 可用工具 / 工作目录）并注入 planner 与 worker 提示词；新增检查点时间线与回滚（平台侧记录），上下文压缩状态在重启后自校验。
- **凭据与 MCP 环境变量加密。** 新增集中凭据管理（可被多个模型配置引用）；凭据与 MCP 环境变量密文统一 `enc:v1:` 前缀，无前缀的存量明文继续按明文处理、无需迁移。

### 防御与检测资料

- **提供防御与检测指南。** [简体中文指南](docs/defense.md)保留旧文件名以兼容链接，与[英文版](docs/defense.md)包含相同的自主 AI 行为、可观测指纹、加固、规则与事件响应资料。
- **提供部署用规则。** [Sigma](detections/sigma/) 处理主机、日志与关联检测；[Suricata](detections/suricata/) 检测 enrich 探测和 norma WebFetch 的 User-Agent。
- **可视化 ATT&CK 覆盖率。** [Navigator 层](detections/attack/)仅使用规则实际 `attack.*` 标签，不增加推测技术。
- **标准化机器可读指标。** [CSV 和 MISP](detections/indicators/)逐行同步，规则支持的指标使用 `to_ids`，主机取证线索与可部署检测区分。
- **提供八类可复现测试。** Sigma 结构/转换、真实事件匹配、后端可移植性、SigmaHQ lint、Suricata 加载/触发、ATT&CK 一致性、指标与源码一致性、MISP/CSV 同步；恶意样本应触发，正常样本应静默。批量 runner、pre-commit 与 CI 执行相同契约。

### 仓库维护

- **保留安全与法律告知。** 仅允许自有或书面授权目标、隔离实验环境，说明韩国信息通信网络法、个人信息保护法及适用地区法律，不因语言重构削弱授权限制。
- **预览图片统一为中文演示截图。** 保留原作者署名和图片来源说明，不将历史演示截图伪称为本次生产验证（历史截图已于 2026-10-16 整体移除，见本节末条）。
- **完善维护与贡献指南。** [维护手册](MAINTAINING.md)涵盖上游同步、语言漂移、指标契约及发布前置条件；[贡献指南](CONTRIBUTING.md)说明验证范围和检测规则契约。
- **每次 push、PR 执行合并门禁。** [Go/数据库/前端/检测规则/语言检查](.github/workflows/ci.yml)合并在单工作流中执行，检测规则另由[独立工作流](.github/workflows/detections.yml)按路径触发。数据库测试按包使用独立的临时 SQLite 库（无需服务容器），内部链接检查只使用标准库、不依赖网络。
- **新增 Go lint 门禁。** [.golangci.yml](.golangci.yml) 以 `govet` 的干净基线起步（后续按清单逐项收紧），CI 与 [pre-commit](.pre-commit-config.yaml) 的 go 钩子使用同一配置，本地可复现；格式检查由 CI 的 gofmt 门禁（Go 1.26 工具链）唯一把关——golangci-lint 内嵌的 gofmt 使用其构建时的 Go 版本，与工具链对中文对齐的行可能判定相反，不重复启用。
- **外部链接周期检查不阻止合并。** 作为 [ci.yml 的定时任务](.github/workflows/ci.yml)每周或手动执行浏览器 User-Agent、GET、重定向检查；限流/机器人限制和允许列表中的上游既有失效链接不计为新的失败。
- **保留贡献与治理设施。** [Issue 模板](.github/ISSUE_TEMPLATE/)、[PR 模板](.github/PULL_REQUEST_TEMPLATE.md)、[安全政策](SECURITY.md)、[行为准则](CODE_OF_CONDUCT.md)改为简体中文，英文版本继续提供。
- **保留英文文档层。** README、变更历史、安全政策、行为准则、贡献/维护指南、防御指南、流量证据设计与检测规则均有英文入口；历史 `*-ko.md` 路径继续有效，但内容现为简体中文。
- **移除历史截图预览并重写架构章节。** 删除 `screenshots/` 目录与 `web/media/dashboard.png`；两个 README 不再展示截图，「系统架构」以六视图全面重建（全景分层 · 任务运行闭环 · 数据底座 · 协作机制 · 成本与可靠性控制面 · 单二进制部署）。
- **恢复三端打包脚本（Linux/macOS/Windows）。** 按 Git 历史还原 2026-10-11 裁剪（`3492c39`）对 [build.sh](build.sh) 的收窄：恢复 linux/darwin 目标、三端 Release 默认清单（保留裁剪期新增的 `windows/arm64`）、非 Windows 包附 `start.sh` 与 macOS UPX 参数；[README](README.md)、[构建与产物](docs/构建与产物.md) 与 [维护手册](MAINTAINING.md) 同步。
- **Release 工作流恢复三端打包。** [release.yml](.github/workflows/release.yml) 的发布矩阵由 Windows 双架构扩展为 Linux/macOS/Windows × amd64/arm64：随 `v*` tag 交叉编译、按平台校验收件头（PE/ELF/Mach-O）与目标架构后发布三端 zip 与 SHA256SUMS；CI（[ci.yml](.github/workflows/ci.yml)）每次 push 的可下载产物同步改为三端 zip（复用同一 `build.sh`，版本取提交短 sha），两处校验共用 [scripts/check-binary-headers.py](scripts/check-binary-headers.py)；[README](README.md)、[README.zh.md](README.zh.md)、[CONTRIBUTING](CONTRIBUTING.md)、[维护手册](MAINTAINING.md) 与[构建与产物](docs/构建与产物.md) 同步。

---

上游版本发布历史与贡献者列表见 [CHANGELOG.zh.md](CHANGELOG.zh.md)。
