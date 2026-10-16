package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TaskEnvContext is the platform's probe of the shared execution environment for
// one task (批次 6 / #10、#19、#22). It is captured at admission by server-side
// probing (server/env_context.go), stored once per task, refreshed on later
// admissions, and injected into worker/planner system prompts so every agent of a
// task reasons about the SAME machine (OS/shell/可用工具/工作目录), instead of
// each run rediscovering it (or assuming the wrong shell).
//
// Env is display/injectable (os/arch/executor/shells/work_dir/data_dir/tools);
// Contract records the checks the platform actually relies on (shell available,
// data dirs writable) and Warnings lists every failed check in operator-readable
// Chinese. A missing row means "no snapshot yet" — callers must tolerate nil.
type TaskEnvContext struct {
	TaskID     int64          `json:"task_id"`
	CapturedAt time.Time      `json:"captured_at"`
	Source     string         `json:"source"`
	Env        map[string]any `json:"env"`
	Contract   map[string]any `json:"contract"`
	Warnings   []string       `json:"warnings"`
}

// UpsertTaskEnvContext stores (or refreshes) the env snapshot for a task. One row
// per task: a later admission overwrites env/contract/captured_at in place. env is
// any JSON-marshalable value (server passes its envSnapshot struct).
func (d *DB) UpsertTaskEnvContext(ctx context.Context, taskID int64, source string,
	env any, contract map[string]any, warnings []string) error {
	if taskID <= 0 {
		return fmt.Errorf("task_env_context: task id required")
	}
	if source == "" {
		source = "probe"
	}
	envB, err := json.Marshal(env)
	if err != nil {
		return err
	}
	contractB, err := json.Marshal(contract)
	if err != nil {
		return err
	}
	warnB, err := json.Marshal(warnings)
	if err != nil {
		return err
	}
	if warnings == nil {
		warnB = []byte("[]")
	}
	_, err = d.ExecContext(ctx, `
INSERT INTO task_env_context(task_id, source, env_json, contract_json, warnings)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT(task_id) DO UPDATE SET
  captured_at=(strftime('%Y-%m-%d %H:%M:%f+00:00','now')),
  source=EXCLUDED.source,
  env_json=EXCLUDED.env_json,
  contract_json=EXCLUDED.contract_json,
  warnings=EXCLUDED.warnings`,
		taskID, source, string(jsonbClean(envB)), string(jsonbClean(contractB)), string(jsonbClean(warnB)))
	return err
}

// GetTaskEnvContext loads one task's snapshot; nil (no error) when the task has
// never been admitted/captured.
func (d *DB) GetTaskEnvContext(ctx context.Context, taskID int64) (*TaskEnvContext, error) {
	var (
		out       TaskEnvContext
		envB      []byte
		contractB []byte
		warnB     []byte
	)
	err := d.QueryRowContext(ctx, `
SELECT task_id, captured_at, source, env_json, contract_json, warnings
FROM task_env_context WHERE task_id=$1`, taskID).
		Scan(&out.TaskID, &out.CapturedAt, &out.Source, &envB, &contractB, &warnB)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(envB, &out.Env)
	_ = json.Unmarshal(contractB, &out.Contract)
	_ = json.Unmarshal(warnB, &out.Warnings)
	return &out, nil
}

// ExplorationEnvDigest returns the compact env digest stamped onto graph nodes
// (#20) for the task that owns this exploration: os / executor / shells / tool
// versions. Nil when the exploration has no task or the task has no snapshot.
func (d *DB) ExplorationEnvDigest(ctx context.Context, explorationID int64) map[string]any {
	if explorationID <= 0 {
		return nil
	}
	var envB []byte
	err := d.QueryRowContext(ctx, `
SELECT tec.env_json FROM task_env_context tec
JOIN tasks t ON t.id = tec.task_id
WHERE t.exploration_id = $1 AND t.deleted_at IS NULL
ORDER BY tec.id LIMIT 1`, explorationID).Scan(&envB)
	if err != nil {
		return nil
	}
	var env map[string]any
	if json.Unmarshal(envB, &env) != nil {
		return nil
	}
	digest := map[string]any{}
	for _, k := range []string{"os", "arch", "executor", "shells", "tools", "work_dir"} {
		if v, ok := env[k]; ok {
			digest[k] = v
		}
	}
	if len(digest) == 0 {
		return nil
	}
	return digest
}
