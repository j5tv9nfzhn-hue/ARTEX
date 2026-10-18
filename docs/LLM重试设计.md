# LLM 五层重试与熔断设计（`docs/LLM重试设计.md`）

> 本文对应 `db/llmretry.go`、`agent/provider.go`、`server/task_llm.go`、`server/llmpool.go` 与 `server/engine.go` 中的五层 LLM 重试、轮询熔断与任务链故障切换实现。

---

## 1. 设计目标与核心不变量

1. **零配置向后兼容**：全局策略存储在 `settings` 表的 `llm_retry_policy` 键（JSON 对象）。当键不存在或字段值为 `0` 时，自动回落到各层内置默认次数与指数退避梯度，与未引入配置表前的行为完全一致。
2. **显式关闭 vs 未配置区分**：
   - `Attempts = 0`：未配置，沿用该层内置默认次数（或由单 Profile 覆盖继承全局配置）。
   - `Attempts = -1`：显式关闭该层重试。
   - `Attempts > 0`：使用指定重试次数（硬上限 `maxRetryAttempts = 20`，防止瞬时抖动演变为无限 Token 消耗）。
   - `IntervalMS = 0`：沿用该层内置指数退避梯度。
   - `IntervalMS > 0`：改用固定毫秒间隔（硬上限 `maxRetryIntervalMS = 3,600,000` 即 1 小时）。
3. **三级优先级覆盖（针对单请求层 `Connect` / `Empty` / `Stream`）**：
   `LLMProfile.Retry`（单模型配置覆盖） $\rightarrow$ `DB.LLMRetryPolicy()`（全局策略） $\rightarrow$ 内置默认常量（`RetryRule.Or` 逐字段合并）。

---

## 2. 五层重试架构全景

| 层级 | 键名 (`LLMRetryPolicy`) | 作用域 | 触发条件与安全边界 | 内置默认次数 | 内置默认退避策略 | 实现位置 |
|---|---|---|---|---|---|---|
| **L1 建连层** | `connect` | 单次请求（可按 Profile 覆盖） | HTTP 建连失败、连接重置、TLS/响应头超时、流开始前的 `429` / `5xx` | `3` 次 | `0.5s` 起指数退避（封顶 `8s`） | `agent/provider.go` $\rightarrow$ `llm.Config.MaxRetries` |
| **L2 空响应层** | `empty` | 单次请求（可按 Profile 覆盖，仅 `openai` 格式） | 流或非流请求正常结束，但返回的 `content` 块与工具调用均为空（兼容网关偶发吞帧） | `2` 次 | `0.5s` 起指数退避（封顶 `8s`） | `agent/provider.go` $\rightarrow$ `llm.Config.EmptyResponseRetries` |
| **L3 流安全窗口层** | `stream` | 单次请求（可按 Profile 覆盖） | SSE 流建立后发生断流、流内 `overloaded` / `429`，且**尚未向调用方交付任何有效输出块**（保证重放幂等，不产生重复半截回复） | `2` 次 | `0.5s` 起指数退避（封顶 `4s`） | `server/task_llm.go` |
| **L4 轮询池熔断层** | `breaker` | 进程级全局 LLM Pool | 同一 Profile 连续发生瞬时错误达到阈值时进入冷却期；**硬错误（余额不足、API Key 失效、模型不存在、配额耗尽）不计次数立即熔断** | 连续 `3` 次瞬时失败触发 | 阶梯冷却 `1min → 5min → 30min`（若设 `IntervalMS > 0` 则用固定冷却时长） | `server/llmpool.go` |
| **L5 意图重跑层** | `intent` | 任务级 Worker 意图调度 | Worker 会话因 `model_error` 中断退出后，由引擎调度整条 Intent 重新拉起恢复 | 瞬时错误 `4` 次（硬失败 `2` 次） | `3s` 起指数退避（封顶 `30s`） | `server/engine.go` |

---

## 3. 与任务级 LLM 故障切换链（Failover Chain）的协同

当任务配置了多模型链（`task.llm_profile_ids`）时：
1. **单模型内消化瞬时抖动**：单个 Profile 先在 L1（`Connect`）、L2（`Empty`）、L3（`Stream`）内按配置完成原地重试。
2. **跨模型自动切换（Automatic Failover）**：若当前活跃 Profile 在原地重试耗尽后仍失败（或命中配额/鉴权硬错误），`server/task_llm.go` 将任务活跃游标（`active_llm_profile_id`）推进到链上的下一个备用 Profile，并写入 `llm_switch` / `llm_failover` 审计活动。
3. **链耗尽保护（`chain_exhausted`）**：当链上所有 Profile 均不可用时，任务标记为 `llm_failover_state = "chain_exhausted"`，前端任务详情页顶部 `TaskLLMControl` 展示红色告警，允许操作员在线更换或重置模型链并自动重开受阻意图。

---

## 4. 存储结构与边界校验

- **全局配置键**：`settings.key = "llm_retry_policy"`
- **JSON 结构示例**：
  ```json
  {
    "connect": { "attempts": 3, "interval_ms": 0 },
    "empty":   { "attempts": 2, "interval_ms": 0 },
    "stream":  { "attempts": 2, "interval_ms": 500 },
    "breaker": { "attempts": 3, "interval_ms": 0 },
    "intent":  { "attempts": 4, "interval_ms": 3000 }
  }
  ```
- **边界钳制（`Clamped()`）**：所有写入路径（全局设置 `SetLLMRetryPolicy` 与单 Profile `RetryOverride.Clamped()`）在入库前强制钳制 `Attempts ∈ [-1, 20]`、`IntervalMS ∈ [0, 3600000]`，与 SQLite 表级 `CHECK` 约束严格对齐。
