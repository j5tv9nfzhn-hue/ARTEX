# ARTEX Go SDK（artexsdk）

ARTEX REST 的官方 Go 客户端，覆盖两个面：**协议信封面** `/api/protocol/*`
（Thread/Turn/Item/Approval/Tree/Hooks，统一信封）与**平台原生面**
（`/api/publish`、`/api/options`、`/api/export/*`、`/api/openapi.json`，原生 JSON）。只依赖标准库，
与服务端 `server/protocol_api.go` 的统一信封对齐：

```json
{"protocol":"v1","resource":"threads","action":"list","id":"","data":{...}}
```

## 安装

```bash
go get github.com/Autumn-27/artex/sdk/artexsdk
```

## 快速上手

```go
client, err := artexsdk.New("http://127.0.0.1:8080", artexsdk.WithAPIKey("…"))
if err != nil { … }

// 线程
threads, cursor, err := client.ListThreads(ctx, 0, 50)
_, err = client.CreateThread(ctx, map[string]any{"title": "新任务"})
_, err = client.ControlThread(ctx, "task-1", map[string]any{"action": "start"})

// 轮次 / 消息 / 工作控制
_, err = client.CreateTurn(ctx, "task-1", map[string]any{"kind": "chat", "text": "你好"})
_, err = client.Steer(ctx, "task-1", map[string]any{"text": "改做 B"})
_, err = client.WorkControl(ctx, "task-1", 3, map[string]any{"action": "pause"})
_, err = client.Kill(ctx, "task-1") // 紧急停止走 Engine.KillWork，唯一停止入口

// 会话树 / 发布流与导出
_, err = client.GetThreadTree(ctx, "task-1")
_, err = client.Hooks(ctx)
rc, err := client.PublishStream(ctx)        // SSE，调用方关闭 body
err = client.Stream(ctx, "/api/publish/stream", func(line []byte) bool { …; return true })

// Options：唯一显式下发面（其它端点均为透传/读取，无隐式下发）
prof, err := client.GetModelProfile(ctx)
_, err = client.SetModelProfile(ctx, "deep")
```

## 设计约定

- **显式下发**：只有 Options 系列方法会修改运行时配置；非 Options 方法绝不隐式下发。
- **广播类指令显式化**：如 `Kill` 必须单独调用，不做批量/隐式触发。
- **透传优先**：对上游既有 DTO 原样透传的方法参数用 `any`，响应以 `Envelope.Data`
  （`json.RawMessage`）保留，调用方按需二次解码。
- **错误**：HTTP ≥400 返回 `*APIError`（含状态码与 `error` 文本）。

## 测试

```bash
go test ./sdk/... -count=1
```

测试全部基于 `httptest`，无外部依赖、无网络。
