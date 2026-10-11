# ARTEX 检测规则测试

简体中文 · [English](README.md)

这些回归可复现地验证[检测规则](../)在恶意样本触发、正常流量静默。不可运行的规则只是主张；测试让审查者能从源码重新验证规则与防御指南的承诺。

不提交二进制 pcap：每次执行确定性生成并在结束后清理，以可读源码而非不透明样本交付，不使仓库膨胀。

## 一次运行全部套件：[`run-all.sh`](run-all.sh)

[`run-all.sh`](run-all.sh)按 CI 顺序执行八套，无需手工调用八个 run.sh。前一套失败仍执行余下套件，最终逐项输出 PASS/FAIL；任何失败返回非零。

先执行 [`check-harness-sync.sh`](check-harness-sync.sh)，要求 runner 列表、[CI 步骤](../../.github/workflows/detections.yml)、磁盘套件目录一致且同序。它补上单个套件无法发现的注册缺口，例如仅加 CI 步骤、没有 runner 条目。该检查是门禁而非第九套，不加入八套结果摘要。

另外两个同样在套件循环前执行且不计为套件：[triage 自测](triage-selftest.sh)与[防御模板检查](check-defense-template.sh)。后者复现指南内联 Sigma 模板的 check、splunk/eql/loki 转换、lucene/kusto 关联拒绝及中英文主体字节一致；模板在 sigma 树之外，工具升级引发承诺过期时其他套件无法捕获。

```sh
detections/tests/run-all.sh
```

预期输出（节选）：

```
===== detection suites summary =====
  PASS  sigma
  PASS  sigma_match
  PASS  sigma_lint
  PASS  sigma_backends
  PASS  suricata
  PASS  attack
  PASS  indicators
  PASS  misp
RESULT: PASS
```

非零退出可直接用于 CI/pre-commit，配置见[仓库根示例](../../.pre-commit-config.yaml)。安装 `pip install pre-commit && pre-commit install` 后，规则及固定来源改动触发 runner，范围与 CI 一致。`PYTHON_IMAGE`、`SIGMA_CLI_VERSION`、`SIGMAHQ_VALIDATORS_VERSION`、`SURICATA_IMAGE` 等覆盖变量由 runner 继承。

## Suricata: [`suricata/`](suricata/)

[`suricata/run.sh`](suricata/run.sh)对[网络规则](../suricata/artex.rules)端到端验证五个性质：

- **有效性：** 整个文件须通过 `suricata -T --init-errors-fatal`，即使没有样本触及某条规则，也检查其解析/初始化；仅 -r 可能跳过坏规则仍返回零。
- **补全存在性：** sid 1000001 对每次补全探测精确触发一次。
- **速度：** 同来源超过 300 秒 30 请求阈值时 sid 1000002 触发。
- **WebFetch 存在性：** sid 1000003 每次 WebFetch 触发一次，同捕获补全 sid 静默，验证两类指纹互相区分。
- **特异性：** 仅将 UA 改成正常浏览器，其他条件相同，ARTEX 告警为零。

[`gen_pcap.py`](suricata/gen_pcap.py)用 [scapy](https://scapy.net)生成同一固定来源的 N 个独立明文 HTTP 请求/响应流，使用指定 UA、固定初始时间，每流间隔一秒。**只写文件，不发送数据包，也不访问网络。**

### 执行

仅需 Docker，scapy 与 Suricata 在容器中运行。

```sh
detections/tests/suricata/run.sh
```

预期输出（节选）：

```
  PASS  ruleset loads with zero parse/init errors (suricata -T)
  PASS  sid 1000001 presence: one alert per probe  (got 35, want eq 35)
  PASS  sid 1000002 velocity: fires past 30-in-300s  (got 5, want ge 1)
  PASS  sid 1000003 presence: one alert per WebFetch request  (got 8, want eq 8)
  PASS  enrich sids stay silent on norma traffic (specificity)  (got 0, want eq 0)
  PASS  benign browser UA produces no ARTEX alerts  (got 0, want eq 0)
RESULT: PASS
```

任何断言失败返回非零。内部镜像可通过 `SURICATA_IMAGE`、`PYTHON_IMAGE` 覆盖。

### 为什么速度告警使用下限而不是精确计数

存在性 `1000001 == 35`、`1000003 == 8` 与正常告警 `== 0` 不随引擎版本变化，使用精确断言。速度告警取决于 detection_filter 的版本边界行为，断言 `>= 1` 并另记基线：Suricata 8.0.7 为 5 次，即超过 30 阈值后的第 31–35 流。

## Sigma: [`sigma/`](sigma/)

[`sigma/run.sh`](sigma/run.sh)用 [sigma-cli](https://github.com/SigmaHQ/sigma-cli)/pySigma 对[规则树](../sigma/)进行结构与编译验证，断言五个性质：

- **有效性：** 全树 check 为零错误、零条件错误、零问题。
- **编译：** `sigma convert -t splunk` 完整生成查询，无错误。
- **指标保留：** UA、guard 标记、`mitmproxy-ca-cert.pem` 等精确原子字符串仍在查询中。
- **关联编译：** 行为规则不被丢弃，生成 event_count/value_count 聚合。
- **真实依赖：** 单独转换关联规则失败，因为原子 id 引用是真正强制的，不是装饰。

这将[总览](../README.ko.md)的结构/编译承诺变为可执行断言。[sigma_match](sigma_match/)补上原子和关联的实际匹配，恶意事件/时间线触发、正常事件静默。解析交给 pySigma，避免手写解析器削弱规则；信任模型如下。

### 执行

仅需 Docker，sigma-cli 与 Splunk 后端在容器运行，不向仓库写产物。

```sh
detections/tests/sigma/run.sh
```

预期输出（节选）：

```
  PASS  sigma check: 0 errors, 0 condition errors, 0 issues
  PASS  whole tree converts to splunk (exit 0)
  PASS  indicator present: artex-enrich/1.0
  PASS  correlation rule fails to convert alone — it requires its atomic base rule
RESULT: PASS
```

任一断言失败返回非零。sigma-cli 固定 3.1.0，可用 `SIGMA_CLI_VERSION`、`PYTHON_IMAGE` 覆盖版本或内部镜像。

## Sigma 真实事件匹配：[`sigma_match/`](sigma_match/)

[`sigma_match/run.sh`](sigma_match/run.sh)验证[原子](../sigma/)和[关联](../sigma/correlation/)在匹配事件真正触发、正常事件静默，将 Suricata 的可复现承诺扩展到主机/日志层。包括三项原子与三项关联性质，共六项：

- **规则与样本双向配对：** 每条原子有 [`events/<名称>.json`](sigma_match/events/)，每样本指回真实规则；无样本新增规则必须失败。
- **真阳性：** 每规则匹配其全部恶意样本。
- **真阴性：** 不匹配任何正常样本，例如仅 `.mitmproxy/` 下 CA 不满足需要同时含 `_ca/` 的 `|all` 条件。
- **关联与时间线双向配对：** 每关联有 [`events/correlation/<名称>.json`](sigma_match/events/correlation/)，每时间线指回规则；事件 ts 为相对秒。
- **关联真阳性：** 同组、同窗满足阈值时触发，例如同 c-ip 在十分钟内接触二十个不同主机。
- **关联真阴性：** 阈值不足、过期、组拆分、temporal 缺少一侧均静默；数量大但不同主机少不触发 fanout，因为广度而非数量才是条件。

由 pySigma 而非手写代码解析：`|contains` 编译为通配值、`|all` 为 AND、`1 of selection_*` 为 OR，关联编译为类型/group-by/窗口/阈值/引用规格。[check.py](sigma_match/check.py)仅遍历编译树和规格，关联复用原子匹配器，权威逻辑保留在 pySigma。遇到不支持语法抛异常、fail-closed，不静默通过，范围见脚本头部。

窗口按每个匹配事件取 timespan 长度的滑动窗口，实际 SIEM 可不同。匹配忽略大小写，遵循目标 Splunk 默认及破坏性命令误报说明；关键词为全文子串。这只是字段/值/条件/聚合回归，不替代实际 SIEM 的字段规范化与环境验证。

### 执行

仅需 Docker，pySigma 在容器中运行，不向仓库写产物。

```sh
detections/tests/sigma_match/run.sh
```

预期输出（节选）：

```
  PASS  rule/sample pairing: 5 atomic rules, 5 event files, no orphans
  PASS  artex_enrich_user_agent: 1/1 positive events matched
  PASS  artex_recording_proxy_ca: 2/2 benign events correctly not matched
  PASS  rule/timeline pairing: 4 correlation rules, 4 timeline files, no orphans
  PASS  artex_enrich_fanout: fired — 20 distinct hosts from one source within the 10-minute window
  PASS  artex_enrich_fanout: quiet — high volume, low breadth: 25 requests from one source but only 4 distinct hosts
RESULT: PASS
```

任一断言失败返回非零。pySigma 固定 2.0.0，可用 `PYSIGMA_VERSION`、`PYTHON_IMAGE` 覆盖。

## Sigma 后端可移植性：[`sigma_backends/`](sigma_backends/)

[`sigma_backends/run.sh`](sigma_backends/run.sh)验证不只单个 Splunk 示例，保持[总览支持矩阵](../README.ko.md)真实可复现。两项均为正断言，仅实际回归失败：

- **关联可移植：** 整树可编译为 Splunk、Elasticsearch eql、Grafana loki，补全指标保留到查询，证明非 Splunk 专用。
- **原子回退：** 五条原子在 lucene、Microsoft kusto 编译；固定版本不支持关联，可部署原子并用产品原生窗口功能关联。

刻意不对“不支持关联”作负断言，避免后端新增能力反而使测试失败；当前局限写在 README，命令可复现。[check.sh](sigma_backends/check.sh)在容器安装固定 sigma-cli 与四种后端，只读加载规则树。

### 执行

仅需 Docker，sigma-cli 与后端在容器中运行，不写仓库。

```sh
detections/tests/sigma_backends/run.sh
```

预期输出（节选）：

```
  PASS  whole tree (atomic + correlation) converts on 'eql', enrich indicator survives
  PASS  five atomic rules convert on 'kusto', enrich indicator survives
RESULT: PASS
```

任一失败返回非零。sigma-cli 固定 3.1.0，由 `SIGMA_CLI_VERSION` 覆盖；后端使用兼容的最新插件，较易受上游升级影响。能力退化时应同时更新固定版本与 README 矩阵，不能忽略红色结果。

## SigmaHQ 约定检查：[`sigma_lint/`](sigma_lint/)

[`sigma_lint/run.sh`](sigma_lint/run.sh)让 README/CONTRIBUTING 的 check 承诺涵盖 SigmaHQ 约定。裸 sigma check 不加载 pySigma-validators-sigmahq，无法检查标题大小写、字段/日志源分类与引用约定。本套安装插件并按 [validators.yml](sigma_lint/validators.yml)基线执行全组，断言：

- **基线干净：** 带 validators.yml 的 check 零错误、零问题。
- **全组真实生效：** 无排除运行须报告已知的四类豁免问题且无其他类别。要求已知问题实际出现，防止插件未加载时空结果假通过。

四项豁免涉及 SigmaHQ 单仓库文件命名（日志源前缀与 correlation_）、分类（通用 application、无产品 process_creation）及分支而非 permalink 引用，不适合引用本仓库活文档的小型独立规则集；理由逐项写在 validators.yml。其余检查全部强制。插件固定 0.21.0，可用 `SIGMAHQ_VALIDATORS_VERSION` 覆盖；升级暴露新约定时须更新规则或基线。

### 执行

仅需 Docker，sigma-cli 与验证插件在容器中运行，不写仓库。

```sh
detections/tests/sigma_lint/run.sh
```

预期输出（节选）：

```
  PASS  sigma check with the documented baseline: 0 errors, 0 issues
  PASS  every reported issue is one of the four documented exclusions
RESULT: PASS
```

## ATT&CK 层：[`attack/`](attack/)

[`attack/run.sh`](attack/run.sh)检查 [Navigator 层](../attack/artex_navigator_layer.json)与[所声称覆盖的规则](../attack/)一致，让审查者从源码复现覆盖主张：

- **有效层：** 合法 JSON，具有 Navigator v4.x 必填字段、格式正确的技术 ID 与有效战术。
- **双向一致：** 有评分技术与 Sigma attack.* 技术标签集合精确一致，战术也一致，无缺失或多余项。
- **有依据：** 每个有评分技术的注释引用真实规则，改名或删除不能静默通过。

这是一致性而非触发测试，不需检测后端，仅 Python 标准库，没有版本敏感告警数。[check.py](attack/check.py)为容器内只读检查器，不写文件。

### 执行

仅需 Docker，检查在 Python 容器执行，不写仓库。

```sh
detections/tests/attack/run.sh
```

预期输出（节选）：

```
  PASS  scored techniques match the rule set exactly (8: T1059, T1105, T1485, T1489, T1557, T1561.002, T1592, T1595)
  PASS  scored tactics match the rule set exactly (collection, command-and-control, credential-access, execution, impact, reconnaissance)
RESULT: PASS
```

任一断言失败返回非零，可用于 CI/pre-commit。内部镜像用 `PYTHON_IMAGE` 覆盖。

## 指标来源真实性：[`indicators/`](indicators/)

[`indicators/run.sh`](indicators/run.sh)验证其他三类测试未覆盖的性质：指标仍是 ARTEX 源码实际发出的字符串。编译、Navigator 一致性与 pcap 触发均不能发现上游将 UA 改成 artex-enrich/2.0 或重写 guard 标记；旧规则可能全绿却不再匹配真实 ARTEX。逐指标双向断言：

- **源码仍生成：** 值仍存在于 enrich/enrich.go、selfupdate/、guard/guard.go 等指定来源；缺失说明上游变更未同步。
- **规则仍固定：** 值仍存在于依赖它的规则；Suricata 使用 startswith 前缀检查，符合实际线上匹配语义。
- **阻止列表映射：** `rm -rf`、`mkfs`、`DROP DATABASE`、`FLUSHALL` 同时存在于 db/db.go 阻止列表和对应狩猎规则；它们只是通用线索，不宣称独有指纹。
- **公开清单有依据：** 逐行检查 [CSV](../indicators/artex_indicators.csv)，每值仍在引用源码和规则，每个已验证指纹也须列入清单，双向防漂移。
- **来源改动触发两门禁：** 从指标推导来源集合，每个必须同时被 [CI push/pull_request paths](../../.github/workflows/detections.yml)及[本地 files 正则](../../.pre-commit-config.yaml)覆盖；新增来源如 cmd/artex/main.go 若只接一侧即失败，防止源码单独改动跳过检测。

这将[总览](../README.ko.md)和贡献契约的“全部指标来自本仓库而非推测”变为可重跑门禁。不需检测后端，仅标准库。[check.py](indicators/check.py)只读访问规则、CSV、固定源码与 CI/pre-commit 配置，不写文件。

### 执行

仅需 Docker，在 Python 容器检查，不写仓库。

```sh
detections/tests/indicators/run.sh
```

预期输出（节选）：

```
  PASS  enrichment prober User-Agent: 'artex-enrich/1.0' emitted by enrich/enrich.go
  PASS  detections/sigma/artex_enrich_user_agent.yml pins 'artex-enrich/1.0'
  PASS  'FLUSHALL' present in both db/db.go and detections/sigma/destructive_command_hunting.yml
  PASS  enrich-user-agent: 'artex-enrich/1.0' grounded in enrich/enrich.go
  PASS  tested fingerprint 'artex-enrich/1.0' is published in the list
  PASS  .github/workflows/detections.yml push paths covers cmd/artex/main.go
  PASS  .pre-commit-config.yaml files covers cmd/artex/main.go
RESULT: PASS
```

任一断言失败返回非零，可用于 CI/pre-commit。内部镜像用 `PYTHON_IMAGE` 覆盖。

## MISP 导出一致性：[`misp/`](misp/)

[`misp/run.sh`](misp/run.sh)验证第二种公开指标形式：[可导入 MISP 事件](../indicators/artex_indicators.misp.json)。CSV 测试负责来源，MISP 测试负责导入产物与 CSV 不漂移，断言：

- **真实 MISP：** 用 [pymisp](https://github.com/MISP/PyMISP)加载，其对象模型拒绝不合法 type，不只是声称有效。
- **逐行同步：** 每 CSV 行精确对应一个类型/类别正确的属性：http.user-agent → user-agent，guard string → pattern-in-file，port → port，ip-dst|port → 带 ip|port 的同类复合值，探索 schema other → other；无孤立属性。新增/删除/改类型须同提交更新事件。
- **to_ids 对应 rule：** 有规则支持的指标为 true；无规则主机取证行为 false 且 disable_correlation 为 true，不静默夸大或减少可用检测。
- **guard 标记字节保留：** CI paths 含 detections/**，CSV/事件改动必须触发套件。

不同于纯标准库套件，本套在容器安装固定 pymisp，不安装到主机。[check.py](misp/check.py)只读访问 CSV、事件与 CI 配置，不写文件。

### 执行

仅需 Docker，在容器安装 pymisp，不写仓库。

```sh
detections/tests/misp/run.sh
```

任一断言失败返回非零。用 `PYTHON_IMAGE`、`PYMISP_VERSION` 覆盖镜像及固定库版本。

## 贡献

新规则应附实际触发测试，确定性生成输入；版本无关性质精确断言，较宽性质使用有记录基线的下限；避免写成攻击指南。见[贡献指南](../../CONTRIBUTING.md)与[规则索引](../README.ko.md)。

八套均由[CI](../../.github/workflows/detections.yml)在 detections/ 的 push/PR 改动触发。指标测试也监视 enrich/、selfupdate/、guard/、db/、cmd/artex/main.go 等固定来源。指标丢失、层漂移、后端退化、约定违规、来源不同步、MISP 不一致及新增来源未接门禁，都应在合并前使构建失败。
