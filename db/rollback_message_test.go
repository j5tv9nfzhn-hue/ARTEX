package db

import (
	"context"
	"fmt"
	"testing"
)

// 消息级回滚的存储原语（气泡回滚箭头）：可见边界查询（首条消息 → 0）、隐藏行
// 过滤后的 Max*、活动元信息（跨会话/跨探索不可解析）、按 id 作废检查点。
func TestMessageRollbackPrimitives(t *testing.T) {
	ctx := context.Background()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// ---- 会话侧 ----
	c, err := d.CreateConversation("mainagent", "rollback-message-primitives", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID)

	s1, err := d.AppendConvActivity(c.ID, Activity{Worker: "mainagent", Kind: "user", Summary: "第一条"})
	if err != nil {
		t.Fatal(err)
	}
	a1, err := d.AppendConvActivity(c.ID, Activity{Worker: "mainagent", Kind: "result", Summary: "回答一"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := d.AppendConvActivity(c.ID, Activity{Worker: "mainagent", Kind: "user", Summary: "第二条"})
	if err != nil {
		t.Fatal(err)
	}

	if kind, reverted, found, err := d.ConvActivityMeta(c.ID, s2); err != nil || !found || reverted || kind != "user" {
		t.Fatalf("meta(s2): kind=%q reverted=%v found=%v err=%v", kind, reverted, found, err)
	}
	if _, _, found, err := d.ConvActivityMeta(c.ID, s1+100000); err != nil || found {
		t.Fatalf("foreign id must not resolve: found=%v err=%v", found, err)
	}

	// 回滚 S2 → 边界 = S2 之前的最新可见活动（a1）
	b, err := d.MaxVisibleConversationActivityBefore(ctx, c.ID, s2)
	if err != nil || b != a1 {
		t.Fatalf("boundary before s2 = %d (want %d) err=%v", b, a1, err)
	}
	hidden, err := d.HideConversationActivitiesAfter(ctx, c.ID, b)
	if err != nil || hidden != 1 {
		t.Fatalf("hide after boundary: hidden=%d err=%v", hidden, err)
	}
	if _, reverted, found, _ := d.ConvActivityMeta(c.ID, s2); !found || !reverted {
		t.Fatalf("s2 should be hidden: found=%v reverted=%v", found, reverted)
	}
	// 隐藏行不再参与「本会话最新可见活动」（检查点边界据此不再虚高）
	if max, err := d.MaxConversationActivityID(ctx, c.ID); err != nil || max != a1 {
		t.Fatalf("max visible = %d (want %d) err=%v", max, a1, err)
	}
	if max, err := d.MaxVisibleConversationActivityBefore(ctx, c.ID, s1); err != nil || max != 0 {
		t.Fatalf("boundary before first message = %d (want 0) err=%v", max, err)
	}

	// 检查点作废：边界落在隐藏区内的作废；等于新边界的保留
	scope := fmt.Sprintf("conv-%d", c.ID)
	inside, err := d.CaptureCheckpoint(ctx, scope, CheckpointReasonUserMessage, "in", CheckpointSnapshot{ActivityID: s2})
	if err != nil {
		t.Fatal(err)
	}
	edge, err := d.CaptureCheckpoint(ctx, scope, CheckpointReasonTurnComplete, "edge", CheckpointSnapshot{ActivityID: a1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.SupersedeCheckpoint(ctx, inside.ID); err != nil || !ok {
		t.Fatalf("supersede inside: ok=%v err=%v", ok, err)
	}
	if ok, err := d.SupersedeCheckpoint(ctx, inside.ID); err != nil || ok {
		t.Fatalf("second supersede must be a no-op: ok=%v err=%v", ok, err)
	}
	if cp, err := d.GetCheckpoint(ctx, inside.ID); err != nil || cp == nil || cp.Status != CheckpointSuperseded {
		t.Fatalf("inside status = %+v err=%v", cp, err)
	}
	if cp, err := d.GetCheckpoint(ctx, edge.ID); err != nil || cp == nil || cp.Status != CheckpointActive {
		t.Fatalf("edge status = %+v err=%v", cp, err)
	}

	// ---- 探索侧（任务）----
	expID, err := d.CreateExploration("rollback-message-primitives", "目标")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)
	otherExp, err := d.CreateExploration("rollback-message-primitives-other", "目标2")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, otherExp)
	other := d.Exploration(otherExp)

	u1, err := es.AppendActivity(Activity{Worker: "mainagent", Kind: "user", Summary: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	w1, err := es.AppendActivity(Activity{Worker: "work#1", Kind: "tool_use", Tool: "Bash", Summary: "跑了命令"})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := es.AppendActivity(Activity{Worker: "mainagent", Kind: "user", Summary: "改一下"})
	if err != nil {
		t.Fatal(err)
	}

	if kind, worker, reverted, found, err := es.ActivityMeta(u2); err != nil || !found || reverted || kind != "user" || worker != "mainagent" {
		t.Fatalf("activity meta(u2): kind=%q worker=%q reverted=%v found=%v err=%v", kind, worker, reverted, found, err)
	}
	// 全局活动 id 不可跨探索解析
	if _, _, _, found, err := other.ActivityMeta(u1); err != nil || found {
		t.Fatalf("cross-exploration id must not resolve: found=%v err=%v", found, err)
	}

	b2, err := d.MaxVisibleExplorationActivityBefore(ctx, expID, u2)
	if err != nil || b2 != w1 {
		t.Fatalf("boundary before u2 = %d (want %d) err=%v", b2, w1, err)
	}
	if hidden, err := d.HideExplorationActivitiesAfter(ctx, expID, b2); err != nil || hidden != 1 {
		t.Fatalf("hide after boundary: hidden=%d err=%v", hidden, err)
	}
	if max, err := d.MaxExplorationActivityID(ctx, expID); err != nil || max != w1 {
		t.Fatalf("max visible = %d (want %d) err=%v", max, w1, err)
	}
	if max, err := d.MaxVisibleExplorationActivityBefore(ctx, expID, u1); err != nil || max != 0 {
		t.Fatalf("boundary before first activity = %d (want 0) err=%v", max, err)
	}
}
