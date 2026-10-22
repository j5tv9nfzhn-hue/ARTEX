# ARTEX 移动端（Android / iOS）

> 这里是「双端 APP」的工作区。当前状态：**R0 调研与决策中，尚未开始编码**。
> 目录、结论、轮次计划都以本目录下的文档为准；代码目录等 R1 立项后再落地，避免先建空壳。

## 一句话定位

手机 App 是 ARTEX 的**远程客户端**，不是把 agent 搬到手机上：agent 依赖 Bash/工具链、工作目录、
SQLite、本地证书与流量代理，这些只在 PC / 服务器上成立。APP 负责「连上去、看得到、下得动指令」。

```
   ┌────────────┐        HTTPS / WSS         ┌─────────────────────────┐
   │ Android APP│ ─────────────────────────► │  ARTEX 服务端（Go）      │
   ├────────────┤                            │  server/ + agent/ + db/ │
   │  iOS APP   │ ─────────────────────────► │  PC 或 云主机 / 三端同步 │
   └────────────┘        （同一套 API）      └─────────────────────────┘
```

## 三条备选路线（详见 `docs/Go与移动端适配度调研-2026-10-22.md`）

| 路线 | 做法 | 结论 |
| --- | --- | --- |
| A. 复用 web 前端 + 原生壳 | `web/` 的 Next.js 静态导出 → Capacitor / WKWebView+WebView 壳 | **首选**（最快、三端 UI 一致、维护成本最低） |
| B. Flutter / React Native | 原生渲染 UI，直连 REST + WSS | 备选（需要原生体验、推送、后台任务时） |
| C. Go 核心 + gomobile | 把 Go 编译成 AAR / XCFramework 塞进 App | **不推荐做主线**（详见调研第 6 节） |

## 目录约定

```
mobile/
  README.md                     ← 本文件
  docs/                         ← 调研、轮次计划、协议草案
  (R1 之后) app/                ← Capacitor / RN 工程
  (R1 之后) shared/             ← 与 Go 端共享的 OpenAPI / 类型定义 / 测试夹具
```

## 铁律（与仓库根目录约定一致）

- 不在本地跑 `go build/vet/test/gofmt`、`npm`、`npx tsc/biome`、`build.sh`、`dev.sh`；移动端同样适用。
- 一个语义一个中文提交；不提交构建产物、密钥、证书。
- 界面文案中文为唯一词库来源；对外文案与 `web/messages/zh.json` 保持同一套口径。

## 轮次计划

见 [`docs/双端APP-多轮计划.md`](docs/双端APP-多轮计划.md)：R0 调研 → R1 接入层 → R2 只读 MVP →
R3 指令与推送 → R4 离线与双向同步 → R5 打包发布。
