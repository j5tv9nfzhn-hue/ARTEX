package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 技能沉淀的暂存层。
//
// 为什么需要暂存而不是直接写盘：技能是 Agent 自己的程序性记忆，写错的
// 代价是长期的——一个描述含糊的技能会被反复加载，把错误流程固化下来。
// 直接落盘等于让它进入之后所有会话，且没有可回溯的审阅记录。
//
// 这里的模型是"提议"：Agent 产出提议，落到 skill_proposals，人工审阅后才
// 写入 skills/ 目录。审阅记录本身也被保留，所以能回答"这条技能是什么
// 时候、依据什么被启用的"。

const (
	// SkillProposalPending 等待人工审阅。
	SkillProposalPending = "pending"
	// SkillProposalApplied 已审阅通过。
	SkillProposalApplied = "applied"
	// SkillProposalRejected 已审阅否决。
	SkillProposalRejected = "rejected"
)

// ErrSkillProposalNotFound 表示提议不存在。
var ErrSkillProposalNotFound = errors.New("skill proposal not found")

// ErrSkillProposalConflict 表示提议已被审阅过，不允许重复处理。
var ErrSkillProposalConflict = errors.New("skill proposal already reviewed")

// skillProposalSchema 用 @TS@ 占位时间戳类型，因为这段 DDL 由 Go 拼装且
// 两种引擎都会执行：PostgreSQL 的 TIMESTAMPTZ 在 SQLite 里不合法。
const skillProposalSchema = `CREATE TABLE IF NOT EXISTS skill_proposals (
    id          TEXT PRIMARY KEY,
    ts          @TS@ NOT NULL DEFAULT @NOW@,
    -- skill 是目标技能名；file 为空表示 SKILL.md，非空表示技能内的支撑文件。
    skill       TEXT NOT NULL,
    file        TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    content     TEXT NOT NULL,
    -- origin 区分提议来源：'agent' 自主沉淀，'curator' 整合复盘，'user' 人工。
    origin      TEXT NOT NULL DEFAULT 'agent',
    -- evidence 是提议依据，指向具体的 activity / finding / retest 记录。
    -- 没有依据的提议仍可提交，但审阅者必须能从空值看出它是空口无凭的。
    evidence    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending',
    -- lint 存审阅时应看到的告警，分号分隔。
    lint        TEXT NOT NULL DEFAULT '',
    reviewed_ts @TS@,
    note        TEXT NOT NULL DEFAULT ''
)`

func skillProposalSchemaSQL() string {
	ts, now := "TIMESTAMPTZ", "now()"
	if engineIsSQLite() {
		// SQLite only accepts a constant, CURRENT_TIMESTAMP, or a
		// parenthesised expression as a column default -- a bare function call
		// is a syntax error. The parenthesised strftime form is also what the
		// translated schema uses everywhere else, so both engines store the
		// same timestamp shape.
		ts = "TIMESTAMP"
		now = `(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))`
	}
	out := strings.ReplaceAll(skillProposalSchema, "@TS@", ts)
	return strings.ReplaceAll(out, "@NOW@", now)
}

func (d *DB) ensureSkillProposalTable() error {
	_, err := d.Exec(skillProposalSchemaSQL())
	return err
}

// SkillProposal 是一条待审阅的技能写入提议。
type SkillProposal struct {
	ID     string
	Skill  string
	File   string
	Action string
	// Content 是完整的新内容，不是补丁：审阅者需要看到落地后的完整样子，
	// 而不是自己拼装前后差异。
	Content string
	Origin  string
	// Evidence 记录提议依据的来源标识，例如 "task=12,retest=45"。
	Evidence string
	Status   string
	Lint     string
	Note     string
}

// skillProposalCols 是读取列清单。
const skillProposalCols = `id,skill,file,action,content,origin,evidence,status,lint,note`

// LintWarning 是一条技能内容告警。告警从不阻止写入，只呈现给审阅者。
type LintWarning struct {
	Rule   string
	Detail string
}

var (
	// 事件编号：正文里密集出现说明写成了事故日志而非可复用规则。
	issueRefRe = regexp.MustCompile(`(?i)\b(?:PR|issue|#)\s?\d{3,}\b`)
	// 日期戳：教训应当脱离时间也成立。
	dateStampRe = regexp.MustCompile(`\b20\d{2}[-/]\d{1,2}[-/]\d{1,2}\b`)

	// 正文上限：超过后一次加载会在整个会话常驻，成本开始超过收益。
	maxBodyChars = 24 * 1000
	// 引用文件上限：超过说明技能已长成文档仓库。
	maxRefFiles = 60
	// 密集度阈值：正常技能引用一两个编号是合理的。
	denseN = 8
)

// LintSkillContent 对提议内容跑告警规则。
//
// 刻意只告警不阻断：规则是启发式的，阻断只会让 Agent 学会绕过规则而不是
// 写出更好的内容。告警该出现在审阅者眼前，而不是挡在写入路径上。
func LintSkillContent(content string) []LintWarning {
	var out []LintWarning
	if n := len([]rune(content)); n > maxBodyChars {
		out = append(out, LintWarning{
			Rule:   "oversized-body",
			Detail: fmt.Sprintf("正文 %d 字符，超过 %d 上限；应把细节拆进 references/", n, maxBodyChars),
		})
	}
	if n := len(issueRefRe.FindAllString(content, -1)); n >= denseN {
		out = append(out, LintWarning{
			Rule:   "incident-log-shape",
			Detail: fmt.Sprintf("正文出现 %d 处 PR/issue 编号；技能应记录可泛化的规则，而非事故叙述", n),
		})
	}
	if n := len(dateStampRe.FindAllString(content, -1)); n >= denseN {
		out = append(out, LintWarning{
			Rule:   "incident-log-shape",
			Detail: fmt.Sprintf("正文出现 %d 处日期；规则应脱离时间也成立", n),
		})
	}
	return out
}

// skillNameRe 沿用 create_skill 的约束：小写字母开头，仅字母/数字/连字符。
var skillNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ProposeSkill 登记一条技能写入提议，不落盘。
//
// id 由调用方提供以保证幂等：同一个提议被重复提交（例如会话重试）不会
// 产生第二条记录，也不会让审阅者看到同一份内容的两份拷贝。
func (d *DB) ProposeSkill(ctx context.Context, p SkillProposal) (*SkillProposal, error) {
	if err := d.ensureSkillProposalTable(); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, errors.New("提议必须有 id")
	}
	if !skillNameRe.MatchString(p.Skill) {
		return nil, fmt.Errorf("技能名不合法: %q", p.Skill)
	}
	if strings.TrimSpace(p.Content) == "" {
		return nil, errors.New("提议内容为空")
	}
	if p.Origin == "" {
		p.Origin = "agent"
	}
	if p.Status == "" {
		p.Status = SkillProposalPending
	}

	warnings := LintSkillContent(p.Content)
	lints := make([]string, 0, len(warnings))
	for _, w := range warnings {
		lints = append(lints, w.Rule+": "+w.Detail)
	}

	_, err := d.ExecContext(ctx, `INSERT INTO skill_proposals
(id,skill,file,action,content,origin,evidence,status,lint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (id) DO NOTHING`,
		p.ID, p.Skill, p.File, p.Action, p.Content, p.Origin, p.Evidence,
		p.Status, strings.Join(lints, "; "))
	if err != nil {
		return nil, err
	}
	return d.GetSkillProposal(ctx, p.ID)
}

// ListSkillProposals 列出提议，status 为空时只返回待审阅的。
func (d *DB) ListSkillProposals(ctx context.Context, status string, limit int) ([]SkillProposal, error) {
	if err := d.ensureSkillProposalTable(); err != nil {
		return nil, err
	}
	if status == "" {
		status = SkillProposalPending
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx,
		`SELECT `+skillProposalCols+` FROM skill_proposals WHERE status=$1 ORDER BY ts DESC, id LIMIT $2`,
		status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillProposal{}
	for rows.Next() {
		var p SkillProposal
		if err := rows.Scan(&p.ID, &p.Skill, &p.File, &p.Action, &p.Content,
			&p.Origin, &p.Evidence, &p.Status, &p.Lint, &p.Note); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetSkillProposal 按 id 取单条提议。
func (d *DB) GetSkillProposal(ctx context.Context, id string) (*SkillProposal, error) {
	if err := d.ensureSkillProposalTable(); err != nil {
		return nil, err
	}
	var p SkillProposal
	err := d.QueryRowContext(ctx,
		`SELECT `+skillProposalCols+` FROM skill_proposals WHERE id=$1`, id).
		Scan(&p.ID, &p.Skill, &p.File, &p.Action, &p.Content,
			&p.Origin, &p.Evidence, &p.Status, &p.Lint, &p.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSkillProposalNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ReviewSkillProposal 记录一条提议的审阅结论。
//
// 刻意不在这里写文件：审阅与落盘分成两步，中间可以再校验一次（技能名是否
// 已被另一条提议占用），也避免"审阅通过但写盘失败"留下数据库与磁盘不一致
// 的状态。写盘由 server 层在拿到 applied 之后执行。
func (d *DB) ReviewSkillProposal(ctx context.Context, id, decision, note string) (*SkillProposal, error) {
	if decision != SkillProposalApplied && decision != SkillProposalRejected {
		return nil, fmt.Errorf("审阅结论只能是 %q 或 %q", SkillProposalApplied, SkillProposalRejected)
	}
	if err := d.ensureSkillProposalTable(); err != nil {
		return nil, err
	}
	res, err := d.ExecContext(ctx,
		`UPDATE skill_proposals SET status=$1, note=$2, reviewed_ts=now()
WHERE id=$3 AND status=$4`,
		decision, note, id, SkillProposalPending)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 要么不存在，要么已被审阅过——两种情况必须区分开。
		if _, err := d.GetSkillProposal(ctx, id); err != nil {
			return nil, err
		}
		return nil, ErrSkillProposalConflict
	}
	return d.GetSkillProposal(ctx, id)
}
