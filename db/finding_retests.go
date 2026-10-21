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

const FindingRetestAgentKey = "retester"

// 复测终止原因存入 finding_retests.error，并显示为复测面板 item.error。
// 与 server/conversations.go 的 convRetest* 原因统一使用中文。
// Agent 未保存结论时，retestNoConclusionReason 覆盖调用方传入的原因；
// RecoverFindingRetests 用 retestServiceRestartReason 封存服务重启后未完成的复测。
// 这些常量不含单引号，可安全插入现有 SQL 字面量位置。
const (
	retestNoConclusionReason   = "Agent 未保存复测结论，请检查对话后重新复测"
	retestServiceRestartReason = "服务重启导致复测中断，请重新启动"
)

var ErrRetestNotRunning = errors.New("本次复测已结束或尚未开始，请从漏洞详情发起新的复测")

// FindingRetest is an immutable historical test once its conversation turn ends.
// Snapshot is only loaded for the agent, never sent with the history list.
type FindingRetest struct {
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
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
}

const retestCols = `id, finding_id, conversation_id, status, verdict, notes, summary, evidence, error, created_at, started_at, finished_at`

// ActiveFindingRetest is the small status payload polled by the findings list.
// Finding IDs use the same string representation as the findings API.
type ActiveFindingRetest struct {
	ID             int64  `json:"id"`
	FindingID      int64  `json:"finding_id,string"`
	ConversationID int64  `json:"conversation_id"`
	Status         string `json:"status"`
}

func (d *DB) ListActiveFindingRetests(ctx context.Context) ([]ActiveFindingRetest, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, finding_id, conversation_id, status FROM finding_retests
	WHERE status IN ('pending','running') AND conversation_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ActiveFindingRetest{}
	for rows.Next() {
		var item ActiveFindingRetest
		if err := rows.Scan(&item.ID, &item.FindingID, &item.ConversationID, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRetest(row interface{ Scan(...any) error }) (*FindingRetest, error) {
	r := &FindingRetest{}
	err := row.Scan(&r.ID, &r.FindingID, &r.ConversationID, &r.Status, &r.Verdict, &r.Notes,
		&r.Summary, &r.Evidence, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// CreateFindingRetest atomically snapshots the source, creates its conversation
// and persists the first message. A finding row lock deduplicates simultaneous
// clicks across clients; an existing active run is returned without dispatching.
//
// SQLite 没有行锁：写事务本身即库级独占，因此这里不加任何子句；
// 返回空串。去重之所以仍然成立，是因为 SQLite 侧的 DSN 带了 _txlock=immediate
// （见 config/engine.go）：事务从 BEGIN 就持有库级写锁，而不是等第一条写语句
// 才升级，因此「锁行 → 查活动复测 → 插入」这一段没有交错窗口，也避免了
// deferred 事务升级写锁时的 SQLITE_BUSY_SNAPSHOT。
func (d *DB) CreateFindingRetest(ctx context.Context, findingID int64, notes string) (*FindingRetest, *Conversation, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback()
	title, snapshot, err := retestSnapshotTx(ctx, tx, findingID)
	if err != nil {
		return nil, nil, false, err
	}
	r, err := scanRetest(tx.QueryRowContext(ctx, `SELECT `+retestCols+` FROM finding_retests WHERE finding_id=$1 AND status IN ('pending','running')`, findingID))
	if err != nil {
		return nil, nil, false, err
	}
	if r != nil {
		return r, nil, false, nil
	}
	// Keep the title within the same limit as ordinary conversations.
	if runes := []rune(title); len(runes) > 100 {
		title = string(runes[:100])
	}
	c, err := scanConv(tx.QueryRowContext(ctx, `INSERT INTO conversations(agent_key,title) VALUES ($1,$2) RETURNING `+convCols,
		FindingRetestAgentKey, fmt.Sprintf("复测 #%d · %s", findingID, title)))
	if err != nil {
		return nil, nil, false, err
	}
	r, err = scanRetest(tx.QueryRowContext(ctx, `INSERT INTO finding_retests(finding_id,conversation_id,notes,snapshot) VALUES ($1,$2,$3,$4) RETURNING `+retestCols,
		findingID, c.ID, strings.TrimSpace(notes), snapshot))
	if err != nil {
		return nil, nil, false, err
	}
	msg := r.InitialMessage()
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail) VALUES ($1,$2,'user',$3,$4)`,
		c.ID, FindingRetestAgentKey, fmt.Sprintf("请复测漏洞 #%d", findingID), msg)
	if err != nil {
		return nil, nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	return r, &c, true, nil
}

// retestSnapshotTx 读出复测快照需要的全部数据，并在 Go 侧拼成
// {"finding": {…}, "assets": [{…}], "constraints": [{…}]}。
//
// 返回值分别是会话标题与快照字节；finding 不存在时返回 sql.ErrNoRows
// （server/finding_retests.go 依赖 errors.Is 把它翻译成 404）。
//
// ── 为什么不再是一条 SQL ────────────────────────────────────────────────────
//
// 原来是这么一条语句：
//
//	SELECT COALESCE(NULLIF(f.name,''), NULLIF(f.vulnclass,''), '未分类'),
//	       jsonb_build_object('finding', to_jsonb(f),
//	        'assets',      COALESCE((SELECT jsonb_agg(to_jsonb(a)) FROM assets a
//	                                  WHERE f.asset_ids @> to_jsonb(ARRAY[a.id])), '[]'::jsonb),
//	        'constraints', COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM task_constraints c
//	                                  JOIN tasks t ON t.exploration_id=c.exploration_id
//	                                  WHERE t.id=f.task_id), '[]'::jsonb))
//	FROM findings f WHERE f.id=$1 FOR UPDATE OF f
//
// 整行序列化由 to_jsonb(行) 完成，聚合由 jsonb_agg 完成；SQLite 两者都没有：
// to_jsonb 没有「展开一行」的通用等价物，jsonb_agg 聚合的又是已经算好的 jsonb
// 值，而整行在那一刻还不是值。所以快照改为在 Go 里组装，两个引擎共用本文件
// 这一份实现。逐列 json_object 的替代方案为什么不采用，见 db/rowjson.go 开头。
//
// ── 事务边界没有变 ──────────────────────────────────────────────────────────
//
// 三条语句与原来那条语句在同一个 tx 内按顺序执行，因此看到的是同一份数据：
// PostgreSQL 侧第一条带 FOR UPDATE OF f，其余两条在同一个事务里只能读到已被
// 锁住的那一行；SQLite 侧没有行锁，但 DSN 的 _txlock=immediate 让事务从 BEGIN
// 起就持有库级写锁，整个快照期间不可能有别的写入者进来。原来的单条语句本来
// 也是一次一致性读，拆成三条并不引入中间态。
//
// ── 一处行为变化：聚合顺序 ──────────────────────────────────────────────────
//
// 原来的 jsonb_agg 没有 ORDER BY，数组顺序由执行计划决定（实际上是不确定的）。
// 现在两条子查询都加了 ORDER BY a.id / c.id，让快照里的资产与约束顺序稳定。
// 这是把「本来就不确定」变成「确定」，不改变任何一条记录是否出现。
func retestSnapshotTx(ctx context.Context, tx *sql.Tx, findingID int64) (string, []byte, error) {
	// 1) 锁住 findings 行，同时取出标题与整行内容。
	//    标题表达式与原语句逐字相同，放在 SQL 里而不是 Go 里，是因为它必须与
	//    读到的正是同一行；SELECT f.* 则是 to_jsonb(f) 的等价物（列增删自动跟随
	//    schema），列名由 rows.Columns() 给出，见 db/rowjson.go。
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(NULLIF(f.name,''), NULLIF(f.vulnclass,''), '未分类'), f.*
FROM findings f WHERE f.id=$1`, findingID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	title, finding, err := scanRetestSourceRow(rows)
	if err != nil {
		return "", nil, err
	}

	// 2) 该漏洞关联的资产整行。f.asset_ids @> to_jsonb(ARRAY[a.id]) 是
	//    「findings 的资产数组里含有这个资产 id」；SQLite 侧换成
	//    artex_json_contains(f.asset_ids, a.id)，见 findingAssetIDsContainExpr。
	assetRows, err := tx.QueryContext(ctx, `SELECT a.* FROM assets a
JOIN findings f ON f.id=$1 WHERE `+findingAssetIDsContainExpr()+` ORDER BY a.id`, findingID)
	if err != nil {
		return "", nil, err
	}
	assets, err := drainRows(assetRows)
	if err != nil {
		return "", nil, err
	}

	// 3) 该漏洞所属任务的约束整行。t.id=f.task_id 在 f.task_id 为 NULL 时不匹配
	//    任何任务，结果是空集合——与原语句的 COALESCE(…, '[]'::jsonb) 相同。
	//    这条语句两个引擎字面相同，不需要方言分支。
	constRows, err := tx.QueryContext(ctx, `SELECT c.* FROM task_constraints c
JOIN tasks t ON t.exploration_id=c.exploration_id
JOIN findings f ON f.task_id=t.id WHERE f.id=$1 ORDER BY c.id`, findingID)
	if err != nil {
		return "", nil, err
	}
	constraints, err := drainRows(constRows)
	if err != nil {
		return "", nil, err
	}

	// assets / constraints 已经是各自编码好的 JSON 片段，用 json.RawMessage
	// 原样嵌进外层对象即可，不需要为了「能塞进 map」先解码一遍。空集合由
	// rowsToJSONObjects 保证是 []，因此这两个键永远不会是 null。
	snapshot, err := marshalSnapshot(map[string]any{
		"finding":     finding,
		"assets":      json.RawMessage(assets),
		"constraints": json.RawMessage(constraints),
	})
	if err != nil {
		return "", nil, err
	}
	return title, snapshot, nil
}

// scanRetestSourceRow 读出「标题 + f.*」这一行的结果：第 0 列是标量标题，
// 第 1 列起是 findings 整行。
func scanRetestSourceRow(rows *sql.Rows) (string, map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return "", nil, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", nil, err
		}
		return "", nil, sql.ErrNoRows
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", nil, err
	}
	// 关闭结果集：SQLite 一个连接同一时刻只能有一个活动结果集，不关就没法在
	// 同一事务里继续跑下面两条语句。rows.Close 可重复调用。
	if err := rows.Close(); err != nil {
		return "", nil, err
	}
	title, _ := vals[0].(string) // COALESCE 恒返回 TEXT，断言失败才退化成空串
	return title, rowJSONObject(cols[1:], vals[1:]), nil
}

// findingAssetIDsContainExpr 表达「这条 finding 关联了资产 a.id」。
//
// 调用点固定写成 `FROM assets a JOIN findings f ON f.id=$1 WHERE <本函数>`。
// PostgreSQL 侧是 `f.asset_ids @> to_jsonb(ARRAY[a.id])`；artex_json_contains(col, x)
// 的语义是「col 解析出的数组里是否存在等于 x 的元素」，即
// EXISTS (SELECT 1 FROM json_each(col) WHERE value = x)，与 jsonb 数组包含完全对应
// （db/dialect_arrays.go 里有同样的对照说明）。值作为函数实参绑定，不存在拼进
// SQL 文本的机会。
func findingAssetIDsContainExpr() string {
	return "artex_json_contains(f.asset_ids, a.id)"
}

func (r *FindingRetest) InitialMessage() string {
	msg := fmt.Sprintf("请复测漏洞 #%d。先调用 get_finding_retest_context 读取本会话关联的原始证据与约束，再执行针对性验证，最后调用 record_finding_retest_result 保存结论。", r.FindingID)
	if r.Notes != "" {
		msg += "\n\n本次复测补充说明：\n" + r.Notes
	}
	return msg
}

func (d *DB) ListFindingRetests(findingID int64) ([]*FindingRetest, error) {
	rows, err := d.Query(`SELECT `+retestCols+` FROM finding_retests WHERE finding_id=$1 ORDER BY id DESC`, findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*FindingRetest{}
	for rows.Next() {
		r, err := scanRetest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) FindingRetestForConversation(ctx context.Context, conversationID int64) (*FindingRetest, error) {
	r, err := scanRetest(d.QueryRowContext(ctx, `SELECT `+retestCols+` FROM finding_retests WHERE conversation_id=$1`, conversationID))
	if err != nil || r == nil {
		return r, err
	}
	err = d.QueryRowContext(ctx, `SELECT snapshot FROM finding_retests WHERE id=$1`, r.ID).Scan(&r.Snapshot)
	return r, err
}

// FailPendingRetestForConversation seals a conversation's unfinished retest when
// the runner could not even load it — the retest ID is unknown on that path, so
// the conversation ID is the only handle. Without it a transient read error
// leaves the row 'pending' forever: the findings list keeps showing 复测中 and
// every later 发起复测 is deduped against a run that is not happening, with only
// a process restart (RecoverFindingRetests) able to clear it.
func (d *DB) FailPendingRetestForConversation(conversationID int64, reason string) error {
	_, err := d.Exec(`UPDATE finding_retests SET status='failed', error=$2, finished_at=now()
		WHERE conversation_id=$1 AND status IN ('pending','running')`, conversationID, reason)
	return err
}

func (d *DB) StartFindingRetest(ctx context.Context, id int64) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE finding_retests SET status='running', started_at=now() WHERE id=$1 AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecordFindingRetestResult never accepts a finding ID: ownership comes from the
// runtime conversation. Identical retries are safe; a second verdict is refused.
func (d *DB) RecordFindingRetestResult(ctx context.Context, conversationID int64, verdict, summary, evidence string) error {
	if verdict != "reproduced" && verdict != "fixed" && verdict != "inconclusive" {
		return errors.New("verdict 必须为 reproduced / fixed / inconclusive")
	}
	summary, evidence = strings.TrimSpace(summary), strings.TrimSpace(evidence)
	if summary == "" || evidence == "" {
		return errors.New("summary 与 evidence 不能为空；无法确认时说明实际检查及阻塞原因")
	}
	if len(summary) > 16000 || len(evidence) > 128000 {
		return errors.New("复测结论过长（summary ≤ 16KB，evidence ≤ 128KB）")
	}
	res, err := d.ExecContext(ctx, `UPDATE finding_retests SET verdict=$2,summary=$3,evidence=$4
	WHERE conversation_id=$1 AND status='running' AND (verdict='' OR (verdict=$2 AND summary=$3 AND evidence=$4))`, conversationID, verdict, summary, evidence)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrRetestNotRunning
	}
	return nil
}

// FinishFindingRetest seals the result. Cancellation/failure takes precedence
// over a staged verdict so an interrupted test cannot appear successfully fixed.
// Only a newly completed fixed verdict updates triage, in the same transaction.
func (d *DB) FinishFindingRetest(id int64, status, reason string) error {
	if status != "completed" && status != "failed" && status != "stopped" {
		return errors.New("invalid terminal retest status")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock the finding before the retest, matching creation and cascading deletion.
	var findingID int64
	err = tx.QueryRow(`SELECT f.id FROM findings f WHERE f.id=(SELECT finding_id FROM finding_retests WHERE id=$1)`, id).Scan(&findingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Finding/retest already deleted.
	}
	if err != nil {
		return err
	}
	var finalStatus, verdict string
	err = tx.QueryRow(`UPDATE finding_retests SET
	status=CASE WHEN $2='completed' AND verdict='' THEN 'failed' ELSE $2 END,
	error=CASE WHEN $2='completed' AND verdict='' THEN '`+retestNoConclusionReason+`' ELSE $3 END,
	finished_at=now() WHERE id=$1 AND status IN ('pending','running') RETURNING status,verdict`, id, status, reason).Scan(&finalStatus, &verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // A replay must not overwrite a later manual triage decision.
	}
	if err != nil {
		return err
	}
	// 复核状态机:独立复测「复现」= 这条发现被独立证据确认,升到 verified;
	// 「未修复(inconclusive)」不降级——没结论不等于被推翻,refuted 只由人工判定。
	if finalStatus == "completed" && verdict == "reproduced" {
		if _, err := tx.Exec(`UPDATE findings SET validation='verified' WHERE id=$1`, findingID); err != nil {
			return err
		}
	}
	if finalStatus == "completed" && verdict == "fixed" {
		// 走带通知的版本，与人工在详情页改状态共用同一套语义。
		//
		// 此前这里是裸的 UPDATE：复测判「已修复」时状态确实变了，但配了
		// on_status_change 的渠道完全收不到推送——状态在界面上悄悄变了，
		// 运维要打开平台才知道。状态更新与推送事件必须一起落库，
		// SetFindingStatusTx 内部处理了「状态没变就不登记」等细节。
		// 用 context.Background()：本函数整条都是无 ctx 的旧风格（d.Begin()/
		// tx.QueryRow/tx.Exec），没有可传递的取消信号，硬加一个 ctx 参数会
		// 牵动 server 侧调用点与多处测试，超出本次改动的范围。
		if _, _, _, _, err := SetFindingStatusTx(context.Background(), tx, findingID, FindingFixed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) RecoverFindingRetests() error {
	_, err := d.Exec(`UPDATE finding_retests SET status='stopped', error='` + retestServiceRestartReason + `', finished_at=now() WHERE status IN ('pending','running')`)
	return err
}
