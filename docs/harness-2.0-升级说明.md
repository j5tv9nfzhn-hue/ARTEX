# Harness 2.0：对标主流 harness 的能力补齐与缺陷修复

> 2026-10-20 · 依据仓库既有两份报告（`Harness工作台-TS实现说明.md`、`Harness与控制台-功能对照.md`）
> 与 2026 年 Claude Code / Codex CLI 的能力形态，对 `/harness` 做的一轮集中升级。
> 全部改动只落在 `web/src/{lib,components}/harness`、`(standalone)/harness` 与 `db/conversation.go`，
> 控制台未动。

---

## 1. 先说修掉的三个真缺陷

| # | 现象 | 根因 | 修复 |
|---|---|---|---|
| 1 | 右栏「计划」永远是空的 | `buildPlan` 只认 `kind="intent"`，而 chat agent 从不产生 intent | 增加 TodoWrite 解析：`{todos:[{content,status}]}` → `todo/active/done`；intent 仍优先 |
| 2 | 计划面板即使解析也拿不到完整清单 | `ConvActivityList / ConvActivityPage` 只 SELECT summary（前 200 字） | `bodyCol` 增加 `tool='TodoWrite'` 的 detail（小 JSON，不是大输出） |
| 3 | 「计划/编辑/自动」只是装饰，后端完全不知道 | 权限档只在 UI 上标注意图 | 选定后把约束写进消息开头（气泡里可见），agent 真能读到；文案同步改成实际行为 |

> 第 1、2 条是"看起来没功能"的典型：面板在、按钮在、数据也在库里，只是**取数与投影各缺一半**。

---

## 2. 新增能力（对标依据）

| 能力 | 对标 | 实现 |
|---|---|---|
| 斜杠命令 `/new /stop /rename /model /perm /changes /plan /files /rollback /copy /status /help` | Claude Code `/rewind /model /permissions`；Codex `/new /model /diff /status` | `lib/harness/commands.ts` + composer 浮层 |
| ⌘K 命令面板（命令 + 会话一起搜） | Codex `/agents`、Claude 会话选择器 | `components/harness/command-menu.tsx` |
| 会话重命名 / 置顶 / 删除 | Claude Code 会话管理 | 左栏行内操作（不用 `window.confirm`，iframe 会吞） |
| 模型切换（会话级 LLM 配置） | Codex `/model` | 页头下拉 → `updateConversationProfile`，下一轮生效 |
| Esc 停止本轮 | Claude Code Esc 中断 | 全局快捷键；输入框已处理的 Esc 不重复触发 |
| 窄屏右栏 | — | `<1280px` 原来直接消失，现在按钮/命令打开抽屉 |
| 带模式的消息 | — | 只读/自动模式在消息开头声明，所见即所发 |

---

## 3. 关键设计取舍

- **不新增假能力**：命令表里每一项都对应一个真的能执行的动作；后端没有的能力（fork、checkpoint 菜单、
  `/compact`）不列入，避免"点了没反应"。
- **不猜数据**：TodoWrite 的 JSON 解析不出来就返回空，绝不按 summary 猜步骤；被截断的记录回退到
  上一份完整快照（有单测覆盖）。
- **不做隐式删除**：删除会话要二次确认；回滚仍然把正文还回输入框（上一轮修复）。
- **显示与行为一致**：权限档从"提示"改为"真的写进消息"。

---

## 4. 验证

| 项 | 结果 |
|---|---|
| `npx tsc --noEmit` | 通过 |
| `npm test` | 32/32（新增 7 条：斜杠匹配、fuzzy、TodoWrite 计划投影） |
| `biome check`（harness 域 + page） | 0 error |
| `go build ./...` / `go vet ./db ./server` / `gofmt -l db` | 干净 |
| `next build`（静态导出） | **未在本机跑**：Windows 下 `NEXT_EXPORT=1 next build` 是 shell 语法，需 CI 或 WSL 验证 |

---

## 5. 未做 / 下一步

1. **@ 提及**（漏洞/资产/接口）：控制台已有 `chat-mentions.ts` 与 `/chat/mentions`，
   harness 输入区还没有弹层；接口形态已确认（`{items,next_cursor}`），可复用。
2. **会话 fork**：后端没有克隆接口，现在只能新建。要做需要一条 `POST /api/conversations/{id}/fork`。
3. **Checkpoint 菜单**：现在只有"撤回上一条"，Claude Code 的 Esc-Esc 列表（可选任意用户消息回滚）
   需要前端把 `items` 里的 user 步骤列成菜单 —— 数据已够，纯 UI 工作。
4. **单条消息操作**：复制、按条重发。
5. **Ctrl+Enter 在审批时允许**：注释里写了，实际没实现。
