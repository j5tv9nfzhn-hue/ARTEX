package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/fsutil"
	"github.com/Autumn-27/artex/stageerr"
	actool "github.com/Autumn-27/norma/tool"
)

// 技能沉淀的门控与审阅。
//
// ── 为什么要门控 ─────────────────────────────────────────────────────────────
//
// create_skill / update_skill_file 原先直接落盘。技能一旦写入就进入之后**所有**
// 会话的技能索引，而一个描述含糊或流程错误的技能不是一次性的浪费：它会被反复
// 加载，把错误流程固化下来，而且没有任何可回溯的记录能回答「这条技能是什么时候、
// 依据什么被启用的」。
//
// 所以默认开启门控：Agent 产出的是**提议**，人工审阅后才写盘。这与 Hermes 的
// write_approval 是同一个模型，区别只在默认方向——这里默认就是安全的那一侧。
//
// 关掉它（SetSkillReviewGate(false)）是允许的，但那是用户的显式选择，不是默认值。

// settingSkillReviewGate 控制 Agent 写技能是否需要人工审阅。
//
// 用设置存储而不是 config 文件：门控是一个需要在运行中调整的开关——审阅积压了
// 想临时放行、或者批量批完之后想收紧，都不该要求重启并改配置文件。既有代码里
// settingLLMPoolOn、settingConstraintsInjectPlanner 都是这个用法。
const settingSkillReviewGate = "skill_review_gate"

// skillGateOverride 强制门控状态，nil 表示跟随数据库设置。
//
// 存在的理由不只是测试便利：开关存在数据库里，而数据库本身可能不可用
// （首次启动、只读部署、只想检查技能文件的临时会话）。那时需要一条不依赖
// 数据库的路径来表达「这次不审阅」，否则就只能靠"数据库不可用时默认开启"
// 把这类会话一并堵死。
func (s *Server) setSkillGateOverride(on bool) { s.skillGateOverride = &on }

// SkillReviewGateEnabled 报告门控是否开启。
//
// 取不到开关时（Server 或数据库不可用）按**开启**处理：拿不到状态时应当
// 偏向更安全的一侧，而不是默默放行。
func (s *Server) SkillReviewGateEnabled() bool {
	if s == nil {
		return true
	}
	if s.skillGateOverride != nil {
		return *s.skillGateOverride
	}
	if s.m == nil || s.m.pg == nil {
		return true
	}
	return s.m.pg.GetBool(settingSkillReviewGate, true)
}

// SetSkillReviewGate 设置门控开关。
func (s *Server) SetSkillReviewGate(on bool) error {
	if s == nil || s.m == nil || s.m.pg == nil {
		return fmt.Errorf("数据库不可用，无法切换技能审阅门控")
	}
	return s.m.pg.SetBool(settingSkillReviewGate, on)
}

// skillProposalID 由内容派生，用于幂等。
//
// 同一份提议重复提交（例如一次会话重试、或 Agent 在两轮里提出完全相同的内容）
// 会得到同一个 id，ProposeSkill 的 ON CONFLICT DO NOTHING 让它只留一条。审阅者
// 因此不会看到同一份内容的两个副本，也不需要自己去辨认哪些是重复的。
//
// 用内容而不是时间戳，是为了让「重复提交」这件事在数据层就不可区分——审阅记录
// 因此可信：每一行都对应一次真实的、内容不同的提议。
func skillProposalID(skill, file, content string) string {
	sum := sha256.Sum256([]byte(skill + "\x00" + file + "\x00" + content))
	return hex.EncodeToString(sum[:])[:24]
}

// buildSkillDocument 拼出完整的 SKILL.md 内容（含 frontmatter）。
//
// 抽取出来是因为两条路径需要同一份产物：直接写盘时写它，门控时把它**原样**存进
// 提议。审阅者必须看到落地后的完整文件——存一份半成品再让人脑补拼装，审阅就
// 失去意义了。
func buildSkillDocument(name, description, instructions string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "description: %s\n", description)
	b.WriteString("---\n\n")
	if body := strings.TrimSpace(instructions); body != "" {
		// 真实内容取代骨架，而不是附加在骨架后面：写出了实际步骤的 Agent
		// 不应该同时收到四个空章节让它去填。
		b.WriteString(body)
		b.WriteString("\n")
	} else {
		b.WriteString(skillTemplate(name, description))
	}
	return b.String()
}

// applySkillFile 把提议内容写入磁盘。
//
// 与 create_skill 原逻辑一致，但**不在失败时删除目录**：删除是 create_skill
// 需要的补偿（它刚建了目录），而这里的目录可能早就存在、里面可能已有其它文件，
// 删掉会连带毁掉不属于这次提议的内容。
func (s *Server) applySkillFile(skill, file, content string) error {
	rel := strings.TrimSpace(file)
	if rel == "" {
		rel = "SKILL.md"
	}
	clean, msg := skillRelPath(rel)
	if msg != "" {
		return stageerr.Msgf(stageerr.StageToolValidate, "非法路径: %s", msg)
	}
	full := filepath.Join(s.skillDir, skill, clean)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return stageerr.Wrap(stageerr.StagePersist, "创建技能子目录失败", err)
	}
	if err := fsutil.WriteFileAtomic(full, []byte(content), 0o644); err != nil {
		return stageerr.Wrap(stageerr.StagePersist, "写入技能文件失败", err)
	}
	if clean == "SKILL.md" {
		s.bindDefaultSkillVisibility(skill)
	}
	return nil
}

// stageSkillProposal 无条件把技能写入请求登记为待审阅提议。
func (s *Server) stageSkillProposal(ctx context.Context, skill, file, content, evidence, origin string) (actool.Result, error) {
	if s.m == nil || s.m.pg == nil {
		return actool.Errorf("技能审阅门控已开启，但数据库不可用，无法登记提议。" +
			"请先恢复数据库，或显式关闭门控（SkillReviewGateEnabled）后再写入。"), nil
	}
	p, err := s.m.pg.ProposeSkill(ctx, db.SkillProposal{
		ID:       skillProposalID(skill, file, content),
		Skill:    skill,
		File:     file,
		Action:   "create",
		Content:  content,
		Origin:   origin,
		Evidence: evidence,
	})
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	msg := fmt.Sprintf("skill proposal staged: %s (id=%s, status=%s)\n"+
		"尚未生效——需要人工审阅后才写入磁盘。用 list_skill_proposals 查看，"+
		"用 review_skill_proposal 批准或否决。", skill, p.ID, p.Status)
	if p.Lint != "" {
		msg += "\n审阅告警: " + p.Lint
	}
	return actool.Text(msg), nil
}

// proposeOrWrite 是技能写入的唯一入口。
//
// 门控关闭时直接落盘（原行为）；开启时登记提议。两条路径都返回一句话给 Agent，
// 但措辞必须说清发生了什么——否则 Agent 会以为技能已经可用了，转头就去加载一个
// 还不存在的技能。
func (s *Server) proposeOrWrite(ctx context.Context, skill, file, content, evidence, origin string) (actool.Result, error) {
	if !s.SkillReviewGateEnabled() {
		if err := s.applySkillFile(skill, file, content); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		target := skill
		if f := strings.TrimSpace(file); f != "" {
			target += "/" + f
		}
		return actool.Text("skill file written: " + target), nil
	}
	return s.stageSkillProposal(ctx, skill, file, content, evidence, origin)
}

// numParam 描述一个数值参数。
//
// 只有本文件用到（缺口阈值、列表条数），所以就地定义而不去动 orchestration.go
// 里那个公共的 strParam——为一个局部用途扩大公共面的改动不值当。
func numParam(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

// ---- 触发信号 ----

// toolListSkillGaps 让 Agent 看到「系统记录到的技能缺口」。
//
// 这是整套沉淀机制的入口，但不是自动的：Agent 需要自己判断某个缺口值不值得写
// 一条技能提案。自动生成的前提是系统已经知道该写什么——而它不知道，模型知道。
func (s *Server) toolListSkillGaps() actool.CoreTool {
	return wrTool("list_skill_gaps",
		"列出被反复请求但并不存在的技能（来自 skill_usage 的未命中记录）。"+
			"这是自主沉淀的触发信号：某条记录重复出现 N 次，说明模型稳定地需要一个还不存在的技能。",
		objSchema(map[string]any{
			"min_misses": numParam("最小未命中次数，默认 3"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				MinMisses int `json:"min_misses"`
			}
			_ = json.Unmarshal(in, &a)
			text, err := s.gapReport(ctx, a.MinMisses)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(text), nil
		})
}

// gapReport 汇总缺口清单。
//
// 单独拆出来而不是留在工具闭包里：这里做的是唯一一处「数据层看不见的东西」——
// skills/ 目录里已有的技能不算缺口。数据层不知道部署布局，所以这个判断必须在
// server 侧做，而它也正是一条需要测试盯住的行为。
func (s *Server) gapReport(ctx context.Context, minMisses int) (string, error) {
	items, err := s.collectSkillGaps(ctx, minMisses)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "没有达到阈值的技能缺口。", nil
	}
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Server) collectSkillGaps(ctx context.Context, minMisses int) ([]map[string]any, error) {
	if s.m == nil || s.m.pg == nil {
		return nil, fmt.Errorf("数据库不可用")
	}
	gaps, err := s.m.pg.SkillGaps(ctx, minMisses)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(gaps))
	for _, g := range gaps {
		if _, err := os.Stat(filepath.Join(s.skillDir, g.Skill)); err == nil {
			continue
		}
		out = append(out, map[string]any{
			"skill":        g.Skill,
			"misses":       g.Misses,
			"agents":       g.Agents,
			"tasks":        g.Tasks,
			"agent_keys":   g.AgentKeys,
			"first_seen":   g.FirstSeen.UTC().Format("2006-01-02T15:04:05Z"),
			"last_seen":    g.LastSeen.UTC().Format("2006-01-02T15:04:05Z"),
			"suggested":    "用 propose_skill 起草一条技能，内容依据这些未命中记录",
			"evidence_fmt": db.SkillGapEvidence(g),
		})
	}
	return out, nil
}

// toolProposeSkill 起草一条技能提议。
//
// 与 create_skill 的区别是它**总是**走暂存，即使门控关闭。用途不同：
// create_skill 是「我要用这个能力」的即时操作，propose_skill 是「我认为该沉淀一条」。
func (s *Server) toolProposeSkill() actool.CoreTool {
	return wrTool("propose_skill",
		"起草一条技能提议（进入待审阅队列，不直接生效）。"+
			"内容纪律：写可泛化的规则，不要写事故日志——规则必须脱离具体故事也站得住。",
		objSchema(map[string]any{
			"name":         strParam("skill 名(小写字母开头，字母/数字/连字符)"),
			"description":  strParam("skill 描述(必填，说明它做什么/何时用)"),
			"instructions": strParam("Markdown 正文说明（可选，留空则用四段模板）"),
			"evidence":     strParam("依据：缺口未命中记录、复测结论等；可留空但审阅者会看出这是空口无凭的"),
		}, "name", "description"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Name, Description, Instructions, Evidence string }
			_ = json.Unmarshal(in, &a)
			if !validSkillName(a.Name) {
				return actool.Errorf("skill 名不合法(小写字母开头，仅字母/数字/连字符，≤64)"), nil
			}
			if strings.TrimSpace(a.Description) == "" {
				return actool.Errorf("description 必填"), nil
			}
			doc := buildSkillDocument(a.Name, a.Description, a.Instructions)
			return s.stageSkillProposal(ctx, a.Name, "", doc, a.Evidence, "agent")
		})
}

// toolDistillTaskSkill 手动将指定任务的结构化产出沉淀为复盘技能提议。
func (s *Server) toolDistillTaskSkill() actool.CoreTool {
	return wrTool("distill_task_skill",
		"将指定任务的已完成意图、关键事实、确认漏洞、复测记录、软件逆向证据与 3-Strike 止损经验确定性沉淀为 case-web-<id> / case-re-<id> 复盘技能。",
		objSchema(map[string]any{
			"task_id": numParam("目标任务 ID（正整数）"),
			"apply":   map[string]any{"type": "boolean", "description": "当审阅门控开启时，是否直接落盘（默认跟随系统设置 skill_auto_distill_apply）"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID int64 `json:"task_id"`
				Apply  *bool `json:"apply"`
			}
			_ = json.Unmarshal(in, &a)
			p, err := s.DistillTaskSkill(ctx, a.TaskID, a.Apply)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if p == nil {
				return actool.Errorf("未能生成技能提议"), nil
			}
			return jsonResult(map[string]any{
				"id":       p.ID,
				"skill":    p.Skill,
				"status":   p.Status,
				"origin":   p.Origin,
				"evidence": p.Evidence,
				"lint":     p.Lint,
			})
		})
}

// ---- 审阅 ----

func (s *Server) toolListSkillProposals() actool.CoreTool {
	return wrTool("list_skill_proposals",
		"列出待审阅的技能提议。审阅前请看 content（落地后的完整文件）与 evidence（依据）。",
		objSchema(map[string]any{
			"status": strParam("pending(默认) / applied / rejected / all"),
			"limit":  numParam("最多返回条数，默认 20"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Status string `json:"status"`
				Limit  int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if s.m == nil || s.m.pg == nil {
				return actool.Errorf("数据库不可用"), nil
			}
			list, err := s.m.pg.ListSkillProposals(ctx, a.Status, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(list) == 0 {
				return actool.Text("没有符合条件的提议。"), nil
			}
			out := make([]map[string]any, 0, len(list))
			for _, p := range list {
				out = append(out, map[string]any{
					"id":          p.ID,
					"skill":       p.Skill,
					"file":        p.File,
					"origin":      p.Origin,
					"status":      p.Status,
					"evidence":    p.Evidence,
					"lint":        p.Lint,
					"note":        p.Note,
					"created_at":  p.CreatedAt,
					"reviewed_at": p.ReviewedAt,
					"content":     p.Content,
				})
			}
			raw, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(string(raw)), nil
		})
}

// toolReviewSkillProposal 批准或否决一条提议。
//
// 批准是**先写盘、后改状态**的顺序，与 db.ReviewSkillProposal 的顺序相反。
// 这是刻意的：ReviewSkillProposal 用 `WHERE status=pending` 保证一条提议只能被
// 处理一次；若先改状态再写盘，写盘失败就会留下一条「已批准但技能不存在」的记录，
// 而它再也无法被重试——状态已经不在 pending 里了。先写盘则会在写盘失败时保持
// pending，审阅者可以直接重试。
func (s *Server) toolReviewSkillProposal() actool.CoreTool {
	return wrTool("review_skill_proposal",
		"审阅一条技能提议：approve（写入磁盘）或 reject（否决，留档不落盘）。"+
			"一条提议只能被处理一次。",
		objSchema(map[string]any{
			"id":       strParam("提议 id"),
			"decision": strParam("approve 或 reject"),
			"note":     strParam("审阅说明（会留档）"),
		}, "id", "decision"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ ID, Decision, Note string }
			_ = json.Unmarshal(in, &a)
			msg, err := s.reviewProposal(ctx, a.ID, a.Decision, a.Note)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(msg), nil
		})
}

// reviewProposal 处理一条提议的审阅。工具闭包只做参数解包。
func (s *Server) reviewProposal(ctx context.Context, id, decision, note string) (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", fmt.Errorf("数据库不可用")
	}
	p, err := s.m.pg.GetSkillProposal(ctx, id)
	if err != nil {
		return "", err
	}
	// 先取出来再判状态，避免对不存在的 id 也报「已审阅」。
	if p.Status != db.SkillProposalPending {
		return "", fmt.Errorf("提议 %s 已是 %s，不能重复审阅", id, p.Status)
	}
	resolved := db.SkillProposalRejected
	switch {
	case strings.EqualFold(strings.TrimSpace(decision), "approve"),
		strings.EqualFold(strings.TrimSpace(decision), "apply"):
		resolved = db.SkillProposalApplied
	case !strings.EqualFold(strings.TrimSpace(decision), "reject"):
		return "", fmt.Errorf("decision 只能是 approve 或 reject")
	}
	if resolved == db.SkillProposalApplied {
		if err := s.applySkillFile(p.Skill, p.File, p.Content); err != nil {
			// 保持 pending：状态先改的话，这条提议就再也无法重试了。
			return "", fmt.Errorf("写盘失败，提议仍为 pending 可重试: %w", err)
		}
	}
	if _, err := s.m.pg.ReviewSkillProposal(ctx, id, resolved, note); err != nil {
		return "", err
	}
	verb := "已否决，留档未落盘"
	if resolved == db.SkillProposalApplied {
		verb = "已批准并写入磁盘"
	}
	return fmt.Sprintf("提议 %s（技能 %s）%s", id, p.Skill, verb), nil
}

// ---- HTTP API 路由处理器（供 /system/skills 审阅界面与手动沉淀调用） ----

// fsListSkillProposals returns skill proposals filtered by status plus per-status counts.
func (s *Server) fsListSkillProposals(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	proposals, err := pg.ListSkillProposals(r.Context(), status, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	counts, err := pg.CountSkillProposals(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"proposals":          proposals,
		"counts":             counts,
		"review_gate":        s.SkillReviewGateEnabled(),
		"auto_distill":       s.SkillAutoDistillEnabled(),
		"auto_distill_apply": s.SkillAutoDistillApplyEnabled(),
	})
}

// fsReviewSkillProposal approves (with optional content edit) or rejects a pending proposal.
func (s *Server) fsReviewSkillProposal(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, 400, "缺少提议 id")
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
		Content  string `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	decision := strings.ToLower(strings.TrimSpace(body.Decision))
	if decision != "approve" && decision != "apply" && decision != "reject" {
		writeErr(w, 400, "decision 只能是 approve 或 reject")
		return
	}
	if (decision == "approve" || decision == "apply") && strings.TrimSpace(body.Content) != "" {
		if _, err := pg.UpdateSkillProposalContent(r.Context(), id, body.Content); err != nil {
			if errors.Is(err, db.ErrSkillProposalNotFound) {
				writeErr(w, 404, "技能提议不存在")
				return
			}
			if errors.Is(err, db.ErrSkillProposalConflict) {
				writeErr(w, 409, "该提议已被审阅，无法修改")
				return
			}
			writeErr(w, 400, err.Error())
			return
		}
	}
	msg, err := s.reviewProposal(r.Context(), id, decision, strings.TrimSpace(body.Note))
	if err != nil {
		if errors.Is(err, db.ErrSkillProposalNotFound) {
			writeErr(w, 404, "技能提议不存在")
			return
		}
		if errors.Is(err, db.ErrSkillProposalConflict) || strings.Contains(err.Error(), "不能重复审阅") {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	updated, _ := pg.GetSkillProposal(r.Context(), id)
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"message":  msg,
		"proposal": updated,
	})
}

// fsSkillGaps returns structured skill gaps (excluding skills already present on disk).
func (s *Server) fsSkillGaps(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	minMisses, _ := strconv.Atoi(r.URL.Query().Get("min_misses"))
	gaps, err := s.collectSkillGaps(r.Context(), minMisses)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"gaps": gaps})
}

// fsDistillTaskSkill manually triggers deterministic skill distillation for a task.
func (s *Server) fsDistillTaskSkill(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var body struct {
		TaskID int64 `json:"task_id"`
		Apply  *bool `json:"apply"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if body.TaskID <= 0 {
		writeErr(w, 400, "task_id 必须为正整数")
		return
	}
	p, err := s.DistillTaskSkill(r.Context(), body.TaskID, body.Apply)
	if err != nil {
		if errors.Is(err, ErrTaskNotSubstantive) {
			writeErr(w, 400, err.Error())
			return
		}
		if strings.Contains(err.Error(), "不存在") {
			writeErr(w, 404, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"proposal": p,
	})
}

// fsBatchReviewSkillProposals batch-approves or batch-rejects multiple pending proposals.
func (s *Server) fsBatchReviewSkillProposals(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var body struct {
		IDs      []string `json:"ids"`
		Decision string   `json:"decision"`
		Note     string   `json:"note"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	decision := strings.ToLower(strings.TrimSpace(body.Decision))
	if decision != "approve" && decision != "apply" && decision != "reject" {
		writeErr(w, 400, "decision 只能是 approve 或 reject")
		return
	}
	if len(body.IDs) == 0 {
		writeErr(w, 400, "请至少选择一条技能提议")
		return
	}
	succeeded := make([]string, 0, len(body.IDs))
	failed := map[string]string{}
	seen := map[string]bool{}
	for _, rawID := range body.IDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := s.reviewProposal(r.Context(), id, decision, strings.TrimSpace(body.Note)); err != nil {
			failed[id] = err.Error()
			continue
		}
		succeeded = append(succeeded, id)
	}
	writeJSON(w, 200, map[string]any{
		"ok":        len(failed) == 0,
		"succeeded": succeeded,
		"failed":    failed,
	})
}

// fsMergeSkillProposals consolidates multiple case/custom proposals into a single generalized skill.
func (s *Server) fsMergeSkillProposals(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		IDs           []string `json:"ids"`
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Apply         bool     `json:"apply"`
		RejectSources bool     `json:"reject_sources"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if !validSkillName(name) {
		writeErr(w, 400, "合并技能名不合法(小写字母开头，仅字母/数字/连字符，≤64)")
		return
	}
	seen := map[string]bool{}
	var sources []db.SkillProposal
	for _, rawID := range body.IDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		p, err := pg.GetSkillProposal(r.Context(), id)
		if err != nil || p == nil {
			writeErr(w, 404, fmt.Sprintf("提议 %s 不存在", id))
			return
		}
		sources = append(sources, *p)
	}
	if len(sources) < 2 {
		writeErr(w, 400, "至少需要选择 2 条技能提议进行合并提炼")
		return
	}
	doc, evidence := mergeSkillProposalDocuments(name, strings.TrimSpace(body.Description), sources)
	merged, err := s.persistCuratorSkill(r.Context(), name, doc, evidence, body.Apply)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if body.RejectSources {
		for _, src := range sources {
			if src.Status == db.SkillProposalPending {
				_, _ = s.reviewProposal(r.Context(), src.ID, "reject", "已合并沉淀至通用技能 "+name)
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"proposal": merged,
	})
}

func mergeSkillProposalDocuments(name, description string, sources []db.SkillProposal) (string, string) {
	sourceNames := make([]string, 0, len(sources))
	sourceIDs := make([]string, 0, len(sources))
	sectionLines := map[string][]string{}
	seenLine := map[string]bool{}

	targetSections := []string{
		"何时使用",
		"已验证的有效路径",
		"关键事实与证据",
		"已确认漏洞与可利用点",
		"漏洞复测与修复验证",
		"逆向样本与静态分析证据",
		"工具与命令模式",
		"失败、误区与止损",
		"人工提示与决策",
		"复用步骤",
		"适用边界与验证要求",
	}

	for _, src := range sources {
		sourceNames = append(sourceNames, src.Skill)
		sourceIDs = append(sourceIDs, src.ID)
		raw := strings.ReplaceAll(src.Content, "\r\n", "\n")
		if strings.HasPrefix(raw, "---\n") {
			if end := strings.Index(raw[4:], "\n---\n"); end >= 0 {
				raw = raw[4+end+5:]
			}
		}
		blocks := strings.Split(raw, "\n## ")
		for i, blk := range blocks {
			if i == 0 && !strings.HasPrefix(strings.TrimSpace(blk), "## ") {
				continue
			}
			blk = strings.TrimPrefix(strings.TrimSpace(blk), "## ")
			nl := strings.Index(blk, "\n")
			if nl < 0 {
				continue
			}
			secTitle := strings.TrimSpace(blk[:nl])
			secBody := strings.TrimSpace(blk[nl+1:])
			for _, line := range strings.Split(secBody, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				dedupKey := secTitle + "\x00" + line
				if seenLine[dedupKey] {
					continue
				}
				seenLine[dedupKey] = true
				sectionLines[secTitle] = append(sectionLines[secTitle], line)
			}
		}
	}

	if description == "" {
		description = fmt.Sprintf("跨任务合并提炼经验（源自 %s 等 %d 份复盘提议）：汇总已验证路径、漏洞证据与 3-Strike 止损规范。", strings.Join(sourceNames, "、"), len(sources))
	}
	description = curatorSingleLine(description, 240)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s · 跨任务合并复盘手册\n\n", name)
	fmt.Fprintf(&b, "> 合并来源：`%s`（提议 ID：`%s`）\n\n", strings.Join(sourceNames, "`, `"), strings.Join(sourceIDs, "`, `"))
	for _, sec := range targetSections {
		lines := sectionLines[sec]
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", sec)
		for _, l := range lines {
			b.WriteString(l)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	evidence := fmt.Sprintf("merged_from=%s skills=%s", strings.Join(sourceIDs, ","), strings.Join(sourceNames, ","))
	return buildSkillDocument(name, description, b.String()), evidence
}

// sysVerificationCapabilities returns a read-only snapshot of human verification PoW & helper capabilities.
func (s *Server) sysVerificationCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, agent.VerificationCapabilitiesSnapshot())
}

// knownSkillDirs 列出已落盘的技能名，测试与诊断用。
func (s *Server) knownSkillDirs() []string {
	entries, err := os.ReadDir(s.skillDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
