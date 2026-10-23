package db

import "time"

// LLM failover health: one row per profile tracking its circuit-breaker state.
// The authoritative copy lives in memory (llmpool.Registry); this table only
// survives a restart so a cooling-off window isn't silently reset by one.

// LLMHealth is one profile's circuit-breaker state.
type LLMHealth struct {
	ProfileID int64      `json:"profile_id"`
	Fails     int        `json:"fails"`      // consecutive failures; cleared on success
	Trips     int        `json:"trips"`      // total trips, drives the backoff ladder
	OpenUntil *time.Time `json:"open_until"` // nil/past = closed (healthy)
	LastError string     `json:"last_error"`
	LastAt    time.Time  `json:"last_at"`
}

// LoadLLMHealth returns the profiles still in an UNEXPIRED cooling-off window.
// Expired rows are deliberately skipped: after a restart a profile that has
// finished cooling should be treated as healthy again and re-probed on its next
// call, not resurrected as broken.
func (d *DB) LoadLLMHealth() ([]LLMHealth, error) {
	rows, err := d.Query(`SELECT profile_id,fails,trips,open_until,COALESCE(last_error,''),last_at
FROM llm_profile_health WHERE open_until IS NOT NULL AND open_until > now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMHealth
	for rows.Next() {
		var h LLMHealth
		if err := rows.Scan(&h.ProfileID, &h.Fails, &h.Trips, &h.OpenUntil, &h.LastError, &h.LastAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SaveLLMHealth upserts one profile's circuit-breaker state.
//
// 写入用 INSERT ... SELECT ... WHERE EXISTS，不要退回直接 INSERT:
// 这条写发生在失败路径上、而且是异步的，ProfileID 可能来自一个「配置刚被删掉」的
// 旧轮询链快照。直接 INSERT 会撞 llm_profile_health.profile_id 的外键，日志里刷
// 「熔断状态落库失败」(线上 8 次)，而那条状态对已删除的配置本来也没有意义。
// EXISTS 把「存在性检查」和「写入」放进同一条语句，不会与删除竞争。
func (d *DB) SaveLLMHealth(h LLMHealth) error {
	_, err := d.Exec(`
INSERT INTO llm_profile_health(profile_id,fails,trips,open_until,last_error,last_at)
SELECT $1,$2,$3,$4,$5,now() WHERE EXISTS (SELECT 1 FROM llm_profiles WHERE id=$1)
ON CONFLICT (profile_id) DO UPDATE SET
  fails=EXCLUDED.fails, trips=EXCLUDED.trips, open_until=EXCLUDED.open_until,
  last_error=EXCLUDED.last_error, last_at=now()`,
		h.ProfileID, h.Fails, h.Trips, h.OpenUntil, h.LastError)
	return err
}

// ClearLLMHealth drops one profile's state (manual "recover now" from the UI).
func (d *DB) ClearLLMHealth(profileID int64) error {
	_, err := d.Exec(`DELETE FROM llm_profile_health WHERE profile_id=$1`, profileID)
	return err
}
