package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// AI 自我进化沉淀（task-end & on-demand skill curator）。
//
// 与 server/skill_review_tools.go 的「缺口触发」互补：
//   - 缺口触发解决「模型反复想要一个不存在的技能」；
//   - 任务收尾沉淀解决「一个任务已经跑出了可复用的路径、证据和失败模式，
//     但任务结束后这些经验只留在该任务的图与会话里」。
//
// 本文件在三种时机收集任务产物并生成一份确定性的 Markdown 技能：
//  1. 任务进入终态（done / failed / timeout）——异步执行，不阻塞状态落库；
//  2. 用户删除任务——在级联删除之前同步执行，否则图/活动/证据已不存在；
//  3. 操作者或 Agent 手动触发（POST /api/skills/distill 或 distill_task_skill 工具）。
//
// 生成过程不调用 LLM：只从已落库的意图、事实、漏洞、复测记录、软件逆向样本与静态证据、
// 活动与工具调用账本中提取结构化的「有效路径 / 关键证据 / 复测验证 / 逆向证据 / 工具模式 / 失败止损」。
// 因此它是可复现、可审计的，也不依赖任务结束时 LLM 是否仍可用。
//
// 写入仍服从已有门控：
//   - skill_review_gate=true（默认）→ 先生成 pending 提议，人工审阅后落盘；
//   - skill_auto_distill_apply=true，或门控被显式关闭 → 直接落盘，并留下 applied 记录。
//
// 默认 auto_distill=true 只表示「自动生成提议」，不表示绕过审阅。

const (
	settingSkillAutoDistill      = "skill_auto_distill"
	settingSkillAutoDistillApply = "skill_auto_distill_apply"

	curatorMaxActivities  = 3000
	curatorMaxIntents     = 300
	curatorMaxFacts       = 200
	curatorMaxFindings    = 100
	curatorMaxGoals       = 100
	curatorMaxHints       = 100
	curatorMaxREEvidences = 40
	curatorMaxRetests     = 12
)

// ErrTaskNotSubstantive indicates that a task has not accumulated enough
// structured artifacts to produce a reusable skill.
var ErrTaskNotSubstantive = errors.New("该任务尚无足够的结构化产出（需至少 1 个漏洞、2 条事实、2 个意图、复测结论或逆向证据），暂无法沉淀复盘技能")

// SkillAutoDistillEnabled reports whether task-end/deletion distillation is on.
// Missing settings default to true: the feature only stages a proposal by default,
// so enabling generation does not bypass the review gate.
func (s *Server) SkillAutoDistillEnabled() bool {
	if s == nil || s.m == nil || s.m.pg == nil {
		return false
	}
	return s.m.pg.GetBool(settingSkillAutoDistill, true)
}

// SetSkillAutoDistill toggles automatic task-end distillation.
func (s *Server) SetSkillAutoDistill(on bool) error {
	if s == nil || s.m == nil || s.m.pg == nil {
		return errors.New("数据库不可用，无法切换自动沉淀")
	}
	return s.m.pg.SetBool(settingSkillAutoDistill, on)
}

// SkillAutoDistillApplyEnabled reports whether curator-generated skills are
// written directly. The review gate remains authoritative: turning the gate off
// (the user's explicit choice for all skill writes) also enables direct writes.
func (s *Server) SkillAutoDistillApplyEnabled() bool {
	if s == nil {
		return false
	}
	if !s.SkillReviewGateEnabled() {
		return true
	}
	if s.m == nil || s.m.pg == nil {
		return false
	}
	return s.m.pg.GetBool(settingSkillAutoDistillApply, false)
}

// SetSkillAutoDistillApply toggles direct writes for curator-generated skills.
func (s *Server) SetSkillAutoDistillApply(on bool) error {
	if s == nil || s.m == nil || s.m.pg == nil {
		return errors.New("数据库不可用，无法切换自动沉淀直写")
	}
	return s.m.pg.SetBool(settingSkillAutoDistillApply, on)
}

// installSkillCuratorHooks wires Manager's lifecycle hooks to this Server. The
// hooks read the current settings on every invocation, so toggles take effect
// without a restart.
func (s *Server) installSkillCuratorHooks() {
	if s == nil || s.m == nil {
		return
	}
	s.m.SetTaskSkillHooks(s.onTaskTerminalSkill, s.onTaskDeletedSkill)
}

func (s *Server) onTaskTerminalSkill(taskID, status string) {
	if !s.SkillAutoDistillEnabled() {
		return
	}
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	task, err := s.m.pg.GetTask(id)
	if err != nil || task == nil {
		if err != nil {
			log.Printf("[skills] 任务 %s 终态沉淀读取失败: %v", taskID, err)
		}
		return
	}
	if err := s.depositTaskSkillOnce(ctx, task, "terminal:"+status); err != nil {
		log.Printf("[skills] 任务 %s 终态沉淀失败: %v", taskID, err)
	}
}

func (s *Server) onTaskDeletedSkill(taskID string, snapshot *db.Task) {
	if !s.SkillAutoDistillEnabled() || snapshot == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.depositTaskSkillOnce(ctx, snapshot, "task-deleted"); err != nil {
		log.Printf("[skills] 任务 %s 删除前沉淀失败: %v", taskID, err)
	}
}

// DistillTaskSkill manually distills one task into a skill proposal (and applies
// it to disk if applyOverride is true, or when nil if SkillAutoDistillApplyEnabled
// is true). Unlike background lifecycle hooks, it returns ErrTaskNotSubstantive
// when the task has no reusable artifacts yet.
func (s *Server) DistillTaskSkill(ctx context.Context, taskID int64, applyOverride *bool) (*db.SkillProposal, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return nil, errors.New("数据库不可用")
	}
	if taskID <= 0 {
		return nil, errors.New("task_id 必须为正整数")
	}
	task, err := s.m.pg.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("任务 #%d 不存在", taskID)
	}
	return s.distillTask(ctx, task, "manual", applyOverride, true)
}

// curatorRetestRecord captures a completed vulnerability retest conclusion.
type curatorRetestRecord struct {
	FindingName string
	VulnClass   string
	Verdict     string
	Summary     string
	Evidence    string
	Notes       string
}

// curatorData is the bounded, read-only snapshot used to compose one skill.
type curatorData struct {
	Task        *db.Task
	Goals       []*db.Node
	Intents     []*db.Node
	Facts       []*db.Node
	Findings    []*db.Node
	Hints       []*db.Node
	Activity    []db.Activity
	Skills      []db.SkillStat
	RESamples   []db.RESample
	RESessions  []db.RESession
	REEvidences []db.REEvidence
	Retests     []curatorRetestRecord
	// Yields maps intent id → fact node ids produced by that intent.
	Yields map[int64][]int64
	// FindingsByIntent maps intent id → confirmed finding count.
	FindingsByIntent map[int64]int
}

func (s *Server) collectCuratorData(ctx context.Context, task *db.Task) *curatorData {
	d := &curatorData{
		Task:             task,
		Yields:           map[int64][]int64{},
		FindingsByIntent: map[int64]int{},
	}
	if task == nil || s.m == nil || s.m.pg == nil {
		return d
	}
	if ctx.Err() != nil {
		return d
	}

	if task.ExplorationID > 0 {
		store := s.m.pg.Exploration(task.ExplorationID)

		if nodes, err := store.ListByKind(db.KindGoal, curatorMaxGoals); err == nil {
			d.Goals = nodes
		}
		if nodes, err := store.ListByKind(db.KindIntent, curatorMaxIntents); err == nil {
			d.Intents = nodes
		}
		if nodes, err := store.ListByKind(db.KindFact, curatorMaxFacts); err == nil {
			d.Facts = nodes
		}
		if nodes, err := store.ListByKind(db.KindFinding, curatorMaxFindings); err == nil {
			d.Findings = nodes
		}
		if nodes, err := store.ListByKind(db.KindHint, curatorMaxHints); err == nil {
			d.Hints = nodes
		}

		cursor := int64(0)
		for len(d.Activity) < curatorMaxActivities && ctx.Err() == nil {
			items, next, err := store.ActivityList(nil, cursor, 500)
			if err != nil {
				log.Printf("[skills] 任务 %d 活动读取失败: %v", task.ID, err)
				break
			}
			if len(items) == 0 {
				break
			}
			d.Activity = append(d.Activity, items...)
			if next <= cursor {
				break
			}
			cursor = next
		}

		if mapping, err := store.FindingIntents(); err == nil {
			for _, intentNode := range mapping {
				d.FindingsByIntent[intentNode]++
			}
		}
		for _, intent := range d.Intents {
			if intent == nil {
				continue
			}
			if ids, err := store.FactsYielded(intent.ID); err == nil {
				d.Yields[intent.ID] = ids
			}
		}
	}

	if task.ID > 0 {
		if stats, err := s.m.pg.SkillCallsByTask(task.ID); err == nil {
			d.Skills = stats
		}
		if samples, err := s.m.pg.RESamples(task.ID); err == nil {
			d.RESamples = samples
		}
		if sessions, err := s.m.pg.RESessions(task.ID); err == nil {
			d.RESessions = sessions
			for _, sess := range sessions {
				if len(d.REEvidences) >= curatorMaxREEvidences {
					break
				}
				evs, err := s.m.pg.REEvidenceList(task.ID, sess.ID, 0)
				if err != nil {
					continue
				}
				for _, ev := range evs {
					d.REEvidences = append(d.REEvidences, ev)
					if len(d.REEvidences) >= curatorMaxREEvidences {
						break
					}
				}
			}
		}
		if dbFindings, _, err := s.m.pg.ListFindingsPage(db.FindingFilter{TaskID: strconv.FormatInt(task.ID, 10)}, 1, 20); err == nil {
			for _, f := range dbFindings {
				if f == nil || len(d.Retests) >= curatorMaxRetests {
					break
				}
				retests, err := s.m.pg.ListFindingRetests(f.ID)
				if err != nil {
					continue
				}
				fName := strings.TrimSpace(f.Name)
				if fName == "" {
					fName = strings.TrimSpace(f.VulnClass)
				}
				for _, rt := range retests {
					if rt == nil || (rt.Status != "done" && strings.TrimSpace(rt.Verdict) == "") {
						continue
					}
					d.Retests = append(d.Retests, curatorRetestRecord{
						FindingName: fName,
						VulnClass:   f.VulnClass,
						Verdict:     rt.Verdict,
						Summary:     rt.Summary,
						Evidence:    rt.Evidence,
						Notes:       rt.Notes,
					})
					if len(d.Retests) >= curatorMaxRetests {
						break
					}
				}
			}
		}
	}
	return d
}

// curatorSubstantive filters out tasks too thin to produce reusable knowledge.
func curatorSubstantive(d *curatorData) bool {
	if d == nil || d.Task == nil {
		return false
	}
	if len(d.Findings) > 0 || len(d.Retests) > 0 {
		return true
	}
	if len(d.Facts) >= 2 || len(d.Intents) >= 2 {
		return true
	}
	if len(d.REEvidences) >= 2 || (len(d.RESamples) > 0 && len(d.REEvidences) >= 1) {
		return true
	}
	return false
}

// curatorSkillName is ASCII-only (the skill name contract): case-<domain>-<id>.
func curatorSkillName(t *db.Task) string {
	domain := "task"
	switch t.AnalysisDomain {
	case db.AnalysisDomainSoftwareRE:
		domain = "re"
	case "web_security", "":
		domain = "web"
	}
	name := fmt.Sprintf("case-%s-%d", domain, t.ID)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func shortContentHash(s string) string {
	// Reuse the proposal id helper's hash shape but keep a short suffix for versioned
	// names. skillProposalID is content-addressed and already collision-resistant.
	return skillProposalID("curator", "", s)[:6]
}

func (s *Server) depositTaskSkill(ctx context.Context, task *db.Task, trigger string) error {
	_, err := s.distillTask(ctx, task, trigger, nil, false)
	return err
}

func (s *Server) distillTask(ctx context.Context, task *db.Task, trigger string, applyOverride *bool, manual bool) (*db.SkillProposal, error) {
	if s == nil || s.m == nil || s.m.pg == nil || task == nil {
		return nil, nil
	}
	if strings.TrimSpace(s.skillDir) == "" {
		if manual {
			return nil, errors.New("技能目录未配置")
		}
		return nil, nil
	}
	d := s.collectCuratorData(ctx, task)
	if !curatorSubstantive(d) {
		if manual {
			return nil, ErrTaskNotSubstantive
		}
		return nil, nil
	}
	name := curatorSkillName(task)
	doc := buildCuratorSkillDocument(d)
	if len([]rune(doc)) > 24000 {
		return nil, fmt.Errorf("生成内容 %d 字符，超过技能正文上限", len([]rune(doc)))
	}

	// Never clobber an existing skill. An identical file means this task was already
	// distilled (terminal then deletion, or a restart retry). Different content gets a
	// deterministic version suffix so a later, richer snapshot is preserved.
	path := filepath.Join(s.skillDir, name, "SKILL.md")
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == doc {
			if !manual {
				return nil, nil
			}
		} else {
			name = name + "-" + shortContentHash(doc)
			path = filepath.Join(s.skillDir, name, "SKILL.md")
			if existingVer, err := os.ReadFile(path); err == nil && string(existingVer) == doc && !manual {
				return nil, nil
			}
		}
	}

	evidence := fmt.Sprintf(
		"task=%d, domain=%s, trigger=%s, status=%s, intents=%d, facts=%d, findings=%d, retests=%d, re_evidences=%d, activities=%d",
		task.ID, task.AnalysisDomain, trigger, task.Status,
		len(d.Intents), len(d.Facts), len(d.Findings), len(d.Retests), len(d.REEvidences), len(d.Activity),
	)
	apply := s.SkillAutoDistillApplyEnabled()
	if applyOverride != nil {
		if !s.SkillReviewGateEnabled() {
			apply = true
		} else {
			apply = *applyOverride
		}
	}
	p, err := s.persistCuratorSkill(ctx, name, doc, evidence, apply)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	log.Printf("[skills] 任务 %d 自动沉淀：skill=%s id=%s status=%s apply=%t trigger=%s",
		task.ID, p.Skill, p.ID, p.Status, apply, trigger)
	return p, nil
}

// persistCuratorSkill stages the proposal first (audit trail), then optionally
// writes and marks it applied. A failed write leaves the proposal pending so the
// operator can retry from the review queue.
func (s *Server) persistCuratorSkill(ctx context.Context, name, doc, evidence string, apply bool) (*db.SkillProposal, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return nil, errors.New("数据库不可用")
	}
	p, err := s.m.pg.ProposeSkill(ctx, db.SkillProposal{
		ID:       skillProposalID(name, "", doc),
		Skill:    name,
		Action:   "create",
		Content:  doc,
		Origin:   "curator",
		Evidence: evidence,
	})
	if err != nil {
		return nil, err
	}
	if !apply || p.Status != db.SkillProposalPending {
		return p, nil
	}
	if err := s.applySkillFile(name, "", doc); err != nil {
		return p, fmt.Errorf("自动沉淀写盘失败，提议仍为 pending 可重试: %w", err)
	}
	return s.m.pg.ReviewSkillProposal(ctx, p.ID, db.SkillProposalApplied, "自动沉淀：任务完成或删除后由系统生成，可审计")
}

// ---- deterministic document composition ----

type curatorToolStat struct {
	Name    string
	Calls   int
	Errors  int
	Tripped bool
	Samples []string
}

func buildCuratorSkillDocument(d *curatorData) string {
	name := curatorSkillName(d.Task)
	description := curatorDescription(d)
	var b strings.Builder

	title := curatorTaskTitle(d.Task)
	goal := curatorInline(d.Task.Goal, 500)
	if goal == "" {
		goal = "（任务未填写目标）"
	}
	fmt.Fprintf(&b, "# %s · 任务复盘手册\n\n", title)
	fmt.Fprintf(&b, "> 来源：ARTEX 任务 #%d（%s）完成后自动沉淀。内容来自该任务的意图、事实、漏洞、活动与工具账本；复用前必须重新核对授权范围、目标版本与环境差异。\n\n", d.Task.ID, curatorDomainLabel(d.Task.AnalysisDomain))

	b.WriteString("## 何时使用\n\n")
	fmt.Fprintf(&b, "- 目标相近：%s\n", goal)
	if keywords := curatorKeywords(d); keywords != "" {
		fmt.Fprintf(&b, "- 命中关键词/组件/漏洞类型：%s\n", keywords)
	}
	fmt.Fprintf(&b, "- 分析域：%s\n", curatorDomainLabel(d.Task.AnalysisDomain))
	b.WriteString("- 当你面对同类目标、同类受阻信号，或准备重复一条已知路径时，先加载本手册再行动。\n\n")

	b.WriteString("## 已验证的有效路径\n\n")
	done := curatorDoneIntents(d)
	if len(done) == 0 {
		b.WriteString("本任务没有进入 done 的意图；以下内容主要来自受阻分析与失败模式，复用时应先补足验证。\n\n")
	} else {
		for i, intent := range done {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "%d. %s", i+1, curatorIntentSummary(intent))
			if facts := len(d.Yields[intent.ID]); facts > 0 {
				fmt.Fprintf(&b, "（产出事实 %d 条", facts)
				if findings := d.FindingsByIntent[intent.ID]; findings > 0 {
					fmt.Fprintf(&b, "、漏洞 %d 条", findings)
				}
				b.WriteString("）")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## 关键事实与证据\n\n")
	facts := curatorRankedFacts(d)
	if len(facts) == 0 {
		b.WriteString("无可用的结构化事实记录。\n\n")
	} else {
		for i, fact := range facts {
			if i >= 12 {
				break
			}
			m := curatorNodeMap(fact)
			summary := curatorInline(curatorFirstString(m, "summary", "text", "name"), 400)
			conf := curatorFirstString(m, "confidence")
			label := "待核对"
			switch conf {
			case "observed":
				label = "观察"
			case "inferred":
				label = "推测"
			}
			fmt.Fprintf(&b, "- [%s] %s", label, summary)
			if ev := curatorInline(curatorEvidence(m), 240); ev != "" {
				fmt.Fprintf(&b, "；证据：%s", ev)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(d.Findings) > 0 {
		b.WriteString("## 已确认漏洞与可利用点\n\n")
		findings := append([]*db.Node(nil), d.Findings...)
		sort.SliceStable(findings, func(i, j int) bool {
			return curatorSeverityRank(curatorFindingSeverity(findings[i])) <
				curatorSeverityRank(curatorFindingSeverity(findings[j]))
		})
		for i, finding := range findings {
			if i >= 12 {
				break
			}
			m := curatorNodeMap(finding)
			sev := strings.ToUpper(curatorFindingSeverity(finding))
			if sev == "" {
				sev = "INFO"
			}
			name := curatorInline(curatorFirstString(m, "name", "vulnclass", "summary"), 200)
			vuln := curatorInline(curatorFirstString(m, "vulnclass"), 100)
			summary := curatorInline(curatorFirstString(m, "summary"), 400)
			fmt.Fprintf(&b, "- [%s] %s", sev, name)
			if vuln != "" && vuln != name {
				fmt.Fprintf(&b, "（%s）", vuln)
			}
			if summary != "" && summary != name {
				fmt.Fprintf(&b, "：%s", summary)
			}
			if ev := curatorInline(curatorEvidence(m), 240); ev != "" {
				fmt.Fprintf(&b, "；证据：%s", ev)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(d.Retests) > 0 {
		b.WriteString("## 漏洞复测与修复验证\n\n")
		for i, rt := range d.Retests {
			if i >= 8 {
				break
			}
			fName := curatorInline(rt.FindingName, 160)
			if fName == "" {
				fName = "未命名漏洞"
			}
			verdictLabel := curatorRetestVerdictLabel(rt.Verdict)
			fmt.Fprintf(&b, "- [%s] %s", verdictLabel, fName)
			if summary := curatorInline(rt.Summary, 260); summary != "" {
				fmt.Fprintf(&b, "：%s", summary)
			}
			if notes := curatorInline(rt.Notes, 160); notes != "" {
				fmt.Fprintf(&b, "（复测条件：%s）", notes)
			}
			if ev := curatorInline(rt.Evidence, 200); ev != "" {
				fmt.Fprintf(&b, "；验证证据：%s", ev)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(d.RESamples) > 0 || len(d.REEvidences) > 0 {
		b.WriteString("## 逆向样本与静态分析证据\n\n")
		for i, sp := range d.RESamples {
			if i >= 6 {
				break
			}
			shaShort := sp.SHA256
			if len(shaShort) > 12 {
				shaShort = shaShort[:12]
			}
			fmt.Fprintf(&b, "- 样本 `%s`（SHA256 前缀 `%s`，%d 字节）\n",
				curatorInline(sp.Name, 120), curatorInline(shaShort, 20), sp.Size)
		}
		for i, ev := range d.REEvidences {
			if i >= 10 {
				break
			}
			status := "成功"
			if ev.IsError {
				status = "异常"
			}
			op := curatorInline(ev.Operation, 60)
			if op == "" {
				op = curatorInline(ev.Tool, 60)
			}
			fmt.Fprintf(&b, "- 静态操作 `%s`（工具 `%s`，%s）", op, curatorInline(ev.Tool, 80), status)
			if diag := curatorInline(ev.Diagnostic, 200); diag != "" {
				fmt.Fprintf(&b, "：%s", diag)
			} else if out := curatorInline(ev.Output, 200); out != "" {
				fmt.Fprintf(&b, "；摘要：%s", out)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## 工具与命令模式\n\n")
	tools, tripped := curatorToolStats(d.Activity)
	if len(tools) == 0 {
		b.WriteString("没有可归纳的工具调用记录。\n\n")
	} else {
		for i, t := range tools {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&b, "- `%s`：调用 %d 次，失败 %d 次", t.Name, t.Calls, t.Errors)
			if t.Tripped {
				b.WriteString("，触发过 3-Strike 同键止损")
			}
			if len(t.Samples) > 0 {
				fmt.Fprintf(&b, "；示例：%s", strings.Join(t.Samples, "；"))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## 失败、误区与止损\n\n")
	failures := 0
	for _, intent := range d.Intents {
		state := strings.ToLower(strings.TrimSpace(intent.State))
		if state == "done" || state == "" || state == "open" || state == "running" {
			continue
		}
		reason := curatorIntentReason(intent)
		if reason == "" {
			reason = "无显式原因，复用前先确认前置条件"
		}
		fmt.Fprintf(&b, "- 意图「%s」状态 %s：%s\n", curatorIntentSummary(intent), state, curatorInline(reason, 300))
		failures++
		if failures >= 10 {
			break
		}
	}
	for i, t := range tools {
		if i >= 8 || t.Errors == 0 {
			continue
		}
		fmt.Fprintf(&b, "- `%s` 出现 %d 次工具错误：先核对参数、授权、网络与目标状态，不要原样重试。\n", t.Name, t.Errors)
		failures++
	}
	if tripped {
		b.WriteString("- 同一请求上下文连续三次相同负反馈会触发 3-Strike 止损：必须换资源/参数/路径，或明确记录阻塞，禁止机械重放。\n")
		failures++
	}
	if failures == 0 {
		b.WriteString("本任务未记录到显式失败或止损信号；仍可能因目标差异而失效。\n")
	}
	b.WriteString("\n")

	if hints := curatorHints(d); len(hints) > 0 {
		b.WriteString("## 人工提示与决策\n\n")
		for i, h := range hints {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", h)
		}
		b.WriteString("\n")
	}

	if len(d.Skills) > 0 {
		b.WriteString("## 本任务实际加载过的技能\n\n")
		for i, s := range d.Skills {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&b, "- `%s`：%d 次\n", s.Skill, s.Calls)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 复用步骤\n\n")
	b.WriteString("1. 先确认目标授权、范围与版本；不要把本任务的目标、凭据或结论直接套到新目标。\n")
	b.WriteString("2. 按「已验证的有效路径」复现最小步骤，优先使用当前真实挂载的工具与技能。\n")
	b.WriteString("3. 命中「失败、误区与止损」中的信号时立即切换策略，禁止重复无增量的同键调用。\n")
	b.WriteString("4. 任何结论都要用可追溯证据（请求/响应、哈希、工具输出、PoC）复核后，再写入事实或漏洞。\n\n")

	b.WriteString("## 适用边界与验证要求\n\n")
	b.WriteString("- 本手册是任务经验摘要，不是漏洞证明，也不是对目标当前状态的保证。\n")
	b.WriteString("- 观察（observed）与推测（inferred）必须分开使用；推测不得当作已验证行为。\n")
	b.WriteString("- 凭据、令牌、Cookie 等敏感值在沉淀时已按模式脱敏；原文仍以任务证据库为准。\n")
	b.WriteString("- 环境、版本、WAF/网关策略、账号权限变化都可能让旧路径失效；失效时记录新事实并重新规划。\n")

	return buildSkillDocument(name, description, b.String())
}

func curatorRetestVerdictLabel(verdict string) string {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "reproduced", "vulnerable", "still_vulnerable":
		return "仍可复现"
	case "fixed", "remediated", "resolved":
		return "已修复"
	case "inconclusive", "unknown":
		return "无法确认"
	default:
		if v := strings.TrimSpace(verdict); v != "" {
			return v
		}
		return "已复测"
	}
}

func curatorDescription(d *curatorData) string {
	title := curatorTaskTitle(d.Task)
	goal := curatorInline(d.Task.Goal, 260)
	if goal == "" {
		goal = "未填写目标"
	}
	keywords := curatorKeywords(d)
	if keywords != "" {
		goal += "；关键词：" + keywords
	}
	desc := fmt.Sprintf(
		"任务复盘《%s》：%s。适用：%s；包含已验证路径、工具模式、失败止损与证据索引。",
		title, goal, curatorDomainLabel(d.Task.AnalysisDomain),
	)
	desc = curatorInline(desc, 280)
	return curatorYAMLScalar(desc)
}

func curatorTaskTitle(t *db.Task) string {
	if t == nil {
		return "未命名任务"
	}
	title := strings.TrimSpace(t.Name)
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(t.Description, "\n", 2)[0])
	}
	if title == "" {
		title = fmt.Sprintf("任务 #%d", t.ID)
	}
	return curatorInline(title, 120)
}

func curatorDomainLabel(domain string) string {
	switch domain {
	case db.AnalysisDomainSoftwareRE:
		return "软件逆向"
	case "web_security", "":
		return "Web 安全"
	default:
		return domain
	}
}

func curatorKeywords(d *curatorData) string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = curatorInline(s, 60)
		if s == "" || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	for _, f := range d.Findings {
		m := curatorNodeMap(f)
		add(curatorFirstString(m, "vulnclass"))
		add(curatorFirstString(m, "name"))
	}
	for _, rt := range d.Retests {
		add(rt.VulnClass)
		add(rt.FindingName)
	}
	for _, sp := range d.RESamples {
		add(sp.Name)
	}
	for _, ev := range d.REEvidences {
		add(ev.Operation)
	}
	for _, intent := range curatorDoneIntents(d) {
		add(curatorIntentSummary(intent))
	}
	// Keep description short and stable: stop after six signals.
	if len(out) > 6 {
		out = out[:6]
	}
	return strings.Join(out, "、")
}

func curatorDoneIntents(d *curatorData) []*db.Node {
	var out []*db.Node
	for _, intent := range d.Intents {
		if strings.EqualFold(strings.TrimSpace(intent.State), "done") {
			out = append(out, intent)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func curatorRankedFacts(d *curatorData) []*db.Node {
	out := make([]*db.Node, 0, len(d.Facts))
	for _, f := range d.Facts {
		if f == nil || f.State == db.StateOrigin {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return curatorConfidenceRank(curatorNodeMap(out[i])) < curatorConfidenceRank(curatorNodeMap(out[j]))
	})
	return out
}

func curatorConfidenceRank(m map[string]any) int {
	switch strings.ToLower(curatorFirstString(m, "confidence")) {
	case "observed":
		return 0
	case "inferred":
		return 2
	default:
		return 1
	}
}

func curatorHints(d *curatorData) []string {
	var out []string
	for _, h := range d.Hints {
		m := curatorNodeMap(h)
		text := curatorInline(curatorFirstString(m, "text", "summary"), 300)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func curatorToolStats(activity []db.Activity) ([]curatorToolStat, bool) {
	byName := map[string]*curatorToolStat{}
	tripped := false
	for _, a := range activity {
		name := strings.TrimSpace(a.Tool)
		switch a.Kind {
		case "tool_use", "tool_call":
			if name == "" {
				continue
			}
			st := byName[name]
			if st == nil {
				st = &curatorToolStat{Name: name}
				byName[name] = st
			}
			st.Calls++
			if sample := curatorInline(curatorToolSample(a), 160); sample != "" && len(st.Samples) < 2 {
				dup := false
				for _, existing := range st.Samples {
					if existing == sample {
						dup = true
						break
					}
				}
				if !dup {
					st.Samples = append(st.Samples, sample)
				}
			}
		case "tool_result":
			if name == "" {
				continue
			}
			st := byName[name]
			if st == nil {
				st = &curatorToolStat{Name: name}
				byName[name] = st
			}
			if a.IsError {
				st.Errors++
			}
			if curatorLoopTrip(a.Summary) {
				st.Tripped = true
				tripped = true
			}
		}
		if !tripped && curatorLoopTrip(a.Summary) {
			tripped = true
		}
	}
	out := make([]curatorToolStat, 0, len(byName))
	for _, st := range byName {
		out = append(out, *st)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		return out[i].Name < out[j].Name
	})
	return out, tripped
}

func curatorLoopTrip(summary string) bool {
	return strings.Contains(summary, "专家战术熔断提醒") ||
		strings.Contains(summary, "3-Strike") ||
		strings.Contains(summary, "三次相同负反馈") ||
		strings.Contains(summary, "三次相同")
}

func curatorToolSample(a db.Activity) string {
	s := strings.TrimSpace(a.Summary)
	if s == "" {
		return ""
	}
	if a.Tool != "" && strings.HasPrefix(s, a.Tool) {
		s = strings.TrimSpace(strings.TrimPrefix(s, a.Tool))
	}
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			for _, key := range []string{"command", "url", "target_url", "file_path", "path", "query", "pattern", "host", "operation", "mode"} {
				if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
					return v
				}
			}
		}
	}
	return s
}

func curatorIntentSummary(n *db.Node) string {
	if n == nil {
		return "未命名意图"
	}
	m := curatorNodeMap(n)
	s := curatorFirstString(m, "summary", "text", "name", "title")
	if s == "" {
		s = string(n.Payload)
	}
	s = curatorInline(s, 280)
	if s == "" {
		return fmt.Sprintf("意图 #%d", n.ID)
	}
	return s
}

func curatorIntentReason(n *db.Node) string {
	if n == nil {
		return ""
	}
	reason := strings.TrimSpace(n.BlockedReason)
	if reason == "" {
		reason = strings.TrimSpace(n.DeleteReason)
	}
	if reason == "" {
		m := curatorNodeMap(n)
		reason = curatorFirstString(m, "reason", "blocked_reason", "error", "delete_reason")
	}
	return reason
}

func curatorFindingSeverity(n *db.Node) string {
	if n == nil {
		return ""
	}
	return curatorFirstString(curatorNodeMap(n), "severity")
}

func curatorSeverityRank(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

func curatorNodeMap(n *db.Node) map[string]any {
	if n == nil || len(n.Payload) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(n.Payload, &m) != nil {
		return nil
	}
	return m
}

func curatorFirstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func curatorEvidence(m map[string]any) string {
	if m == nil {
		return ""
	}
	switch v := m["evidence"].(type) {
	case string:
		return v
	case map[string]any:
		return curatorFirstString(v, "poc", "summary", "detail", "request", "output")
	}
	return ""
}

var (
	curatorPEMRe       = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	curatorURLAuthRe   = regexp.MustCompile(`(?i)\b(https?://[^/\s:@]+):([^@\s/]+)@`)
	curatorCookieHdrRe = regexp.MustCompile(`(?i)\b(set-cookie|cookie)\s*:\s*[^\r\n'"` + "`" + `]+`)
	curatorSecretKVRe  = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|apikey|authorization|cookie|session(?:id)?)\b\s*[:=]\s*[^\s,;，；]+`)
	curatorBearerRe    = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`)
	curatorTokenRe     = regexp.MustCompile(`\b(?:sk|pk|ak|ssk|ghp|gho|ghu|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b`)
	curatorJWTRe       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
)

// curatorRedact removes the highest-risk credential shapes before text enters a
// skill file. It is deliberately pattern-based and bounded: skills must never
// carry raw passwords, bearer tokens, cookies, URL basic-auth or common API-key shapes.
func curatorRedact(s string) string {
	s = curatorPEMRe.ReplaceAllString(s, "[私钥已脱敏]")
	s = curatorURLAuthRe.ReplaceAllString(s, "$1:[已脱敏]@")
	s = curatorCookieHdrRe.ReplaceAllString(s, "$1: [已脱敏]")
	// Bearer/JWT/token shapes before generic key=value so multi-part headers are masked cleanly.
	s = curatorBearerRe.ReplaceAllString(s, "$1 [已脱敏]")
	s = curatorJWTRe.ReplaceAllString(s, "[已脱敏]")
	s = curatorTokenRe.ReplaceAllString(s, "[已脱敏]")
	s = curatorSecretKVRe.ReplaceAllString(s, "$1=[已脱敏]")
	return s
}

func curatorInline(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = curatorRedact(s)
	s = strings.ReplaceAll(s, "```", "'''")
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 {
		r := []rune(s)
		if len(r) > max {
			s = string(r[:max]) + "…"
		}
	}
	return s
}

func curatorYAMLScalar(s string) string {
	s = strings.ReplaceAll(s, ":", "：")
	s = strings.ReplaceAll(s, "#", "＃")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// curatorMu prevents a terminal hook and a deletion hook from composing/writing the
// same task twice in one process. The proposal id is content-addressed, so the
// database remains the final idempotency guard across restarts.
var curatorMu sync.Mutex
var curatorInFlight = map[int64]bool{}

func (s *Server) depositTaskSkillOnce(ctx context.Context, task *db.Task, trigger string) error {
	if task == nil {
		return nil
	}
	curatorMu.Lock()
	if curatorInFlight[task.ID] {
		curatorMu.Unlock()
		return nil
	}
	curatorInFlight[task.ID] = true
	curatorMu.Unlock()
	defer func() {
		curatorMu.Lock()
		delete(curatorInFlight, task.ID)
		curatorMu.Unlock()
	}()
	return s.depositTaskSkill(ctx, task, trigger)
}
