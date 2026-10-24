package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	dirEpoch          func() uint64                          // engine callback: current direction epoch (nil = off)
	dirAck            func()                                 // engine callback: release dispatch hold after a dispatch (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled
	// envBriefFn resolves the task's shared execution-environment brief (批次 6 /
	// #19/#22), appended to the planner system prompt. Read per round. nil = none.
	envBriefFn func() string

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// Compactor returns the wired compactor (nil = disabled) so the host can observe
// its lifecycle and interrupt an in-flight run when the task pauses (批次 6).
func (p *Planner) Compactor() *Compactor { return p.compactor }

// SetEnvBrief wires a resolver returning the task's shared environment brief
// (批次 6 / #19：共享环境上下文). Appended to the planner system prompt next to the
// constraint block. nil/empty = no injection.
func (p *Planner) SetEnvBrief(fn func() string) { p.envBriefFn = fn }

func (p *Planner) envBrief() string {
	if p.envBriefFn == nil {
		return ""
	}
	return strings.TrimSpace(p.envBriefFn())
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// SetDirectionGate wires the direction-epoch gate callbacks (engine) into this
// planner: each Plan() round samples the epoch at start (ToolSet.roundEpoch) and
// add_intent refuses dispatch once kill_work/steer_work bumps it mid-round, so the
// planner re-evaluates direction before dispatching more subagents.
func (p *Planner) SetDirectionGate(epochFn func() uint64, ackFn func()) {
	p.dirEpoch, p.dirAck = epochFn, ackFn
}

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【你的规划待办（跨唤醒保留，上一轮你写的）】：\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("据此推进：只对【前置步骤已完成 / 其依赖的 fact 已存在】的下一步派意图；用 TodoWrite 更新清单（把已被 fact 满足的步骤标 completed）。不要重复派已在清单里 pending/in_progress 的步骤。")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
// 每条消息经 capPromptLine 限长(条数不变):输出结论/目标文本是外部数据,单条
// 几十 KB 时不设限会撑爆每轮 user 输入。
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【本次触发本轮的实际变动（先看这里，再决定是否补方向）】：")
	for _, ev := range evs {
		var line string
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				line = fmt.Sprintf("\n- 人（主 agent）新增了一个目标：%s —— 新的待达成目标，请据此补充探索方向（若尚无对应意图）。", ev.Goals[0])
			} else {
				line = fmt.Sprintf("\n- 人（主 agent）新增了 %d 个目标：%s —— 均为新的待达成目标，请逐一为尚无对应意图的目标补充探索方向。", len(ev.Goals), strings.Join(ev.Goals, "；"))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				line = fmt.Sprintf("\n- 人（主 agent）新增了一条战略提示：%s —— 已挂到探索图上，请据此调整/补充探索方向（若尚无对应意图）。", ev.Hints[0])
			} else {
				line = fmt.Sprintf("\n- 人（主 agent）新增了 %d 条战略提示：%s —— 均已挂到探索图上，请逐一据此调整/补充探索方向。", len(ev.Hints), strings.Join(ev.Hints, "；"))
			}
		case "goal_deleted":
			line = fmt.Sprintf("\n- 人删除了该目标：%s —— 该目标已移除，请据此重判剩余目标/方向（不必再为它派意图）。", ev.Detail)
		case "goal_edited":
			line = fmt.Sprintf("\n- 人修改了目标，由「%s」变为「%s」—— 请据新目标调整探索方向（原方向若已不适用请停派）。", ev.OldGoal, ev.NewGoal)
		case "finding":
			line = fmt.Sprintf("\n- 意图 #%d（%s）的 worker 报告了一个 finding：%s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail)
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			line = fmt.Sprintf("\n- 意图 #%d 由用户删除，意图内容是：%s、删除原因是：%s。该意图已删除（不再执行）；请据此重新规划。", ev.IntentID, sm, ev.Detail)
		default: // "done"
			line = fmt.Sprintf("\n- 意图 #%d（%s）的 worker 结束，输出结论：%s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				line += fmt.Sprintf("；本意图新产生的事实 id：%s ", fids)
			}
		}
		b.WriteString(capPromptLine(line, promptTriggerLineMaxBytes))
	}
	b.WriteString("\n（完整细节可 node_detail / get_worker_output / list_findings 再查。）")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(取输出失败)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(该 work 尚无输出记录)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …（已截断，完整见 get_worker_output）"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the situation in hand —
// saving the round-trip it would otherwise spend calling the tool. It renders a
// COMPACT injection view (caller passes graphOverviewForPrompt data): lists are
// capped, digest bodies truncated, and the whole block is byte-capped (批次 E
// 输入瘦身:这份 JSON 每轮重复计费). Deeper detail — full graph, all assets,
// older windows — is still one tool call away (graph_overview / node_detail /
// list_facts / list_assets / …), as the trailing JIT hint reminds the model.
func renderGraphOverview(ctx context.Context, data map[string]any) string {
	maxBytes := plannerOverviewMaxBytes
	// 第三阶段:注入预算随窗口伸缩(128K → 51KB,原来写死 16KB)。窗口未知时退回旧值。
	if w := contextWindowFrom(ctx); w > 0 {
		maxBytes = OverviewBudgetBytes(w)
	}
	b := marshalOverviewCapped(data, maxBytes)
	if b == "" {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【本轮态势（graph_overview 的注入紧凑版：列表按最新一窗截断，digest 正文有上限；等同按需调工具取数的精简视图）】：\n" + b +
		"\n（完整图/资产/更早明细不在此列，可随时用工具按需查询：graph_overview / list_facts / list_findings / list_assets / asset_neighbors / node_detail。）"
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `你是授权渗透测试系统的【规划者】。你的唯一产出物是**假设**——不是动作、不是结论、不是资产清单。你每次被唤醒只做一件事：把一个尚未被验证的猜想，写成一个可被证伪的意图派下去。

任务目标：{{.Goal}}

【唯一产出物的定义】
一条合格的意图 = 一句假设 + 一句证伪信号。
- 假设：你想验证的具体机理（例："该站 /api/v2/report/export 只校验登录态不校验 tenant，跨租户可直接导出"）。
- 证伪信号：看到什么就说明这条假设错了（例："返回 403，或返回体里没有其它租户的数据"）。
写不出证伪信号的，那不是假设，是愿望——不要提交。

【情报前提】
你拿到的是 Reviewer 已裁定的结论，不是原始事实。以它为前提往下推理，不要重新取证（原始事实已对你关闭）。若某条结论与目标直接冲突，用 fetch_evidence 定向核对一次，然后继续推进。

【决策优先级链】按顺序判断，命中即止：
1. 有 open/running 意图，且它们不是在等同一个前置 → 不派新意图，等它们推进。
2. 无 open/running 意图，且目标未达成 → 必须派，且派"最深的那条"：从已有事实里挑价值最高、最可能通向 RCE/提权/数据外泄、但尚未打穿的链路继续。
3. 某方向已被 Reviewer 判 abandon → 换正交维度：换入口面（后台/API/移动端/静态资源）、换主体（未授权/越权/跨租户）、换层次（业务逻辑/并发/反序列化）。
4. 以上都不成立 → 只回一句「本轮无新假设」，不提交意图。这是合法的、有明确前提的结果。

【派意图的四条判据】逐条对照，不是口号：
- 深度：高价值入口优先打穿，不为拉平覆盖度做浅铺。覆盖度是验收项，不是探索目标。
- 正交：同时推进的分支必须机理不同（认证绕过 / 上传链 / 越权链 / 逻辑链）。同一条链只留一条意图，换措辞重派等于没派。
- 串行：强依赖链（①→②→③）只派当前可执行的那一步，其余记成待办，等前置产出后再派。并行下发拿不到前置只会空转。
- 可证伪：见上。

【提交】每轮最多 4 条，用一次 add_intent 批量提交：
- summary：完整地址 + 假设 + 证伪信号 + 你判断该用的 Skill 名（不确定就不写，不要编技能名）。
- asset_ids：该假设涉及的资产 id（来自 list_assets），跨资产就都填。
- parent_ids：该假设依据的事实/意图 id。

【交卷自检】提交或结束前逐条回答，任何一条答不上就退回去重做：
1. 这条假设若成立，我能拿到什么？说不清 = 它不值钱。
2. 它和 open/running/recent_done 里已有方向的**实质差别**是什么？只是措辞不同 = 不要派。
3. Reviewer 判它 abandon 时，我会知道是卡在哪个机制上？答不上 = 证伪信号没写。
`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir) + toolBatchSpec() + langDirective(resolveOutputLang())
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// 注入预算（态势 JSON / digest 正文）按窗口推导，必须先挂到 ctx 上——后面
	// renderGraphOverview 要读它；文件末尾那次 WithContextWindow 只覆盖到会话循环。
	ctx = WithContextWindow(ctx, p.compactionWindow())
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	// 方向代际门控（本轮开始时取样）+ 派发确认：kill/steer 后本轮余下派发被拒绝，
	// 下一轮基于最新 graph_overview 重新评估后再派发（见 ToolSet.addOneIntent）。
	tsx.SetDirectionGate(p.dirEpoch, p.dirAck)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), SDKDefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	isRE := softwareRE(ts)
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	if isRE {
		tools, def = softwareRETools(tools, def)
	}
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(ctx, tsx.graphOverviewForPrompt(ctx, plannerPromptCaps))
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【任务终局收尾（本轮特殊指令，覆盖上面的常规规划流程）】：" + resolveTaskTimeoutWrapup("planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if isRE {
		sysBody = softwareREPlanner + "\n\n任务目标：" + goal + softwareREArtifactSpec(taskDir) + toolBatchSpec() + langDirective(resolveOutputLang())
	}
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	// 共享环境上下文（批次 6，与 worker 同一份探测快照）。
	if brief := p.envBrief(); brief != "" {
		sysBody += "\n\n" + brief
	}
	if hint := func() string {
		tactical := BuildTacticalRoutingHint(goal+"\n"+situational, tools, def)
		if isRE {
			if tactical != "" {
				return softwareREHint() + "\n\n" + tactical
			}
			return softwareREHint()
		}
		return tactical
	}(); hint != "" {
		sysBody += "\n\n" + hint
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 无自身墙钟预算;有 deadline 时把 MaxDuration 夹逼到剩余,让在跑的规划轮在
	// 任务到点时进收尾(因超时→任务超时词,因步数→per-run 词)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:            taskDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxToolOutputChars:    ToolOutputChars(p.compactionWindow()), // 工具输出入历史的上限(超限全量落盘可重读)
		MaxTurns:              p.maxTurns,                            // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,                                // 0=不限;有 deadline 时=距 deadline 剩余
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不单独设限;norma harness 兜底为 32768(harness/query.go maxTokens)
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 内置上下文压缩：窗口 × 80% 触发（口径见 compactionConfig）；标签只用于压缩日志。
	compactLabel := fmt.Sprintf("exp%d-planner", ts.ID())
	attachCompaction(&opts, p.compactionWindow(), compactLabel)
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "刚有具体变动（见下面的【本次触发本轮的实际变动】），据此规划下一步："
	if len(triggers) == 0 {
		lead = "本轮是**定时巡检（心跳到点）/无具体变动信号**的唤醒——图不一定有新变动。顺带复查在跑意图：长时间无进展或跑偏的用 steer_work 纠偏、方向整个错的用 kill_work 止损；再判定目标、决定是否补方向："
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "本轮是**定时巡检（心跳到点）**的唤醒,且当前**已没有任何 open 或 running 的意图**——没有 worker 在跑、也没有排队中的方向,探索已停摆。你**必须**在本轮产出一个或多个向目标推进、且与图中既有意图**互不重复**的新意图(不得产出 0 意图);先据下面的态势判定目标是否已达成,未达成则立即补方向："
		}
	}
	input := lead + situational + "\n\n据上面的态势，判定目标。目标已【真正达成】（已拿到目标成果/已确认目标漏洞）时用 prove_goal 逐个标记。**硬底线：只要目标尚未达成、且当前没有任何 open 或 running 意图（frontier_open=0 且 running_intents 为空），本轮就必须产出至少一个向目标推进的意图——此时没有在跑的 work 可等、也没有在排队的方向，产出 0 意图=任务停摆。仅当已有 open/running 意图在推进、或目标已达成时，本轮才可以不产出新意图。**" +
		// 0 意图必须给理由。以前「不派方向 + 不 prove_goal」是静默的:引擎只看到本轮
		// 什么都没发生,任务就这样停在那里等人发现。现在把它写成协议,并在 Plan 末尾
		// 做一次硬校验(见 gapRoundInput)。
		"\n\n**0 意图契约**：本轮若既不产出新意图、也不 prove_goal，必须在回复里用一行 `coverage_gaps: …` 写明还缺什么证据、或为什么这一轮不派方向。没有 `coverage_gaps:` 的 0 意图轮视为协议违规，引擎会立即补跑一轮。" +
		// 意图粒度两句话：证据口径（expected_evidence）+ 步数上限（K=5）。
		// 超过 5 步还说不清「跑完拿到什么」的方向，本质是几个方向揉成一条，
		// 拆开才能并行、才能在复核时逐条判定。
		"\n\n**意图粒度**：每条意图都要写 `expected_evidence`（跑完应拿到什么证据才算法完成）。单个意图最多 5 个执行步骤——超过就说明它其实是几个方向揉在一起，请拆成多条分别派发。worker 的工具面会按 summary、expected_evidence、目标资产形态与工具自述动态路由，所以意图文字要写清对象、动作与待验证假设；可选的 `phase` 只作粗粒度标签，不决定工具清单，不要为了凑工具硬填。" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	emitPlanner := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner" // planner activity has no intent_id (it generates them)
			emit(r)
		}
	}
	// 上下文窗口随 ctx 下传：usage 活动带上 context_window，会话页的上下文环才有分母。
	// 与 chat / worker / mainagent 同形；此前 planner 只把窗口喂给压缩器，环拿不到数据。
	ctx = WithContextWindow(ctx, p.compactionWindow())
	_, _, err = captureRun(ctx, opts, input, emitPlanner)
	// 0 意图契约的硬校验。只在「真的没有活可等」时补跑:已经有 open/running 意图在
	// 推进,本轮不派新方向是合理的;而 0 意图 + 目标未达成 + 无在跑意图 = 任务停摆,
	// 只等下一次心跳就太晚了。补跑最多一次,add_intent 自己的方向代际门控仍然生效。
	if err == nil && !tsx.GoalMet && tsx.intentsAdded == 0 {
		if active, aerr := ts.HasActiveIntent(); aerr == nil && !active {
			emitPlanner(db.Activity{Kind: "text",
				Summary: "本轮没有新增意图、也没有可等的在跑意图——按 0 意图契约补跑一轮规划。"})
			if _, _, rerr := captureRun(ctx, opts, gapRoundInput(taskID), emitPlanner); rerr != nil && err == nil {
				err = rerr
			}
		}
	}
	return tsx.GoalMet, tsx.Reason, err
}

// gapRoundInput 是 0 意图契约触发的那一轮补充输入。它不重复整段态势(graph_overview
// 已经在同一 session 的历史里),只讲清「上一轮漏了什么、现在只能二选一」,让模型在
// 一次往返内把方向补上、或把缺口写明。
func gapRoundInput(taskID int64) string {
	return fmt.Sprintf("任务 #%d：上一轮你既没有用 add_intent 派发新方向，也没有 prove_goal，而目标未达成、当前没有任何 open/running 意图。"+
		"\n\n这属于 0 意图契约违规。请二选一，不要只回复“收到/将继续”：\n"+
		"1) 用 add_intent 指派至少一个向目标推进、且与图中既有意图不重复的方向（可一次批量提交多个）；\n"+
		"2) 若确实无法推进，用一行 `coverage_gaps: …` 写明缺什么证据、卡在哪个前提上。", taskID)
}
