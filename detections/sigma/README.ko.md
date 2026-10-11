# ARTEX 检测规则（Sigma / 主机、日志、SIEM）

简体中文 · [English](README.md)

本目录负责主机/日志/SIEM 层，将防御指南[中文](../../docs/defense-ko.md)/[English](../../docs/defense-en.md)第 4 节表达为厂商中立 [Sigma](https://sigmahq.io)，再转换为产品查询。所有指标基于实际源码，不是猜测。网络层在 [suricata](../suricata/)，完整规则、ATT&CK、CSV/MISP、triage 入口见[总览](../README.ko.md)。仅用于防御自有或书面授权系统。

## 原子（atomic）规则

每条对应一个可观察事实，可单独或作为整树一部分转换。

- **[`artex_enrich_user_agent.yml`](artex_enrich_user_agent.yml)**：*ARTEX Asset Enrichment Probe User-Agent*，`enrich/enrich.go` 入站 `artex-enrich/1.0`，目标侧辅助指标，high。
- **[`artex_selfupdate_egress.yml`](artex_selfupdate_egress.yml)**：*ARTEX Self-Update Egress User-Agent*，`selfupdate/github.go` 出站 `artex-selfupdate`，主机取证 egress，medium。
- **[`artex_guard_audit_framing.yml`](artex_guard_audit_framing.yml)**：*ARTEX Platform Guard Audit-Log Framing*，`guard/guard.go` 工具阻断日志的平台标记，主机取证，high。
- **[`artex_recording_proxy_ca.yml`](artex_recording_proxy_ca.yml)**：*ARTEX Recording-Proxy MITM CA Certificate Artifact*，`traffic/traffic.go` 的 `_ca/mitmproxy-ca-cert.pem`，因文件名与独立 mitmproxy 共享，仅作狩猎线索，medium。
- **[`destructive_command_hunting.yml`](destructive_command_hunting.yml)**：*Destructive Command Execution (ARTEX Guard-List Hunting)*，对应 `db/db.go` deny 列表，不是 ARTEX 专属，medium。

## 关联（correlation）规则（行为检测）· [`correlation/`](correlation/)

关联规则表达指南 4.1–4.2、4.4 节行为层。通过 `id` 引用原子规则，因此**必须转换整棵 sigma 树**；[Sigma 测试](../tests/sigma/)验证真实引用依赖。

- **[Enrichment Scan Velocity](correlation/artex_enrich_scan_velocity.yml)**：同源短窗口集中探测；补全并发 4、无限速，`event_count`，high。
- **[Enrichment Fan-Out](correlation/artex_enrich_fanout.yml)**：同源探测不同主机，关注广度而非仅请求量，`value_count`，high。
- **[Guard-Block Burst](correlation/artex_guard_block_burst.yml)**：同主机重复产生 guard 标记，区分运行触发与文档引用，`event_count`，high。
- **[Guard Marker With Destructive Command](correlation/artex_guard_marker_then_destructive.yml)**：同主机窗口内标记与破坏命令组合，提高特异性，`temporal`，high。

阈值和窗口仅为保守起点，应按自身基线调整。纯 Web 枚举→探测→认证不能归结为单一专属 UA，仍需环境基础检测。[指南 4.2](../../docs/defense-ko.md)提供通用模板，因没有 ARTEX 源码固定依据，刻意不纳入本已测规则树。

## 部署前须知：范围与局限

- **静态指标可变。** UA、CA 文件可修改，原子指标缺失不证明安全；长期信号是持续、自适应的侦察→枚举→探测→认证/注入行为。
- **破坏命令是通用狩猎。** 结合管理员正常活动、允许列表和上下文，不凭单次命中归属 ARTEX。
- **端口/schema 属主机取证。** `:8787`、`127.0.0.1:8788` 与探索 schema 由 [CSV](../indicators/)及 [triage](../triage/)表达，不发布高噪声网络规则。
- **logsource 与字段为通用值。** category/product、`cs-user-agent`、`CommandLine`、`TargetFilename` 须通过 `-p` 管线映射实际产品 schema。

## 验证与转换

原资料使用 [sigma-cli](https://github.com/SigmaHQ/sigma-cli)（pySigma）验证；从仓库根执行：

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install sigma-cli

# 结构与最佳实践，预期 0 errors、0 issues
sigma check detections/sigma/

# SigmaHQ 全部约定，按已记录基线预期 0 issues
pip install pySigma-validators-sigmahq
sigma check --validation-config detections/tests/sigma_lint/validators.yml detections/sigma/

# 转换整树，以解析关联规则引用
sigma plugin install splunk
sigma convert -t splunk --without-pipeline detections/sigma/
```

后端关联支持不同，Splunk、Elasticsearch EQL、Loki 转换整树；Lucene、OpenSearch、Microsoft kusto 转换五条原子，在产品内原生表达时间窗。实测支持矩阵、`--without-pipeline` 和 `-p` 映射见[总览](../README.ko.md)，由 [sigma_backends](../tests/sigma_backends/)复现。

## 测试

[`tests/`](../tests/)中四套 Sigma 测试仅需 Docker：[sigma](../tests/sigma/)验证结构、整树编译及关联单独失败；[sigma_match](../tests/sigma_match/)验证恶意触发、正常静默；[sigma_backends](../tests/sigma_backends/)验证五种后端；[sigma_lint](../tests/sigma_lint/)验证全部 SigmaHQ 基线零问题。见[测试说明](../tests/README.ko.md)。

## 贡献

新规则应有可观察指标依据，description 明确局限，通过 `sigma check --validation-config ../tests/sigma_lint/validators.yml .`，不得成为攻击教程。见[贡献指南](../../CONTRIBUTING.md)、[网络层](../suricata/)与[总览](../README.ko.md)。
