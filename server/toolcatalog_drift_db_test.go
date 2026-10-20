package server

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// TestToolCatalogDriftOnDB 是跨环境对齐用的诊断入口：把任意一台机器的 artex.db
// **拷贝**一份后指进来，打印「代码说明 vs 该库说明」的指纹与差异清单。
//
//	set ARTEX_TOOLCATALOG_DB=C:\path\to\artex.db
//	go test ./server -run TestToolCatalogDriftOnDB -v
//
// 三端各跑一次：code_fingerprint 相同 = 模型看到的工具说明书逐字相同；
// 若不同，items 里会列出具体是哪些工具的说明被旧版本 seed 钉住了。
func TestToolCatalogDriftOnDB(t *testing.T) {
	src := os.Getenv("ARTEX_TOOLCATALOG_DB")
	if src == "" {
		t.Skip("未设置 ARTEX_TOOLCATALOG_DB，跳过实测库诊断")
	}
	// 永远在副本上操作：调用方给的库可能是正在运行的线上库，只读是底线。
	dst := filepath.Join(t.TempDir(), "artex-drift.sqlite")
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("创建副本失败: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("复制库失败: %v", err)
	}
	out.Close()

	pg, err := db.Open("file:" + filepath.ToSlash(dst))
	if err != nil {
		t.Fatalf("打开副本失败: %v", err)
	}
	defer pg.Close()
	s := &Server{m: &Manager{pg: pg}}

	rep := s.toolCatalogDrift()
	t.Logf("code_fingerprint=%s row_fingerprint=%s drifted=%v sync_enabled=%v counts=%v",
		rep.CodeFingerprint, rep.RowFingerprint, rep.Drifted, rep.SyncEnabled, rep.Counts)
	for _, it := range rep.Items {
		if it.Status == "in_sync" {
			continue
		}
		t.Logf("  [%s] %-24s %s", it.Status, it.Key, it.Note)
	}
	// 只报告，不判定失败：这是一个诊断工具，差异本身就是结论。
	_ = json.Marshal
	_ = agent.ToolSeed{}
}
