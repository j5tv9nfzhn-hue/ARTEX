package db

// ToolUsage is one catalog tool invocation. It stores attribution dimensions only:
// tool arguments and results are deliberately excluded from the ledger.
type ToolUsage struct {
	ToolKey       string `json:"tool_key"`
	AgentKey      string `json:"agent_key"`
	TaskID        int64  `json:"task_id"`
	ExplorationID int64  `json:"exploration_id"`
	IntentID      int64  `json:"intent_id"`
	SessionID     string `json:"session_id"`
	// IsError 标记这次调用是否以错误收场（工具返回 IsError，或 handler 直接返回
	// error）。错误率是判断「工具好不好用」的唯一客观指标，必须写在账本里：
	// activity 会随任务数据一起被清理，账本不该跟着丢。
	IsError bool `json:"is_error"`
	// ErrorClass 是失败的归因类别（见 agent 包的 9 类，含任务 27 新增的 quota）。
	// 空串表示非错误或未归因。
	// 有它之后，「Bash 错误率高」才能拆成「多少是写法不对、多少是环境缺依赖」。
	ErrorClass string `json:"error_class,omitempty"`
}

// InsertToolUsage appends one ledger row. Runtime callers treat metering as
// best-effort so a statistics failure never interrupts the tool itself.
func (d *DB) InsertToolUsage(u *ToolUsage) error {
	_, err := d.Exec(`
INSERT INTO tool_usage(tool_key, agent_key, task_id, exploration_id, intent_id, session_id, is_error, error_class)
VALUES ($1, NULLIF($2,''), $3, $4, $5, NULLIF($6,''), $7, $8)`,
		u.ToolKey, u.AgentKey, nullIfZero(u.TaskID), nullIfZero(u.ExplorationID),
		nullIfZero(u.IntentID), u.SessionID, u.IsError, u.ErrorClass)
	return err
}

// ToolUsageCounts returns invocation totals keyed by catalog tool key. Entries
// without calls are absent; API callers merge the result into the tools catalog.
func (d *DB) ToolUsageCounts() (map[string]int, error) {
	rows, err := d.Query(`
SELECT tool_key, COUNT(*)
FROM tool_usage
GROUP BY tool_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var calls int
		if err := rows.Scan(&key, &calls); err != nil {
			return nil, err
		}
		out[key] = calls
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	for _, aggregate := range archived {
		for key, calls := range aggregate.Tools {
			out[key] += calls
		}
	}
	return out, nil
}
