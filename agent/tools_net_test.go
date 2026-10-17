package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestParsePortSpec(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  string
		limit int
		want  []int
	}{
		{"sorted unique", "443,80,80", 2, []int{80, 443}},
		{"inclusive overlap", " 82, 80 - 82,81 ", 3, []int{80, 81, 82}},
		{"boundaries", "65535,1", 2, []int{1, 65535}},
		{"same endpoints", "80-80", 1, []int{80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePortSpec(tc.spec, tc.limit)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	for _, spec := range []string{"", " ", "0", "65536", "-1", "+80", "80.0", "1e2", "abc", "80,", ",80", "80,,81", "82-80", "80-", "80-81-82", "1-257", strings.Repeat("1", 4097)} {
		t.Run("reject "+spec[:min(len(spec), 30)], func(t *testing.T) {
			got, err := parsePortSpec(spec, 256)
			if err == nil || got != nil {
				t.Fatalf("must reject without partial output: %v %v", got, err)
			}
		})
	}
	for _, limit := range []int{0, -1, 65536} {
		if _, err := parsePortSpec("80", limit); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	if got, err := parsePortSpec("80,81", 1); err == nil || got != nil {
		t.Fatalf("limit must reject, not truncate: %v %v", got, err)
	}
}
