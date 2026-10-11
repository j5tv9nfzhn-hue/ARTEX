# ARTEX 检测规则


> 本目录将[防御与检测指南](../docs/defense.md)第 4 节的伪规则转换为厂商中立的 [Sigma](https://sigmahq.io)，可进一步转换为各 SIEM、EDR 的查询语言。所有指标均基于本仓库源码实际存在的字符串或行为，而不是猜测。仅用于防御和检测自有或获得明确书面授权的系统。

## 原子（atomic）规则

- **`sigma/artex_enrich_user_agent.yml`**：资产补全（`enrich/enrich.go`）发送的入站 `artex-enrich/1.0` User-Agent，是目标侧辅助指标。`level: high`。
- **`sigma/artex_selfupdate_egress.yml`**：自更新（`selfupdate/github.go`）发送的出站 `artex-selfupdate` User-Agent，是主机/取证侧 egress 指标。`level: medium`。
- **`sigma/artex_guard_audit_framing.yml`**：工具调用被阻断时，`guard/guard.go` 写入审计日志的平台控制标记，属于主机/取证指标。`level: high`。
- **`sigma/destructive_command_hunting.yml`**：对应 `db/db.go` 种子中的破坏性 Shell/DB 命令 deny 列表，属于通用威胁狩猎线索，不是 ARTEX 专属指纹。`level: medium`。
- **`sigma/artex_recording_proxy_ca.yml`**：记录代理（`traffic/traffic.go`）生成的 `_ca/mitmproxy-ca-cert.pem`。属于主机取证产物，文件名也被独立 mitmproxy 使用，因此只能作为狩猎线索。`level: medium`。

## 关联（correlation）规则：行为检测

静态字符串可以改变，行为更难隐藏。[`sigma/correlation/`](sigma/correlation/) 将防御指南 4.1–4.2、4.4 节的行为层表达为 Sigma 关联。它们通过 `id` 引用上述原子规则，转换时必须提供整个 `sigma/` 树，不能只提供一个关联文件。

- **`sigma/correlation/artex_enrich_scan_velocity.yml`**：同一来源在短窗口集中发送 `artex-enrich/1.0` 探测。补全并发为 4、无速率限制，可捕获单条存在规则未体现的速度。`event_count`，`level: high`。
- **`sigma/correlation/artex_enrich_fanout.yml`**：同一来源将补全 UA 发送到多个**不同**主机，关注自动遍历资产的广度，不仅是数量。`value_count`，`level: high`。
- **`sigma/correlation/artex_guard_block_burst.yml`**：同一主机重复产生平台控制标记，表示运行中的调用触及自身 guard，而非文档偶然引用。`event_count`，`level: high`。
- **`sigma/correlation/artex_guard_marker_then_destructive.yml`**：同一主机在窗口中先后出现 guard 标记与破坏性命令，将 ARTEX 标记与通用行为组合提高特异性（指南 §4.2）。`temporal`，`level: high`。

阈值、窗口是保守起点，应按环境基线调整。纯 Web 多阶段模式“枚举→探测→认证”不能归结为一个 ARTEX 专属 UA，仍需环境专用基础规则。[防御指南 §4.2](../docs/defense.md#42-siem-关联规则)提供通用 Sigma 模板；它无法以 ARTEX 源码固定依据，因此没有放进本目录的已测规则树。

## 网络规则（Suricata）

Sigma 关注主机和日志；网络侧可见的两类 UA 在 [`suricata/`](suricata/) 中由 [Suricata](https://suricata.io) 表达。`enrich/enrich.go` 的 `artex-enrich/1.0` 对应存在与高速枚举规则（sid 1000001、1000002）；norma SDK WebFetch 的 `norma/0.4`（`github.com/Autumn-27/norma/tool/webfetch.go`）对应存在规则（sid 1000003）。Bash 执行的 curl、nmap 等其他工具使用自己的指纹，没有 ARTEX 专属 UA，所以网络规则刻意限制为这两类。TLS 限制、`suricata -T` 与参考 pcap 验证见 [`suricata/README.md`](suricata/README.md)。

## ATT&CK 覆盖率

[MITRE ATT&CK](https://attack.mitre.org/) Navigator 层 [`attack/artex_navigator_layer.json`](attack/)汇总六种战术（侦察、命令与控制、执行、影响、凭据访问、收集）的八种技术。仅依据规则 `attack.*` 标签，按专属指纹或通用狩猎线索的检测强度计分，不添加猜测。用 [Navigator](https://mitre-attack.github.io/attack-navigator/)查看，评分、技术与规则对应及“覆盖不代表完整检测”的说明见 [`attack/README.md`](attack/README.md)。[一致性测试](tests/attack/run.sh)防止层与规则漂移。

## 机器可读的威胁指标

[`indicators/artex_indicators.csv`](indicators/)汇总可导入威胁情报平台、SIEM 查询表或 triage 清单的原子指标：补全/自更新 UA、guard 标记、服务/代理默认端点、代理 CA 和 PostgreSQL 探索图 schema。每行注明来源文件及适用规则。

同一指标也提供 [MISP](https://www.misp-project.org/) 事件 [`artex_indicators.misp.json`](indicators/)，支持直接导入或进一步导出 STIX。规则支持的指纹标为 `to_ids`；主机端口、schema 线索不标。为避免误报，通用破坏性命令及 norma SDK 共享的 `norma/0.4` 不进入导入清单；后者虽被 Suricata sid 1000003 检测，却不是 ARTEX 专属字符串。列、MISP 类型映射、限制与同步测试见 [`indicators/README.md`](indicators/README.md)。

## 主机分类（triage）

没有 SIEM、仅获得可疑主机 Shell 的响应人员，可运行只读 [`triage/artex_host_triage.py`](triage/)检查本地是否有 ARTEX 痕迹。除同类指纹外，还覆盖 CSV 刻意没有 Sigma 规则的三个主机/DB 指标：监听端口、记录代理端点、PostgreSQL 探索 schema；这些必须在主机上确认。

工具也从 `/proc` 或 `--proc-from` 转储检查进程是否同时携带代理与 mitmproxy CA 信任变量，依据 `agent/worker.go` 的注入行为。各发现仅为带有对应指标局限的分类线索，不是归属证明。见 [`triage/README.md`](triage/README.md)，内置 `--self-test` 接入合并门禁。

## 测试

[`tests/`](tests/)提供仅需 Docker 的可复现规则测试。

- **Suricata**（[`tests/suricata/run.sh`](tests/suricata/run.sh)）：先以 `suricata -T --init-errors-fatal` 加载完整规则，再由 scapy 确定性合成 pcap，运行 `suricata -r`。存在规则每探测触发一次，速度规则超过阈值触发，正常 UA 告警为零。捕获文件每次生成，不提交二进制。
- **Sigma**（[`tests/sigma/run.sh`](tests/sigma/run.sh)）：验证 check 零错误、整树编译、原子指标保留到查询、关联规则单独转换失败；最后一项证明其真实依赖被引用原子规则。
- **Sigma 事件匹配**（[`tests/sigma_match/run.sh`](tests/sigma_match/run.sh)）：原子恶意样本触发、正常样本静默；例如只有 `.mitmproxy/` 的 CA 不满足要求 `_ca/` 的规则。关联在同组、同窗口满足阈值时触发，在阈值不足、过期、组拆分、缺少阶段时静默。pySigma 解析条件，测试遍历编译树与聚合规格，复用原子匹配器。
- **ATT&CK**（[`tests/attack/run.sh`](tests/attack/run.sh)）：技术、战术与规则 `attack.*` 标签完全一致，各技术指向实际规则；任何单向增删均失败。
- **指标依据**（[`tests/indicators/run.sh`](tests/indicators/run.sh)）：固定 UA、guard 标记、破坏 token 与真实源码一致，CSV 每行仍有源码/规则依据；所有固定的非 detections 来源都在 CI push/PR paths 中。捕获“规则仍编译触发，但上游已经改指纹”的漂移。
- **MISP 同步**（[`tests/misp/run.sh`](tests/misp/run.sh)）：pymisp 实际加载有效事件，拒绝不存在的属性类型；值、预期类型/类别、`to_ids`、`disable_correlation` 与 CSV 逐行一致。可检测指标 `to_ids` 开启，主机端口分类提示关闭且禁关联。事件另有说明、UUID、标签，手工维护，CSV 增删或改类型时必须同提交同步。
- **Sigma 后端可移植性**（[`tests/sigma_backends/run.sh`](tests/sigma_backends/run.sh)）：整树编译为 Splunk、Elasticsearch eql、Grafana Loki；五条原子规则也在不支持关联的 lucene、Microsoft kusto 编译，复现下面支持矩阵。
- **SigmaHQ lint**（[`tests/sigma_lint/run.sh`](tests/sigma_lint/run.sh)）：加载全部 `pySigma-validators-sigmahq` 验证器，不是普通 check；按 [`validators.yml`](tests/sigma_lint/validators.yml)基线零问题，并确认仅有四项已说明的不适用检查排除。新标题、字段或分类问题会使构建失败。

八类规则测试之外，还有三个非规则门禁由同一 CI 与 [`tests/run-all.sh`](tests/run-all.sh)运行：harness 同步检查 runner、CI、目录的套件与顺序一致；[`triage-selftest.sh`](tests/triage-selftest.sh)验证合成主机命中、干净主机零发现；[`check-defense-template.sh`](tests/check-defense-template.sh)复现指南内联模板的 check、splunk/eql/loki 转换、lucene/kusto 关联拒绝和中英文规则主体字节一致。模板位于规则树之外，工具升级造成文档承诺过期时其他套件不会发现。

任何断言失败均返回非零。详情见 [`tests/README.md`](tests/README.md)。

## 正确理解规则与局限

- **静态指标可改变。** 缺少 `artex-enrich/1.0` 或 `artex-selfupdate` 不代表安全。更持久的信号是同源侦察、枚举、探测、认证/注入随响应持续调整；速度、fanout、guard 突发和多阶段关联已表达于规则，纯 Web 多阶段仍需环境基础检测，见指南 1、2、4.1–4.2 节。
- **破坏性命令是通用狩猎。** 合法管理员也可能运行同样命令，应结合允许列表与上下文，不能仅凭命中认定 ARTEX。
- **端口是主机取证指标。** 默认服务 `:8787`、代理 `127.0.0.1:8788` 适合在可疑主机用 ss/netstat 确认，不作为高噪声 Sigma 网络规则。CSV 将其标为分类线索，[triage](triage/)为没有 SIEM 的响应人员检查端口、代理产物、日志标记与 PostgreSQL schema。

## 验证与转换

原资料使用 [sigma-cli](https://github.com/SigmaHQ/sigma-cli)（pySigma）验证这些规则。复现方法：

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install sigma-cli

# 结构与最佳实践检查，预期零错误、零问题
sigma check detections/sigma/

# 加载 SigmaHQ 全部约定验证器，使用独立规则集的已记录基线
# 普通 sigma check 不加载这些验证器
pip install pySigma-validators-sigmahq
sigma check --validation-config detections/tests/sigma_lint/validators.yml detections/sigma/

# 编译为目标查询语言，例如 Splunk
sigma plugin install splunk
sigma convert -t splunk --without-pipeline detections/sigma/artex_enrich_user_agent.yml

# 转换整树，以解析关联规则通过 id 引用的原子规则
sigma convert -t splunk --without-pipeline detections/sigma/
```

基线启用 SigmaHQ 全部约定，仅排除四项针对其 monorepo 文件布局和分类体系、而不适用本独立规则集的检查。理由见 [`validators.yml`](tests/sigma_lint/validators.yml)，由 lint 测试机械验证。

### Sigma 后端可移植性

关联规则引用原子 `id`，只有支持 Sigma 关联转换的后端可以编译整树。`-t` 选择很重要。以下为原资料固定基线（sigma-cli 3.1.0 与兼容后端）记录的支持情况，由 [`sigma_backends`](tests/sigma_backends/run.sh)复现。

- **整树（原子与关联）：** Splunk（`-t splunk`）、Elasticsearch EQL（`-t eql`）、Grafana Loki（`-t loki`）。
- **仅原子规则：** Elasticsearch Lucene（`-t lucene`）、OpenSearch（`-t opensearch_lucene`）、Microsoft kusto（Sentinel/Defender XDR，`-t kusto`）。转换五条原子，在产品内原生表达窗口，如 Sentinel 的 `summarize ... by bin(TimeGenerated, 30m)`；传整树会报 `Backend does not support correlation rules`。

```sh
# 仅转换原子规则，例如 Microsoft Sentinel / Defender
sigma plugin install kusto
sigma convert -t kusto --without-pipeline \
  detections/sigma/artex_enrich_user_agent.yml \
  detections/sigma/artex_selfupdate_egress.yml \
  detections/sigma/artex_guard_audit_framing.yml \
  detections/sigma/artex_recording_proxy_ca.yml \
  detections/sigma/destructive_command_hunting.yml
```

固定版本的原记录说明：ES|QL（`-t esql`）拒绝 guard 标记规则，报 `String value expressions are not supported`，当时建议转换其余三条原子；新增规则需另核实支持。IBM QRadar 插件 `ibm-qradar-aql` 与固定 pySigma 不兼容、需 `--force-install`，故不在测试矩阵。用 `sigma list targets` 查看已安装目标。

例子用 `--without-pipeline` 保留 `cs-user-agent`、`cs-host`、`CommandLine` 等通用字段。实际部署应移除该参数，使用 `-p` 处理管线映射产品 schema（`sigma list pipelines`）。管线还可能要求通用 logsource 没有指定的目标表，如 sentinel_asim 在设置正确 `query_table` 前会报 `Unable to determine table name`；必须按环境映射字段和表。

## 贡献

欢迎检测和加固贡献：指标须有可观察事实依据，description 诚实说明限制，通过 SigmaHQ 基线 `sigma check --validation-config tests/sigma_lint/validators.yml`，不写成攻击操作指南。见 [`CONTRIBUTING.md`](../CONTRIBUTING.md)。
