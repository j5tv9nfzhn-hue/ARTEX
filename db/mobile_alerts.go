package db

import (
	"fmt"
	"time"
)

// mobileTablesSchema 建立手机端两张表：告警收件箱与推送令牌。
// 只用到 @SERIAL@ 一个方言占位符，其余列一律显式类型，避免跨库 DDL 差异。
const mobileTablesSchema = `
CREATE TABLE IF NOT EXISTS mobile_alerts (
    id       @SERIAL@ PRIMARY KEY,
    ts       BIGINT NOT NULL,
    level    TEXT NOT NULL,
    kind     TEXT NOT NULL,
    severity TEXT,
    title    TEXT NOT NULL,
    body     TEXT,
    href     TEXT,
    task_id  TEXT,
    read_at  BIGINT
);
CREATE INDEX IF NOT EXISTS idx_mobile_alerts_ts ON mobile_alerts(ts);
CREATE TABLE IF NOT EXISTS mobile_push_tokens (
    id        @SERIAL@ PRIMARY KEY,
    platform  TEXT NOT NULL,
    token     TEXT NOT NULL,
    device    TEXT,
    last_seen BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mobile_push_token ON mobile_push_tokens(platform, token);
`

// EnsureMobileTables 幂等地补齐手机端表结构；调用方在启动时执行一次。
func (d *DB) EnsureMobileTables() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(pgTypeSQL(mobileTablesSchema)); err != nil {
		return err
	}
	return tx.Commit()
}

// MobileAlert 是手机端收件箱的一行。UI 通知原本只存在内存 ring 里（进程重启即丢），
// 这里落库以便回溯、已读与未读计数。
type MobileAlert struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	Level    string `json:"level"`
	Kind     string `json:"kind"`
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Href     string `json:"href,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
	ReadAt   int64  `json:"read_at,omitempty"`
}

// MobilePushToken 是一台已注册设备的推送凭据（APNs / FCM）。
type MobilePushToken struct {
	Platform string `json:"platform"`
	Token    string `json:"token"`
	Device   string `json:"device,omitempty"`
	LastSeen int64  `json:"last_seen"`
}

// InsertMobileAlert 追加一条收件箱记录。尽力而为：写失败只影响回溯，不影响通知本身。
func (d *DB) InsertMobileAlert(a *MobileAlert) error {
	if a == nil || a.Title == "" {
		return nil
	}
	ts := a.TS
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	_, err := d.Exec(`
INSERT INTO mobile_alerts(ts, level, kind, severity, title, body, href, task_id, read_at)
VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULL)`,
		ts, a.Level, a.Kind, a.Severity, a.Title, a.Body, a.Href, a.TaskID)
	return err
}

// ListMobileAlerts 按时间倒序返回收件箱。sinceID>0 表示只要更新的记录；
// level 非空按级别过滤；unreadOnly 只要未读。
func (d *DB) ListMobileAlerts(limit int, sinceID int64, level string, unreadOnly bool) ([]MobileAlert, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `
SELECT id, ts, level, kind, COALESCE(severity,''), title, COALESCE(body,''),
       COALESCE(href,''), COALESCE(task_id,''), COALESCE(read_at,0)
FROM mobile_alerts
WHERE 1=1`
	args := []any{}
	if sinceID > 0 {
		args = append(args, sinceID)
		q += fmt.Sprintf(" AND id > $%d", len(args))
	}
	if level != "" {
		args = append(args, level)
		q += fmt.Sprintf(" AND level = $%d", len(args))
	}
	if unreadOnly {
		q += " AND read_at IS NULL"
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MobileAlert{}
	for rows.Next() {
		var a MobileAlert
		if err := rows.Scan(&a.ID, &a.TS, &a.Level, &a.Kind, &a.Severity, &a.Title,
			&a.Body, &a.Href, &a.TaskID, &a.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MobileAlertUnread 返回未读总数与按级别分布。
func (d *DB) MobileAlertUnread() (int, map[string]int, error) {
	rows, err := d.Query(`
SELECT level, COUNT(*)
FROM mobile_alerts
WHERE read_at IS NULL
GROUP BY level`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	total := 0
	byLevel := map[string]int{}
	for rows.Next() {
		var level string
		var n int
		if err := rows.Scan(&level, &n); err != nil {
			return 0, nil, err
		}
		byLevel[level] = n
		total += n
	}
	return total, byLevel, rows.Err()
}

// MarkMobileAlertsRead 把指定记录标记为已读；重复标记不改动原时间戳。
func (d *DB) MarkMobileAlertsRead(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, err := d.Exec(`UPDATE mobile_alerts SET read_at = $1 WHERE id = $2 AND read_at IS NULL`, now, id); err != nil {
			return err
		}
	}
	return nil
}

// MarkAllMobileAlertsRead 清空未读。
func (d *DB) MarkAllMobileAlertsRead() error {
	_, err := d.Exec(`UPDATE mobile_alerts SET read_at = $1 WHERE read_at IS NULL`, time.Now().UnixMilli())
	return err
}

// RegisterPushToken 注册或刷新一台设备的推送凭据。
func (d *DB) RegisterPushToken(platform, token, device string) error {
	now := time.Now().UnixMilli()
	res, err := d.Exec(`
UPDATE mobile_push_tokens SET device = NULLIF($1,''), last_seen = $2
WHERE platform = $3 AND token = $4`, device, now, platform, token)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	_, err = d.Exec(`
INSERT INTO mobile_push_tokens(platform, token, device, last_seen)
VALUES ($1, $2, NULLIF($3,''), $4)`, platform, token, device, now)
	return err
}

// DeletePushToken 注销一台设备（换机、退出登录时调用）。
func (d *DB) DeletePushToken(token string) error {
	_, err := d.Exec(`DELETE FROM mobile_push_tokens WHERE token = $1`, token)
	return err
}

// ListPushTokens 供推送发送端枚举目标设备。
func (d *DB) ListPushTokens() ([]MobilePushToken, error) {
	rows, err := d.Query(`
SELECT platform, token, COALESCE(device,''), last_seen
FROM mobile_push_tokens
ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MobilePushToken{}
	for rows.Next() {
		var t MobilePushToken
		if err := rows.Scan(&t.Platform, &t.Token, &t.Device, &t.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
