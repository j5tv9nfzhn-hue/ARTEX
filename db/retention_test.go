package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture creation is code for a FUTURE authorized test run, never a command
// against the operator's live DB. Only a t.TempDir SQLite file is opened.
func newRetentionFixture(t *testing.T) *DB {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "retention.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	d := &DB{DB: raw}
	statements := []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT,updated_at TEXT)",
		"CREATE TABLE llm_usage(id INTEGER PRIMARY KEY,ts TEXT,task_id TEXT,exploration_id INTEGER)",
		"CREATE TABLE server_logs(id INTEGER PRIMARY KEY,created_at TEXT,level TEXT,tag TEXT)",
		"CREATE TABLE tasks(id INTEGER PRIMARY KEY)", "CREATE TABLE findings(id INTEGER PRIMARY KEY)",
		"CREATE TABLE assets(id INTEGER PRIMARY KEY)", "CREATE TABLE conversations(id INTEGER PRIMARY KEY)", "CREATE TABLE intercept_pending(id INTEGER PRIMARY KEY)",
		"CREATE TABLE notification_events(id INTEGER PRIMARY KEY,created_at TEXT,fanned_out INTEGER,finding_id INTEGER,snapshot TEXT)",
		"CREATE TABLE notification_deliveries(id INTEGER PRIMARY KEY,event_id INTEGER REFERENCES notification_events(id) ON DELETE CASCADE,created_at TEXT,state TEXT,sent_at TEXT,batch_id INTEGER)",
		"CREATE TABLE usage_refs(id INTEGER PRIMARY KEY,usage_id INTEGER REFERENCES llm_usage(id) ON DELETE CASCADE)",
	}
	for _, stmt := range statements {
		if _, err = d.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise actual metadata DDL (including append-only triggers), not a copied
	// version that could silently diverge from production schema.
	for _, stmt := range sqliteStatements(schemaSQLiteSQL) {
		if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS retention_") || strings.HasPrefix(stmt, "CREATE TRIGGER IF NOT EXISTS retention_") {
			if _, err = d.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	return d
}

func TestRetentionPolicyDefaultsAndBounds(t *testing.T) {
	p := DefaultRetentionPolicy()
	// 保底档是「默认开启」，依据见 db/retention.go#DefaultRetentionPolicy：
	// 不依赖用户点按钮，但谓词足够保守（带 task_id 的 usage 永不删）。关闭仍然
	// 合法，只是不再等于默认档。
	if !p.Enabled {
		t.Fatal("默认档应当开启；要关闭请显式保存一条 enabled=false 的策略")
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.UsageDays = 1
	if p.Validate() == nil {
		t.Fatal("unsafe short usage window accepted")
	}
	p = DefaultRetentionPolicy()
	p.MaxRowsPerTable = 501
	if p.Validate() == nil {
		t.Fatal("unbounded batch accepted")
	}
	p = DefaultRetentionPolicy()
	p.UsageDays = 0
	p.LogDays = 0
	p.NotificationDays = 0
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionPreviewScopeAndReferenceRecheck(t *testing.T) {
	d := newRetentionFixture(t)
	ctx := context.Background()
	for _, stmt := range []string{
		"INSERT INTO llm_usage VALUES(1,'2000-01-01',NULL,NULL),(2,'2000-01-01','99',NULL),(3,'2000-01-01',NULL,NULL)",
		"INSERT INTO server_logs VALUES(1,'2000-01-01','info','runtime'),(2,'2000-01-01','warn','runtime'),(3,'2000-01-01','info','auth'),(4,'2000-01-01','info','runtime')",
		`INSERT INTO notification_events VALUES(1,'2000-01-01',1,999,'{"task_id":0,"asset_ids":[]}'),(2,'2000-01-01',1,998,'{"task_id":0,"asset_ids":[]}')`,
		"INSERT INTO notification_deliveries VALUES(1,1,'2000-01-01','sent','2000-01-01',NULL),(2,2,'2000-01-01','pending',NULL,NULL)",
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	// 这条复查的是「关闭时拒执」这道闸门本身，与默认档无关：先显式存一条
	// enabled=false 的策略，否则默认档（开启）会让下面的断言失去意义。
	off := DefaultRetentionPolicy()
	off.Enabled = false
	if err := d.SaveRetentionPolicy(ctx, off); err != nil {
		t.Fatal(err)
	}
	preview, err := d.CreateRetentionPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ExecuteRetention(ctx, preview.Token); !errors.Is(err, ErrRetentionDisabled) {
		t.Fatalf("disabled policy: %v", err)
	}
	p := DefaultRetentionPolicy()
	p.Enabled = true
	if err = d.SaveRetentionPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = d.ExecuteRetention(ctx, preview.Token); !errors.Is(err, ErrRetentionStale) {
		t.Fatalf("old policy preview survived save: %v", err)
	}
	preview, err = d.CreateRetentionPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Candidate protection changes after preview; execution must recheck.
	for _, stmt := range []string{"INSERT INTO usage_refs VALUES(1,3)", "INSERT INTO llm_usage VALUES(4,'2000-01-01',NULL,NULL)", "UPDATE server_logs SET level='error' WHERE id=4"} {
		if _, err = d.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	result, err := d.ExecuteRetention(ctx, preview.Token)
	if err != nil || !result.Committed {
		t.Fatalf("execute: %v", err)
	}
	checks := []struct {
		query string
		want  int
	}{
		{"SELECT count(*) FROM llm_usage WHERE id=1", 0}, {"SELECT count(*) FROM llm_usage WHERE id IN (2,3,4)", 3},
		{"SELECT count(*) FROM server_logs WHERE id=1", 0}, {"SELECT count(*) FROM server_logs WHERE id IN (2,3,4)", 3},
		{"SELECT count(*) FROM notification_deliveries WHERE id=1", 0}, {"SELECT count(*) FROM notification_deliveries WHERE id=2", 1},
		{"SELECT count(*) FROM notification_events WHERE id=1", 1},
	}
	for _, check := range checks {
		var n int
		if err = d.QueryRow(check.query).Scan(&n); err != nil || n != check.want {
			t.Fatalf("%s: %d / %v", check.query, n, err)
		}
	}
	if _, err = d.ExecuteRetention(ctx, preview.Token); !errors.Is(err, ErrRetentionStale) {
		t.Fatalf("token replay: %v", err)
	}
	if _, err = d.Exec("DELETE FROM retention_audit"); err == nil {
		t.Fatal("audit is deletable")
	}
}

func TestRetentionRollbackAndExpiredPreview(t *testing.T) {
	d := newRetentionFixture(t)
	ctx := context.Background()
	p := DefaultRetentionPolicy()
	p.Enabled = true
	if err := d.SaveRetentionPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"INSERT INTO llm_usage VALUES(1,'2000-01-01',NULL,NULL)",
		"INSERT INTO server_logs VALUES(1,'2000-01-01','info','runtime')",
		"CREATE TRIGGER refuse_log_delete BEFORE DELETE ON server_logs BEGIN SELECT RAISE(ABORT,'fixture stop'); END",
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := d.CreateRetentionPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.ExecuteRetention(ctx, preview.Token); err == nil {
		t.Fatal("forced failure reported success")
	}
	var n int
	if err = d.QueryRow("SELECT count(*) FROM llm_usage WHERE id=1").Scan(&n); err != nil || n != 1 {
		t.Fatal("failed transaction partially deleted usage")
	}
	if _, err = d.Exec("UPDATE retention_plans SET expires_at='2000-01-01'"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.ExecuteRetention(ctx, preview.Token); !errors.Is(err, ErrRetentionStale) {
		t.Fatalf("expired preview: %v", err)
	}
}
