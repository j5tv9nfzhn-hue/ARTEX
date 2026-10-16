package db

import (
	"testing"
)

// testDSN returns the DSN the suite should use.
//
// Unlike the PostgreSQL backend this replaces, resolution cannot fail: the
// database is a file that TestMain points at a temp directory (or that the
// developer pinned via ARTEX_SQLITE_PATH), so there is no "no database
// configured" case to skip on.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn, _, err := DSN()
	if err != nil {
		t.Fatalf("DSN 解析失败：%v", err)
	}
	return dsn
}

// TestOpenSeed opens the suite database, applies the schema, seeds, and verifies
// the builtin agents + their variable catalog exist.
func TestOpenSeed(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}

	defer d.Close()

	wantAgents := len(builtinAgents)
	var agents int
	if err := d.QueryRow(`SELECT count(*) FROM agents WHERE builtin`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != wantAgents {
		t.Fatalf("want %d builtin agents, got %d", wantAgents, agents)
	}

	// planner should have at least its seeded catalog vars
	wantPlannerVars := 0
	for _, a := range builtinAgents {
		if a.key == "planner" {
			wantPlannerVars = len(a.vars)
			break
		}
	}
	var plannerVars int
	if err := d.QueryRow(`
SELECT count(*) FROM agent_prompt_vars v
JOIN agents a ON a.id = v.agent_id
WHERE a.key = 'planner'`).Scan(&plannerVars); err != nil {
		t.Fatal(err)
	}
	if plannerVars < wantPlannerVars {
		t.Fatalf("want at least %d planner vars, got %d", wantPlannerVars, plannerVars)
	}

	// re-open must be idempotent (no duplicate agents)
	d2, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if err := d2.QueryRow(`SELECT count(*) FROM agents WHERE builtin`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != wantAgents {
		t.Fatalf("after re-open want %d agents, got %d", wantAgents, agents)
	}
}
