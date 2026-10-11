package db

import (
	"testing"
	"unicode"
)

// TestManualTaskScopeSummaryLocalized pins the manual task-scope provenance label.
// It is stored on task_asset_links.source_summary (AddTaskScope / SetTaskAssetSource)
// and rendered verbatim on the task detail sessions/assets tabs, so reverting it to
// Chinese would surface mixed-language provenance labels in the same panel. The
// existing task_assets_test.go already asserts the stored value equals this constant
// via the symbol, so pinning the constant here protects the user-facing text too.
func TestManualTaskScopeSummaryLocalized(t *testing.T) {
	if manualTaskScopeSummary == "" {
		t.Fatal("manualTaskScopeSummary：字符串为空")
	}
	hasHangul := false
	for _, r := range manualTaskScopeSummary {
		if unicode.Is(unicode.Han, r) {
			t.Fatalf("manualTaskScopeSummary：仍有韩文残留：%q", manualTaskScopeSummary)
		}
		if unicode.Is(unicode.Hangul, r) {
			hasHangul = true
		}
	}
	if !hasHangul {
		t.Fatalf("manualTaskScopeSummary：未包含中文：%q", manualTaskScopeSummary)
	}
}
