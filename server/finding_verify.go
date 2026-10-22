package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

// ── P2 verifier：把「独立复核」做成链路上的一等角色 ─────────────────────────────
//
// 改造前，findings.validation 这个字段只是数据结构：没有任何代码会把它从
// unverified 推到 verified，于是报告里的「已复核」口径永远为 0，而资产真值、
// 报告正文实际上仍然把 worker 的自述当事实。这里补上推它的那条流水线：
//
//	发现落库 →（策略判定：高危强制 / 中低危采样）→ 排队 → 复核会话
//	        → 复核员用只读工具独立复现 → 唯一结论 → findings.validation
//
// 三条不可让步的性质：
//  1. 独立：复核员是另一个 agent（verifier）、另一个会话、只有只读工具；
//     数据层还挡住了「复核员复核自己上报的发现」（db.ErrVerifySelfClaimed）。
//  2. 只有结论才改状态：会话中断/异常/无结论 ⇒ 保持 unverified，绝不当成通过。
//  3. 有成本上限：并发 1、队列有界，超出就丢弃并留日志——宁可少复核一条，
//     也不能让复核把正常任务挤掉。
//
// 触发闸门（阈值 + 采样）由系统设置页配置，判定逻辑见 server/verify_policy.go：
// 等级达到阈值强制复核、低于阈值按比例抽查、也可以整体关闭（只保留人工发起）。

const (
	// verifySeedFlag 是「复核员已播种」的一次性标记（与 retester 的 flag 同构）。
	verifySeedFlag = "finding_verifier_seed_v1"
	// verifyQueueCap 是待复核队列长度。满时丢弃而非阻塞：复核是旁路，不能反压探测任务。
	verifyQueueCap = 64
	// verifyMaxAttempts 是「聊天不可用（模型未配好/服务停止中）」时的重试次数。
	verifyMaxAttempts = 3
	// verifySampleRateEnv 在系统设置未配置采样比例时生效（0–1），默认 0.25。
	verifySampleRateEnv = "ARTEX_VERIFY_SAMPLE_RATE"
)

var errVerifyAgentMissing = "复核 Agent 未启用：请在 Agent 页面启用 verifier，并确认它绑定了 get_finding_verification_context 与 record_finding_verification_result"

// verifyQueue 是进程级待复核队列。用 Server 字段而不是包级变量，
// 这样集成测试里多个 Server 实例互不干扰。
type verifyRequest struct {
	findingID int64
	reason    string
}

func verifySampleRate() float64 {
	if v := strings.TrimSpace(os.Getenv(verifySampleRateEnv)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return 0.25
}

// enqueueAutoVerification 由 db.OnFindingInserted 钩子调用，必须立即返回：
// 它运行在证据写入路径上，任何阻塞都会拖慢任务本身。
func (s *Server) enqueueAutoVerification(findingID int64, severity string) {
	if s == nil || findingID <= 0 {
		return
	}
	ok, reason := shouldAutoVerify(severity)
	if !ok {
		return
	}
	select {
	case s.verifyQueue <- verifyRequest{findingID: findingID, reason: reason}:
	default:
		log.Printf("[verify] 队列已满（%d），跳过发现 #%d 的自动复核", verifyQueueCap, findingID)
	}
}

// runVerificationQueue 是复核队列的唯一消费者：串行执行，避免 N 个复核会话同时
// 抢模型配额；每次失败按退避重试，重试用尽就放弃（发现保持 unverified，可手工重发）。
func (s *Server) runVerificationQueue(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-s.verifyQueue:
			s.verifyOne(ctx, req)
		}
	}
}

func (s *Server) verifyOne(ctx context.Context, req verifyRequest) {
	for attempt := 1; attempt <= verifyMaxAttempts; attempt++ {
		_, _, err := s.startVerification(ctx, req.findingID, req.reason, "")
		switch {
		case err == nil:
			return
		case errors.Is(err, db.ErrVerifySelfClaimed):
			log.Printf("[verify] 发现 #%d 由复核员自己上报，跳过：%v", req.findingID, err)
			return
		}
		if ctx.Err() != nil {
			return
		}
		// 聊天暂不可用（模型/配置/停止中）是可恢复的，退避后再试；其它错误同样
		// 重试一次的成本很低，而漏复核的代价是口径长期为 0。
		wait := time.Duration(attempt) * 5 * time.Second
		log.Printf("[verify] 发现 #%d 第 %d 次启动失败（%v），%s 后重试", req.findingID, attempt, err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	log.Printf("[verify] 发现 #%d 自动复核放弃（%d 次重试均失败），保持未复核", req.findingID, verifyMaxAttempts)
}

// startVerification 建立复核记录与独立会话，并异步跑一轮对话。
//
// 与 startFindingRetest 的差别只有一处：这里不校验「工具是否绑在 agent 上」以外的
// 前端前置条件（自动复核没有 HTTP 请求可回复），失败一律变成 error 由调用方记录。
func (s *Server) startVerification(ctx context.Context, findingID int64, reason, notes string) (*db.FindingVerification, bool, error) {
	pg := s.m.pg
	if pg == nil {
		return nil, false, errors.New("数据库未就绪")
	}
	a, err := pg.GetAgentByKey(db.VerifierAgentKey)
	if err != nil {
		return nil, false, err
	}
	if a == nil || !a.Enabled {
		return nil, false, errors.New(errVerifyAgentMissing)
	}
	for _, key := range []string{"get_finding_verification_context", "record_finding_verification_result"} {
		t, err := pg.GetTool(key)
		if err != nil {
			return nil, false, err
		}
		if t == nil || !t.Enabled || !slices.Contains(t.Agents, a.Key) {
			return nil, false, errors.New("复核工具未就绪：" + key)
		}
	}
	if s.resolveChatAgent(&db.Conversation{AgentKey: a.Key}) == nil {
		return nil, false, errors.New(s.chatUnavailableReason())
	}
	if ctx.Err() != nil {
		return nil, false, errors.New("服务正在停止")
	}
	verification, conv, created, err := pg.CreateFindingVerification(ctx, findingID, notes, reason)
	if err != nil {
		return nil, false, err
	}
	if !created {
		return verification, false, nil // 已有进行中的复核，去重
	}
	busyKey := s.convBusyKey(conv.ID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()
	// 归因：与复测一致，把复核消耗记到原发现所属任务名下，泳道记为 verifier。
	taskID := ""
	if f, err := pg.GetFinding(findingID); err == nil && f != nil && f.TaskID != nil {
		taskID = strconv.FormatInt(*f.TaskID, 10)
	}
	s.runConversation(conv, verification.InitialMessage(), busyKey,
		conversationAttribution{taskID: taskID, worker: db.VerifierAgentKey})
	return verification, true, nil
}

// ── HTTP ────────────────────────────────────────────────────────────────────

// startFindingVerification 是「人工点一下开始复核」的入口（POST .../verifications）。
func (s *Server) startFindingVerification(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	var req struct {
		Notes string `json:"notes"`
	}
	if !decodeConversationRequest(w, r, &req) {
		return
	}
	f, err := pg.GetFinding(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if f == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	v, created, err := s.startVerification(r.Context(), id, "manual", strings.TrimSpace(req.Notes))
	if err != nil {
		// 409 覆盖三类「现在不能复核」:自身上报、复核 agent/工具未就绪、聊天不可用。
		// 前端对这三类的处理完全相同(提示原文),所以不细分成 400/503。
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusAccepted
	}
	writeJSON(w, code, map[string]any{"verification": v, "created": created})
}

func (s *Server) listFindingVerifications(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	items, err := pg.ListFindingVerifications(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verifications": items})
}

func (s *Server) listActiveFindingVerifications(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListActiveFindingVerifications(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verifications": items})
}

// ── 复核员工具（只读 + 一条写结论）────────────────────────────────────────────

// findingVerifyTools 是复核员能被绑定的全部工具。
//
// 只读性是这里的设计核心：get_* 只能读本会话快照，record_* 只写 finding_verifications，
// 没有任何工具能改 findings 的正文、状态或资产。复核员因此不可能「顺手把发现改成
// verified」以外的动作；而 verified 这一步是 db.FinishFindingVerification 在会话
// 成功结束后做的，工具本身也改不了 validation。
func (s *Server) findingVerifyTools() []actool.CoreTool {
	return []actool.CoreTool{
		roTool("get_finding_verification_context", "读取当前复核会话关联的发现原文、上报者证据、关联资产、复核状态与任务约束。无参数，只能读取本会话。",
			objSchema(map[string]any{}), func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
				v, err := s.m.pg.FindingVerificationForConversation(ctx, intercept.ConvIDFromContext(ctx))
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if v == nil {
					return actool.Errorf("当前会话未关联复核记录，请从漏洞详情发起复核"), nil
				}
				var constraints []db.Constraint
				f, err := s.m.pg.GetFinding(v.FindingID)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if f != nil && f.TaskID != nil {
					if task, ok := s.m.Task(strconv.FormatInt(*f.TaskID, 10)); ok {
						constraints, err = task.Store.ListConstraints()
						if err != nil {
							return actool.Errorf(err.Error()), nil
						}
					}
				}
				return jsonResult(map[string]any{
					"verification":        v,
					"snapshot":            v.Snapshot,
					"current_constraints": constraints,
				})
			}),
		wrTool("record_finding_verification_result", "为当前复核会话保存唯一结论。只有 verified 才会把该发现计入资产真值与报告正文；refuted 标记为不成立；inconclusive 表示无法确认（发现仍是未复核）。必须提供本次独立检查的证据。",
			objSchema(map[string]any{
				"verdict":  map[string]any{"type": "string", "enum": []string{"verified", "refuted", "inconclusive"}},
				"summary":  strParam("本次复核结论摘要"),
				"evidence": strParam("Markdown：独立复现/推翻的实际步骤、观察、与上报证据的差异；无法确认则列出已检查内容与阻塞原因"),
			}, "verdict", "summary", "evidence"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					Verdict  string `json:"verdict"`
					Summary  string `json:"summary"`
					Evidence string `json:"evidence"`
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if err := s.m.pg.RecordFindingVerificationResult(ctx, intercept.ConvIDFromContext(ctx), a.Verdict, a.Summary, a.Evidence); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(map[string]any{"saved": true, "verdict": a.Verdict})
			}),
	}
}

// seedFindingVerifier 与 seedFindingRetester 同构：工具入库按 agent 绑定、agent 与
// 默认提示词原子播种、用 settings 标记保证只做一次（用户改过就不覆盖）。
func (s *Server) seedFindingVerifier() error {
	for _, t := range s.findingVerifyTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings, _ := json.Marshal([]string{db.VerifierAgentKey})
		if err := s.m.pg.SeedTool(t.Name(), t.Description(), schema, bindings); err != nil {
			return err
		}
	}
	tx, err := s.m.pg.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 与复测用不同的锁号：两条播种互不相干，没必要串行。
	if err = db.AdvisoryLock(context.Background(), tx, 7337741011); err != nil {
		return err
	}
	var done string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key=$1`, verifySeedFlag).Scan(&done)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if done == "true" {
		return nil
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO agents(key,name,description,role,builtin,enabled)
VALUES ($1,'独立复核','对已登记的漏洞做独立只读复核，决定它计入真值还是被推翻。','assistant',false,true)
ON CONFLICT (key) DO NOTHING RETURNING id`, db.VerifierAgentKey).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if id > 0 {
		var pid int64
		if err = tx.QueryRow(`INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by)
VALUES ($1,1,$2,'内置默认','system') RETURNING id`, id, agent.VerifierDefaultPrompt).Scan(&pid); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, pid, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES ($1,'true') ON CONFLICT(key) DO UPDATE SET value='true'`, verifySeedFlag); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) finishVerification(id int64, status, reason string) {
	if err := s.m.pg.FinishFindingVerification(id, status, reason); err != nil {
		log.Printf("[verify %d] finish: %v", id, err)
	}
}

// verifyQueueOnce 只给测试用：把当前队列里的一条任务跑完再返回，避免测试里
// 依赖后台 goroutine 的时序。
func (s *Server) verifyQueueOnce(ctx context.Context) bool {
	select {
	case req := <-s.verifyQueue:
		s.verifyOne(ctx, req)
		return true
	default:
		return false
	}
}
