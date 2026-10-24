# 内置技能包来源与许可

本目录下除 `case-web-*`、`ai-coding-sandbox-tactics`、`wasm-cloud-saas-tactics` 之外的
技能包，全部来自上游开源仓库，未做手写改写：

- 仓库：https://github.com/mukul975/Anthropic-Cybersecurity-Skills
- 固定提交：`54a798831d2266a3ca61ce68a7acb80b81160d57`
- 许可证：Apache License 2.0（全文见同目录 `LICENSE-APACHE-2.0`）
- 内置规模：818 个技能包（`SKILL.md` + `references/` + `scripts/` + `assets/`）
- 同步方式：按上述提交整包拷贝；未逐条翻译或改写。上游每个技能目录自带的
  LICENSE 副本（817 份完全相同的 Apache 全文）已去重，合并为本目录这一份。

ARTEX 自有的任务沉淀技能包（`case-web-*` 等 12 个）来自 ARTEX 任务复盘自动沉淀，
位于同一目录，与上游技能包共用同一 `skills/{id}/SKILL.md` 结构。

## 许可合规

Apache-2.0 要求随分发提供许可证副本与变更说明：本文件即变更说明（去重 LICENSE、
未修改技能正文）。若需再分发，请保留 `LICENSE-APACHE-2.0` 与本文件。
