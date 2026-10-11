# ARTEX 机器可读威胁指标


[`artex_indicators.csv`](artex_indicators.csv)汇总 ARTEX 实际产生的指纹，供威胁情报平台、SIEM 查询表和 triage 清单导入，而非描述检测逻辑。所有值均有源码依据，与[规则](../README.md)及防御指南[中文](../../docs/defense.md)/[English](../../docs/defense.md)第 2 节相同。每行注明来源及适用规则。

## 列定义

- **`id`**：稳定指标 slug。
- **`type`**：`http.user-agent`、`string`（日志/文件字面量）、`port`、`ip-dst|port`、`other`（如 DB schema 对象名），映射相应 MISP/STIX 属性类型。
- **`value`**：精确值，非 ASCII guard 标记也保持原样。
- **`perspective`**：`target` 为发往被探测系统的流量，`forensic` 为运行或中转 ARTEX 的主机侧痕迹；混淆视角会导致错误结论。
- **`source`**：产生指标的仓库相对源码路径，以 `;` 分隔；来源改变时指标必须同步。
- **`rule`**：基于精确值的规则，以 `;` 分隔；直接主机分类、未发布噪声规则的行为空。
- **`description`**：单行说明，必要时注明局限。

## MISP 事件导出

同一指标也提供 [MISP](https://www.misp-project.org/) 事件 [`artex_indicators.misp.json`](artex_indicators.misp.json)，无需手工映射 CSV 即可导入 MISP 或兼容平台。STIX 2.1 可由 MISP 自身导出，本仓库不另外维护可能丢失信息的第二格式。

- **类型映射：** `http.user-agent → user-agent`；guard `string → pattern-in-file`（Artifacts dropped）；`port → port`；`ip-dst|port → ip-dst|port`，MISP 用 `ip|port`，故 `127.0.0.1:8788` 存为 `127.0.0.1|8788`；schema `other → other`（Other）。
- **`to_ids` 依据 rule：** 有规则的可检测指标为 true；默认端口、回环代理、schema 只是分类线索，设 `to_ids: false`、`disable_correlation: true`，避免常见端口、127.0.0.1、通用表名污染关联。
- **导入：** 用 [pymisp](https://github.com/MISP/PyMISP) 的 `MISPEvent().load_file("artex_indicators.misp.json")`、UI 的 *Add event → Populate from … → MISP format* 或 REST API。事件默认未发布，带 `tlp:clear`，导入时按实例设置分发范围及发布状态。

## 正确理解指标与局限

- **指纹可变，缺失不证明安全。** UA、端口可以修改，缺少任一值不证明没有 ARTEX；持久信号是行为，见关联规则及指南 1、2、4.1–4.2 节。
- **通用狩猎线索刻意排除。** `rm -rf`、`DROP DATABASE` 等也被正常管理员使用，不是 ARTEX 专属 IoC；保留在[破坏命令规则](../sigma/destructive_command_hunting.yml)和指南，而非导入为阻断指标。
- **norma WebFetch UA 是网络签名，不是专属导入指标。** `norma/0.4` 由 SDK `github.com/Autumn-27/norma/tool/webfetch.go` 固定，所有 norma 应用可能发送。Suricata sid 1000003 匹配 `norma/`，但 CSV 不收录，以免对所有 SDK 流量误报。该值来自固定依赖而非本仓库源码，指标来源测试不能像 ARTEX 本地字符串一样重读它。见 [Suricata 说明](../suricata/README.md)。
- **主机端口用于分类，不是盲目阻断。** `:8787`、`127.0.0.1:8788` 应通过 ss/netstat 及上下文确认。
- **探索 schema 是 DB 调查线索。** `exploration_nodes` 应与 `exploration_edges`、`exploration_anchors`、`assets`、`companies`、`activity` 及 agent_prompts 种子共同确认；单表不足以归属，改名或删除后的缺失也不证明安全。
- **rule 为空的三个主机/DB 指标有执行工具。** [只读 triage](../triage/)检查监听、代理与 schema，让无 SIEM 的响应人员无需逐项手工操作 ss/netstat/psql。

## 验证

[指标来源测试](../tests/indicators/run.sh)重读 CSV，要求每行的值仍存在于指定源码并固定于指定规则，且全部已知指标都在清单中。来源漂移或遗漏会失败。执行方法如下。

```sh
detections/tests/indicators/run.sh
```

[MISP 同步测试](../tests/misp/run.sh)用 pymisp 加载有效属性类型，逐行比较值、预期类型/类别、与 rule 对应的 to_ids。事件手工维护，另有属性注释、稳定 UUID、事件标签，不能用有损生成器覆盖。CSV 增删或改类型时，同提交更新 [`artex_indicators.misp.json`](artex_indicators.misp.json)，新属性提供新 uuid 与来源 comment；不一致时测试失败。执行方法如下。

```sh
detections/tests/misp/run.sh
```
