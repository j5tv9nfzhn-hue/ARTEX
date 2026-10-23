package agent

// cold-digest §4/§5/§7: the Compactor ties the pure algorithms (coldgraph.go)
// to the store (db/digest.go) and the LLM. It runs in two modes:
//
//	maintain — cheap, synchronous, once per planner round: bump round_no,
//	           recompute hot/cold, stamp/clear cold_since_round (§2.3). This is
//	           the bookkeeping the planner does anyway; it never calls the LLM.
//	minor/major — background, off the planner hot path (§7): group cold nodes
//	           and compress each ≥2 block into a digest via the LLM. minor folds
//	           only the not-yet-covered cold set (tiered append); major re-derives
//	           the whole grouping from source and merges fragments (§5.1/§5.2),
//	           reusing bodies whose signature is unchanged (§5.3).
//
// Concurrency: one compaction per task at a time (mutex), ≥cooldown between runs,
// and a commit-time liveness recheck drops any member that revived while the body
// was being generated so a digest never covers a hot node.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Compactor performs background cold-node compaction for many explorations.
type Compactor struct {
	prov     llm.Provider
	model    string
	params   coldParams
	n, m     int           // minor / major thresholds (§7 N=20, M=8)
	cooldown time.Duration // min gap between compactions per task (§7 60s)
	maxDur   time.Duration // hard cap on one background compaction

	observer func(CompactionEvent) // host hook (批次 6: 压缩状态/中断检查点); nil = off
	windowFn func() int            // 当前任务上下文窗口（摘要预算用）；nil = 按默认窗口

	// 本轮压缩的产出统计（供 compaction_state 落库对账）：注入字节 / 估算 token。
	// 后台单轮只压一个探索（tryStart 保证），故进程级累加即可，run 开始时清零。
	statInjected  atomic.Int64
	statDigestTok atomic.Int64

	mu      sync.Mutex
	running map[int64]bool
	lastRun map[int64]time.Time
	cancels map[int64]context.CancelCauseFunc // 在跑的压缩 → 可被任务暂停打断
}

// CompactionEvent reports one background compaction lifecycle step to the host
// (批次 6 / #27、#28、#37). The host persists it into compaction_state and, when a
// run is interrupted, writes a timeline checkpoint so a resume can verify
// consistency ("图要么含该 digest、要么完全不含" — AddDigest 是事务性的).
type CompactionEvent struct {
	ExpID int64          // exploration the compaction belongs to
	Stage string         // "start" | "finish"
	Phase string         // "minor" | "major"
	Info  map[string]any // start: round / uncompressed / active_digests
	Err   error          // finish: nil = clean end; non-nil = interrupted / timed out / internal
}

// SetObserver wires the host's compaction lifecycle observer. nil = none. Called
// from the background goroutine, so the observer must be safe for concurrent use.
func (c *Compactor) SetObserver(fn func(CompactionEvent)) { c.observer = fn }

func (c *Compactor) notify(ev CompactionEvent) {
	if c != nil && c.observer != nil {
		c.observer(ev)
	}
}

// Running reports whether a background compaction is currently in flight for the
// exploration (进程内事实)。重启后 running 为空而 compaction_state 仍是 running,
// 这一不一致正是恢复校验要处理的场景。
func (c *Compactor) Running(expID int64) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running[expID]
}

// Interrupt aborts the in-flight background compaction for one exploration (task
// pause/stop). Compaction deliberately outlives a planner round, so 暂停任务时它
// 不会自己停;由宿主显式打断。已提交的 digest 不受影响,未完成的分块留待下轮重做。
// Returns whether a run was actually cancelled.
func (c *Compactor) Interrupt(expID int64, cause error) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	cancel := c.cancels[expID]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	if cause == nil {
		cause = context.Canceled
	}
	cancel(cause)
	return true
}

// NewCompactor builds a compactor. prov/model are used for the §4 body LLM call
// (same model the agent runs on, per §4). A nil Compactor is a safe no-op.
func NewCompactor(prov llm.Provider, model string) *Compactor {
	return &Compactor{
		prov:     prov,
		model:    model,
		params:   defaultColdParams(),
		n:        20,
		m:        8,
		cooldown: 60 * time.Second,
		maxDur:   5 * time.Minute,
		running:  map[int64]bool{},
		lastRun:  map[int64]time.Time{},
		cancels:  map[int64]context.CancelCauseFunc{},
	}
}

// OnPlannerRound is the single entry the planner calls each wake-up. It bumps the
// round, maintains the cold stamps synchronously, then (if a threshold is hit and
// no compaction is running / cooling down) launches a background compaction that
// outlives this planner round.
func (c *Compactor) OnPlannerRound(ctx context.Context, ts *db.ExplorationStore) {
	if c == nil || c.prov == nil || ts == nil {
		return
	}
	round, uncompressed, activeDigests, err := c.maintain(ts)
	if err != nil {
		log.Printf("[compaction] maintain exp=%d: %v", ts.ID(), err)
		return
	}
	needMinor := uncompressed >= c.n
	needMajor := activeDigests >= c.m
	if !needMinor && !needMajor {
		return
	}
	if !c.tryStart(ts.ID()) {
		return // already running, or within cooldown —派生态最终一致，下轮再压
	}
	phase := "minor"
	if needMajor {
		phase = "major"
	}
	info := map[string]any{"round": round, "uncompressed": uncompressed, "active_digests": activeDigests}
	go func() {
		expID := ts.ID()
		defer c.finish(expID)
		// cause-cancelable parent so task pause can interrupt this run distinctly
		// from the hard timeout (批次 6 / #28).
		parent, cancelCause := context.WithCancelCause(context.WithoutCancel(ctx))
		defer cancelCause(nil)
		bg, stopTimeout := context.WithTimeout(parent, c.maxDur)
		defer stopTimeout()
		c.setCancel(expID, cancelCause)
		defer c.clearCancel(expID)
		c.statInjected.Store(0)
		c.statDigestTok.Store(0)
		c.notify(CompactionEvent{ExpID: expID, Stage: "start", Phase: phase, Info: info})
		// 压缩是裸 provider 调用（compress 里直接 prov.Complete），不经过 agentcore
		// 的会话循环，所以 ctx 上没有 session id；按 session-id 头做提示缓存/粘性
		// 路由的网关（opencode zen 缺 x-opencode-session 直接 400）就收不到该头。
		// 这里补一个按探索稳定的 id：同一探索的所有压缩请求共享它，既能带上头，
		// 也让 llmrec 能把这次调用的 token 归因回该探索（此前记不到）。
		bg = transcript.WithSessionID(bg, fmt.Sprintf("exp%d-compactor", expID))
		if needMajor {
			c.major(bg, ts)
		} else {
			c.minor(bg, ts)
		}
		// context.Cause: nil = 正常结束; 非 nil = 超时(cause=DeadlineExceeded)或
		// 被任务暂停打断(cause=宿主传入的 AbortPausedByUser 等)。
		// 产出规模随 finish 一起上报：compaction_state 的 injected_bytes /
		// digest_tokens_est 由宿主落库，用来回答「这次摘要到底写了多少、值不值」。
		info["injected_bytes"] = c.statInjected.Load()
		info["digest_tokens_est"] = c.statDigestTok.Load()
		c.notify(CompactionEvent{ExpID: expID, Stage: "finish", Phase: phase, Info: info, Err: context.Cause(bg)})
	}()
}

// maintain bumps round_no, recomputes hot/cold over the whole graph, and applies
// the cold_since_round stamp/clear ops (§2.3). Returns the new round plus the
// counts that drive the trigger: how many eligible-cold nodes are not yet covered
// (minor) and how many active digests exist (major).
func (c *Compactor) maintain(ts *db.ExplorationStore) (round int64, uncompressed, activeDigests int, err error) {
	round, err = ts.BumpRound()
	if err != nil {
		return
	}
	g, _, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	hot := g.hotSet()
	structCold := g.structuralCold(hot)
	ops := computeStampOps(structCold, stamps, round)
	if err = ts.ApplyStampOps(toDBStampOps(ops)); err != nil {
		return
	}
	applyStampsInPlace(stamps, ops)
	elig := g.eligibleCold(structCold, stamps, round, c.params)
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed++
		}
	}
	ad, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	activeDigests = len(ad)
	return
}

// minor folds the not-yet-covered eligible-cold set into new digest segments
// (tiered append, §5). Existing digests are untouched.
func (c *Compactor) minor(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return
	}
	covered, err := ts.CoveredMembers()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	uncompressed := map[int64]bool{}
	for id := range elig {
		if _, ok := covered[id]; !ok {
			uncompressed[id] = true
		}
	}
	blocks := g.group(uncompressed, c.params)
	if len(blocks) == 0 {
		return // this batch has no ≥2 connected/shared-parent block — nothing to fold (§7)
	}
	for _, b := range blocks {
		c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, nil))
	}
	// A minor may have pushed the segment count over M → merge in the same run.
	if ad, e := ts.ActiveDigests(); e == nil && len(ad) >= c.m {
		c.major(ctx, ts)
	}
}

// major re-derives the whole grouping from source over ALL eligible-cold nodes
// (§5.1 回源重压), then reconciles against the active digests by signature:
// unchanged blocks keep their digest (no LLM), stale digests are superseded, and
// new/changed blocks are compressed afresh. This is where tiered fragments of one
// direction merge and where "later became connected" blocks unify (§5.2).
func (c *Compactor) major(ctx context.Context, ts *db.ExplorationStore) {
	round, err := ts.RoundNo()
	if err != nil {
		return
	}
	g, nodeByID, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	stamps, err := ts.ColdStamps()
	if err != nil {
		return
	}
	cvers, err := ts.ContentVersions()
	if err != nil {
		return
	}
	active, err := ts.ActiveDigests()
	if err != nil {
		return
	}
	hot := g.hotSet()
	elig := g.eligibleCold(g.structuralCold(hot), stamps, round, c.params)
	blocks := g.group(elig, c.params)

	bySig := map[string]*db.Node{}
	for _, d := range active {
		sig, _ := digestSigGen(d)
		bySig[sig] = d
	}
	desired := map[string]bool{}
	var toCreate []block
	for _, b := range blocks {
		sig := blockSignature(b, cvers)
		desired[sig] = true
		if _, ok := bySig[sig]; ok {
			continue // unchanged → reuse the existing digest, skip LLM (§5.3)
		}
		toCreate = append(toCreate, b)
	}
	// Supersede stale digests FIRST (atomic drop of their covers edges) so a member
	// is never covered by both an old and a new digest (§5.1 one-member-one-digest).
	var stale []int64
	for _, d := range active {
		sig, _ := digestSigGen(d)
		if !desired[sig] {
			stale = append(stale, d.ID)
		}
	}
	// 被打断时先别作废旧 digest：保持「压缩前边界」原样，下一轮从头重压。
	if ctx.Err() == nil {
		if err := ts.SupersedeDigests(stale); err != nil {
			log.Printf("[compaction] supersede exp=%d: %v", ts.ID(), err)
		}
	}
	for _, b := range toCreate {
		c.foldBlock(ctx, ts, g, b, nodeByID, cvers, c.generationFor(b, active))
	}
}

// foldBlock compresses one block and writes its digest — with a commit-time
// liveness recheck (§ concurrency): between grouping and write the graph may have
// changed, so any member that has since gone hot (revived) is dropped from the
// covers set. If the block dissolves below K it is skipped.
func (c *Compactor) foldBlock(ctx context.Context, ts *db.ExplorationStore, g *coldGraph, b block, nodeByID map[int64]*db.Node, cvers map[int64]int, generation int) {
	if ctx.Err() != nil {
		return // interrupted before this block: nothing committed, nothing to undo
	}
	body, err := c.compress(ctx, g, b, nodeByID)
	if err != nil {
		log.Printf("[compaction] compress exp=%d block=%v: %v", ts.ID(), b.Members, err)
		return
	}
	// Re-read fresh state and drop any member that revived while we compressed.
	fresh, _, err := loadColdGraph(ts)
	if err != nil {
		return
	}
	freshHot := fresh.hotSet()
	members := make([]int64, 0, len(b.Members))
	for _, mID := range b.Members {
		if !freshHot[mID] {
			members = append(members, mID)
		}
	}
	if len(members) < c.params.K {
		return // block revived out from under us — leave those nodes hot, don't fold
	}
	final := block{Members: members, Anchors: b.Anchors}
	payload := digestPayload(body, final, nodeByID, generation, blockSignature(final, cvers))
	if _, err := ts.AddDigest(payload, members); err != nil {
		log.Printf("[compaction] add digest exp=%d: %v", ts.ID(), err)
	}
}

// generationFor computes a digest's重摘代次 (§1): 1 for a fresh fold; for a major
// merge, max(generation) over the active digests that overlap this block's
// members, +1.
func (c *Compactor) generationFor(b block, active []*db.Node) int {
	if len(active) == 0 {
		return 1
	}
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	best := 0
	for _, d := range active {
		_, gen := digestSigGen(d)
		for _, m := range digestMemberIDs(d) {
			if memberSet[m] {
				if gen > best {
					best = gen
				}
				break
			}
		}
	}
	return best + 1
}

// tryStart acquires the per-task compaction lock, honoring the cooldown.
func (c *Compactor) tryStart(expID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[expID] {
		return false
	}
	if t, ok := c.lastRun[expID]; ok && time.Since(t) < c.cooldown {
		return false
	}
	c.running[expID] = true
	return true
}

func (c *Compactor) finish(expID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[expID] = false
	c.lastRun[expID] = time.Now()
}

// setCancel registers the running compression's cause-cancel so Interrupt can
// reach it; clearCancel removes it on completion.
func (c *Compactor) setCancel(expID int64, cancel context.CancelCauseFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancels[expID] = cancel
}

func (c *Compactor) clearCancel(expID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cancels, expID)
}

// --- helpers: db ↔ coldgraph ---

// loadColdGraph reads the exploration's nodes + edges and builds the cold-graph
// view plus an id→node index (for summaries/payload during compression).
func loadColdGraph(ts *db.ExplorationStore) (*coldGraph, map[int64]*db.Node, error) {
	// Compaction must see the WHOLE graph, not the default row caps — pass a very
	// high limit so the LIMIT clause is effectively unbounded for real task sizes.
	const allRows = 1 << 30
	nodes, err := ts.Nodes(allRows)
	if err != nil {
		return nil, nil, err
	}
	edges, err := ts.Edges(allRows)
	if err != nil {
		return nil, nil, err
	}
	cgNodes := make([]cgNode, 0, len(nodes))
	byID := make(map[int64]*db.Node, len(nodes))
	for _, n := range nodes {
		cgNodes = append(cgNodes, cgNode{ID: n.ID, Kind: n.Kind, State: n.State})
		byID[n.ID] = n
	}
	cgEdges := make([]cgEdge, 0, len(edges))
	for _, e := range edges {
		cgEdges = append(cgEdges, cgEdge{From: e.From, Rel: e.Rel, To: e.To})
	}
	return newColdGraph(cgNodes, cgEdges), byID, nil
}

func toDBStampOps(ops []stampOp) []db.StampOp {
	out := make([]db.StampOp, len(ops))
	for i, o := range ops {
		out[i] = db.StampOp{ID: o.ID, Set: o.Set, Round: o.Round}
	}
	return out
}

// applyStampsInPlace folds the just-applied ops into the in-memory stamp map so
// eligibility can be computed immediately without a re-read.
func applyStampsInPlace(stamps map[int64]*int64, ops []stampOp) {
	for _, o := range ops {
		if o.Set {
			r := o.Round
			stamps[o.ID] = &r
		} else {
			stamps[o.ID] = nil
		}
	}
}

// --- helpers: digest payload ---

// digestPayload builds the digest node payload (cold-digest §1): the body, the
// member ids split by kind (restore cache; source of truth is the covers edges),
// the anchor ids, the generation, and the change-detection signature.
func digestPayload(body string, b block, nodeByID map[int64]*db.Node, generation int, signature string) map[string]any {
	var facts, intents []int64
	for _, m := range b.Members {
		if n := nodeByID[m]; n != nil && n.Kind == db.KindIntent {
			intents = append(intents, m)
		} else {
			facts = append(facts, m)
		}
	}
	return map[string]any{
		"body":       body,
		"member_ids": map[string]any{"facts": facts, "intents": intents},
		"anchor_ids": b.Anchors,
		"generation": generation,
		"signature":  signature,
	}
}

func digestSigGen(n *db.Node) (string, int) {
	var p struct {
		Signature  string `json:"signature"`
		Generation int    `json:"generation"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Signature, p.Generation
}

func digestMemberIDs(n *db.Node) []int64 {
	var p struct {
		MemberIDs struct {
			Facts   []int64 `json:"facts"`
			Intents []int64 `json:"intents"`
		} `json:"member_ids"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return append(append([]int64{}, p.MemberIDs.Facts...), p.MemberIDs.Intents...)
}

// --- helpers: compression input + LLM (§4) ---

func nodeSummary(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
		if t, ok := p["text"].(string); ok {
			return t
		}
	}
	return ""
}

func nodeConfidence(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok {
			return c
		}
	}
	return ""
}

// buildCompressionInput renders the connected sub-graph for the §4 prompt:
// member nodes (summary + id + kind + state + confidence), the internal blood
// edges among members, and — for a §3.1 shared-parent group — the anchor parents
// as context ("共同父 #p"), which are NOT members.
func buildCompressionInput(g *coldGraph, b block, nodeByID map[int64]*db.Node) string {
	memberSet := make(map[int64]bool, len(b.Members))
	for _, m := range b.Members {
		memberSet[m] = true
	}
	var sb strings.Builder
	sb.WriteString("【成员节点（要压缩的）】：\n")
	for _, m := range b.Members {
		n := nodeByID[m]
		kind := "fact"
		if n != nil && n.Kind == db.KindIntent {
			kind = "intent"
		}
		state := ""
		if n != nil {
			state = n.State
		}
		line := fmt.Sprintf("- #%d [%s/%s] %s", m, kind, state, nodeSummary(n))
		if conf := nodeConfidence(n); conf != "" {
			line += fmt.Sprintf(" (confidence=%s)", conf)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	// internal edges among members
	var edgeLines []string
	for _, m := range b.Members {
		for _, to := range g.children[m] {
			if memberSet[to] {
				edgeLines = append(edgeLines, fmt.Sprintf("- #%d 产出/派生→ #%d", m, to))
			}
		}
	}
	if len(edgeLines) > 0 {
		sb.WriteString("\n【成员之间的血缘边（父→子）】：\n")
		sort.Strings(edgeLines)
		sb.WriteString(strings.Join(edgeLines, "\n"))
		sb.WriteByte('\n')
	}
	if len(b.Anchors) > 0 {
		sb.WriteString("\n【共同父 / 上下文锚（不是成员，只用于理解这些结果从哪个意图探出）】：\n")
		for _, a := range b.Anchors {
			n := nodeByID[a]
			state := ""
			if n != nil {
				state = n.State
			}
			fmt.Fprintf(&sb, "- #%d [%s] %s\n", a, state, nodeSummary(n))
		}
	}
	return sb.String()
}

// compress runs the §4 body LLM call on one block. Uses the same model the agent
// runs on; thinking disabled (a pure summarization step).
//
// 输出上限不再是固定 800，而是按「这一块的输入量 × 窗口」推导（DigestBudgetTokens）：
// 固定 800 的问题是**摘要短于信息量**，规划者读完仍不确定，只能回头重查原节点，
// 于是调用次数反弹——省下的 token 又花回去，还多出几轮延迟。
//
// 生成后追加一行机械锚点（appendDigestAnchors）：由代码从原始节点抽取 id / 资产 /
// 漏洞 / 路径，**不参与任何字符预算**。这样正文无论如何被注入侧截断，「这条结论
// 对应哪个节点、哪个资产」都不会丢。
func (c *Compactor) compress(ctx context.Context, g *coldGraph, b block, nodeByID map[int64]*db.Node) (string, error) {
	input := buildCompressionInput(g, b, nodeByID)
	req := llm.CompletionRequest{
		System:    []string{compressionSystemPrompt},
		Messages:  []llm.Message{llm.UserText(input)},
		MaxTokens: DigestBudgetTokens(estimateTextTokens(input), c.windowTokens()),
		Thinking:  "disabled",
	}
	msg, _, _, err := c.prov.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(msg.Text())
	if body == "" {
		return "", fmt.Errorf("empty body from model")
	}
	body = appendDigestAnchors(body, b, nodeByID)
	// 统计口径与注入侧一致：按字符（rune）算体积、按 estimateTextTokens 估 token。
	c.statInjected.Add(int64(runeLen(body)))
	c.statDigestTok.Add(int64(estimateTextTokens(body)))
	return body, nil
}

// windowTokens 返回本压缩器使用的上下文窗口（供预算推导）。
// 未注入解析器时退回默认窗口——冷摘要是后台任务，窗口取不准只会让预算偏保守。
func (c *Compactor) windowTokens() int {
	if c.windowFn != nil {
		if w := c.windowFn(); w > 0 {
			return w
		}
	}
	return defaultWindowK * 1000
}

// SetWindowResolver 注册「当前任务的上下文窗口」。与 planner/worker 的
// SetCompactionWindowResolver 同源，保证摘要预算与压缩触发线用的是同一个窗口。
func (c *Compactor) SetWindowResolver(fn func() int) { c.windowFn = fn }

// digestAnchorPatterns 是「可回溯指针」的机械抽取规则。
//
// 只抽结构化、可从原始节点复现的片段——id、IPv4(:port)、CVE、绝对/相对路径、常见
// 工具名。不抽自然语言描述：那是模型的活儿，代码只负责**不丢**。
var digestAnchorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`), // IP / IP:port
	regexp.MustCompile(`\bCVE-\d{4}-\d{4,7}\b`),                    // CVE
	regexp.MustCompile(`(?i)\b(?:[A-Z]:\\|/)[\w.\-/]{3,}`),         // 绝对路径（含盘符）
	regexp.MustCompile(`\b[\w.-]+\.(?:go|py|js|ts|tsx|json|yaml|yml|txt|log|md|sh|ps1|bat)\b`),
}

// appendDigestAnchors 在 body 之后追加一行 [[anchors]]。
//
// 为什么放在代码里而不是让模型输出：模型写摘要时会丢 id（它认为那是噪音），而 id 正是
// 「这条结论能不能被复核」的唯一凭据。抽取逻辑必须是确定的、与模型无关的。
func appendDigestAnchors(body string, b block, nodeByID map[int64]*db.Node) string {
	set := map[string]bool{}
	var ordered []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || set[v] {
			return
		}
		set[v] = true
		ordered = append(ordered, v)
	}
	// 成员与锚点节点的 id 永远在最前面：它们是最低成本的定位手段。
	for _, m := range b.Members {
		add(fmt.Sprintf("#%d", m))
	}
	for _, a := range b.Anchors {
		add(fmt.Sprintf("#%d", a))
	}
	for _, m := range b.Members {
		text := nodeSummary(nodeByID[m])
		if text == "" {
			continue
		}
		for _, re := range digestAnchorPatterns {
			for _, hit := range re.FindAllString(text, 4) {
				add(hit)
			}
		}
	}
	if len(ordered) == 0 || len(ordered) > 24 {
		if len(ordered) > 24 {
			ordered = ordered[:24]
		}
	}
	if len(ordered) == 0 {
		return body
	}
	return body + "\n\n[[anchors]] " + strings.Join(ordered, " · ")
}

// compressionSystemPrompt is the §4 body prompt.
const compressionSystemPrompt = `你在压缩一组【彼此关联】的探索节点，产出一段综合结论(body)，供规划者快速掌握"这一片已经探明了什么"。

输入是一个连通子图：
- 节点：每条是一个意图或事实的 summary（一句话），带 id、类型(intent/fact)、state、confidence(若有)。
- 关系：节点之间的血缘边（A 派生自 B / A 产出 B），说明它们如何串联。
- 若节点间没有直接血缘边、但同属一个上游意图（会另给出该上游意图作为"共同父 #p"），则按"这个意图（#p）探到了什么"来综合它们——共同父只是上下文锚、不是要压缩的成员。

据此写一段 body：
1. 综合、不罗列：顺着关系把因果串起来（哪个事实催生哪个意图、哪条意图产出了哪个结论），讲成"这一片探索得出了什么"，不要把每条 summary 抄一遍。
2. 保留区分度：彼此不同的结论分别说清，别揉成一句笼统的话。
3. 保留证据强度：带 confidence 的结论标出 observed / inferred；inferred 的否定/存疑结论要点明它只是推断、可复核，别写成定论。
4. 带上 id：每条结论后标注来源节点 id（如"…（#12,#28）"），让规划者能按 id 还原原节点。
5. 正向陈述、只写输入里有的：不脑补、不引入输入中没有的判断。
6. 长度随内容自适应：结论少就短，多且互不相同就写够——但整体显著短于所有输入 summary 的总和。

只输出 body 正文本身。`
