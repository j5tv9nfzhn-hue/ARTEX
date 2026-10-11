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

### 防御与检测资料

- **提供防御与检测指南。** [简体中文指南](docs/defense.md)保留旧文件名以兼容链接，与[英文版](docs/defense.md)包含相同的自主 AI 行为、可观测指纹、加固、规则与事件响应资料。
- **提供部署用规则。** [Sigma](detections/sigma/) 处理主机、日志与关联检测；[Suricata](detections/suricata/) 检测 enrich 探测和 norma WebFetch 的 User-Agent。
- **可视化 ATT&CK 覆盖率。** [Navigator 层](detections/attack/)仅使用规则实际 `attack.*` 标签，不增加推测技术。
- **标准化机器可读指标。** [CSV 和 MISP](detections/indicators/)逐行同步，规则支持的指标使用 `to_ids`，主机取证线索与可部署检测区分。
- **提供八类可复现测试。** Sigma 结构/转换、真实事件匹配、后端可移植性、SigmaHQ lint、Suricata 加载/触发、ATT&CK 一致性、指标与源码一致性、MISP/CSV 同步；恶意样本应触发，正常样本应静默。批量 runner、pre-commit 与 CI 执行相同契约。

### 仓库维护

- **保留安全与法律告知。** 仅允许自有或书面授权目标、隔离实验环境，说明韩国信息通信网络法、个人信息保护法及适用地区法律，不因语言重构削弱授权限制。
- **预览图片统一为中文演示截图。** 保留原作者署名和图片来源说明，不将历史演示截图伪称为本次生产验证。
- **完善维护与贡献指南。** [维护手册](MAINTAINING.md)涵盖上游同步、语言漂移、指标契约及发布前置条件；[贡献指南](CONTRIBUTING.md)说明验证范围和检测规则契约。
- **每次 push、PR 执行合并门禁。** [Go/数据库/前端/检测规则/语言检查](.github/workflows/ci.yml)合并在单工作流中执行，检测规则另由[独立工作流](.github/workflows/detections.yml)按路径触发。数据库测试按包隔离 PostgreSQL，内部链接检查只使用标准库、不依赖网络。
- **外部链接周期检查不阻止合并。** 作为 [ci.yml 的定时任务](.github/workflows/ci.yml)每周或手动执行浏览器 User-Agent、GET、重定向检查；限流/机器人限制和允许列表中的上游既有失效链接不计为新的失败。
- **保留贡献与治理设施。** [Issue 模板](.github/ISSUE_TEMPLATE/)、[PR 模板](.github/PULL_REQUEST_TEMPLATE.md)、[安全政策](SECURITY.md)、[行为准则](CODE_OF_CONDUCT.md)改为简体中文，英文版本继续提供。
- **保留英文文档层。** README、变更历史、安全政策、行为准则、贡献/维护指南、防御指南、流量证据设计与检测规则均有英文入口；历史 `*-ko.md` 路径继续有效，但内容现为简体中文。

---

上游版本发布历史与贡献者列表见 [CHANGELOG.zh.md](CHANGELOG.zh.md)。
