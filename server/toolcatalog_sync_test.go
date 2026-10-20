package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// newCatalogTestServer 用一个隔离的 SQLite 库把内置工具目录 seed 好，返回可调用同步的 Server。
// 不启动 HTTP，也不碰任何全局运行态（wireTools 改的全局钩子在 Cleanup 里还原）。
func newCatalogTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	pg, err := db.Open("file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "catalog-sync.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	origResolve, origBinding := agent.ToolResolve, agent.FindingTrafficBindingEnabled
	wireTools(pg, nil)
	t.Cleanup(func() {
		agent.ToolResolve = origResolve
		agent.FindingTrafficBindingEnabled = origBinding
	})
	t.Setenv("AGENT_CORE_DISABLE_INTERACTIVE_SHELL", "1")
	return &Server{m: &Manager{pg: pg}}, pg
}

// codeSeed 取出代码侧当前版本的某个工具说明。
func codeSeed(t *testing.T, s *Server, key string) agent.ToolSeed {
	t.Helper()
	sd, ok := s.codeToolSeeds()[key]
	if !ok {
		t.Fatalf("代码里没有工具 %s", key)
	}
	return sd
}

// TestSyncToolCatalog_AdoptsStaleRow 覆盖「老库停留在安装当天说明」这一主场景：
// 行内容被改旧（模拟旧版本 seed 的措辞）、没有指纹 → 同步后应回到代码默认值，并留下备份。
func TestSyncToolCatalog_AdoptsStaleRow(t *testing.T) {
	s, pg := newCatalogTestServer(t)
	const key = "http_sequence"
	sd := codeSeed(t, s, key)

	row, err := pg.GetTool(key)
	if err != nil || row == nil {
		t.Fatalf("seed 后应存在 %s: %v", key, err)
	}
	staleSchema := json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string"}},"required":["mode"]}`)
	if err := pg.RefreshToolDefaults(key, "旧版本的描述", staleSchema); err != nil {
		t.Fatal(err)
	}

	if n := s.syncToolCatalog(); n == 0 {
		t.Fatal("应把漂移的行刷新为代码默认值")
	}
	got, err := pg.GetTool(key)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Description != sd.Desc {
		t.Fatalf("描述未对齐代码默认值：\n got=%.60q\nwant=%.60q", got.Description, sd.Desc)
	}
	if backup, _, _ := pg.GetSetting(toolSeedBackupPrefix + key); backup == "" {
		t.Fatal("覆盖前应把旧内容备份到 settings")
	}
	// 幂等：第二次同步不再改动。
	if n := s.syncToolCatalog(); n != 0 {
		t.Fatalf("同步应幂等，第二次却改了 %d 个", n)
	}
}

// TestSyncToolCatalog_KeepsOperatorEdit 覆盖「运维在页面上改过描述」的场景：
// 已改过的行不应被代码默认值覆盖。
func TestSyncToolCatalog_KeepsOperatorEdit(t *testing.T) {
	s, pg := newCatalogTestServer(t)
	const key = "http_request"
	row, err := pg.GetTool(key)
	if err != nil || row == nil {
		t.Fatalf("seed 后应存在 %s: %v", key, err)
	}
	agents, _ := json.Marshal(row.Agents)
	if err := pg.UpdateTool(key, "运维自定义描述", row.Schema, agents, true); err != nil {
		t.Fatal(err)
	}
	// 先跑一次同步建立指纹；此时该行已与代码不同，会被当作「首次接管」覆盖一次。
	s.syncToolCatalog()
	// 之后再改一次——这时的改动发生在有指纹之后，属于运维编辑，必须永久保留。
	cur, _ := pg.GetTool(key)
	agents, _ = json.Marshal(cur.Agents)
	if err := pg.UpdateTool(key, "运维第二次改的描述", cur.Schema, agents, true); err != nil {
		t.Fatal(err)
	}
	s.syncToolCatalog()
	got, err := pg.GetTool(key)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Description != "运维第二次改的描述" {
		t.Fatalf("运维编辑被覆盖：%q", got.Description)
	}
}

// TestToolCatalogDriftFingerprint 覆盖诊断口径：目录一致时两个指纹相同，任一漂移则不同。
func TestToolCatalogDriftFingerprint(t *testing.T) {
	s, pg := newCatalogTestServer(t)
	rep := s.toolCatalogDrift()
	if rep.CodeFingerprint == "" || rep.RowFingerprint == "" {
		t.Fatal("指纹不能为空")
	}
	if rep.Drifted {
		t.Fatalf("刚 seed 完不应漂移：counts=%v", rep.Counts)
	}
	const key = "extract_data"
	row, _ := pg.GetTool(key)
	if row == nil {
		t.Fatal("extract_data 未 seed")
	}
	if err := pg.RefreshToolDefaults(key, "被改坏的说明", row.Schema); err != nil {
		t.Fatal(err)
	}
	rep = s.toolCatalogDrift()
	if !rep.Drifted || rep.CodeFingerprint == rep.RowFingerprint {
		t.Fatalf("改动后应报告漂移：code=%s row=%s", rep.CodeFingerprint, rep.RowFingerprint)
	}
	if rep.Counts["drifted"] == 0 {
		t.Fatalf("应统计到漂移项：%v", rep.Counts)
	}
}

// TestSyncToolCatalog_OptOut 覆盖 tool_catalog_sync=off：只诊断、不写库。
func TestSyncToolCatalog_OptOut(t *testing.T) {
	s, pg := newCatalogTestServer(t)
	const key = "http_fuzz"
	row, _ := pg.GetTool(key)
	if row == nil {
		t.Fatal("http_fuzz 未 seed")
	}
	if err := pg.RefreshToolDefaults(key, "旧描述", row.Schema); err != nil {
		t.Fatal(err)
	}
	if err := pg.SetSetting(toolCatalogSyncSetting, "off"); err != nil {
		t.Fatal(err)
	}
	if n := s.syncToolCatalog(); n != 0 {
		t.Fatalf("关闭时不应写库，实际改了 %d", n)
	}
	got, _ := pg.GetTool(key)
	if got.Description != "旧描述" {
		t.Fatalf("关闭同步后不应覆盖：%q", got.Description)
	}
	if rep := s.toolCatalogDrift(); rep.SyncEnabled {
		t.Fatal("关闭时应报告 sync_enabled=false")
	}
}
