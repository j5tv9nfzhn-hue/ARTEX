package server

// Reviewer 接线层：把 Worker 的原始交接单送进 agent.Review，把判决落库并写线索，
// 再把这个判决挂到 Planner 的注入面上。
//
// 这条链上「物理切断」是这样保证的：
//
//	1. intent_handoffs（含 tail/last_output/L2 自注）只有 Reviewer 与人读；
//	2. tools.go 的注入面只从 reviewer_verdicts 取情报，未经判决的意图其 fact 被
//	   折叠成计数（facts_unreviewed）——Planner 拿不到正文；
//	3. Planner 的提示词里写死「已清洗并判决，直接采信，禁止回读原始事实」。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// errReviewerNoStore 表示 Reviewer 依赖的存储/服务句柄还没就绪。
var errReviewerNoStore = errors.New("reviewer: store not ready")

// db 侧的 reviewer 台账方法挂在 *db.ExplorationStore 上，而它们的键全部来自入参，
// 不读 store 自己的 expID；唯一依赖 expID 的是 Clues()（线索要记来源探索）。
// 所以除 Clues 外的取用一律走 Exploration(0)，语义等价——别"顺手"传个 id 进去。

var reviewerHookOnce sync.Once

// installReviewerHooks 把「意图 id → 判决摘要」的查询接到 agent 的注入面上。
// 用 once 惰性安装，避免去改 manager 的启动序列；钩子本身无状态，键是意图 id。
func (e *Engine) installReviewerHooks() {
	reviewerHookOnce.Do(func() {
		agent.ReviewerVerdictsForIntents = func(ids []int64) map[int64]agent.ReviewerBrief {
			return e.reviewerBriefsForIntents(ids)
		}
		agent.ReviewerEvidenceLoader = func(refID int64) (agent.ReviewerEvidence, error) {
			return e.loaderEvidence(refID)
		}
		agent.ReviewerEvidenceTouch = func(refID int64) {
			if e != nil && e.m != nil && e.m.pg != nil {
				if err := e.m.pg.Exploration(0).TouchReviewerEvidence(refID); err != nil {
					log.Printf("[reviewer] 证据 %d 访问记账失败: %v", refID, err)
				}
			}
		}
	})
}

// reviewerBriefsForIntents 批量取判决摘要。
func (e *Engine) reviewerBriefsForIntents(ids []int64) map[int64]agent.ReviewerBrief {
	if e == nil || e.m == nil || e.m.pg == nil || len(ids) == 0 {
		return nil
	}
	rows, err := e.m.pg.Exploration(0).ReviewerVerdictsByIntents(ids)
	if err != nil {
		log.Printf("[reviewer] 取判决摘要失败: %v", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	out := make(map[int64]agent.ReviewerBrief, len(rows))
	for _, r := range rows {
		b := agent.ReviewerBrief{
			Verdict:     agent.ReviewerVerdict(r.Verdict),
			Attribution: agent.ReviewerAttribution(r.Attribution),
			Summary:     r.Summary,
			Confidence:  r.Confidence,
		}
		for _, f := range db.DecodeReviewerList(r.FactsJSON) {
			if s, ok := f["summary"].(string); ok && s != "" {
				b.Facts = append(b.Facts, s)
			}
		}
		for _, c := range db.DecodeReviewerList(r.CluesJSON) {
			if s, ok := c["value"].(string); ok && s != "" {
				b.Clues = append(b.Clues, s)
			}
		}
		b.Negative = db.DecodeReviewerStrings(r.NegativeJSON)
		b.Evidence = db.DecodeReviewerInts(r.EvidenceJSON)
		out[r.IntentID] = b
	}
	return out
}

// runReviewerForIntent 在交接单落库之后调用：交接单是原始材料，Reviewer 是它的
// 唯一消费者。任何失败只记日志——判决是增强，不能反过来影响任务收尾。
func (e *Engine) runReviewerForIntent(t *Task, intent *db.Node, runID int64, rec handoffRecord) {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intent == nil {
		return
	}
	e.installReviewerHooks()
	// t.ID 是字符串形式的任务 id，而台账列是 BIGINT；统一在这里解析一次，
	// 下面所有写库路径都用这个 tid，避免每次各解一遍（也避免把 string 直接
	// 塞进 int64 字段这种编译错）。
	tid, _ := strconv.ParseInt(t.ID, 10, 64)

	in := agent.ReviewerInput{
		TaskID:         t.ID,
		ExplorationID:  t.ExpID,
		IntentID:       intent.ID,
		RunID:          runID,
		IntentSummary:  clipText(string(intent.Payload), 800),
		Objective:      rec.Scope.Objective,
		EndState:       rec.Trigger.Detail,
		StopReason:     rec.Trigger.Kind,
		TerminalReason: rec.Trigger.Kind,
		ToolCalls:      rec.Progress.StepsTotal,
		Wrote: agent.WriteCounts{
			Facts:    rec.Progress.Written["facts"],
			Assets:   rec.Progress.Written["assets"],
			Findings: rec.Progress.Written["findings"],
		},
		LastOutput: rec.Progress.LastOutput,
	}
	for _, st := range rec.Progress.Tail {
		if st.IsError {
			in.ToolErrors++
		}
		line := strings.TrimSpace(st.Tool)
		if line == "" {
			line = strings.TrimSpace(st.Kind)
		}
		if st.Summary != "" {
			line += ": " + st.Summary
		} else if st.Tool == "" {
			continue
		}
		if st.IsError {
			line += " (error)"
		}
		in.Tail = append(in.Tail, line)
	}
	if rec.OpenWork != nil {
		in.OpenWork = append(in.OpenWork, rec.OpenWork.NextActions...)
		in.OpenWork = append(in.OpenWork, rec.OpenWork.Hypotheses...)
		in.OpenWork = append(in.OpenWork, rec.OpenWork.NegativeResults...)
	}

	// run 的终态在 Trigger.Detail 里（"done / " 这类），单独抠出来给规则引擎用。
	in.EndState = reviewerStateOf(rec)
	// 模型兜底：规则判不出来的灰区才走它。选型见 agent/reviewer.go 的注释
	// （MiniMax-M3.1-Flash 优先，deepseek-v4.1-flash 次选，均为跨族/快速档）。
	in.LLM = agent.ReviewerLLM
	// 证据落库：存的是**清洗后**的快照，raw 不进表。
	in.SaveEvidence = func(kind, body string) (int64, error) {
		return e.m.pg.Exploration(t.ExpID).PutReviewerEvidence(tid, intent.ID, runID, kind, body)
	}
	// 判定过程也落活动流：回顾者面板要和规划者一样能看见「读了什么、凭什么判」，
	// 而不是只有一条结论。行按 run_id 隔离（同一意图复测会产生多个 run，按意图
	// 取会把两次判定搅在一起），前端用 review:<run_id> 打开这一条。
	reviewNode := intent.ID
	reviewMeta := json.RawMessage(fmt.Sprintf(`{"run_id":%d}`, runID))
	in.Emit = func(a db.Activity) {
		a.Worker = "reviewer"
		a.NodeID = &reviewNode
		a.Metadata = reviewMeta
		e.emitActivity(t, a)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	res := agent.Review(ctx, in)

	factsJSON, _ := json.Marshal(res.Facts)
	cluesJSON, _ := json.Marshal(res.Clues)
	carryJSON, _ := json.Marshal(res.CarryOver)
	negJSON, _ := json.Marshal(res.Negative)
	evidenceJSON, _ := json.Marshal(res.EvidenceRef)

	_, err := e.m.pg.Exploration(t.ExpID).PutReviewerVerdict(db.ReviewerVerdictInput{
		TaskID:        tid,
		ExplorationID: t.ExpID,
		IntentID:      intent.ID,
		RunID:         runID,
		Verdict:       string(res.Verdict),
		Attribution:   string(res.Attribution),
		Confidence:    res.Confidence,
		DecidedBy:     res.DecidedBy,
		Summary:       res.Summary,
		FactsJSON:     string(factsJSON),
		CluesJSON:     string(cluesJSON),
		CarryJSON:     string(carryJSON),
		NegativeJSON:  string(negJSON),
		Sanitized:     res.Sanitized,
		RawBytes:      res.RawBytes,
		EvidenceJSON:  string(evidenceJSON),
	})
	if err != nil {
		log.Printf("[reviewer] task %s 意图 #%d 写判决失败: %v", t.ID, intent.ID, err)
		return
	}
	log.Printf("[reviewer] task %s 意图 #%d run %d → %s（%s, conf=%.2f, %s, raw=%dB, evidence=%v）",
		t.ID, intent.ID, runID, res.Verdict, res.Attribution, res.Confidence, res.DecidedBy,
		res.RawBytes, res.EvidenceRef)

	// W3：线索落库。abandon 的阴性结论也进线索库——否则下一轮 Planner 会再把
	// 同一条死路派一遍，"换了字的原地打转"就是这么来的。
	e.writeReviewerClues(t, intent, res)
}

// reviewerStateOf 从交接单里还原 run 的终态。
func reviewerStateOf(rec handoffRecord) string {
	d := rec.Trigger.Detail
	if i := strings.Index(d, "/"); i > 0 {
		d = d[:i]
	}
	return strings.TrimSpace(d)
}

// writeReviewerClues 把判决里的线索写进 clues 表（W3 的落点）。
// kind 必须先落在 db 的合法枚举里，否则会被 NormalizeClue 降级成 note。
func (e *Engine) writeReviewerClues(t *Task, intent *db.Node, res agent.ReviewerResult) {
	store := e.m.pg.Exploration(t.ExpID).Clues()
	if store == nil {
		return
	}
	var inputs []db.ClueInput
	for _, c := range res.Clues {
		inputs = append(inputs, db.ClueInput{
			Kind:       c.Kind,
			Key:        c.Key,
			Value:      c.Value,
			Source:     "reviewer",
			Confidence: c.Confidence,
			ProducedBy: agent.ReviewerKey,
			IntentID:   intent.ID,
		})
	}
	for _, n := range res.Negative {
		inputs = append(inputs, db.ClueInput{
			Kind:       "note",
			Key:        "negative:" + clipText(n, 120),
			Value:      n,
			Source:     "reviewer",
			Confidence: "observed",
			ProducedBy: agent.ReviewerKey,
			IntentID:   intent.ID,
		})
	}
	if len(inputs) == 0 {
		return
	}
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	added, seen, err := store.PutMany(tid, inputs)
	if err != nil {
		log.Printf("[reviewer] task %s 意图 #%d 写线索失败: %v", t.ID, intent.ID, err)
		return
	}
	log.Printf("[reviewer] task %s 意图 #%d 线索新增 %d、去重命中 %d", t.ID, intent.ID, added, seen)
}

// loaderEvidence 是 fetch_evidence 的读取端。返回落库时那份已清洗的正文。
func (e *Engine) loaderEvidence(refID int64) (agent.ReviewerEvidence, error) {
	if e == nil || e.m == nil || e.m.pg == nil {
		return agent.ReviewerEvidence{}, errReviewerNoStore
	}
	row, err := e.m.pg.Exploration(0).GetReviewerEvidence(refID)
	if err != nil {
		return agent.ReviewerEvidence{}, err
	}
	return agent.ReviewerEvidence{
		ID:        row.ID,
		Kind:      row.Kind,
		Body:      row.Body,
		Touches:   row.Touches,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// ---- 情报闸门：先落库、再唤醒；落不进去就降级，绝不放行 ----

const (
	// reviewerGateFailThreshold 是连续多少次闸门超时后打开熔断。
	reviewerGateFailThreshold = 3
	// reviewerGateCooldown 是熔断打开后的冷却时长。冷却期内不再逐 run 空等，
	// 直接走降级路径——存储明显病着的时候，每个 run 白等 5s 只会把整条流水线拖死。
	reviewerGateCooldown = 60 * time.Second
)

var reviewerGateBreaker = struct {
	mu        sync.Mutex
	fails     int
	openUntil time.Time
}{}

// reviewerGateOpen 报告熔断是否打开。冷却结束后的第一次调用做「半开」：
// 放一个 run 去试，失败会立刻重新打开熔断。
func reviewerGateOpen() bool {
	g := &reviewerGateBreaker
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fails < reviewerGateFailThreshold {
		return false
	}
	if time.Now().Before(g.openUntil) {
		return true
	}
	g.fails = reviewerGateFailThreshold - 1 // 半开：只放一个探针
	return false
}

func reviewerGateFail() {
	g := &reviewerGateBreaker
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails++
	if g.fails >= reviewerGateFailThreshold {
		g.openUntil = time.Now().Add(reviewerGateCooldown)
	}
}

func reviewerGateSucceed() {
	g := &reviewerGateBreaker
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails = 0
	g.openUntil = time.Time{}
}

// awaitReviewerVerdict 是「先落库、再唤醒 Planner」的硬闸门。
//
// 三条铁律：
//  1. 判决在库里 → 放行；
//  2. 判决不在库里 → **绝不放行未经裁定的情报**，改为写一条「本 run 作废」的
//     降级判决（abandon / harness / fallback）。降级判决同样是判决，写进去之后
//     Planner 看到的是「这轮白跑，换方向」，而不是这轮没洗过的原始产出——顺序
//     因此始终成立，三层保护也没有被绕过；
//  3. 连降级判决都写不进去（存储已经不可写）→ 返回 false，调用方拒绝唤醒，
//     任务停摆等人工介入。宁可停，不可放。
//
// 熔断只作用于「等」这个动作，不作用于「降级」这个结论。
func (e *Engine) awaitReviewerVerdict(t *Task, intent *db.Node, runID int64, d time.Duration) bool {
	if e == nil || e.m == nil || e.m.pg == nil || t == nil || intent == nil || runID <= 0 {
		return true // 没有台账就没有闸门可言
	}
	if reviewerGateOpen() {
		return e.degradeReviewerVerdict(t, intent, runID, "闸门熔断打开")
	}
	deadline := time.Now().Add(d)
	for {
		ok, err := e.m.pg.Exploration(t.ExpID).HasReviewerVerdict(runID)
		if err == nil && ok {
			reviewerGateSucceed()
			return true
		}
		if time.Now().After(deadline) {
			reviewerGateFail()
			return e.degradeReviewerVerdict(t, intent, runID, "等待判决落库超时")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// degradeReviewerVerdict 是强制降级：把「本轮情报一律不采信」写成一条正式判决。
// 返回 true 表示降级判决已落库（可以唤醒 Planner，它只会看到作废结论）；
// 返回 false 表示连这条都写不进去（必须停摆，不得唤醒）。
func (e *Engine) degradeReviewerVerdict(t *Task, intent *db.Node, runID int64, why string) bool {
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	_, err := e.m.pg.Exploration(t.ExpID).PutReviewerVerdict(db.ReviewerVerdictInput{
		TaskID:        tid,
		ExplorationID: t.ExpID,
		IntentID:      intent.ID,
		RunID:         runID,
		Verdict:       string(agent.VerdictAbandon),
		Attribution:   string(agent.AttrHarness),
		Confidence:    1,
		DecidedBy:     "fallback",
		Summary: "情报闸门降级（" + why + "）：本 run 的产出全部作废，不得作为规划依据。" +
			"请更换方向重新规划；若反复出现，先排查存储健康度。",
		Sanitized: true,
	})
	if err != nil {
		log.Printf("[reviewer] 降级判决写入失败（%s），run %d 拒绝唤醒 Planner，任务停摆待人工介入: %v",
			why, runID, err)
		return false
	}
	log.Printf("[reviewer] 强制降级：run %d 以 abandon/harness 落库（%s），本 run 产出作废", runID, why)
	return true
}

// reviewerComplete 是 Reviewer 的模型调用实现，由 Server 在启动时接线到
// agent.ReviewerLLM。
//
// 走 taskLLMRuntime：它自带「角色绑定 → 任务链 → 全局」的解析与配额/熔断记账，
// 所以 Reviewer 的调用会像其他角色一样被计量、被 failover 接住，而不是绕开池子
// 裸打一个 HTTP。agentKey 用 "reviewer"：用户可以在设置里单独给它绑一个便宜模型；
// 没绑就自然回落到任务链/全局，不会报错。
func (s *Server) reviewerComplete(ctx context.Context, taskID, system, user string) (string, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return "", errReviewerNoStore
	}
	rt := &taskLLMRuntime{s: s, taskID: taskID, agentKey: agent.ReviewerKey}
	req := llm.CompletionRequest{
		System:    []string{system},
		Messages:  []llm.Message{llm.UserText(user)},
		MaxTokens: 1200,
		// 判决是收敛型任务，不需要推理预算；关掉思考也避免把 thinking 块写回。
		Thinking: "disabled",
	}
	msg, _, _, err := rt.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	return msg.Text(), nil
}
