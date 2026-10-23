package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type recordingToolUsage struct {
	rows []*db.ToolUsage
	err  error
}

func (r *recordingToolUsage) InsertToolUsage(row *db.ToolUsage) error {
	copy := *row
	r.rows = append(r.rows, &copy)
	return r.err
}

func TestMeterToolRecordsAttributionAndDelegates(t *testing.T) {
	calls := 0
	base := actool.Build(actool.Spec{
		Name: "record_fact",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			calls++
			return actool.Result{}, nil
		},
	})
	recorder := &recordingToolUsage{}
	ri := agent.RunInfo{TaskID: 42, ExplorationID: 8, IntentID: 9, SessionID: "session-1"}
	wrapped := meterTool(base, recorder, "record_fact", "worker", ri)

	if _, err := wrapped.Call(context.Background(), json.RawMessage(`{"secret":"not persisted"}`), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("delegate calls: want 1, got %d", calls)
	}
	if len(recorder.rows) != 1 {
		t.Fatalf("usage rows: want 1, got %d", len(recorder.rows))
	}
	got := recorder.rows[0]
	if got.ToolKey != "record_fact" || got.AgentKey != "worker" || got.TaskID != 42 ||
		got.ExplorationID != 8 || got.IntentID != 9 || got.SessionID != "session-1" {
		t.Fatalf("unexpected attribution: %+v", got)
	}
}

// 错误标记必须落账：否则「工具错误率」只能靠 activity，而 activity 会随任务数据
// 一起被清理——线上正是这样把 47374 次调用的错误信息丢干净的。
func TestMeterToolRecordsError(t *testing.T) {
	base := actool.Build(actool.Spec{
		Name: "http_request",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Errorf("boom"), nil
		},
	})
	recorder := &recordingToolUsage{}
	wrapped := meterTool(base, recorder, "http_request", "worker", agent.RunInfo{})
	if _, err := wrapped.Call(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(recorder.rows) != 1 || !recorder.rows[0].IsError {
		t.Fatalf("IsError not recorded for an error result: %+v", recorder.rows)
	}

	// handler 直接返回 error（网络失败、panic 之前的提前返回）同样算错误。
	base2 := actool.Build(actool.Spec{
		Name: "http_request",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Result{}, errors.New("dial tcp: timeout")
		},
	})
	recorder2 := &recordingToolUsage{}
	wrapped2 := meterTool(base2, recorder2, "http_request", "worker", agent.RunInfo{})
	if _, err := wrapped2.Call(context.Background(), nil, nil); err == nil {
		t.Fatal("want the handler error surfaced to the caller")
	}
	if len(recorder2.rows) != 1 || !recorder2.rows[0].IsError {
		t.Fatalf("IsError not recorded for a handler error: %+v", recorder2.rows)
	}
}

func TestMeterToolFailureDoesNotBreakInvocation(t *testing.T) {
	calls := 0
	base := actool.Build(actool.Spec{
		Name: "list_facts",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			calls++
			return actool.Result{}, nil
		},
	})
	recorder := &recordingToolUsage{err: errors.New("ledger unavailable")}
	wrapped := meterTool(base, recorder, "list_facts", "worker", agent.RunInfo{})

	if _, err := wrapped.Call(context.Background(), nil, nil); err != nil {
		t.Fatalf("metering error leaked into tool call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("delegate calls: want 1, got %d", calls)
	}
}
