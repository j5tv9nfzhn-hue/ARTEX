# XSS 全链路审计（批次 8 · 条目 48–49）

批次 8 对「前端注入 / HTML 拼接」的审计与加固记录。结论：前端仅 2 处 `dangerouslySetInnerHTML`，均不接收不受信任输入，其中 chart 样式注入点已做防御性加固；Markdown 渲染链未启用原始 HTML 透传；后端只输出 JSON，**不渲染 HTML**，敏感接口带 `Cache-Control: no-store` 且凭据只以掩码/hint 形式出现。

> **状态（2026-10-16）：** 本文全部修复点已落地并经源码静态核对（`sanitizeStyleToken`、`noStore`、`maskMCPEnvForResponse` 均在位）；本轮未运行编译与测试（用户要求跳过）。

## 审计范围与方法

- 前端全量搜索：`dangerouslySetInnerHTML`、`innerHTML`/`outerHTML`、`insertAdjacentHTML`、`document.write`、`eval(`/`new Function(`。
- 逐点确认 2 处 `dangerouslySetInnerHTML` 的数据来源是否为用户可控。
- Markdown 渲染链检查（react-markdown 插件与 URL 处理）。
- 后端检查 HTML 模板/内容类型输出；核对凭据字段的响应面（掩码）。

## 前端结论

| 位置 | 数据来源 | 结论 |
|---|---|---|
| `web/src/scripts/theme-boot.tsx`（boot 脚本） | `JSON.stringify` 拼接的是本地常量配置（`lib/preferences/preferences-config.ts`），cookie/localStorage 读出的值只参与比较并写入 `classList`/`setAttribute`（React 属性转义） | 无注入面，无需改动 |
| `web/src/components/ui/chart.tsx` `ChartStyle` | `__html` 拼接 CSS 规则；`id` 来自 `useId`（当前唯一调用方 dashboard 不传 `id`），变量名/颜色来自开发者定义的 `ChartConfig` | **已加固**：新增 `sanitizeStyleToken`，剥离 `{}<>"'` 反斜杠与分号等可逃逸字符，杜绝 CSS/HTML 逃逸 |

其余检查结果：

- `web/src/components/markdown.tsx`：react-markdown + remark-gfm，**未启用 `rehype-raw`**，Markdown 文本里的原始 HTML 一律按文本转义渲染；链接统一 `target="_blank" rel="noreferrer"`；react-markdown 默认 `urlTransform` 过滤 `javascript:` 等危险协议。
- 全库未发现其他 `innerHTML` / `outerHTML` / `insertAdjacentHTML` / `document.write` / `eval(` / `new Function(` 使用。
- 所有后端数据（错误消息、报告、证据、模型输出等）均经 React JSX 文本插值渲染，天然转义。

## 后端结论

- server 只返回 JSON，不存在 `text/html` 模板输出；`text/template`（`server/server_mgmt.go` 的 `validateTemplate`/`renderPrompt`）仅用于提示词校验/示例渲染，产物进 LLM prompt，不落地为 HTML。
- 凭据不回传明文：settings 只回 `*_key_set` 布尔（`server/server.go` `getSettings`）；模型配置只回 `api_key_hint`；MCP env/headers 出库前经 `maskMCPEnvForResponse`（`server/mcp_env.go`）；通知渠道凭据掩码（`server/notify_api.go`）；凭据列表只回 hint 与引用关系（`server/llm_credentials.go`）。
- 携带凭据状态/掩码的 GET 全部 `Cache-Control: no-store`（`server/server.go` 的 `noStore`），防止共享缓存或浏览器磁盘缓存回放他人凭据视图。

## 残留与建议

- 若未来给 `ChartContainer` 传入外部可控的 `id` 或颜色值，`sanitizeStyleToken` 已兜底；仍应避免把用户输入拼进任何 `__html`。
- 升级 react-markdown 时保持不引入 `rehype-raw`；如确需渲染原始 HTML，必须先接入白名单净化（如 rehype-sanitize）。
- `web/src/lib/mock/` 下的演示数据只用于开发模式（无后端时的内置 mock），不构成生产响应面。
