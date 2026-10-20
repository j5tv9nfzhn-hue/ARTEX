package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferModeField(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"steps→chain", `{"steps":[{"url":"http://a"}]}`, "chain", true},
		{"base_exchange_id→replay", `{"base_exchange_id":"ex1","override_body":"a=1"}`, "replay", true},
		{"identities→bola", `{"identities":[{"name":"anon"}]}`, "bola", true},
		{"race_count→race", `{"url":"http://a","race_count":15}`, "race", true},
		{"exchange_id_a→diff", `{"exchange_id_a":"x","exchange_id_b":"y"}`, "diff", true},
		{"显式 mode 不推断", `{"mode":"timing","steps":[{"url":"http://a"}]}`, "", false},
		{"两个模式同时被暗示则不猜", `{"steps":[{"url":"http://a"}],"identities":[{"name":"anon"}]}`, "", false},
		{"空值不算给出", `{"steps":[],"base_exchange_id":""}`, "", false},
		{"无判别字段", `{"url":"http://a"}`, "", false},
	}
	for _, c := range cases {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.in), &obj); err != nil {
			t.Fatalf("%s: bad fixture: %v", c.name, err)
		}
		got, ok := inferModeField("http_sequence", obj)
		if ok != c.ok || got != c.want {
			t.Fatalf("%s: got (%q,%v) want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestInputErrorHintCarriesRepairInfo(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"mode"},
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"description": "执行模式：chain | replay | bola | race | timing | diff",
			},
			"url": map[string]any{"type": "string", "description": "目标 URL"},
		},
	}
	in := json.RawMessage(`{"url":"http://a/b"}`)
	err := &fakeErr{"input: missing required property \"mode\""}
	got := inputErrorHint("http_sequence", schema, in, err)
	for _, want := range []string{
		`missing required property "mode"`,
		"本次实际收到字段：url",
		"必填但缺失：mode",
		"chain | replay | bola | race | timing | diff",
		"不要原样重试",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示缺少 %q，实际：\n%s", want, got)
		}
	}
}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }

func TestInputErrorHintTypeMismatch(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"strip_headers": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "要剥掉的请求头名称列表",
			},
		},
	}
	in := json.RawMessage(`{"strip_headers":{"Cookie":true}}`)
	got := inputErrorHint("http_sequence", schema, in, &fakeErr{"identities[0].strip_headers: expected array"})
	for _, want := range []string{"identities[0].strip_headers 要求 array", "array<string>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示缺少 %q，实际：\n%s", want, got)
		}
	}
}

func TestRe2RepeatHint(t *testing.T) {
	if hint := re2RepeatHint(`(?s)<div>(.{0,2200})</div>`); hint == "" || !strings.Contains(hint, "1000") {
		t.Fatalf("应提示 RE2 上限，实际 %q", hint)
	}
	if hint := re2RepeatHint(`a{0,10}`); hint != "" {
		t.Fatalf("合法量词不该提示，实际 %q", hint)
	}
}

func TestStringListAcceptsThreeShapes(t *testing.T) {
	cases := map[string][]string{
		`["Cookie","Authorization"]`:  {"Cookie", "Authorization"},
		`"Cookie"`:                    {"Cookie"},
		`{"Cookie":true,"X-Token":1}`: {"Cookie", "X-Token"},
		`null`:                        nil,
	}
	for in, want := range cases {
		var sl stringList
		if err := json.Unmarshal([]byte(in), &sl); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if strings.Join(sl, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: got %v want %v", in, []string(sl), want)
		}
	}
}

func TestStringListRejectsNumbers(t *testing.T) {
	var sl stringList
	if err := json.Unmarshal([]byte(`42`), &sl); err == nil {
		t.Fatalf("数字应报错，实际 %v", []string(sl))
	}
}

func TestLocalPathErrorListsNearbyFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"output-1792496464-27.txt", "output-1792496464-28.txt", "other.log"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := &ToolSet{}
	missing := filepath.Join(dir, "output-1792496464-29.txt")
	msg := ts.localPathError("读取文件", "cmd-output/output-1792496464-29.txt", missing)
	for _, want := range []string{"路径不存在", "output-1792496464-28.txt", "output-1792496464-27.txt"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("提示缺少 %q，实际：\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "other.log") {
		t.Fatalf("不该把不相关文件列进来：\n%s", msg)
	}
}

func TestLocalPathErrorFallsBackToWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wordlist.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := &ToolSet{workDir: dir}
	msg := ts.localPathError("读取 wordlist_file", "wl/probe.txt", filepath.Join(dir, "wl", "probe.txt"))
	if !strings.Contains(msg, "工作目录") {
		t.Fatalf("应回落到工作目录列表：\n%s", msg)
	}
}
