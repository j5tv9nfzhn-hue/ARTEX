package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 本文件负责把「模型给错参数」变成一条【一次就能改对】的反馈，而不是一句
// 「请核对工具参数说明后重新调用」。两块能力：
//
//  1. inferModeField —— mode 型工具（http_sequence 等）漏传 mode 时，从它同时给出的
//     判别字段反推。只在**唯一可判定**时生效：给了 steps 必然是 chain，给了
//     base_exchange_id/override_* 必然是 replay……两个 mode 同时被暗示则不猜。
//     判定结果写日志，不静默。
//  2. inputErrorHint —— 校验失败时把「实际收到的字段 / 必填但缺失 / 该字段声明的类型、
//     枚举与说明」一并回给模型。同样的错误下一次就能改对，而不是反复盲试。
//
// 两者都不改变工具的语义与安全边界：不清空、不降级、不放宽校验，只是把错误讲清楚。

// modeInference 记录「哪些入参只有在某个 mode 下才有意义」。
var modeInference = map[string][]struct{ field, mode string }{
	"http_sequence": {
		{field: "steps", mode: "chain"},
		{field: "initial_vars", mode: "chain"},
		{field: "base_exchange_id", mode: "replay"},
		{field: "override_method", mode: "replay"},
		{field: "override_url", mode: "replay"},
		{field: "override_path", mode: "replay"},
		{field: "override_query", mode: "replay"},
		{field: "override_headers", mode: "replay"},
		{field: "override_cookie", mode: "replay"},
		{field: "override_body", mode: "replay"},
		{field: "strip_headers", mode: "replay"},
		{field: "identities", mode: "bola"},
		{field: "race_count", mode: "race"},
		{field: "baseline_url", mode: "timing"},
		{field: "baseline_body", mode: "timing"},
		{field: "baseline_runs", mode: "timing"},
		{field: "exchange_id_a", mode: "diff"},
		{field: "exchange_id_b", mode: "diff"},
		{field: "text_a", mode: "diff"},
		{field: "text_b", mode: "diff"},
	},
}

// emptyJSONScalar 判断一个入参值是否等价于「没给」。
func emptyJSONScalar(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", `""`, "[]", "{}":
		return true
	}
	return false
}

// inferModeField 在 mode 缺失（或为空）时，按已给出的判别字段反推唯一 mode。
func inferModeField(toolName string, obj map[string]json.RawMessage) (string, bool) {
	rules, ok := modeInference[toolName]
	if !ok {
		return "", false
	}
	if raw, present := obj["mode"]; present && !emptyJSONScalar(raw) {
		return "", false
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if raw, present := obj[r.field]; present && !emptyJSONScalar(raw) {
			seen[r.mode] = true
		}
	}
	if len(seen) != 1 {
		return "", false
	}
	for mode := range seen {
		return mode, true
	}
	return "", false
}

var (
	missingPropRe  = regexp.MustCompile(`missing required property "([^"]+)"`)
	expectedTypeRe = regexp.MustCompile(`^([A-Za-z0-9_.\[\]]+): expected (\w+)`)
)

// inputErrorHint 组装参数校验失败的模型可见文本：原始错误 + 收到什么 + 缺什么 + 缺的字段怎么填。
func inputErrorHint(toolName string, schema map[string]any, in json.RawMessage, verr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "参数校验未通过（stage=tool_validate，工具 %s）：%v。", toolName, verr)

	props, _ := schema["properties"].(map[string]any)
	required := toStrings(schema["required"])
	got := receivedKeys(in)
	if len(got) == 0 {
		b.WriteString("本次实际收到：空对象。")
	} else {
		fmt.Fprintf(&b, "本次实际收到字段：%s。", strings.Join(got, ", "))
	}

	if len(required) > 0 {
		var missing []string
		for _, r := range required {
			if !containsString(got, r) {
				missing = append(missing, r)
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(&b, " 必填但缺失：%s。", strings.Join(missing, ", "))
			for _, m := range missing {
				if brief := schemaPropBrief(props, m); brief != "" {
					fmt.Fprintf(&b, " [%s] %s；", m, brief)
				}
			}
		}
	}

	local := verr.Error()
	if m := expectedTypeRe.FindStringSubmatch(local); m != nil {
		path, want := m[1], m[2]
		leaf := path
		if i := strings.LastIndexAny(leaf, "."); i >= 0 {
			leaf = leaf[i+1:]
		}
		if brief := schemaPropBrief(props, leaf); brief != "" {
			fmt.Fprintf(&b, " 字段 %s 要求 %s：%s；", path, want, brief)
		} else {
			fmt.Fprintf(&b, " 字段 %s 要求 %s；", path, want)
		}
	}

	if toolName == "http_sequence" && !containsString(got, "mode") {
		b.WriteString(" 提示：mode 可省略，但必须能由入参唯一确定（steps→chain，base_exchange_id/override_*→replay，identities→bola，race_count→race，baseline_*→timing，exchange_id_a/b 或 text_a/b→diff）；也可显式传 mode=chain | replay | bola | race | timing | diff。")
	}

	b.WriteString("请按上述说明修正后重新调用，不要原样重试。")
	return b.String()
}

// schemaPropBrief 用一行话描述某个参数：类型 / 枚举 / 说明首句。
func schemaPropBrief(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	pm, ok := props[key].(map[string]any)
	if !ok {
		return ""
	}
	var parts []string
	if t, _ := pm["type"].(string); t != "" {
		if items, ok := pm["items"].(map[string]any); ok {
			if it, _ := items["type"].(string); it != "" {
				t = "array<" + it + ">"
			}
		}
		parts = append(parts, t)
	}
	if modes := enumOf(pm); len(modes) > 0 {
		parts = append(parts, "可选值: "+strings.Join(modes, " | "))
	}
	if d, _ := pm["description"].(string); strings.TrimSpace(d) != "" {
		parts = append(parts, firstSentence(d, 160))
	}
	return strings.Join(parts, "；")
}

func enumOf(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return toStrings(m["enum"])
}

// receivedKeys 返回本次入参的顶层字段名（排序，最多 24 个），用于告诉模型它到底发了什么。
func receivedKeys(in json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(in, &obj); err != nil || len(obj) == 0 {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k, v := range obj {
		if emptyJSONScalar(v) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 24 {
		keys = keys[:24]
	}
	return keys
}

// toStrings 把 schema 里的 []any / []string 统一成 []string（JSON Schema 的 required、
// enum 在 map[string]any 里落地为 []any）。
func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func firstSentence(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.IndexAny(s, "。.;；"); i > 0 && i < limit {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return s
}

var repeatCountRe = regexp.MustCompile(`\{(\d*),?(\d*)\}`)

// re2RepeatHint 识别 Go RE2 的重复次数上限（1000）导致的编译失败。
// 模型常用 {0,2000}/{0,2200} 表示「最多吃掉这么多字符」，而 RE2 上限是 1000，
// 直接回一句 "invalid repeat count" 会让它把同一条正则反复重试。
func re2RepeatHint(expr string) string {
	for _, m := range repeatCountRe.FindAllStringSubmatch(expr, -1) {
		for _, raw := range []string{m[1], m[2]} {
			if raw == "" {
				continue
			}
			if n, err := strconv.Atoi(raw); err == nil && n > 1000 {
				return fmt.Sprintf("正则表达式超出 Go RE2 的重复次数上限 1000：%s 中的 %s 要求 %d 次重复。请改为 {0,1000}，或改用 .*? / (?s).*? 配合更精确的锚点，并用 max_matches 控制返回量。", expr, m[0], n)
			}
		}
	}
	return ""
}
