# ARTEX 手机端 = 总控台（独立设计，不是网页缩小版）

**定位**：手机端不是 PC 工作台的搬运，也不是公网 web 面板的缩小版，而是**驾驶舱**——
控制管理、信息与流量消耗、完整通知（含系统提示）、进度展示、模型消耗。

**两条硬边界**
1. **功能不搬运**：手机看全局与结论，PC 看细节与编辑（编辑器 / diff / 长文 / 终端一律回 PC）。
2. **界面不复用**：独立设计一套原生 UI，不 import `web/` 的任何组件与样式；只共享 API 契约与数据类型。
   否则用户一眼看出是网页套壳，会直接问：**为什么不打开公网面板？**

## 手机端必须给出的 7 件网页给不了的东西

锁屏推送 · 通知直达对象 · 离线可读 · 手势快操作（左滑停止 / 右滑已读）· 主屏角标 · 首屏即结论 · 生物识别解锁。
做不到这些，就不该发 App。

## 五块功能（范围）

| # | 功能 | 要点 |
| --- | --- | --- |
| 1 | **总控控制管理** | 在线状态、运行/排队任务、待审批数、启停与切换、批/拒审批、切模型档位 |
| 2 | **信息消耗面板** | 资产增量、流量条数、拦截命中、缓存命中率，可按任务/类型钻取 |
| 3 | **完整通知（含系统提示）** | 分级收件箱：P0 高危/审批/失败/掉线，P1 阶段完成/预算，P2 系统提示；全量入库可回溯 |
| 4 | **进度展示** | 阶段→步骤→当前工具与耗时，实时增量，停止/重试 |
| 5 | **模型消耗** | 输入/缓存/输出 token、按模型与任务、当日累计、预算使用率与告警 |

## 技术栈与形态

- **React Native + Expo（TypeScript）**：原生组件渲染、`expo-notifications` 推送、EAS Build 云构建（iOS 无需本地 macOS）、EAS Update 热更。
- **排除 Capacitor / WebView 壳**：那正是"网页套壳"，与本轮决定相反。
- 深色驾驶舱设计语言；底部 4 tab（总览 / 任务 / 通知 / 消耗）；主要动作在拇指区，触控 ≥44pt。

## 目录

```
mobile/
├── README.md                                    ← 本文
└── docs/
    ├── 移动端UI独立设计-2026-10-22.md            ← 主文档：为什么不复用 web、选型对比、设计 token、四屏 IA、U1–U6
    ├── 移动端总控台-范围与轮次-2026-10-22.md     ← 五块功能、数据来源映射、通知分级、T1–T6
    ├── 移动端策略-三通道-2026-10-22.md           ← 通道：原生 App / 公网面板兜底 / IM Bot / 远程桌面
    ├── Go与移动端适配度调研-2026-10-22.md         ← 若真要端侧 Go 的否决证据（体积/类型/后台限制）
    └── archive/
        └── 双端APP-多轮计划-已驳回.md            ← 被驳回的旧方案（功能搬运），留档
```

## 现状实测（关键缺口）

- **数据基本齐了**：`/api/stats`（`server/server.go:1215`）已含 `engine_mode`、`llm_configured`、`traffic`、
  `assets`、`exploration`、goals/intents、`last_activity`；`dashboard/page.tsx` 已聚合
  `Stats / UsageStats / ConvTokenSummary / ReliabilityMetrics / Finding / InterceptPending / TrafficExchange /
  LLMProfile / Task` → **手机端对齐这套口径即可，不新造业务**（但界面另写）。
- `notify/` 七路**出站**通知齐全，但全仓无 `getUpdates` / `setWebhook` / inbound → 入站命令要新增。
- 缺 `/api/push/register`（推送 token 上报）、缺统一收件箱接口（列表/已读/静默）。
- `web/` 无 `manifest.json`（面板也不打算做成 PWA 主路径，仅作兜底）。

## 铁律

- 本目录以文档为主；移动端同样不做本地构建（不跑 `npm` / `npx expo` / `go build`）。
- 手机端不跑 agent、不跑 Bash、不跑代理；不复制数据模型。
- 危险动作（停止、批量审批、改预算）一律二次确认。

## 轮次索引

**U1–U6（界面与 App）**：骨架 → 总览+消耗 → 通知收件箱 → 进度与控制 → 推送与审批 → 打磨上架
（见《移动端UI独立设计》）。
**T1–T6（服务端与数据）**：只读总览 → 通知接口 → 进度接口 → 推送注册 → 消耗口径 → 令牌与设备管理
（见《移动端总控台》）。

---

## 已落地（2026-10-22 · 提交 `8ea4432`）

**后端（Go）**

| 接口 | 说明 |
| --- | --- |
| `GET /api/mobile/overview` | 总览一次拉全：引擎状态、运行/排队/暂停、未读告警（含按级别）、今日 token、资产与流量 |
| `GET /api/mobile/alerts?limit&since_id&level&unread` | 分级收件箱，附未读总数与按级别分布 |
| `POST /api/mobile/alerts/read` `{ids}` | 标记已读 |
| `POST /api/mobile/alerts/read-all` | 清空未读 |
| `GET /api/mobile/usage?days=7` | 近 N 天 token 曲线 + 按配置档分布（金额口径仍在 web） |
| `POST /api/mobile/push/register` `{platform,token,device}` | 设备推送凭据注册 |
| `DELETE /api/mobile/push/register?token=` | 注销设备 |

- 新增表：`mobile_alerts`（收件箱，含 `read_at`）、`mobile_push_tokens`（设备凭据）。
- `server/ui_notify.go` 的 UI 事件现在**异步落库**，手机端收件箱因此可回溯、可统计未读；落库失败不影响原内存通知。
- 任务控制复用 `POST /api/tasks/control/batch`（只支持 `pause` / `resume`；ARTEX 的「停止」即暂停）。

**前端（Expo + React Native + TypeScript）**

- 明暗**双色系统**：`src/theme/tokens.ts` 两套完整 token，`ThemeProvider` 支持「跟随系统 / 浅色 / 深色」并持久化。
- 四屏：总览 / 任务 / 通知 / 消耗；另有登录（服务器地址 + 密码）、设置、任务详情。
- 数据层：`usePoll` 轮询（前台 8–10s，后台不请求），Bearer 鉴权，令牌存 `expo-secure-store`。
- 组件全部自写（Card / StatNumber / StatusDot / ProgressRing / Bars / Chip / Empty / Screen），**与 `web/` 零复用**。

**CI**：`.github/workflows/mobile.yml`（`npm install` + `tsc --noEmit`）自 v1.0.0 起为**硬门禁**——版本已在云端解析通过，不再 `continue-on-error`。

### 第一版补齐（提交 `mobile-v1.0.0`）

- **审批**：通知页顶部「待审批」区，展示工具名/调用方/原因/入参预览，批准或拒绝都需二次确认（复用 `GET /api/intercept/pending` 与 `POST /api/intercept/pending/{id}/decide`）。
- **任务进度**：新增 `GET /api/mobile/tasks`，按目标达成率给出 0..1 的完成度；没有目标时返回 `-1`，界面画灰环而不是假装 0%。列表与详情都用真实比例。
- **离线兜底**：`usePoll` 支持缓存键，请求失败时回落到上次成功的载荷，并在页脚标注「缓存数据 · 更新时间」。
- **打包**：`.github/workflows/mobile-build.yml` —— Android `assembleRelease` 出 APK，iOS 在 macOS runner 上不签名构建出模拟器 `.app`，两者都作为 artifact 上传（保留 14 天）。

### 仍未做（明确不假装完成）

- [ ] **推送发送端**：只做了设备令牌注册与表结构，没有实现 APNs/FCM 发送。要真正弹通知，需 Apple/Google 凭据，或改用 Bark/ntfy 这类转发。
- [ ] **iOS 真机/上架**：工作流产出的 `.app` 只能装模拟器；真机需要 Apple 开发者账号与证书（建议届时走 EAS Build）。
- [ ] **金额口径**：token 与 PC 同源，但价格折算仍在 web。

### 本地怎么跑（不在 CI 里）

```bash
cd mobile && npm install && npx expo start
```

仓库铁律：这台机器不跑构建，上面这条只写在这里备查。
