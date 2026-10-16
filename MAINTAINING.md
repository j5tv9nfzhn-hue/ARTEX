# 上游同步与语言漂移防护（维护指南）


本文说明如何在吸收 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) 改动时维护简体中文默认输出。原分支在 2026-10 记录上游无法公开访问；404 可能涉及可见性或权限，不能据此断言永久删除。贡献与授权边界见 [CONTRIBUTING.md](CONTRIBUTING.md)，用户说明见 [README.md](README.md)。

目标是**保留技术行为，将项目自有韩文统一为简体中文；历史 ko/en/es 配置一律归一回退为 zh**。

## 1. 本地化结构概览

仓库继承上游历史，另叠加安全、语言与检测资料改动。同步时需要区分：

- 原有中文推理正文、工具协议与外部原始材料，不作无关改写。
- 代码固定的输出语言约束：`langDirective()` 默认简体中文，收尾同样遵循选定语言。
- 用户可见文案：`web/messages/zh.json` 是**唯一词库**（en/es 词库与 ko 兼容副本已移除）；后端 Go 消息和注释也统一中文。

浏览器显示语言与数据库输出语言是不同配置。默认配置优先级是 `ARTEX_LANG > config.json.language > zh`，运行时数据库设置由 server Hook 接入。

## 2. 保留原文的资产（避免误译）

- **内部推理正文。** `agent/promptcatalog.go` 和 `agent_prompts` 原有中文指令保持其角色、工具、范围及判断语义，不为了界面语言翻译成其他语言。
- **兼作上下文的消息。** 活动记录、停止原因、审批阻断、流量证据等可能同时进入 Planner/Reporter 输入。修改自然语言时同步解析、提示及回归，不能改变协议字段或模型标记。
- **上游中文技术稿与署名。** `README.zh.md`、`CHANGELOG.zh.md` 保留原作者内容和归属；必要的语言入口与安全说明应与当前分支一致，不能借翻译删去授权、安全、维护增补。
- **原始技术材料。** 命令、payload、代码接口、URL、标识符、SQL、用户输入、外部供应商日志和证据原文不作翻译。项目自有韩文注释、说明则应译成中文，不能以“保留原文”为由留下韩文。

## 3. 上游跟踪基线

需要同步时，可配置上游 remote：

```bash
git remote add upstream https://github.com/Autumn-27/ARTEX
git remote -v
```

历史维护记录的上游基线是 **`d003372`**（上游 main，2026-10-03，PR #189 `fix/sse-same-origin`）。这是原分支记录的同步基线，不是本次语言重构产生的新提交，也不表示今天已经成功 fetch。

本次语言重构基于工作区原始提交 `c653bcdc38032b6f3129e571ae6073b6fe1c8c77`；仅修改语言不应擅自推进上游跟踪基线。

## 4. 获取与合入上游改动

### 4.1 获取并检查差异

```bash
git fetch upstream
git rev-list --count d003372..upstream/main
git log --oneline d003372..upstream/main
```

fetch 只更新 remote tracking refs，不直接改工作树或 HEAD。没有未合入提交则无需同步。无法获取上游或 SDK 时应明确记录，不以本地缓存成功当作全新环境可复现。

### 4.2 分类变更文件

```bash
git log --name-status --oneline d003372..upstream/main
```

- 推理正文、种子或兼作 Agent 输入的消息：保留技术语义，审查语言尾指令与解析接口。
- Go 逻辑：保留上游功能，新用户消息译为简体中文；更新 zh 词库及对应测试。
- UI：提取稳定消息键，更新 zh 词库，不重新硬编码到 JSX。
- 固定检测指标：`enrich`、`selfupdate` 的 User-Agent，`guard` 审计标记、`db` 破坏性命令 deny 列表、`cmd/artex/main.go` 端口、`traffic` CA 文件名和 `db/schema_sqlite.sql` 指纹，不属于普通文案。若上游确实改变指标，应同步 Sigma、Suricata、ATT&CK、CSV、MISP 和测试。

### 4.3 合入与翻译

按功能审查和合入，遵循当前开发环境的分支约束；不要为了本地化擅自改变模块路径、工具名或 API 枚举。合入后立即执行第 5 节门禁。

历史案例（2026-10-05）：原记录中 `86729b6` 与合并 `b55ceb1` 增加模型回退审批 Token 计量，涉及 `db/llm_usage.go`、`llmrec/llmrec.go`、server、intercept 页面、API/types/mock。原文当时尚未推进 `d003372` 基线。该记录只用于说明如何区分逻辑、接口和新文案，不作为当前远端状态报告。

## 5. 词库对称与语言漂移检查

### 5.1 唯一词库（zh）与残留检查

`npm run extract:i18n` 递归增量合并词库，保留人工维护键及已改为 `t(key)` 的消息，不自动删除旧键。**当前仓库只有一份词库 `web/messages/zh.json`**（en/es 词库与 ko 兼容副本已移除；`web/src/i18n/config.ts` 的 `LOCALES` 只含 `zh`）；提取脚本里对其他语言路径的输出分支是历史遗留，不构成新的 UI 语言选项。

现要求：

- 任何新界面文案必须落进 `zh.json` 的消息键，不硬编码在 JSX。
- 项目词库无韩文字母/音节；ICU 参数与格式占位符保持一致。

```bash
cd web
npm test
npm run extract:i18n  # 修改源消息后提取，审阅生成差异
npm test
cd ..
python3 -I scripts/check-language.py --source
```

旧维护记录曾有 2,950 个 ko/zh 叶子键及“ko 无 Han”的基线；均不再适用本分支。键数量可增长，不能为了固定旧计数删除新消息。

### 5.2 检查推理正文的原有语义

原有中文推理正文应继续保留。历史 2026-10-05 曾记录 `promptcatalog.go` 有 70 行 Han；这仅是粗线索，不是当前必须固定的计数，也不能证明判断性能不变。

```bash
python3 -c "import re; han=re.compile(r'[㐀-鿿]'); t=open('agent/promptcatalog.go').read(); print('promptcatalog.go Han lines =', sum(1 for l in t.splitlines() if han.search(l)))"
git diff -- agent/promptcatalog.go agent/prompt.go agent/wrapup.go
```

审查内部角色边界、工具选择、超时和自定义收尾；输出语言采用一致的默认、归一及兼容规则。单靠字符计数或格式化不能证明模型质量。

### 5.3 检查静态构建的韩文残留

```bash
cd web && npm ci && npm test && npm run check && npm run build:static && cd ..
python3 -I scripts/check-language.py --web
```

扫描 `web/out/**/*.html` 的可见内容，解码 HTML 实体，排除 script/style 与隐藏注释；中文合法，韩文字母和音节不合法。兼容入口 `check-web-cjk.py` 采用同一逻辑。

### 5.4 检测指标与源码保持一致

```bash
detections/tests/indicators/run.sh
```

指标测试双向验证实际来源与规则。失败时区分来源变更和规则漂移，并同步规则、层、CSV/MISP。

新增来源文件时，必须同步 [detections.yml](.github/workflows/detections.yml) 的 push/pull_request paths；测试本身检查所有固定的非 detections 来源均在触发范围内，否则单改源码可能漏跑。

### 5.5 升级检测工具固定版本

sigma-cli、SigmaHQ 验证器与 Suricata 使用固定版本，可由环境变量覆盖。升级可能改变规则约定或后端能力，不只是语言问题。

- 新 lint 问题优先修正规则；确实不适用独立规则集的例外，须在 [validators.yml](detections/tests/sigma_lint/validators.yml)写明理由。
- 后端支持变化同步 `sigma_backends` 与文档支持矩阵。
- 防御指南内联模板的正、负能力承诺由独立门禁验证，不得把新增支持当作可以忽略的失败。

## 6. 最终构建与测试

按[开发环境说明](CONTRIBUTING.md#开发环境)执行 Go、前端、数据库、语言和文档回归。没有本地 Go 可用 Docker：

```bash
docker run --rm -v "$PWD":/src -w /src \
  -v artexko-gomod:/go/pkg/mod -v artexko-gocache:/root/.cache/go-build \
  golang:1.26 sh -c 'go build ./... && go vet ./... && go test ./... -count=1'
```

用户消息测试应断言中文包含 Han、不含 Hangul；历史语言值（ko/en/es 等）归一回退到 zh 词库是正确行为，不能断言回退结果“必须不同于源消息”。底层错误和用户证据则必须保留。

数据库相关包各自使用临时 SQLite 库、无需外部数据库服务；缺 Go、Docker 或 SDK 时如实标记未验证。模型质量需要授权沙箱端到端检查，不以便宜模型、前端构建或 Go 解析替代。

## 7. 基线更新记录

仅在实际合入并验证新的上游功能后，更新第 3 节上游基线，并在同一或后续提交说明。语言重构不自动视为上游同步。

提交规范见[贡献指南](CONTRIBUTING.md#提交消息)：

```text
chore(upstream): 合入 d003372..b55ceb1 的审批计量逻辑并同步新文案
```

## 8. 审查与验证注意事项

### 8.1 查询 CI 时显式指定仓库

fork 可能同时有 origin/upstream，gh 的默认仓库可能不是当前仓库，导致查看错误的 CI 或误判 404。显式指定：

```bash
gh run list -R j5tv9nfzhn-hue/ARTEX --workflow ci.yml --limit 5
gh run list -R j5tv9nfzhn-hue/ARTEX --workflow detections.yml --limit 5
```

可在自己的开发环境通过 `gh repo set-default j5tv9nfzhn-hue/ARTEX` 设置默认，再用 `gh repo set-default --view` 确认；这是本地 gh 配置，不提交到仓库。认证失败应修复平台 GitHub 连接，不在聊天或仓库保存 token。

### 8.2 外部链接用浏览器方式 GET 检查

防御指南引用 boho.or.kr、fsec.or.kr、pipc.go.kr 等韩国官方渠道。部分服务器拒绝 HEAD 或默认 curl User-Agent，另有重定向，因此单凭 `curl -I` 的 400/403 不应判为失效。

```bash
UA='Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36'
for u in https://www.boho.or.kr https://www.fsec.or.kr https://www.pipc.go.kr; do
  curl -sS -L -A "$UA" -o /dev/null -w "$u -> %{http_code} %{url_effective}\n" "$u"
done
```

原分支记录这三个链接在 2026-10-06 用该方法返回 200，PIPC 两次重定向。该结果是历史检查，不代表本次环境已重新访问。

`check-external-links.py` 自动提取跟踪 Markdown 中代码块、inline code 之外的外部链接，排除保留/占位主机，使用浏览器 UA、GET、重定向并对临时错误重试：

- OK：2xx/3xx。
- RESTRICTED：401/403/405/429，主机仍存在但访问受限。
- ALLOWED：允许列表中的上游历史失效例外。
- DOWN：404/410/持续 5xx、连接错误等可能失效。

```bash
python3 -I scripts/check-external-links.py --list   # 离线预览目标
python3 -I scripts/check-external-links.py --strict # 需要相应网络访问权限
```

外部检查容易波动，**不作为 PR 合并门禁**。[ci.yml 的定时任务](.github/workflows/ci.yml)每周一或手动运行；strict 仅对允许列表之外的 DOWN 失败。允许列表需保留无法修复的理由，不能掩盖本仓库可修复的损坏链接。

## 9. Release 发布流程

批准发布后才创建和推送 v* tag，[release.yml](.github/workflows/release.yml)将产生公开 Release。语言重构本身不自动发布、不创建新镜像，不可把本地成功宣称为真实发布成功。

### 9.1 创建发布

在有明确发布权限且决定发布的维护环境中，用选定版本创建 v* tag 并推送，即可触发工作流。不要用本地验证 tag 触发真实 Release。

### 9.2 各 job 的职责

- **frontend：** 一次静态导出 `web/out`，上传 web-dist，各目标复用。
- **binaries：** Windows amd64/arm64 交叉编译和 zip；Linux runner 无法执行 PE，改用 PE 结构校验（MZ 头、PE 签名、机器类型）。
- **release：** 汇总 zip、生成 SHA256SUMS，创建 GitHub Release。

GitHub Release 只发布 Windows 可执行程序，不再发布 Docker 镜像；三端（Linux/macOS/Windows）打包由 `build.sh` 提供，本地可用 `./build.sh --release` 产出（见 §9.3）。

### 9.3 构建路径与前置条件

- web-dist 放入 `server/webui/dist`，`server/webui_embed.go` 的 `//go:embed all:webui/dist` 内嵌它；binaries 用 `ARTEX_SKIP_FRONTEND=1` 防止重复编译前端。
- `build.sh --target OS/ARCH` 输出 `dist/artex-OS-ARCH/artex`（Windows 为 `artex.exe`）和 zip；包含对应启动脚本（`start.bat` / `start.sh`）、skills、config.example.json、README。
- 一键更新由 `selfupdate.AssetName` 按当前平台拼 `artex-<版本>-<os>-<架构>.zip`，与 `package_binary` 命名一致。

### 9.4 不发布 tag 的本地验证

```bash
# 1. 静态前端
cd web && npm ci && npm run build:static && cd ..
# 2. 内嵌目录
rm -rf server/webui/dist && mkdir -p server/webui/dist && cp -a web/out/. server/webui/dist/
# 3. 仅构建一个目标，不创建 tag 或 Release
ARTEX_SKIP_FRONTEND=1 ARTEX_SKIP_NPM_CI=1 ARTEX_COMPRESS=0 \
ARTEX_PACKAGE=1 ARTEX_PACKAGE_DIR=dist ARTEX_BUILD_VERSION=v0.0.0-local \
./build.sh --target windows/amd64
# 4. 检查 dist/artex-windows-amd64/artex.exe、zip 与 SHA256SUMS
```

Linux 上无法执行 PE，用 `file dist/artex-windows-amd64/artex.exe` 确认为 PE32+ 可执行文件。`dist/`、`server/webui/dist/`、数据库、日志、私钥、测试/扫描产物均不提交 Git。
