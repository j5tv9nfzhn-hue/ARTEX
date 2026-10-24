package db

import (
	"testing"
)

func TestClueCacheDedupeAndQuery(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.EnsureClueCacheTables(); err != nil {
		t.Fatal(err)
	}
	taskID := int64(990001)
	store := &ClueStore{db: d, expID: 7}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM clues WHERE task_id=$1`, taskID) })

	// 同一个端点写成两种 URL 形态，必须落到同一行（去重的意义）。
	id1, created1, err := store.Put(taskID, ClueInput{
		Kind: "endpoint", Key: "https://api.example.test/Api/V1/Orders?id=1", Value: "GET /Api/V1/Orders?id=1 → 200",
		Source: "http_request", Confidence: "observed",
	})
	if err != nil || !created1 {
		t.Fatalf("first put: id=%d created=%v err=%v", id1, created1, err)
	}
	id2, created2, err := store.Put(taskID, ClueInput{
		Kind: "endpoint", Key: "https://api.example.test/Api/V1/Orders?id=1", Value: "GET /Api/V1/Orders?id=1 → 401",
		Source: "http_request", Confidence: "observed",
	})
	if err != nil || created2 || id2 != id1 {
		t.Fatalf("重复线索应合并到同一行：id=%d created=%v err=%v（首条 %d）", id2, created2, err, id1)
	}

	// 大小写/空白不同的同一键也合并。
	if _, created3, err := store.Put(taskID, ClueInput{
		Kind: "ENDPOINT", Key: "  https://api.example.test/Api/V1/Orders?id=1  ", Value: "again",
	}); err != nil || created3 {
		t.Fatalf("大小写/空白差异不应新建行：created=%v err=%v", created3, err)
	}

	// 未知分类落 note，而不是丢弃。
	if _, _, err := store.Put(taskID, ClueInput{Kind: "莫名其妙的分类", Key: "x", Value: "y"}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.Put(taskID, ClueInput{Kind: "secret", Key: "js:ak", Value: "AKIA-EXAMPLE-1234", Source: "web_recon", Confidence: "observed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Put(taskID, ClueInput{Kind: "error", Key: "sqlerr:mysql", Value: "You have an error in your SQL syntax"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Query(taskID, "endpoint", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("endpoint 去重后应只有 1 条，得到 %d：%+v", len(got), got)
	}
	if got[0].SeenCount != 3 {
		t.Fatalf("seen_count 应为 3（三次观察），得到 %d", got[0].SeenCount)
	}

	// 关键字检索跨 key/value。
	hits, err := store.Query(taskID, "", "akia", 10)
	if err != nil || len(hits) != 1 || hits[0].Kind != "secret" {
		t.Fatalf("按值检索失败：%+v err=%v", hits, err)
	}
	// 分类 + 关键字组合。
	if hits, err = store.Query(taskID, "error", "sql", 10); err != nil || len(hits) != 1 {
		t.Fatalf("分类+关键字检索失败：%+v err=%v", hits, err)
	}

	if err := store.Touch([]int64{id1}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Query(taskID, "endpoint", "", 1)
	if len(got) != 1 || got[0].UsedCount != 1 {
		t.Fatalf("Touch 未生效：%+v", got)
	}

	stats, err := store.Stats(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if stats["endpoint"] != 1 || stats["secret"] != 1 || stats["error"] != 1 || stats["note"] != 1 {
		t.Fatalf("分类统计不对：%+v", stats)
	}
}

func TestClueNormalizeFallsBackToValueAsKey(t *testing.T) {
	in := NormalizeClue(ClueInput{Kind: "endpoint", Value: "  /api/v1/orders  "})
	if in.Key != "/api/v1/orders" {
		t.Fatalf("缺 key 时应回退到值，得到 %q", in.Key)
	}
	if in.Confidence != "inferred" {
		t.Fatalf("未声明置信度应默认 inferred，得到 %q", in.Confidence)
	}
}
