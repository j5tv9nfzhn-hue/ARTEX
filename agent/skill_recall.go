// skill_recall.go — 技能再加载（skill recall）：让技能在多轮工具、压缩之后、跨 run 续跑时被重新取回。
//
// 背景（问题不是「索引」，是「时效」）：内置技能库 830 个，走渐进式披露——第一层只有
// 名称+描述，正文只在模型主动调用 Skill 时才进入上下文。现有实现只在 run 开始时按
// 任务/意图文本给一次路由建议（BuildTacticalRoutingHint），于是：
//
//  1. 上下文被压缩后，技能正文被摘要掉，模型却以为自己「还记得」；
//  2. 长任务跑到中后段，攻击面已经从 A 领域漂到 B 领域，路由建议还是开跑时那一条；
//  3. 换 run 续跑（新会话、交接给下一位子代理）时，上一 run 加载过的技能既不在
//     上下文里，也没人告诉模型「你以前加载过它」。
//
// 本文件不新增任何模型调用、不改工具语义、不改压缩策略：只在三个事件点把一小段
// 提示**追加到工具结果末尾**（与 tool_steering 同一通道，模型侧零额外等待），并且
// 全部带预算与去重，避免变成噪声：
//
//	A. 压缩后提醒（由 attachCompaction 的观察者打点，见 markCompaction）；
//	B. 方向漂移重算（每 skillRecallRerouteEvery 次工具调用，用技能描述做本地词面
//	   匹配，只在新出现的候选上提示一次）；
//	C. 跨 run 续跑提醒（同一任务此前加载过、但本 run 还没加载过时提示一次）。
//
// 与七阶段的关系：这是「工具装配层」的旁路增强，挂在所有 Agent 共用的
// AugmentTools 上，不新增工具、不改角色分工、不改权限模型、不改上下文窗口策略。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/intercept"
)

const (
	// skillRecallNoteBudget 是单个任务最多追加多少条再加载提示。
	// 取 4：一条压缩提醒、一条跨 run 提醒、再加两条方向漂移，够了；再多就是噪声。
	skillRecallNoteBudget = 4
	// skillRecallRerouteEvery 是「每多少次工具调用重算一次方向」。
	skillRecallRerouteEvery = 6
	// skillRecallObsCap / skillRecallObsLen 是观察窗口：只留最近若干条、每条截断，
	// 避免长跑任务里这块状态无界增长。
	skillRecallObsCap = 12
	skillRecallObsLen = 200
	// skillRecallHitMin 是判定「候选技能」的最低词面命中数（至少 2 个独立词命中）。
	skillRecallHitMin = 2
	// skillRecallScopeMax 是进程内保留多少个任务的技能记忆；超了整表清空（只丢记忆，
	// 不影响正确性），避免长跑进程里 map 无界增长。
	skillRecallScopeMax = 256
	// skillRecallIndexMax 是技能目录索引缓存条数（每个 Agent 的可见清单不同，
	// 描述文本作为缓存键）。
	skillRecallIndexMax = 8
)

// skillRecallState 是**按任务**累积的技能记忆：跨 run 保留（同一任务换 run 续跑时要
// 用），同一任务内并发 run（如并发子代理）由互斥锁保护。
type skillRecallState struct {
	mu        sync.Mutex
	loaded    []string        // 本任务按顺序加载过的技能名
	loadedSet map[string]bool // 去重用
	injected  map[string]bool // 已经提示过的技能名（不重复提示）
	obs       []string        // 最近的工具观察窗口
	calls     int             // 本任务累计工具调用次数
	notes     int             // 已追加提示条数（预算）
	// compactEpoch 由压缩观察者递增，epochSeen 是本次读取到的值；两者不等即「压缩过」。
	compactEpoch int64
	epochSeen    int64
	// prevRunLoaded / runLoaded 用来判定「跨 run 续跑」：上一个 run 加载过技能、
	// 而这个 run 还没加载过，就提示一次「正文不在当前上下文」。
	prevRunLoaded     int
	runLoaded         int
	resumeNotePending bool
}

func newSkillRecallState() *skillRecallState {
	return &skillRecallState{
		loadedSet: map[string]bool{},
		injected:  map[string]bool{},
	}
}

// skillRecallScope 是按任务保存的技能记忆表（进程内）。
var skillRecallScope = struct {
	sync.Mutex
	m map[string]*skillRecallState
}{m: map[string]*skillRecallState{}}

// skillRecallFor 取（或建立）当前任务的技能记忆。
//
//	有任务上下文（worker/planner/mainagent…）→ 按 taskID 共享，跨 run 保留；
//	无任务上下文（chat 等）→ 每次装配一个新的，仅本 run 内有效。
func skillRecallFor(ctx context.Context, agentKey string) *skillRecallState {
	id := intercept.TaskIDFromContext(ctx)
	if id == "" {
		return newSkillRecallState()
	}
	key := "task:" + id + ":" + agentKey
	skillRecallScope.Lock()
	defer skillRecallScope.Unlock()
	if skillRecallScope.m == nil {
		skillRecallScope.m = map[string]*skillRecallState{}
	}
	if st, ok := skillRecallScope.m[key]; ok {
		return st
	}
	if len(skillRecallScope.m) >= skillRecallScopeMax {
		skillRecallScope.m = map[string]*skillRecallState{}
	}
	st := newSkillRecallState()
	skillRecallScope.m[key] = st
	return st
}

// markCompaction 由压缩观察者调用：只记「压缩发生过」，具体提示等到下一次工具结果
// 再追加——压缩本身在模型调用之前，插不进对话。
func (s *skillRecallState) markCompaction() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.compactEpoch++
	s.mu.Unlock()
}

// beginRun 在每次工具面装配（= 每个 run 开始）时调用：把上一个 run 的加载情况
// 结算成「是否需要续跑提示」，并重置本 run 计数。装配发生在 run 起始，因此这里的
// 判定是精确的「上一个 run 加载过、这个 run 还没有」。
func (s *skillRecallState) beginRun() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prevRunLoaded = s.runLoaded
	s.runLoaded = 0
	s.resumeNotePending = s.prevRunLoaded > 0
}

func (s *skillRecallState) markLoaded(name string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loadedSet[name] {
		s.loadedSet[name] = true
		s.loaded = append(s.loaded, name)
	}
	s.runLoaded++
}

// observe 记录一次工具调用，并在需要时返回要追加的提示（空串表示不追加）。
func (s *skillRecallState) observe(toolName string, in json.RawMessage, idx *skillIndex) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	if len(s.obs) >= skillRecallObsCap {
		s.obs = s.obs[1:]
	}
	s.obs = append(s.obs, toolName+": "+clipText(toolArgText(in), skillRecallObsLen))

	// C. 跨 run 续跑：上一次 run 加载过技能、本 run 还没加载过时，先提醒一次
	//    「这些技能以前加载过，正文不在当前上下文里」。是否该提示由 beginRun 判定。
	if s.resumeNotePending {
		s.resumeNotePending = false
		if len(s.loaded) > 0 && s.notes < skillRecallNoteBudget {
			s.notes++
			return fmt.Sprintf("<skill_recall>\n【技能时效】本任务此前加载过技能：%s。"+
				"当前 run 的上下文里**没有**它们的正文（新 run / 早期上下文已不在）。"+
				"接下来涉及这些领域的动作前，先用 `Skill({\"name\":\"…\"})` 重新加载，不要凭记忆复述步骤。\n</skill_recall>",
				strings.Join(s.loaded, "、"))
		}
	}

	// A. 压缩之后：正文与早期证据已被移出上下文，提醒重新取回（每次压缩只提示一次）。
	if s.compactEpoch > s.epochSeen {
		s.epochSeen = s.compactEpoch
		if s.notes < skillRecallNoteBudget {
			s.notes++
			if len(s.loaded) > 0 {
				return fmt.Sprintf("<skill_recall>\n【技能重挂载】上下文刚被压缩，技能正文与早期证据可能已被移出。"+
					"此前加载过：%s。**下一步只要落在这几个领域，先重新 `Skill` 加载再执行**；"+
					"只想回看某段结论时，用 Grep 在技能目录的 `references/*.md` 上按关键词定位。\n</skill_recall>",
					strings.Join(s.loaded, "、"))
			}
			return "<skill_recall>\n【技能重挂载】上下文刚被压缩，早期证据可能已被移出。" +
				"接下来动手前，先按当前攻击面用 `Skill` 加载最贴近的技能（不确定名字就按域检索技能清单），再执行。\n</skill_recall>"
		}
	}

	// B. 方向漂移：每 N 次工具调用，用最近观察做一次本地词面匹配，只在**新**候选上提示。
	if idx != nil && s.calls%skillRecallRerouteEvery == 0 && s.notes < skillRecallNoteBudget {
		text := strings.ToLower(strings.Join(s.obs, "\n"))
		name, score := idx.best(text)
		if name != "" && score >= skillRecallHitMin && !s.loadedSet[name] && !s.injected[name] {
			s.injected[name] = true
			s.notes++
			return fmt.Sprintf("<skill_recall>\n【方向漂移】最近的操作更像是 `%s` 的领域，而本任务还没加载过它。"+
				"下一步动手前先 `Skill({\"name\":\"%s\"})` 取回细则（正文不会自己回来）。\n</skill_recall>", name, name)
		}
	}
	return ""
}

// skillRecallTool 把「技能再加载」挂在普通工具外面：只读观察 + 结果末尾追加提示。
type skillRecallTool struct {
	actool.CoreTool
	st      *skillRecallState
	idx     *skillIndex
	name    string
	isSkill bool
}

// AcceptsRawInput 沿用内层：本包装不参与入参纠偏，避免改变校验行为。
func (s *skillRecallTool) AcceptsRawInput() bool { return innerAcceptsRawInput(s.CoreTool) }

func (s *skillRecallTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	res, err := s.CoreTool.Call(ctx, in, tc)
	if err != nil {
		return res, err
	}
	if s.isSkill {
		if !res.IsError {
			s.st.markLoaded(skillCallName(in))
		}
		return res, nil
	}
	if note := s.st.observe(s.name, in, s.idx); note != "" {
		// 与 tool_steering 一致：追加一个 text block，保留既有 block 与 IsError。
		res.Content = append(append([]llm.ContentBlock(nil), res.Content...), llm.TextBlock(note))
	}
	return res, nil
}

// wrapSkillRecall 把一整套工具包上技能再加载观察（Skill 工具自身走记录分支）。
func wrapSkillRecall(tools []actool.CoreTool, st *skillRecallState, idx *skillIndex) []actool.CoreTool {
	if st == nil || len(tools) == 0 {
		return tools
	}
	st.beginRun()
	out := make([]actool.CoreTool, len(tools))
	for i, t := range tools {
		name := t.Name()
		out[i] = &skillRecallTool{CoreTool: t, st: st, idx: idx, name: name, isSkill: name == "Skill"}
	}
	return out
}

// skillToolDescOf 从工具面里取 Skill 工具的描述（内含 <available_skills> 清单），
// 供建立技能索引用；没有 Skill 工具时返回空串。
func skillToolDescOf(tools []actool.CoreTool) string {
	for _, t := range tools {
		if t != nil && t.Name() == "Skill" {
			return t.Description()
		}
	}
	return ""
}

// skillCallName 取 Skill 调用的技能名（兼容 name/skill 两种入参写法）。
func skillCallName(in json.RawMessage) string {
	var p struct {
		Name  string `json:"name"`
		Skill string `json:"skill"`
	}
	if err := json.Unmarshal(in, &p); err != nil {
		return ""
	}
	if p.Name != "" {
		return p.Name
	}
	return p.Skill
}

// toolArgText 把工具入参压成一行可读文本，喂给方向漂移匹配（不做语义解析）。
func toolArgText(in json.RawMessage) string {
	if len(in) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(in, &v); err != nil {
		return string(in)
	}
	if m, ok := v.(map[string]any); ok {
		parts := make([]string, 0, len(m))
		for k, val := range m {
			switch s := val.(type) {
			case string:
				parts = append(parts, k+"="+s)
			case float64, bool:
				parts = append(parts, fmt.Sprintf("%s=%v", k, s))
			}
			if len(parts) >= 6 {
				break
			}
		}
		return strings.Join(parts, " ")
	}
	return string(in)
}

func clipText(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------- 技能目录索引（本地词面匹配，不调用模型） ----------

// skillIndex 是「技能名 → 描述词」的倒排式索引，用于方向漂移时挑候选技能。
// 索引按可见清单描述文本缓存：不同 Agent 的可见技能不同，但同一进程内描述是稳定的。
type skillIndex struct {
	entries []skillIndexEntry
}

type skillIndexEntry struct {
	name   string
	tokens []string
}

var skillIndexCache = struct {
	sync.Mutex
	m map[uint64]*skillIndex
}{m: map[uint64]*skillIndex{}}

// skillIndexFor 由 Skill 工具的描述（<available_skills> 清单）建立索引，带缓存。
func skillIndexFor(skillToolDesc string) *skillIndex {
	if strings.TrimSpace(skillToolDesc) == "" {
		return nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(skillToolDesc))
	key := h.Sum64()

	skillIndexCache.Lock()
	if idx, ok := skillIndexCache.m[key]; ok {
		skillIndexCache.Unlock()
		return idx
	}
	skillIndexCache.Unlock()

	entries := parseAvailableSkillsFromToolDesc(skillToolDesc)
	if len(entries) == 0 {
		return nil
	}
	idx := &skillIndex{entries: make([]skillIndexEntry, 0, len(entries))}
	for _, e := range entries {
		toks := skillMatchTokens(e.name + " " + e.description)
		if len(toks) == 0 {
			continue
		}
		idx.entries = append(idx.entries, skillIndexEntry{name: e.name, tokens: toks})
	}
	// 830 个技能每条几十个词，索引常驻内存可以接受；但按可见清单描述缓存，
	// 条数封顶，避免多 Agent 反复装配把缓存涨爆。
	skillIndexCache.Lock()
	if len(skillIndexCache.m) >= skillRecallIndexMax {
		skillIndexCache.m = map[uint64]*skillIndex{}
	}
	skillIndexCache.m[key] = idx
	skillIndexCache.Unlock()
	return idx
}

// best 返回命中词最多的技能名与命中数。倒排匹配是 O(技能数×词数) 的纯字符串操作，
// 只在「每 N 次工具调用」触发一次，不进模型调用路径。
func (x *skillIndex) best(text string) (string, int) {
	if x == nil || text == "" {
		return "", 0
	}
	bestName, bestScore := "", 0
	for _, e := range x.entries {
		score := 0
		for _, tok := range e.tokens {
			if len(tok) < 4 { // 过短的词（api/sql → 已保留；the/for → 被停用表滤掉）
				continue
			}
			if strings.Contains(text, tok) {
				score++
			}
		}
		if score > bestScore {
			bestName, bestScore = e.name, score
		}
	}
	return bestName, bestScore
}

// skillMatchTokens 把技能名与描述切成用于词面匹配的词：ASCII 按词边界、CJK 按 2-gram。
func skillMatchTokens(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range splitSkillTokens(strings.ToLower(s)) {
		if len(t) < 4 || skillRecallStop[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 24 {
			break
		}
	}
	return out
}

// skillRecallStop 是词面匹配里没有区分度的常见词（英文高频 + 技能描述套话）。
var skillRecallStop = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "when": true, "where": true,
	"which": true, "have": true, "your": true, "into": true, "used": true, "using": true,
	"use": true, "and": true, "the": true, "for": true, "not": true, "are": true,
	"skill": true, "skills": true, "step": true, "steps": true, "task": true, "tasks": true,
	"security": true, "include": true, "including": true, "example": true, "examples": true,
	"should": true, "must": true, "will": true, "also": true, "such": true, "whether": true,
	"following": true, "following_": true, "based": true, "ensure": true, "used_": true,
}
