package db

import (
	"context"
	"errors"
	"testing"
)

// verifyDB 造一个干净的库与一条待复核发现。
func verifyDB(t *testing.T) (*DB, int64) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	fid, err := d.AddFinding(0, 0, "verify-test", "测试发现", "high", "summary", "evidence", "web-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM conversations WHERE id IN (SELECT conversation_id FROM finding_verifications WHERE finding_id=$1)`, fid)
		_, _ = d.DeleteFinding(fid)
		d.Close()
	})
	return d, fid
}

// runVerification 把一次复核从建单跑到封存，返回记录。
func runVerification(t *testing.T, d *DB, fid int64, verdict string) *FindingVerification {
	t.Helper()
	v, conv, created, err := d.CreateFindingVerification(t.Context(), fid, "", "test")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created || conv == nil {
		t.Fatal("expected a fresh verification conversation")
	}
	if ok, err := d.StartFindingVerification(t.Context(), v.ID); err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	if err := d.RecordFindingVerificationResult(t.Context(), conv.ID, verdict, "小结", "证据"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := d.FinishFindingVerification(v.ID, "completed", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, err := d.FindingVerificationForConversation(t.Context(), conv.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v %v", got, err)
	}
	return got
}

// TestVerificationVerdictDrivesValidation 钉住三条映射：只有复核结论才改
// findings.validation，且「没复核完」既不是通过也不是推翻。
func TestVerificationVerdictDrivesValidation(t *testing.T) {
	d, fid := verifyDB(t)

	// 新发现默认未复核。
	if v, err := d.FindingValidationOf(fid); err != nil || v != FindingUnverified {
		t.Fatalf("new finding: %q %v", v, err)
	}

	// 无结论收尾（Agent 没保存结论）⇒ failed，validation 不动。
	v, conv, created, err := d.CreateFindingVerification(t.Context(), fid, "", "test")
	if err != nil || !created || conv == nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.StartFindingVerification(t.Context(), v.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishFindingVerification(v.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	got, err := d.FindingVerificationForConversation(t.Context(), conv.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Verdict != "" || got.Error == "" {
		t.Fatalf("no verdict should seal as failed: %+v", got)
	}
	if val, _ := d.FindingValidationOf(fid); val != FindingUnverified {
		t.Fatalf("a conclusion-less run must not verify: %q", val)
	}

	// inconclusive ⇒ 仍然未复核。
	if v := runVerification(t, d, fid, verifyVerdictInconclusive); v.Verdict != verifyVerdictInconclusive {
		t.Fatalf("verdict: %+v", v)
	}
	if val, _ := d.FindingValidationOf(fid); val != FindingUnverified {
		t.Fatalf("inconclusive must stay unverified: %q", val)
	}

	// verified ⇒ 通过。
	runVerification(t, d, fid, verifyVerdictVerified)
	if val, _ := d.FindingValidationOf(fid); val != FindingVerified {
		t.Fatalf("verified: %q", val)
	}

	// refuted ⇒ 推翻（覆盖前一个结论：后一次复核是更新的判断）。
	runVerification(t, d, fid, verifyVerdictRefuted)
	if val, _ := d.FindingValidationOf(fid); val != FindingRefuted {
		t.Fatalf("refuted: %q", val)
	}
}

// TestVerificationRejectsSelfClaimedFinding 是「不得自己验证自己」的数据层闸门。
func TestVerificationRejectsSelfClaimedFinding(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(0, 0, "verify-self", "自我上报", "high", "s", "e", VerifierAgentKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteFinding(fid); d.Close() })
	if _, _, _, err := d.CreateFindingVerification(context.Background(), fid, "", "test"); !errors.Is(err, ErrVerifySelfClaimed) {
		t.Fatalf("self-claim must be rejected, got %v", err)
	}
}

// TestVerificationCoverageAcrossTwentyFindings 是 P2 的验收用例：
// 抽 20 条发现走完整复核流水线，要求 verified 覆盖率 100%（20/20）。
func TestVerificationCoverageAcrossTwentyFindings(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	ids := make([]int64, 0, n)
	severities := []string{"critical", "high", "medium", "low"}
	for i := range n {
		fid, err := d.AddFinding(0, 0, "coverage", "发现", severities[i%len(severities)], "s", "e", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, fid)
	}
	t.Cleanup(func() {
		for _, fid := range ids {
			_, _ = d.Exec(`DELETE FROM conversations WHERE id IN (SELECT conversation_id FROM finding_verifications WHERE finding_id=$1)`, fid)
			_, _ = d.DeleteFinding(fid)
		}
		d.Close()
	})
	for _, fid := range ids {
		runVerification(t, d, fid, verifyVerdictVerified)
	}
	verified := 0
	for _, fid := range ids {
		val, err := d.FindingValidationOf(fid)
		if err != nil {
			t.Fatal(err)
		}
		if val == FindingVerified {
			verified++
		}
	}
	if verified != n {
		t.Fatalf("verified coverage = %d/%d, want 100%%", verified, n)
	}
}
