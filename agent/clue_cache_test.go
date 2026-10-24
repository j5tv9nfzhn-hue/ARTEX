package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// clueTestSet 造一个带线索缓存的 ToolSet。数据库不可用时跳过（与其它 agent
// 测试一致的口径）。
func clueTestSet(t *testing.T) (*ToolSet, *db.DB, int64) {
	t.Helper()
	d := testDB(t)
	if err := d.EnsureClueCacheTables(); err != nil {
		t.Fatalf("建线索表：%v", err)
	}
	const taskID = int64(990777)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM clues WHERE task_id=$1`, taskID)
		d.Close()
	})
	ts := NewToolSet(d.Exploration(4242), "worker")
	ts.SetTaskID(taskID)
	return ts, d, taskID
}

// TestCacheClueThenQueryRoundTrip：材料写进去、换个 run 还能读回来，且读回来是
// 结构化（带来源/置信度/命中次数），不是一段流水账。
func TestCacheClueThenQueryRoundTrip(t *testing.T) {
	ts, _, _ := clueTestSet(t)
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: 990777, IntentID: 1234})

	cache := ts.cacheClueTool()
	res, err := cache.Call(ctx, json.RawMessage(`{
		"clues":[
			{"kind":"endpoint","key":"/api/v1/orders/{id}","value":"GET /api/v1/orders/{id} (Host: api.example.test) → 200，参数 id 可枚举","source":"http_request","confidence":"observed"},
			{"kind":"secret","key":"js:AK","value":"static/app.js 里硬编码 AKIA-EXAMPLE / endpoint https://internal.example.test","source":"web_recon","confidence":"observed"},
			{"kind":"error","key":"sqlerr:mysql","value":"You have an error in your SQL syntax near ''''","source":"http_request","confidence":"observed"}
		]}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("cache_clue 失败：%v / %s", err, res.Flatten())
	}
	var w struct {
		Created int `json:"created"`
		Merged  int `json:"merged"`
	}
	if err := json.Unmarshal([]byte(res.Flatten()), &w); err != nil {
		t.Fatalf("返回不是 JSON：%s", res.Flatten())
	}
	if w.Created != 3 {
		t.Fatalf("应新建 3 条，得到 %d（%s）", w.Created, res.Flatten())
	}

	// 另一个 worker / 另一个 run 来查（新 ToolSet，同任务）。
	other := NewToolSet(ts.ts, "worker")
	other.SetTaskID(990777)
	q := other.queryCluesTool()
	res2, err := q.Call(ctx, json.RawMessage(`{"q":"api.example.test"}`), nil)
	if err != nil || res2.IsError {
		t.Fatalf("query_clues 失败：%v / %s", err, res2.Flatten())
	}
	var out struct {
		Clues []struct {
			Kind       string `json:"kind"`
			Key        string `json:"key"`
			Source     string `json:"source"`
			Confidence string `json:"confidence"`
		} `json:"clues"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(res2.Flatten()), &out); err != nil {
		t.Fatalf("返回不是 JSON：%s", res2.Flatten())
	}
	if out.Count != 1 || out.Clues[0].Kind != "endpoint" || out.Clues[0].Source != "http_request" {
		t.Fatalf("按主机检索结果不对：%s", res2.Flatten())
	}

	// 按分类查 error。
	res3, _ := q.Call(ctx, json.RawMessage(`{"kind":"error","mark_used":false}`), nil)
	if !strings.Contains(res3.Flatten(), "sqlerr:mysql") {
		t.Fatalf("按分类检索失败：%s", res3.Flatten())
	}

	// 空命中要给换关键字的提示，而不是空数组了事。
	res4, _ := q.Call(ctx, json.RawMessage(`{"q":"不存在的关键字"}`), nil)
	if !strings.Contains(res4.Flatten(), "hint") {
		t.Fatalf("空命中应带 hint：%s", res4.Flatten())
	}
}

// TestClueSwitchHintCarriesMaterials：连续失败的提示里必须带**缓存里的真实
// 材料**与「禁止原地重试」，而不是一句空泛的「换个思路」。
func TestClueSwitchHintCarriesMaterials(t *testing.T) {
	ts, d, _ := clueTestSet(t)
	cs := ts.Clues()
	if cs == nil {
		t.Fatal("线索缓存未接线")
	}
	if _, _, err := cs.Put(990777, db.ClueInput{
		Kind: "endpoint", Key: "/api/v1/internal/users", Value: "管理端接口，未鉴权可访问",
		Source: "http_request", Confidence: "observed",
	}); err != nil {
		t.Fatal(err)
	}
	ctx := WithRunInfo(WithClueStore(context.Background(), cs), RunInfo{TaskID: 990777, IntentID: 9})

	hint := clueSwitchHint(ctx, "http_request https://api.example.test/api/v1/orders/1", 6)
	if !strings.Contains(hint, "禁止原地重试") {
		t.Fatalf("提示缺少换分支硬指令：%s", hint)
	}
	// 目标不匹配时退到「本任务最近的材料」，仍要带出可用的东西。
	if !strings.Contains(hint, "/api/v1/internal/users") {
		t.Fatalf("提示没有带出缓存材料：%s", hint)
	}

	// 缓存为空：仍然要给出可执行的动作，不能只说「换个思路」。
	// 用另一个任务 id——同一个任务下的缓存当然不是空的（下面那半句就是在测它）。
	const emptyTask = int64(990778)
	emptyTs := NewToolSet(ts.ts, "worker")
	emptyTs.SetTaskID(emptyTask)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM clues WHERE task_id=$1`, emptyTask) })
	emptyCtx := WithRunInfo(WithClueStore(context.Background(), emptyTs.Clues()), RunInfo{TaskID: emptyTask})
	empty := clueSwitchHint(emptyCtx, "http_request https://api.example.test/x", 6)
	if !strings.Contains(empty, "cache_clue") {
		t.Fatalf("空缓存提示应引导写入缓存：%s", empty)
	}
	// 没有接线时返回空串（不编造）。
	if got := clueSwitchHint(context.Background(), "http_request https://x/y", 6); got != "" {
		t.Fatalf("未接线时应返回空串，得到 %q", got)
	}
}

// TestLoopBreakerAppendsClueHint：3-strike 触发时，工具结果里必须真的多出这段
// 提示——这是「连续失败→主动查缓存」唯一的执行点，不能靠模型自觉。
func TestLoopBreakerAppendsClueHint(t *testing.T) {
	ts, _, _ := clueTestSet(t)
	cs := ts.Clues()
	if _, _, err := cs.Put(990777, db.ClueInput{Kind: "endpoint", Key: "/admin/api/debug", Value: "调试接口返回环境变量", Source: "http_request", Confidence: "observed"}); err != nil {
		t.Fatal(err)
	}
	ctx := WithRunInfo(WithClueStore(context.Background(), cs), RunInfo{TaskID: 990777, IntentID: 11})

	lb := newLoopBreaker()
	in := json.RawMessage(`{"url":"https://api.example.test/admin/api/debug","method":"GET"}`)
	var last actool.Result
	for i := 0; i < loopStrikeLimit; i++ {
		release, blocked := lb.before(ctx, "http_request", in)
		if blocked != nil {
			t.Fatalf("第 %d 次就提前拦截", i+1)
		}
		last = lb.inspect(ctx, "http_request", in, actool.Text(`{"status":403,"body":"forbidden","body_truncated":false}`), nil)
		release()
	}
	text := last.Flatten()
	if !strings.Contains(text, "[线索缓存 / 换分支]") {
		t.Fatalf("3-strike 未附带线索提示：%s", text)
	}
	if !strings.Contains(text, "/admin/api/debug") {
		t.Fatalf("提示里没有缓存材料：%s", text)
	}

	// 之后的调用被拦下时，提示同样要在——否则模型只在第一次看到就忘了。
	release, blocked := lb.before(ctx, "http_request", in)
	release()
	if blocked == nil {
		t.Fatal("第 4 次应被拦截")
	}
	if !strings.Contains(blocked.Flatten(), "[线索缓存 / 换分支]") {
		t.Fatalf("拦截结果里没有线索提示：%s", blocked.Flatten())
	}
}
