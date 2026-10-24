package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// 线索缓存（Task & Clue Cache）。
//
// ── 为什么要有它 ─────────────────────────────────────────────────────────────
//
// 2026-10-24 的实测：一条意图跑 94–177 轮、单个 worker 的输入常年爬到
// 20–50K/100K token，而整条任务最终只落了两个低危。钱都花在「反复重新发现
// 已知事实」上：上层把上一轮的观察塞回聊天上下文，下层 worker 再从零复述一遍；
// 上下文一压缩，前面找到的端点、密钥、报错就没了，于是再去扫一遍同样的地方。
//
// MazeRunner（arXiv:2608.14216）把这件事归因为 contextual amnesia，并用一张
// persistent Task & Clue Cache 解决：子任务完成率 47.7% vs 36.2%，去掉缓存
// 掉到 58.8% 且开销 +27%。
//
// ── 这里存什么 ───────────────────────────────────────────────────────────────
//
// 只存「以后还会被别的意图用到」的结构化材料，不存流水账：
//
//	asset       资产（域名/IP/端口/服务指纹）
//	endpoint    端点（方法 + 路径 + 参数名）
//	param       参数（名字 + 位置 + 可疑点）
//	secret      JS/配置里泄露的密钥、令牌、内网地址
//	error       异常报错（指纹化：同一类报错去重成一条）
//	credential  凭据片段（用户名/角色/令牌的**存在性**，不存明文口令）
//	config      后台/框架配置暴露（Swagger、actuator、.git、调试开关）
//	note        其它可复用的结论
//
// 每行都带来源（哪个工具）、产出者（哪条 work）、置信度（observed/inferred），
// 于是 worker 拿到的不是「模型上一轮说过什么」，而是「这条结论是谁、用什么
// 工具、在哪看到的」。
const clueCacheSchema = `
CREATE TABLE IF NOT EXISTS clues (
    id             @SERIAL@ PRIMARY KEY,
    task_id        BIGINT NOT NULL DEFAULT 0,
    exploration_id BIGINT NOT NULL DEFAULT 0,
    intent_id      BIGINT NOT NULL DEFAULT 0,
    kind           TEXT NOT NULL DEFAULT 'note',
    key            TEXT NOT NULL DEFAULT '',
    value          TEXT NOT NULL DEFAULT '',
    source         TEXT NOT NULL DEFAULT '',
    confidence     TEXT NOT NULL DEFAULT 'inferred',
    produced_by    TEXT NOT NULL DEFAULT '',
    seen_count     INTEGER NOT NULL DEFAULT 1,
    used_count     INTEGER NOT NULL DEFAULT 0,
    created_at     @TS@ NOT NULL DEFAULT @NOW@,
    last_seen_at   @TS@ NOT NULL DEFAULT @NOW@,
    last_used_at   @TS@
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_clues_uniq ON clues(task_id, kind, key);
CREATE INDEX IF NOT EXISTS idx_clues_task ON clues(task_id, kind, id);
`

// EnsureClueCacheTables 建线索缓存表（幂等，manager 启动时调用）。
func (d *DB) EnsureClueCacheTables() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(pgTypeSQL(clueCacheSchema)); err != nil {
		return err
	}
	return tx.Commit()
}

// ClueKind 是缓存条目的分类。故意保持很短：分类是检索维度，不是流程阶段。
type Clue struct {
	ID            int64     `json:"id"`
	TaskID        int64     `json:"task_id"`
	ExplorationID int64     `json:"exploration_id"`
	IntentID      int64     `json:"intent_id"`
	Kind          string    `json:"kind"`
	Key           string    `json:"key"`
	Value         string    `json:"value"`
	Source        string    `json:"source"`
	Confidence    string    `json:"confidence"`
	ProducedBy    string    `json:"produced_by"`
	SeenCount     int       `json:"seen_count"`
	UsedCount     int       `json:"used_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// ClueInput 是一条待写入的线索。
type ClueInput struct {
	Kind       string
	Key        string
	Value      string
	Source     string
	Confidence string
	ProducedBy string
	IntentID   int64
}

// ClueStore 是绑定到某个探索库的线索缓存句柄。
type ClueStore struct {
	db    *DB
	expID int64
}

// Clues 返回线索缓存句柄（与 exploration 绑定，便于记录来源）。
func (s *ExplorationStore) Clues() *ClueStore {
	if s == nil {
		return nil
	}
	return &ClueStore{db: s.db, expID: s.expID}
}

// 允许的 kind。未知分类落到 note，而不是拒绝——worker 写进来的是材料，不是
// 协议消息，格式错误不该让它整条写回失败。
var clueKinds = map[string]bool{
	"asset": true, "endpoint": true, "param": true, "secret": true,
	"error": true, "credential": true, "config": true, "note": true,
}

const (
	clueKeyMax   = 300
	clueValueMax = 4000
)

// NormalizeClue 把一条线索收成可比对的形状：分类白名单、键去噪声、值截断。
//
// 键的归一化决定了去重质量：同一个端点写成 "https://HOST/api/v1/x?b=2&a=1"
// 与 "/api/v1/x?a=1&b=2" 必须落到同一行，否则「不塞聊天上下文」的收益会被
// 大量重复行吃掉。保守做法：小写、去首尾空白、去掉 scheme+host（主机信息放
// value 里）、折叠内部空白。
func NormalizeClue(in ClueInput) ClueInput {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !clueKinds[kind] {
		kind = "note"
	}
	key := strings.ToLower(strings.TrimSpace(in.Key))
	if key == "" {
		// 没给键就用值的前 120 字做键：宁可粗一点，也不让条目因为缺 key 丢掉。
		key = strings.ToLower(strings.TrimSpace(in.Value))
		if len([]rune(key)) > 120 {
			key = string([]rune(key)[:120])
		}
	}
	key = collapseClueSpace(key)
	if len([]rune(key)) > clueKeyMax {
		key = string([]rune(key)[:clueKeyMax])
	}
	value := strings.TrimSpace(in.Value)
	if len([]rune(value)) > clueValueMax {
		value = string([]rune(value)[:clueValueMax]) + "…(截断)"
	}
	conf := strings.ToLower(strings.TrimSpace(in.Confidence))
	if conf != "observed" {
		conf = "inferred"
	}
	return ClueInput{
		Kind:       kind,
		Key:        key,
		Value:      value,
		Source:     truncClueField(in.Source, 80),
		Confidence: conf,
		ProducedBy: truncClueField(in.ProducedBy, 80),
		IntentID:   in.IntentID,
	}
}

func collapseClueSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncClueField(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Put 写入一条线索。同 (task, kind, key) 已存在时只 update：刷新 value/source/
// 置信度并 seen_count+1——重复观察本身是有价值的信号（说明这个点被反复撞到），
// 但不该让表无限膨胀。
//
// 返回 (id, 是否新建)。
func (s *ClueStore) Put(taskID int64, in ClueInput) (int64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, fmt.Errorf("线索缓存不可用")
	}
	in = NormalizeClue(in)
	if in.Value == "" {
		return 0, false, fmt.Errorf("线索内容为空")
	}
	var existing int64
	err := s.db.QueryRow(
		`SELECT id FROM clues WHERE task_id=$1 AND kind=$2 AND key=$3`, taskID, in.Kind, in.Key).Scan(&existing)
	if err == nil {
		_, uerr := s.db.Exec(
			`UPDATE clues SET value=$1, source=$2, confidence=$3, produced_by=$4, intent_id=$5,
			        seen_count=seen_count+1, last_seen_at=`+sqlNowExpr()+`
			  WHERE id=$6`,
			in.Value, in.Source, in.Confidence, in.ProducedBy, in.IntentID, existing)
		return existing, false, uerr
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	var id int64
	if err := s.db.QueryRow(
		`INSERT INTO clues(task_id, exploration_id, intent_id, kind, key, value, source, confidence, produced_by)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		taskID, s.expID, in.IntentID, in.Kind, in.Key, in.Value, in.Source, in.Confidence, in.ProducedBy,
	).Scan(&id); err != nil {
		// 并发写同一条线索：唯一索引冲突时退化成 update。
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			if err2 := s.db.QueryRow(
				`SELECT id FROM clues WHERE task_id=$1 AND kind=$2 AND key=$3`, taskID, in.Kind, in.Key).Scan(&existing); err2 == nil {
				_, uerr := s.db.Exec(
					`UPDATE clues SET seen_count=seen_count+1, last_seen_at=`+sqlNowExpr()+` WHERE id=$1`, existing)
				return existing, false, uerr
			}
		}
		return 0, false, err
	}
	return id, true, nil
}

// PutMany 批量写入，单条失败不影响其余（返回成功条数与首个错误）。
func (s *ClueStore) PutMany(taskID int64, ins []ClueInput) (int, int, error) {
	created, updated := 0, 0
	var firstErr error
	for _, in := range ins {
		_, isNew, err := s.Put(taskID, in)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if isNew {
			created++
		} else {
			updated++
		}
	}
	return created, updated, firstErr
}

// Query 按分类与关键字检索。match 为空 = 该分类全部；非空时对 key/value 做
// 子串匹配（大小写不敏感）。结果按「最近被看到」倒序。
func (s *ClueStore) Query(taskID int64, kind, match string, limit int) ([]Clue, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("线索缓存不可用")
	}
	k := strings.ToLower(strings.TrimSpace(kind))
	args := []any{taskID}
	where := []string{"task_id=$1"}
	if k != "" && k != "all" {
		args = append(args, k)
		where = append(where, fmt.Sprintf("kind=$%d", len(args)))
	}
	if m := strings.ToLower(strings.TrimSpace(match)); m != "" {
		args = append(args, "%"+m+"%")
		where = append(where, fmt.Sprintf("(key LIKE $%d OR value LIKE $%d)", len(args), len(args)))
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	args = append(args, limit)
	q := `SELECT id, task_id, exploration_id, intent_id, kind, key, value, source, confidence,
	             produced_by, seen_count, used_count, created_at
	      FROM clues WHERE ` + strings.Join(where, " AND ") + `
	      ORDER BY last_seen_at DESC, id DESC LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Clue
	for rows.Next() {
		var c Clue
		if err := rows.Scan(&c.ID, &c.TaskID, &c.ExplorationID, &c.IntentID, &c.Kind, &c.Key,
			&c.Value, &c.Source, &c.Confidence, &c.ProducedBy, &c.SeenCount, &c.UsedCount, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Touch 给一批线索记「被用过一次」。这让人和模型都能看出：哪些线索一直被复用
// （used_count 高 = 真正承重的材料），哪些写完就没人碰（说明当时记的是流水账）。
func (s *ClueStore) Touch(ids []int64) error {
	if s == nil || s.db == nil || len(ids) == 0 {
		return nil
	}
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE clues SET used_count=used_count+1, last_used_at=`+sqlNowExpr()+
		` WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	return err
}

// Stats 返回本任务各分类的条目数，用于把「缓存里有多少东西」直接告诉 planner，
// 而不是让它去翻流水账。
func (s *ClueStore) Stats(taskID int64) (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("线索缓存不可用")
	}
	rows, err := s.db.Query(`SELECT kind, count(*) FROM clues WHERE task_id=$1 GROUP BY kind`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int, 8)
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// sqlNowExpr 是「当前时间」的 SQL 写法（与 schema 里的 @NOW@ 同源）。
func sqlNowExpr() string {
	return `(strftime('%Y-%m-%d %H:%M:%f+00:00','now'))`
}
