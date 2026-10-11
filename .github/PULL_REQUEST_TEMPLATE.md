<!--
  本模板适用于 ARTEX 简体中文版。
  提交前请阅读 CONTRIBUTING.md 的贡献规范与本地化原则。
-->

## 摘要

<!-- 用一两句话说明改动内容与原因。 -->

## 改动类型

- [ ] 缺陷修复（`fix`）
- [ ] 新功能（`feat`）
- [ ] 文档（`docs`）
- [ ] 本地化或翻译（`i18n`）
- [ ] 重构或整理（`refactor` / `chore`）
- [ ] 其他：

## 关联 Issue

<!-- 例如：Closes #123 -->

## 验证方式

<!-- 列出实际执行的命令、结果与未验证项目，不将 SKIP 当作数据库测试通过。 -->

- [ ] Go 改动：`go build ./...`、`go vet ./...`、`go test ./...`。
- [ ] 涉及数据库的包：在独立 PostgreSQL 环境运行集成测试。
- [ ] Web 改动：`npm test`、`npm run check`、`npm run build:static`。
- [ ] 文档改动：`python3 -I scripts/check-doc-links.py`。
- [ ] 语言残留：`python3 -I scripts/check-language.py --source`，构建后加 `--web`。
- [ ] UI 改动：附截图，并说明是否为演示数据。

## 本地化检查

- [ ] 保留内部推理提示词的技术语义、工具接口及角色边界。
- [ ] 项目自有文案、注释、输出提示和说明使用简体中文，无韩文残留。
- [ ] 命令、payload、API/JSON 字段、工具名、URL、原始日志和用户证据没有被误译。
- [ ] UI 使用 `web/messages/zh.json` 消息键，en/es 键同步，ko 兼容副本与 zh 一致。
- [ ] 默认 zh、旧 ko 归一 zh 的行为及格式占位符经过回归验证。

## 使用范围确认

- [ ] 改动不鼓励超出[使用范围](../README.md#使用范围与法律告知)或违反适用法律的行为；验证仅在自有、明确授权目标或本地隔离环境进行。
- [ ] 未提交测试/扫描产物、凭据、API Key、私钥或运行时数据库。
