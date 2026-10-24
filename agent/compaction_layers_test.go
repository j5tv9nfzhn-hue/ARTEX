package agent

import (
	"strings"
	"testing"
)

func TestLayerPlanForRoles(t *testing.T) {
	plan := LayerPlanFor("worker", 0)
	if len(plan) != 3 {
		t.Fatalf("layers=%d", len(plan))
	}
	kinds := map[LayerKind]bool{}
	for _, p := range plan {
		kinds[p.Layer] = true
		if p.Role != "worker" {
			t.Fatalf("role=%s", p.Role)
		}
		if p.Layer == LayerWindow {
			w, _ := p.Detail["window_tokens"].(int)
			if w != defaultWindowK*1000 {
				t.Fatalf("default window=%d", w)
			}
		}
	}
	for _, k := range []LayerKind{LayerToolClip, LayerWindow, LayerGraphDigest} {
		if !kinds[k] {
			t.Fatalf("missing layer %s", k)
		}
	}

	plan2 := LayerPlanFor("planner", 24000)
	for _, p := range plan2 {
		if p.Layer == LayerWindow {
			w, _ := p.Detail["window_tokens"].(int)
			if w != 24000 {
				t.Fatalf("window=%d", w)
			}
		}
	}
}

func TestDescribeLayerPlan(t *testing.T) {
	s := DescribeLayerPlan(LayerPlanFor("chat", 8000))
	if !strings.Contains(s, "tool_output_clip") || !strings.Contains(s, "旁路") {
		t.Fatalf("desc=%q", s)
	}
}

func TestChainCompactionObserver(t *testing.T) {
	var seen []string
	chain := ChainCompactionObserver(
		func(ev CompactionEvent) { seen = append(seen, "a:"+ev.Stage) },
		nil,
		func(ev CompactionEvent) {
			seen = append(seen, "b:"+ev.Stage)
			panic("observer must not break the chain")
		},
		func(ev CompactionEvent) { seen = append(seen, "c:"+ev.Stage) },
	)
	chain(CompactionEvent{Stage: "start", Phase: "minor"})
	if len(seen) != 3 || seen[0] != "a:start" || seen[2] != "c:start" {
		t.Fatalf("seen=%v", seen)
	}
}
