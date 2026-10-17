package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/skill"
	actool "github.com/Autumn-27/norma/tool"
)

// A minimal disposable ledger, never the configured application database.
func newSkillUsageMeterFixture(t *testing.T) *db.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "skill-usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(`CREATE TABLE skill_usage (
		id INTEGER PRIMARY KEY, skill TEXT NOT NULL, agent_key TEXT, task_id INTEGER,
		exploration_id INTEGER, intent_id INTEGER, session_id TEXT, args_len INTEGER, found BOOLEAN)`)
	if err != nil {
		t.Fatal(err)
	}
	return &db.DB{DB: raw}
}

type skillUsageResultFixture struct {
	actool.CoreTool
	result actool.Result
	err    error
}

func (f *skillUsageResultFixture) Call(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
	return f.result, f.err
}

func TestSkillUsageMeterCanonicalDimensions(t *testing.T) {
	pg := newSkillUsageMeterFixture(t)
	reg := skill.NewRegistry(skill.Skill{Name: "api-recon", Instructions: "fixture"})
	tool := meterSkillTool(reg.Tool(), pg, reg, "worker", agent.RunInfo{TaskID: 17, ExplorationID: 18, IntentID: 19, SessionID: "fixture-session"})
	result, err := tool.Call(context.Background(), json.RawMessage(`{"skill":"api","args":"上下文"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("%v / %s", err, result.Flatten())
	}
	var u db.SkillUsage
	err = pg.QueryRow(`SELECT skill,agent_key,task_id,exploration_id,intent_id,session_id,args_len,found FROM skill_usage`).Scan(
		&u.Skill, &u.AgentKey, &u.TaskID, &u.ExplorationID, &u.IntentID, &u.SessionID, &u.ArgsLen, &u.Found)
	if err != nil {
		t.Fatal(err)
	}
	if u.Skill != "api-recon" || u.AgentKey != "worker" || u.TaskID != 17 || u.ExplorationID != 18 || u.IntentID != 19 || u.SessionID != "fixture-session" || u.ArgsLen != len("上下文") || !u.Found {
		t.Fatalf("unexpected usage: %+v", u)
	}
}

func TestSkillUsageMeterFoundIsNotExecutionSuccess(t *testing.T) {
	for _, goError := range []bool{false, true} {
		pg := newSkillUsageMeterFixture(t)
		reg := skill.NewRegistry(skill.Skill{Name: "api-recon", Instructions: "fixture"})
		fixtureErr := errors.New("fixture call error")
		inner := &skillUsageResultFixture{CoreTool: reg.Tool(), result: actool.Errorf("fixture error result")}
		if goError {
			inner.err = fixtureErr
		}
		tool := meterSkillTool(inner, pg, reg, "worker", agent.RunInfo{})
		result, err := tool.Call(context.Background(), json.RawMessage(`{"name":"api-recon"}`), nil)
		if !result.IsError || (goError && !errors.Is(err, fixtureErr)) || (!goError && err != nil) {
			t.Fatalf("result/error changed: %v / %+v", err, result)
		}
		var found bool
		if err = pg.QueryRow(`SELECT found FROM skill_usage`).Scan(&found); err != nil || !found {
			t.Fatalf("known skill marked missing: %v, %v", found, err)
		}
	}
}

func TestSkillUsageMeterMissingVersusMalformed(t *testing.T) {
	pg := newSkillUsageMeterFixture(t)
	reg := skill.NewRegistry(skill.Skill{Name: "api-recon", Instructions: "fixture"})
	tool := meterSkillTool(reg.Tool(), pg, reg, "worker", agent.RunInfo{})
	result, err := tool.Call(context.Background(), json.RawMessage(`{"name":"missing-fixture-skill"}`), nil)
	if err != nil || !result.IsError {
		t.Fatalf("missing skill result changed: %v / %+v", err, result)
	}
	var found bool
	if err = pg.QueryRow(`SELECT found FROM skill_usage`).Scan(&found); err != nil || found {
		t.Fatalf("missing skill not recorded: %v / %v", found, err)
	}
	result, err = tool.Call(context.Background(), json.RawMessage(`{"name":"api-recon","args":{}}`), nil)
	if err != nil || !result.IsError {
		t.Fatalf("malformed input not rejected: %v", err)
	}
	var count int
	if err = pg.QueryRow(`SELECT count(*) FROM skill_usage`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("malformed input polluted missing stats: %d / %v", count, err)
	}
}

func TestSkillUsageMeterLedgerFailureIsBestEffort(t *testing.T) {
	pg := newSkillUsageMeterFixture(t)
	if _, err := pg.Exec(`DROP TABLE skill_usage`); err != nil {
		t.Fatal(err)
	}
	reg := skill.NewRegistry(skill.Skill{Name: "api-recon", Instructions: "fixture"})
	result, err := meterSkillTool(reg.Tool(), pg, reg, "worker", agent.RunInfo{}).Call(context.Background(), json.RawMessage(`{"name":"api-recon"}`), nil)
	if err != nil || result.IsError || len(result.Extra) != 1 {
		t.Fatalf("metering failure broke Skill: %v / %+v", err, result)
	}
}
