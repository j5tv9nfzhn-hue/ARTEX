package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// VerifierAgentKey 是独立复核员的 agent key。
//
// 与 retester 的关系：复测问的是「现在还修好没有」，复核问的是「这条发现到底成不成立」。
// 两者都需要「独立第二个视角」，但结论落到的字段不同（复测改 findings.status，
// 复核改 findings.validation），因此各自一张表、各自一句 prompt。共用的只有
// findingSnapshotTx（快照）与「会话成功结束才封存结论」这条事务纪律。
const VerifierAgentKey = "verifier"

// 复核终止原因写入 finding_verifications.error。与 db 包其余终止原因一样不含单引号，
// 可直接插进 SQL 字面量位置。
const (
	verifyNoConclusionReason   = "复核员未保存复核结论，请检查对话后重新复核"
	verifyServiceRestartReason = "服务重启导致复核中断，请重新启动"
)

var (
	// ErrVerificationNotRunning 与 ErrRetestNotRunning 同构：都是「这条记录已经封存了」。
	ErrVerificationNotRunning = errors.New("本次复核已结束或尚未开始，请重新发起复核")
	// ErrVerifySelfClaimed 是「不得自己验证自己」的数据层闸门：复核员自己上报的
	// 发现，不能由复核员再复核一遍。命中时取不到第二条独立证据，等于自我盖章。
	ErrVerifySelfClaimed = errors.New("复核员自己上报的发现必须由其它泳道复核，不能自我复核")
)

// 复核结论 → findings.validation 的唯一映射。刻意做成表而不是散落各处的 if：
// 「哪些结论算真值」是这份报告的口径来源，必须只有一处定义。
const (
	verifyVerdictVerified     = "verified"
	verifyVerdictRefuted      = "refuted"
	verifyVerdictInconclusive = "inconclusive"
)

// FindingVerification 是一次复核的完整记录。封存后不可变（与 FindingRetest 同规矩）。
type FindingVerification struct {
	ID             int64           `json:"id"`
	FindingID      int64           `json:"finding_id"`
	ConversationID *int64          `json:"conversation_id"`
	Status         string          `json:"status"`
	Verdict        string          `json:"verdict"`
	Notes          string          `json:"notes"`
	Snapshot       json.RawMessage `json:"snapshot,omitempty"`
	Summary        string          `json:"summary"`
	Evidence       string          `json:"evidence"`
	Error          string          `json:"error"`
	Reason         string          `json:"reason"` // 为什么触发复核：severity / manual / sample
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
}

const verificationCols = `id, finding_id, conversation_id, status, verdict, notes, summary, evidence, error, reason, created_at, started_at, finished_at`

// ActiveFindingVerification 是列表页轮询用的小载荷。
type ActiveFindingVerification struct {
	ID             int64  `json:"id"`
	FindingID      int64  `json:"finding_id,string"`
	ConversationID int64  `json:"conversation_id"`
	Status         string `json:"status"`
}

func (d *DB) ListActiveFindingVerifications(ctx context.Context) ([]ActiveFindingVerification, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, finding_id, conversation_id, status FROM finding_verifications
	WHERE status IN ('pending','running') AND conversation_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ActiveFindingVerification{}
	for rows.Next() {
		var item ActiveFindingVerification
		if err := rows.Scan(&item.ID, &item.FindingID, &item.ConversationID, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanVerification(row interface{ Scan(...any) error }) (*FindingVerification, error) {
	v := &FindingVerification{}
	err := row.Scan(&v.ID, &v.FindingID, &v.ConversationID, &v.Status, &v.Verdict, &v.Notes,
		&v.Summary, &v.Evidence, &v.Error, &v.Reason, &v.CreatedAt, &v.StartedAt, &v.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// CreateFindingVerification 快照源发现、建独立会话、落首条消息，与 CreateFindingRetest
// 完全同构（同样的 _txlock=immediate 去重论证，见 CreateFindingRetest 注释）。
//
// 与复测的两处差异：
//  1. 拒绝复核员自己上报的发现（ErrVerifySelfClaimed），「不得自己验证自己」；
//  2. reason 记录本次复核的触发原因（severity/manual/sample），便于事后核对
//     「高危是否都复核了」这条验收口径。
func (d *DB) CreateFindingVerification(ctx context.Context, findingID int64, notes, reason string) (*FindingVerification, *Conversation, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback()
	var worker string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(worker,'') FROM findings WHERE id=$1`, findingID).Scan(&worker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, sql.ErrNoRows
	}
	if err != nil {
		return nil, nil, false, err
	}
	if strings.TrimSpace(worker) == VerifierAgentKey {
		return nil, nil, false, ErrVerifySelfClaimed
	}
	title, snapshot, err := findingSnapshotTx(ctx, tx, findingID)
	if err != nil {
		return nil, nil, false, err
	}
	existing, err := scanVerification(tx.QueryRowContext(ctx, `SELECT `+verificationCols+` FROM finding_verifications
WHERE finding_id=$1 AND status IN ('pending','running')`, findingID))
	if err != nil {
		return nil, nil, false, err
	}
	if existing != nil {
		return existing, nil, false, nil
	}
	if runes := []rune(title); len(runes) > 100 {
		title = string(runes[:100])
	}
	c, err := scanConv(tx.QueryRowContext(ctx, `INSERT INTO conversations(agent_key,title) VALUES ($1,$2) RETURNING `+convCols,
		VerifierAgentKey, fmt.Sprintf("复核 #%d · %s", findingID, title)))
	if err != nil {
		return nil, nil, false, err
	}
	v, err := scanVerification(tx.QueryRowContext(ctx, `INSERT INTO finding_verifications(finding_id,conversation_id,notes,snapshot,reason)
VALUES ($1,$2,$3,$4,$5) RETURNING `+verificationCols,
		findingID, c.ID, strings.TrimSpace(notes), snapshot, strings.TrimSpace(reason)))
	if err != nil {
		return nil, nil, false, err
	}
	msg := v.InitialMessage()
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail)
VALUES ($1,$2,'user',$3,$4)`, c.ID, VerifierAgentKey, fmt.Sprintf("请独立复核发现 #%d", findingID), msg); err != nil {
		return nil, nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	return v, &c, true, nil
}

// InitialMessage 是复核会话的首条消息。
//
// 措辞上刻意把「你是复核员，不是上报者」写在最前：这一段会作为 user 消息进入上下文，
// 是复核员唯一能看到的立场声明。真正的约束在工具层（只读、只能读本会话快照），
// 这里只负责把任务说清楚，不指望 prompt 承担隔离职责。
func (v *FindingVerification) InitialMessage() string {
	var b strings.Builder
	b.WriteString("你是独立复核员。有人上报了一条漏洞，你需要在**不采信其结论**的前提下，用只读手段独立确认它是否成立。\n\n")
	fmt.Fprintf(&b, "步骤：\n1. 上报内容、关联资产与任务约束已由系统在本次会话里直接提供（你没有任何工具，也无需调用工具）。\n")
	b.WriteString("2. 只看得到只读工具：自己去请求、去复算、去找反证，不要仅凭上报描述推断。\n")
	b.WriteString("3. 结束前输出唯一结论 JSON（不要 Markdown 围栏）：{"verdict":"verified|refuted|inconclusive","summary":"...","evidence":"<Markdown>"}，系统会据此保存。\n")
	b.WriteString("   - verified：你独立复现或拿到同等强度的独立证据；\n")
	b.WriteString("   - refuted：按描述无法复现且能说明原因（已修复、环境不存在、证据不成立）；\n")
	b.WriteString("   - inconclusive：受限于权限/网络/时间无法确认——这不算复核通过，发现仍按未复核计。\n")
	if len(v.Notes) > 0 {
		fmt.Fprintf(&b, "\n发起人补充说明：%s\n", v.Notes)
	}
	return b.String()
}

func (d *DB) ListFindingVerifications(findingID int64) ([]*FindingVerification, error) {
	rows, err := d.Query(`SELECT `+verificationCols+` FROM finding_verifications WHERE finding_id=$1 ORDER BY id DESC`, findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*FindingVerification{}
	for rows.Next() {
		v, err := scanVerification(rows)
		if err != nil {
			return nil, err
		}
		v.Snapshot = nil // 历史列表不带快照，与复测一致
		items = append(items, v)
	}
	return items, rows.Err()
}

// FindingVerificationForConversation 由会话反查复核记录。会话已删除时返回 nil。
func (d *DB) FindingVerificationForConversation(ctx context.Context, conversationID int64) (*FindingVerification, error) {
	return scanVerification(d.QueryRowContext(ctx, `SELECT `+verificationCols+` FROM finding_verifications
WHERE conversation_id=$1`, conversationID))
}

func (d *DB) FailPendingVerificationForConversation(conversationID int64, reason string) error {
	_, err := d.Exec(`UPDATE finding_verifications SET status='failed', error=$2, finished_at=now()
WHERE conversation_id=$1 AND status IN ('pending','running')`, conversationID, reason)
	return err
}

func (d *DB) StartFindingVerification(ctx context.Context, id int64) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE finding_verifications SET status='running', started_at=now()
WHERE id=$1 AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecordFindingVerificationResult 保存结论。幂等：同一条结论重复写不算错，
// 换一条结论则拒绝（结论一旦写定，同一回合内不得反悔，与复测同一套纪律）。
func (d *DB) RecordFindingVerificationResult(ctx context.Context, conversationID int64, verdict, summary, evidence string) error {
	switch verdict {
	case verifyVerdictVerified, verifyVerdictRefuted, verifyVerdictInconclusive:
	default:
		return errors.New("verdict 必须为 verified / refuted / inconclusive")
	}
	summary, evidence = strings.TrimSpace(summary), strings.TrimSpace(evidence)
	if summary == "" || evidence == "" {
		return errors.New("summary 与 evidence 不能为空；无法确认时说明实际检查及阻塞原因")
	}
	if len(summary) > 16000 || len(evidence) > 128000 {
		return errors.New("复核结论过长（summary ≤ 16KB，evidence ≤ 128KB）")
	}
	res, err := d.ExecContext(ctx, `UPDATE finding_verifications SET verdict=$2, summary=$3, evidence=$4
WHERE conversation_id=$1 AND status='running' AND (verdict='' OR (verdict=$2 AND summary=$3 AND evidence=$4))`,
		conversationID, verdict, summary, evidence)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrVerificationNotRunning
	}
	return nil
}

// FinishFindingVerification 封存一次复核，并在同一个事务里推 findings.validation。
//
// 三条规矩：
//   - 取消/失败优先于已写好的结论，中断的复核不能看起来像复核通过；
//   - 只有 completed 且有结论才动 validation，其它情况（failed/stopped/无结论）保持原状，
//     即停在 unverified——「没复核完」不等于「复核通过」，也不等于「被推翻」；
//   - validation 只前进不静默回退：已经是 verified 的发现，一次 inconclusive 不会把它
//     打回 unverified；refuted 同样只有明确结论才能写。
func (d *DB) FinishFindingVerification(id int64, status, reason string) error {
	if status != "completed" && status != "failed" && status != "stopped" {
		return errors.New("invalid terminal verification status")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var findingID int64
	err = tx.QueryRow(`SELECT v.finding_id FROM finding_verifications v WHERE v.id=$1`, id).Scan(&findingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // 发现/记录已删除
	}
	if err != nil {
		return err
	}
	var finalStatus, verdict string
	err = tx.QueryRow(`UPDATE finding_verifications SET
status=CASE WHEN $2='completed' AND verdict='' THEN 'failed' ELSE $2 END,
error=CASE WHEN $2='completed' AND verdict='' THEN '`+verifyNoConclusionReason+`' ELSE $3 END,
finished_at=now() WHERE id=$1 AND status IN ('pending','running') RETURNING status,verdict`, id, status, reason).
		Scan(&finalStatus, &verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // 重放不得覆盖后来的结论
	}
	if err != nil {
		return err
	}
	if finalStatus != "completed" {
		return tx.Commit()
	}
	switch verdict {
	case verifyVerdictVerified:
		if _, err := tx.Exec(`UPDATE findings SET validation='verified' WHERE id=$1`, findingID); err != nil {
			return err
		}
	case verifyVerdictRefuted:
		if _, err := tx.Exec(`UPDATE findings SET validation='refuted' WHERE id=$1`, findingID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecoverFindingVerifications 把上次进程遗留的 pending/running 复核封存为 failed。
// 与 RecoverFindingRetests 同构：不是「什么都不做」，因为遗留状态会让面板永远转圈。
func (d *DB) RecoverFindingVerifications() error {
	_, err := d.Exec(`UPDATE finding_verifications SET status='failed', error='` + verifyServiceRestartReason + `', finished_at=now()
WHERE status IN ('pending','running')`)
	return err
}

// FindingValidationOf 读出单条发现的复核态（空串按 unverified 计）。
func (d *DB) FindingValidationOf(id int64) (string, error) {
	var v string
	err := d.QueryRow(`SELECT COALESCE(validation,'') FROM findings WHERE id=$1`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if v == "" {
		v = FindingUnverified
	}
	return v, nil
}
