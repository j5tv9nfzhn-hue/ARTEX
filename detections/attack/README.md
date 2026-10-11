# ARTEX ATT&CK 覆盖率


本层将检测规则的 [MITRE ATT&CK](https://attack.mitre.org/) Enterprise 技术汇总为 Navigator，手工依据 [Sigma](../sigma/) 的 `attack.*` 标签生成。每项来自实际源码字符串/行为，不含推测。[一致性测试](../tests/attack/run.sh)确保层与规则双向一致。

- **`artex_navigator_layer.json`**：ATT&CK Navigator v4.5 格式的层。

## 分值含义

覆盖表示“本仓库有标记该技术的检测”，不是“该技术已完整覆盖”。分值刻意区分检测强度：

- **100：ARTEX 专属指纹或行为。** 补全、自更新 UA、guard 审计标记，或基于这些指标的速度、fanout、guard 突发。
- **50–65：通用狩猎线索。** 破坏性命令可能来自正常管理员，不能凭命中归属攻击。65 表示与 ARTEX guard 标记关联，提高了特异性。

## 覆盖技术

八种技术横跨六种战术，每项对应实际规则：

- **侦察：T1595（Active Scanning）、T1592（Gather Victim Host Information）。** [`artex_enrich_user_agent.yml`](../sigma/artex_enrich_user_agent.yml)、[速度](../sigma/correlation/artex_enrich_scan_velocity.yml)、[fanout](../sigma/correlation/artex_enrich_fanout.yml)与 [Suricata](../suricata/artex.rules) sid 1000001/1000002。
- **命令与控制：T1105（Ingress Tool Transfer）。** [`artex_selfupdate_egress.yml`](../sigma/artex_selfupdate_egress.yml)。
- **执行：T1059（Command and Scripting Interpreter）。** [guard 标记](../sigma/artex_guard_audit_framing.yml)、[guard 突发](../sigma/correlation/artex_guard_block_burst.yml)、[标记与破坏行为关联](../sigma/correlation/artex_guard_marker_then_destructive.yml)。
- **影响：T1485（Data Destruction）、T1561.002（Disk Structure Wipe）、T1489（Service Stop）。** [破坏性命令](../sigma/destructive_command_hunting.yml)，T1485 另有[多阶段关联](../sigma/correlation/artex_guard_marker_then_destructive.yml)。
- **凭据访问/收集：T1557（Adversary-in-the-Middle）。** [记录代理 CA](../sigma/artex_recording_proxy_ca.yml)检测 `traffic/traffic.go` 为解密、记录 Worker 流量生成的 MITM 根 CA，属于主机取证狩猎线索。

## 使用方法

1. 打开 [ATT&CK Navigator](https://mitre-attack.github.io/attack-navigator/)。
2. 选择 **Open Existing Layer → Upload from local**，加载 `artex_navigator_layer.json`，或使用本仓库 raw URL。
3. 技术按检测强度着色，注释注明来源规则和防御指南章节。

## 范围与局限

- **覆盖不代表完整。** 标记技术不意味着检测其所有变种。网络侧两类 UA 仅覆盖补全 `artex-enrich/1.0` 与 WebFetch `norma/0.4`，其余流量使用工具自身指纹；持久检测依赖行为，见防御指南[中文](../../docs/defense.md)/[English](../../docs/defense.md)第 1–2、4.1–4.2 节。纯 Web 多阶段仍需环境基础规则。
- **静态指标可变。** UA 可被配置，未出现不表示安全；同样限制也写在规则文件。

## 验证与贡献

运行[一致性测试](../tests/attack/run.sh)，仅需 Docker。它要求计分技术/战术与规则 `attack.*` 标签完全一致，且每项都指向真实规则文件。

```sh
detections/tests/attack/run.sh
```

添加规则或修改标签时同步更新层；规则技术缺失于层、或层技术不存在于规则，均会失败。见[规则总览](../README.md)和[贡献指南](../../CONTRIBUTING.md)。
