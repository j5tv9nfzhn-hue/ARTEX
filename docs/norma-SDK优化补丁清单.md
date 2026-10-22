# norma SDK 优化补丁清单（供上游立项）

[norma](https://github.com/Autumn-27/norma) 是独立于本仓库维护的 Agent SDK，本仓库以 Go module 方式依赖。本文把 ARTEX 成本效率优化中「必须改 SDK 才能解决」的点整理成补丁清单，供在 **norma 仓**立项时逐条评估；本文只做建议，不在本仓库修改 SDK。

- **核验版本：** `v0.4.3`，本地模块缓存 `E:\Go\gopath\pkg\mod\github.com\!autumn-27\norma@v0.4.3\`（只读核对）。升级版本后行号可能漂移，以符号名（函数/字段）为准。
- **相关文档：** ARTEX 侧已落地的成本效率改动见[成本效率优化实施记录](成本效率优化-实施记录.md)。

## 清单总览

| # | 主题 | 优先级 | 一句话 |
|---|---|---|---|
| 1 | messages/tools 级缓存断点 | 高 | system 之外，tools 与消息序列也需要 cache_control 断点 |
| 2 | MicroCompact 工具白名单可配置 | 高 | compactableTools 硬编码 8 个工具，宿主的大输出工具无法参与清退 |
| 3 | 摘要 provider 注入 | 中 | 内建压缩固定用主会话 Provider 做摘要，宿主无法指定便宜模型 |
| 4 | 截断恢复次数未暴露 | 低 | maxOutputTokensRecoveryLimit=3 不可配置 |
| 5 | OpenAI prompt_cache_key | 中 | OpenAI 格式请求缺 prompt_cache_key 字段 |
| 6 | TokenBudget 语义评价 | 低 | 是「续跑激励」不是硬上限，不建议宿主当成本闸 |
| 7 | 压缩触发比例不可配 | 中 | 触发点由 ContextWindow 反推，同一套默认值在 128K/1M 上差 18 个百分点 |
| 8 | 文件工具的并发调度 | 高 | 路径感知并发接口 + 去掉队头阻塞；宿主侧已先落 workaround |

## 1. messages / tools 级缓存断点

**现状：** `llm/anthropic.go:86-106`（`buildAnthropicSystem`）只在 system prompt 上放置 `cache_control: {"type":"ephemeral"}` 断点；工具定义结构 `llm/anthropic.go:44-48`（`anthropicTool`）与消息内容块 `llm/types.go:30-53`（`ContentBlock`）都没有缓存放位。一次请求里只有 system 段能命中前缀缓存。

**建议改动：** 序列化请求时，在 tools 区块末尾与消息序列尾部各加一个 ephemeral 断点（总量控制在 Anthropic 上限 4 个以内），让工具定义与多轮历史跨轮复用。

**收益：** ARTEX 工具集很大（几十个工具、schema 体积可观），且任务天然是多轮长会话；这两个断点直接降低每轮的输入 token 开销。

**优先级：高。**

## 2. MicroCompact 工具白名单可配置

**现状：** `compaction/compaction.go:59-65` 的 `compactableTools` 硬编码 8 个工具（Read / Bash / Grep / Glob / WebFetch / WebSearch / Edit / Write），`Config`（同文件 40-45 行）没有对应字段；`microKeepRecent = 5`（同文件 69 行）同样固定。

**建议改动：** 把该白名单提升为 `Config` 字段（默认沿用现列表），或提供「按工具输出体积阈值清退」的通用策略，让宿主把大输出工具纳入。

**收益：** ARTEX 的域内大输出工具（`traffic_search` / `get_worker_trace` / `list_assets` 等）不在白名单里，长任务中这些历史只能等 AutoCompact（更晚、更贵）处理，MicroCompact 的省 token 能力打折。

**优先级：高。**

## 3. 摘要 provider 注入

**现状：** SDK 已提供两条注入面——`compaction.ProviderSummarizer(p, maxOutputTokens, nonStreaming)`（`compaction/summarizer.go:30`）可单独构造 Summarizer，`Options.Compactor`（`agentcore/agentcore.go:67-74`）允许宿主整体接管上下文管理（与内建 `Options.Compaction` 互斥）。但**内建路径固定用主会话 Provider 做摘要**：`agentcore/agentcore.go:266-267` 直接 `compaction.New(..., compaction.ProviderSummarizer(opts.Provider, opts.MaxTokens, opts.NonStreaming))`——摘要与主任务同模型、同 key。

**建议改动：** 在内建路径上增加 `Options.SummaryProvider llm.Provider`（或等价字段），为空时回退 `opts.Provider`；或在文档里把「用 Compactor 接管注入便宜摘要模型」写成官方推荐路径。

**收益：** 摘要单次输出上限是 20000 token（`compaction/summarizer.go:13`），用主力贵模型做摘要成本可观；允许注入便宜模型后，长任务的压缩成本可控。ARTEX 走的是内建 `Options.Compaction`（`agent/provider.go:129-137`），命中该限制。

**优先级：中。**

## 4. 截断恢复次数未暴露

**现状：** `harness/query.go:255-264` 常量组里 `maxOutputTokensRecoveryLimit = 3`（截断后「不道歉、续写」的恢复次数）与 `escalatedMaxTokens = 64000`（抬额上限）都是包级常量；恢复流程见 `harness/query.go:630-649`。`EscalateMaxTokens` 已通过配置暴露（`harness/query.go:95-97`、`agentcore/agentcore.go:184-186`），仅恢复次数不可配。

**建议改动：** 在 `QueryInput` / `agentcore.Options` 暴露恢复次数（0=默认 3，-1=关闭）。

**收益：** 截断恢复会整段重发历史，属高成本路径，宿主可以按成本敏感度收紧或关闭。ARTEX 已刻意不启用抬额（`agent/provider.go:477-481` 注释），但恢复次数仍是固定的 3 次。

**优先级：低。**

## 5. OpenAI prompt_cache_key

**现状：** 全 SDK 没有 `prompt_cache_key`：OpenAI 格式请求结构 `llm/openai.go:46-61`（`oaReq`）与 `llm/provider.go:56-111`（`Config`）均无相应字段（全库 grep 无命中）。

**建议改动：** 在 `Config` 增加 `PromptCacheKey`（或由 SessionID 自动派生），随 OpenAI 格式请求下发 `prompt_cache_key`。

**收益：** OpenAI 及兼容网关会按 key 提高前缀缓存命中率；同一任务多轮前缀稳定，命中率提升直接降低输入 token 费用。Anthropic 格式走缓存断点，不受影响。

**优先级：中。**

## 6. TokenBudget 的诚实评价（不建议启用）

**现状：** `agentcore/agentcore.go:180-183` 与 `harness/query.go:91-94` 定义 `TokenBudget`：>0 时在「本该停止」后继续工作，直到约 90% 的输出 token 预算或边际收益递减（判定见 `harness/query.go:653` 起的 `checkBudgetContinue`，阈值常量在 255-258 行）。

**评价与建议：** 它是**续跑激励**而不是硬上限——口径是输出 token，且以「≈90% 停止」为正目标；把它当成本闸，预算会变成「至少花到 90%」。ARTEX 已有自己的任务级硬闸（达到 input+output 上限即自动暂停，见实施记录第 2 节），与该语义冲突，故未接入。**建议不要为成本控制在 ARTEX 启用；SDK 侧可做的只是把「非硬上限」写进字段注释/文档，避免宿主误用。**

**优先级：低（文档/语义澄清；不建议启用）。**

## 7. 压缩触发比例不可配（AutoThresholdRatio）

**现状：** `compaction/compaction.go` 的 `Config` 只有 `ContextWindow` / `MaxOutputTokens` / `KeepRecent`，阈值全部派生：
`autoThreshold = effectiveWindow − buffer`、`predictiveOver(t) = t + maxTurnGrowth > effectiveWindow`；
`buffer` 按 effectiveWindow 分 50000 / 30000 / 13000 三档，`maxTurnGrowth = reserved + 15000`。
结果：同一套默认参数在 128K / 200K / 400K / 1M 窗口上的实际触发比例是 75.8% / 84.5% / 92.3% / 94.2%，
窗口越大压得越晚——1M 窗口要烧到 94 万 token 才开始压缩。

**建议改动：** `Config` 增加 `AutoThresholdRatio float64`（0 = 维持现状）。
非零时令 `autoThreshold = int(ContextWindow × ratio)`，并把 `predictiveOver` 的边界按同一比例推导
（或将 `effectiveWindow` 反解为 `autoThreshold + buffer`），`buffer` / `maxTurnGrowth` 仅作为默认策略保留。

**宿主侧临时方案（已在 ARTEX 落地）：** 反解等效窗口——先定触发点，再倒推一个 `ContextWindow` 传进来，
使 `min(autoThreshold, predictiveThreshold)` 恰好落在目标比例上。实现与验证矩阵见
[上下文压缩黄金比例-落地说明](上下文压缩黄金比例-落地说明-2026-10-21.md)。
副作用是 micro 清退线与预测线随等效窗口一起小幅前移（≤3%），且宿主需复刻阈值算法做自检。

**优先级：中。**

## 8. 文件工具的并发调度（路径感知 + 队头阻塞）

**现状：**

- `tool/tool.go`：`CoreTool.IsConcurrencySafe(input) bool` 只能回答「安全 / 不安全」，无法表达「只与同路径的调用互斥」。
- `tool/files.go`：`Write` / `Edit` 与 `tool/multiedit.go` 的 `MultiEdit` 都**没有声明** `Concurrent`，`Build` 的 fail-closed 默认使它们独占执行。
- `harness/streaming.go`：`canStartLocked` 在存在执行中的不安全工具时拒绝**所有**工具；`processQueue` 遇到起不来的不安全工具直接 `break`（队头阻塞）；`drain` 遇到第一个未完成的不安全工具即停止放行。
- 没有多文件编辑原语：`MultiEdit` 只作用于单文件，`Read` 只吃一个 `file_path`。

**建议改动：**

1. `CoreTool` 增加可选接口（与 `AcceptsRawInput` 同一套类型断言发现机制，不破坏既有实现）：
   `Resources(input) []Access`，`Access{Key string; Mode Read|Write}`；`Write` / `Edit` / `MultiEdit` 声明 `file:<规范化绝对路径>`（`Abs` + `Clean` + 软链接归一，Windows 不区分大小写）。
2. 调度器把「全局独占」换成「按资源键的读写锁」：同键 FIFO、跨键并行、多键按字典序加锁；`processQueue` 不再 `break`，改为「键冲突就跳过、继续看后面的调用」。
3. 新增多文件补丁工具（V4A 或 JSON 形态皆可）：一次调用 N 个 FileOp，两阶段（全部解析校验 → 全部落盘），任一失败回滚。
4. 写工具内部改成原子写（同目录临时文件 + `rename`，Windows 对 `EBUSY/EPERM/EACCES` 重试）。

**宿主侧临时方案（已在 ARTEX 落地，见 [工具并发与多文件补丁-落地说明](工具并发与多文件补丁-落地说明.md)）：**

用宿主包装层实现同样的语义——把 `Read/Write/Edit/MultiEdit` 包成「按路径分片加读写锁、并对外声明并发安全」的工具，全部装配点改走 `agent.SDKDefaultTools()`；再在宿主侧新增 `apply_patch` 多文件原子补丁工具与「批量调用」提示词尾段。

代价是仍停在 SDK 的「安全 / 不安全」二值语义上：同路径 FIFO 依旧是竞态（锁的获取顺序不等于队列顺序），`Grep/Glob/LS` 这类目录级只读工具无法参与路径锁。

**收益：** 不同文件的写/读不再互相阻塞；多文件改动从 N 次模型往返降到 1 次。

**优先级：高。**
