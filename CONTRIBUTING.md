# 贡献指南（Contributing）


感谢关注 ARTEX。提交贡献前请阅读[使用范围与法律责任](#使用范围与法律责任)、[本地化原则](#本地化原则)和[行为准则](CODE_OF_CONDUCT.md)。

- 缺陷、功能、本地化问题请使用[Issue 模板](https://github.com/j5tv9nfzhn-hue/ARTEX/issues/new/choose)。
- 安全漏洞**不要公开提交**，请遵循 [SECURITY.md](SECURITY.md) 的私密报告流程。

## 使用范围与法律责任

ARTEX 是由 LLM 多 Agent 自主执行渗透测试的进攻性安全工具，贡献者与使用者遵循相同边界：

- 只在自有、获得明确书面授权的目标，或自有本地隔离环境中验证，例如 Docker 运行的 OWASP Juice Shop、DVWA。
- 不接受针对超出授权范围的生产、真实、远程系统执行扫描或利用的代码，也不接受鼓励这种使用的改动。
- 在韩国，未经授权侵入或妨碍他人网络涉及信息通信网络法，个人信息处理涉及个人信息保护法。详见 [README](README.md#使用范围与法律告知)。其他地区同样必须遵守适用法律。

代码与文档按“现状（AS IS）”提供，其实际使用行为的法律责任由执行者承担。

## 本地化原则

目标是保留 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) 的技术行为，同时将项目自有韩文统一为简体中文。上游原分支曾记录无法公开访问，保留链接用于归属，不保证可用性。

- **保留推理语义。** `agent/promptcatalog.go` 与 `agent_prompts` 的原有中文行为正文不作无关翻译；角色、范围、工具边界不能因语言重构改变。其他项目自有韩文提示、说明和注释仍应译为简体中文。
- **明确用户输出语言。** `report_finding`、`record_fact`、最终报告、对话默认中文，`agent/prompt.go` 的 `langDirective()` 在角色 system prompt 末尾追加约束；收尾也遵循选定语言。
- **协议与原始材料不改写。** 命令、payload、API/JSON 字段、枚举、工具名、SQL、URL、用户输入、原始日志和外部供应商诊断保持原样。
- **中文作为源词库。** UI 使用 `web/messages/zh.json` 的稳定消息键，en/es 键同步；ko.json 仅为中文兼容副本。新界面语言选项只包含 zh/en/es。
- **兼容旧配置。** ko 归一为 zh，空值或未知值按各层既定优先级回退；不把未知环境变量误当作有效值屏蔽文件配置。
- **格式与结构必须保留。** `%s/%d/%v/%w`、ICU 参数、模板占位符、Markdown 结构及用于解析的审批标记需同步前后端和测试。
- **输出约束是提示而非硬过滤。** 模型可能混入技术英文或受角色影响；不能为了“纯中文”破坏代码或证据。模型端到端质量需在授权沙箱独立验证，不能仅凭构建成功推断。
- **禁止新韩文残留。** `scripts/check-language.py` 同时检查源码、注释、词库、脚本、配置说明和文档；允许中文，不能沿用旧的“禁止 Han”门禁。

上游同步、词库提取与漂移检查见 [MAINTAINING.md](MAINTAINING.md)。

## 开发环境

Go 后端可内嵌 Next.js 静态前端，数据库为 PostgreSQL。涉及 `report_finding.traffic_refs` 或报告自动绑定时，先阅读[多流量证据设计](docs/finding-traffic-evidence.md)，中文原稿也保留在[同目录](docs/finding-traffic-evidence.md)。旧 `-ko` 文件名仅为链接兼容。

### 要求版本

- Go 1.26 或以上，以 `go.mod` 为准。
- Node 22 LTS 或更新版本；Node 类型擦除测试至少需要 22.6，与 CI 使用 Node 22 一致。
- Docker、Docker Compose，用于可复现依赖与数据库隔离测试。

### 后端（Go）

```bash
go build ./...
go vet ./...
go test ./...
```

没有本地 Go 时可用 Docker，named volume 保存模块、编译缓存：

```bash
docker run --rm -v "$PWD":/src -w /src \
  -v artexko-gomod:/go/pkg/mod -v artexko-gocache:/root/.cache/go-build \
  golang:1.26 sh -c 'go build ./... && go vet ./... && go test ./...'
```

#### 数据库集成测试（需要 PostgreSQL）

无数据库时 `agent`、`config`、`db`、`evidence`、`llmrec`、`server` 中部分测试会 `SKIP`，包仍可能显示 `ok`。**跳过不等于数据库路径通过**，修改这些包后必须另外提供数据库；CI `go-db` 为每包创建独立 PostgreSQL，避免共享数据、连接清理和 schema 依赖。

以下在隔离 Docker 网络使用与 CI 相同的 `postgres:16-alpine`，示例密码仅用于一次性本地测试：

```bash
# 1. 创建隔离网络和空数据库
docker network create artexko-db 2>/dev/null || true
docker run -d --name artexko-pg --network artexko-db \
  -e POSTGRES_USER=artex -e POSTGRES_PASSWORD=artex -e POSTGRES_DB=artex \
  postgres:16-alpine
until docker exec artexko-pg pg_isready -U artex -d artex >/dev/null 2>&1; do sleep 1; done

# 2. 提供 DSN，仅执行一个集成包；其他包分别使用新的空数据库
docker run --rm --network artexko-db -v "$PWD":/src -w /src \
  -v artexko-gomod:/go/pkg/mod -v artexko-gocache:/root/.cache/go-build \
  -e ARTEX_PG_DSN='postgres://artex:artex@artexko-pg:5432/artex?sslmode=disable' \
  golang:1.26 sh -c 'go test ./agent/ -count=1'

# 3. 清理此示例创建的容器和网络，不操作其他环境
docker rm -f artexko-pg && docker network rm artexko-db
```

其余包将 `./agent/` 替换为对应包，使用各自新建的数据库。

### 前端（web）

```bash
cd web
npm ci
npm run dev          # 开发服务
npm test             # 纯 Node 回归测试，不需要后端
npm run build        # 生产构建
npm run build:static # 静态导出，含 TypeScript 类型检查
npm run check        # Biome lint、格式、可访问性合并门禁
npm run check:fix    # 自动修复，请审阅差异
```

没有 Node 或原生依赖平台不匹配时，可用独立 node_modules volume：

```bash
docker run --rm -v "$PWD":/src -w /src/web \
  -v artexko-web-nm:/src/web/node_modules \
  node:22 bash -c 'npm ci && npm test && npm run check && npm run build:static'
```

`lint-staged` 在提交时对已暂存文件执行 `biome check --write`。Biome error 必须为零；已有 warning/info 不改变退出码，不应借语言重构无关地清理全部样式债务。

### 完整运行（Docker Compose）

```bash
cp .env.example .env  # 必须修改 POSTGRES_PASSWORD
docker compose up -d
```

此路径依赖 Compose 中配置的镜像，原分支记录上游镜像曾无法拉取；需要本分支改动时应先源码构建或使用自己有权限发布的镜像，见 [README](README.md#快速开始docker-compose)。

## 贡献流程

1. 大改动先开 Issue 协调方向；拼写、链接、明确缺陷可直接提交 PR。
2. 在自己的工作分支完成改动，遵循所在开发环境的分支约束。
3. **实际运行相关验证并记录边界。** Go 运行 build/vet/test，数据库包另跑集成；Web 运行 Node 回归、Biome 与静态构建。无工具、依赖无法获取、SKIP 或仅解析源码，均应明确标记，不得当作完整通过。
4. 任何自有文本改动后运行语言检查；文档改动运行内部链接检查。锚点按 GitHub slug 计算，修改标题时必须同步引用。

   ```bash
   python3 -I scripts/check-language.py --source
   python3 -I -m unittest discover -s scripts/tests -v
   python3 -I scripts/check-doc-links.py
   # 在 web 静态构建后执行
   python3 -I scripts/check-language.py --web
   ```

5. 使用 [PR 模板](.github/PULL_REQUEST_TEMPLATE.md)说明内容、原因、执行命令与结果；UI 改动附截图并注明演示数据。
6. 用户可见改动在 [CHANGELOG.md](CHANGELOG.md) 的 Unreleased 中记录。纯内部重构、测试可酌情省略。

[pre-commit 配置](.pre-commit-config.yaml)包括 docs、language 与 detections 门禁。`pip install pre-commit && pre-commit install` 后可在提交前执行；文档、语言只用 Python 标准库，不需要网络或 Docker。

### 提交消息

沿用 `type(scope): 说明`，说明使用简体中文；type 可为 feat/fix/docs/chore/refactor/test/i18n，scope 可为 agent/web/server 等，也可省略。

```text
i18n(agent): 默认产物改为简体中文，兼容旧 ko 配置
docs: 翻译使用与贡献指南，保留上游归属
i18n(web): 同步中文词库与语言回归测试
```

## 检测规则与测试贡献

[`detections/`](detections/) 提供 Sigma、Suricata、MITRE ATT&CK 层、指标及可复现测试。八类规则测试与额外门禁机械执行以下契约：

- **每个指标必须有可观察的源码依据。** 字符串、User-Agent、行为阈值不可猜测，注明来源，例如 `artex-enrich/1.0` 来自 `enrich/enrich.go`。[指标测试](detections/tests/indicators/)要求源码与规则持续一致；CSV 改动必须同步 [MISP 事件](detections/indicators/artex_indicators.misp.json)，[MISP 测试](detections/tests/misp/)逐行比较并由 pymisp 验证格式。
- **诚实说明局限。** Sigma `description` 和 Suricata 注释说明漏报、误报；泛化行为（如破坏性命令）不是 ARTEX 专属指纹，单次命中不能完成归属判断。
- **通过静态验证。** Sigma 使用 `sigma check --validation-config detections/tests/sigma_lint/validators.yml`，不仅是默认核心验证器；标题、字段、日志分类、引用也需符合 SigmaHQ。四项单仓库不适用的例外及理由在 validators.yml 中。Suricata 须通过 `suricata -T`。
- **附可复现测试。** 输入确定性生成，不提交 pcap 等二进制产物；版本无关的触发、无误报属性使用精确断言，随版本变化的计数采用下限并注明基线。新增关联规则须在[后端可移植性测试](detections/tests/sigma_backends/)验证，与[规则支持矩阵](detections/README.md)一致。
- **同步 ATT&CK。** `attack.*` 标签与 [Navigator 层](detections/attack/artex_navigator_layer.json)技术、分值双向一致，不增加规则不存在的推测技术。
- **仅提供防御与检测资料。** 不接受帮助未经授权攻击的利用步骤或规避检测教程。

```bash
detections/tests/sigma/run.sh           # 结构、转换与指标保留
detections/tests/sigma_match/run.sh     # 恶意事件触发，正常事件静默
detections/tests/sigma_lint/run.sh      # SigmaHQ 验证器与基线
detections/tests/sigma_backends/run.sh  # 关联规则后端支持
detections/tests/suricata/run.sh        # 合成 pcap 与告警断言
detections/tests/attack/run.sh          # 层与规则双向一致
detections/tests/indicators/run.sh      # 指标与源码一致
detections/tests/misp/run.sh            # CSV/MISP 同步与格式
```

[run-all.sh](detections/tests/run-all.sh)按 CI 顺序执行全部套件，单项失败仍继续汇总，任意失败最终返回非零；另执行 harness 同步、triage 自测和防御内联模板门禁。测试只需 Docker，不向仓库写入生成产物。

[检测 CI](.github/workflows/detections.yml)在规则、层、指标以及其来源文件变化时触发，包括 `enrich/`、`selfupdate/`、`guard/`、`db/`、`cmd/artex/main.go`，防止上游改变 User-Agent、标记、端口或 schema 后资料过期。

规则依据、局限见 [detections/README.md](detections/README.md)，断言与执行方法见 [detections/tests/README.md](detections/tests/README.md)。

## 许可证

项目使用 **AGPL-3.0**。提交贡献表示同意贡献按相同许可公开；修改后通过网络提供服务时，应向使用者提供对应完整源码。全文见 [LICENSE](LICENSE)。
