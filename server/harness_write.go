package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 一键配置：plan（只算不写）→ preview（给人看 diff）→ apply（备份后原子写）。
//
// 为什么所有方言都用「文本手术」而不是「反序列化再序列化」：
// JSONC 里全是注释，Go 的 encoding/json 读进来再写出去，注释、键序、缩进
// 全部消失。用户看到自己 200 行的 settings.json 被重排成 40 行，第一反应
// 是再也不敢点第二次。所以这里只做两件事：定位、插入；写完整份内容再
// 反解一遍做校验，解不开就当失败，不落盘。

// HarnessChange 是一条「对某个文件做的改动」。plan 与 apply 返回同一种结构。
type HarnessChange struct {
	ClientID    string `json:"client_id"`
	ClientName  string `json:"client_name"`
	Tier        string `json:"tier"`
	NeedsVerify bool   `json:"needs_verify"`
	Scope       string `json:"scope"`
	Format      string `json:"format"`
	Strategy    string `json:"strategy"`
	Path        string `json:"path"`
	Display     string `json:"display_path"`
	// Action: create / update / noop / manual / skip
	Action  string `json:"action"`
	Reason  string `json:"reason,omitempty"`
	Diff    string `json:"diff,omitempty"`
	After   string `json:"after,omitempty"`
	Snippet string `json:"snippet,omitempty"`

	Applied bool   `json:"applied,omitempty"`
	Backup  string `json:"backup,omitempty"`
	Error   string `json:"error,omitempty"`

	// afterFull 是完整的新内容。After 只用于预览，可能被截断；
	// apply 必须用这份，否则会把「预览截断版」写进文件。
	afterFull string
}

// HarnessPlanInput 是 plan/apply 共用的入参。
type HarnessPlanInput struct {
	Targets    []string
	ProjectDir string
	BaseURL    string // 形如 http://127.0.0.1:8787
	Token      string
	Exe        string
}

// harnessKV 是「有序的键值对」——JSON/TOML/YAML 三种渲染共用一份数据，
// 保证三种方言写出来的字段顺序一致，diff 更好读。
type harnessKV struct {
	Key   string
	Value any // string / []string / []harnessKV
}

// harnessEntry 生成写进配置的那一段，按客户端方言和 artex 的接入方式决定字段。
func harnessEntry(c HarnessClient, in HarnessPlanInput) []harnessKV {
	url := strings.TrimRight(in.BaseURL, "/") + "/mcp"

	if c.Artex == "stdio" {
		// stdio 只是控制台的透明前置：不建库、不拉进程。因此控制台地址与
		// token 必须随配置一起发下去，否则客户端会在自己的当前目录
		// （往往就是桌面）新建一个空库，然后报一句看不懂的错。
		args := []string{"mcp", "serve", "--stdio", "--addr", strings.TrimRight(in.BaseURL, "/")}
		env := []harnessKV{{"ARTEX_MCP_TOKEN", in.Token}}
		switch c.ID {
		case "opencode":
			return []harnessKV{
				{"type", "local"},
				{"command", append([]string{in.Exe}, args...)},
				{"environment", env},
				{"enabled", true},
			}
		case "zed":
			// Zed 的 stdio 是嵌套 command 对象，不是 command+args 平铺。
			return []harnessKV{
				{"command", []harnessKV{{"path", in.Exe}, {"args", args}, {"env", env}}},
			}
		}
		return []harnessKV{
			{"command", in.Exe},
			{"args", args},
			{"env", env},
			{"enabled", true},
		}
	}

	out := make([]harnessKV, 0, 4)
	// type_field 形如 "type=http" / "transport=streamable-http"。
	if c.Remote.TypeField != "" {
		if k, v, ok := strings.Cut(c.Remote.TypeField, "="); ok && k != "" {
			out = append(out, harnessKV{strings.TrimSpace(k), strings.TrimSpace(v)})
		}
	}
	out = append(out, harnessKV{c.Remote.Field, url})
	if c.ID == "opencode" {
		// OpenCode 的 remote 不显式关 oauth 会先去试 OAuth，永远连不上。
		out = append(out, harnessKV{"oauth", false})
	}
	out = append(out, harnessKV{"headers", []harnessKV{{"Authorization", "Bearer " + in.Token}}})
	return out
}

// ---- 渲染 ----

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// renderJSON 单行紧凑输出：插入到别人文件里时，多行值会把缩进搞乱。
func renderJSON(kvs []harnessKV) string {
	parts := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		parts = append(parts, jsonQuote(kv.Key)+":"+renderJSONValue(kv.Value))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func renderJSONValue(v any) string {
	switch t := v.(type) {
	case string:
		return jsonQuote(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []string:
		parts := make([]string, 0, len(t))
		for _, s := range t {
			parts = append(parts, jsonQuote(s))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case []harnessKV:
		return renderJSON(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// renderTOMLBody 输出表体（不含表头）。headers 用内联表，避免再生成子表——
// 替换时只需删掉一个表，不会留下孤立的 [xxx.headers]。
func renderTOMLBody(kvs []harnessKV) string {
	var b strings.Builder
	for _, kv := range kvs {
		switch t := kv.Value.(type) {
		case string:
			b.WriteString(fmt.Sprintf("%s = %s\n", kv.Key, jsonQuote(t)))
		case bool:
			b.WriteString(fmt.Sprintf("%s = %t\n", kv.Key, t))
		case []string:
			parts := make([]string, 0, len(t))
			for _, s := range t {
				parts = append(parts, jsonQuote(s))
			}
			b.WriteString(fmt.Sprintf("%s = [%s]\n", kv.Key, strings.Join(parts, ", ")))
		case []harnessKV:
			inner := make([]string, 0, len(t))
			for _, sub := range t {
				inner = append(inner, fmt.Sprintf("%s = %s", sub.Key, jsonQuote(fmt.Sprint(sub.Value))))
			}
			b.WriteString(fmt.Sprintf("%s = { %s }\n", kv.Key, strings.Join(inner, ", ")))
		}
	}
	return b.String()
}

// renderYAMLBlock 输出以 name: 开头的块，缩进从 base 开始（base 为空格数）。
func renderYAMLBlock(name string, kvs []harnessKV, base int) string {
	pad := strings.Repeat(" ", base)
	var b strings.Builder
	b.WriteString(pad + name + ":\n")
	for _, kv := range kvs {
		switch t := kv.Value.(type) {
		case string:
			b.WriteString(fmt.Sprintf("%s  %s: %s\n", pad, kv.Key, yamlScalar(t)))
		case bool:
			b.WriteString(fmt.Sprintf("%s  %s: %t\n", pad, kv.Key, t))
		case []string:
			b.WriteString(fmt.Sprintf("%s  %s:\n", pad, kv.Key))
			for _, s := range t {
				b.WriteString(fmt.Sprintf("%s    - %s\n", pad, yamlScalar(s)))
			}
		case []harnessKV:
			b.WriteString(fmt.Sprintf("%s  %s:\n", pad, kv.Key))
			for _, sub := range t {
				b.WriteString(fmt.Sprintf("%s    %s: %s\n", pad, sub.Key, yamlScalar(fmt.Sprint(sub.Value))))
			}
		}
	}
	return b.String()
}

// yamlScalar 只在必要时加引号：URL 与命令行里没有 YAML 元字符时保持裸写更可读。
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	need := strings.ContainsAny(s, ":#{}[],&*?|-<>=!%@`'\"") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ")
	if !need {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// ---- JSON / JSONC 文本手术 ----

func jsoncSkipWS(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n':
			i++
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
		default:
			return i
		}
	}
	return i
}

func jsoncStringEnd(s string, i int) int {
	i++ // 跳过开引号
	for i < len(s) {
		if s[i] == '\\' {
			i += 2
			continue
		}
		if s[i] == '"' {
			return i + 1
		}
		i++
	}
	return len(s)
}

func jsoncValueEnd(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '"':
		return jsoncStringEnd(s, i)
	case '{', '[':
		depth := 0
		for i < len(s) {
			switch s[i] {
			case '"':
				i = jsoncStringEnd(s, i)
				continue
			case '/':
				if i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '*') {
					i = jsoncSkipWS(s, i)
					continue
				}
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return len(s)
	default:
		j := i
		for j < len(s) && !strings.ContainsRune(",}\n", rune(s[j])) {
			j++
		}
		for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
			j--
		}
		return j
	}
}

func jsoncObjectBounds(s string, i int) (int, int, bool) {
	if i >= len(s) || s[i] != '{' {
		return 0, 0, false
	}
	depth := 0
	for i < len(s) {
		switch s[i] {
		case '"':
			i = jsoncStringEnd(s, i)
			continue
		case '/':
			if i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '*') {
				i = jsoncSkipWS(s, i)
				continue
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return 0, i, true
			}
		}
		i++
	}
	return 0, 0, false
}

// jsoncFindMember 在 [open,close] 这个对象里找 key，返回值的字节区间。
func jsoncFindMember(s string, open, close int, key string) (int, int, bool) {
	i := jsoncSkipWS(s, open+1)
	for i < close {
		if s[i] != '"' {
			return 0, 0, false
		}
		ks := i
		ke := jsoncStringEnd(s, i)
		var got string
		if err := json.Unmarshal([]byte(s[ks:ke]), &got); err != nil {
			return 0, 0, false
		}
		i = jsoncSkipWS(s, ke)
		if i >= len(s) || s[i] != ':' {
			return 0, 0, false
		}
		vs := jsoncSkipWS(s, i+1)
		ve := jsoncValueEnd(s, vs)
		if got == key {
			return vs, ve, true
		}
		i = jsoncSkipWS(s, ve)
		if i < len(s) && s[i] == ',' {
			i = jsoncSkipWS(s, i+1)
		}
	}
	return 0, 0, false
}

func lineIndentOf(s string, pos int) string {
	start := strings.LastIndexByte(s[:pos], '\n') + 1
	end := start
	for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
		end++
	}
	return s[start:end]
}

func jsoncInsertMember(s string, open, close int, key, raw string) string {
	indent := lineIndentOf(s, open) + "  "
	member := fmt.Sprintf("%s: %s", jsonQuote(key), raw)

	i := jsoncSkipWS(s, open+1)
	if i >= close { // 空对象，不能有前导逗号
		return s[:open+1] + "\n" + indent + member + "\n" + lineIndentOf(s, open) + s[close:]
	}
	prev := close - 1
	for prev > open && strings.ContainsRune(" \t\r\n", rune(s[prev])) {
		prev--
	}
	return s[:prev+1] + ",\n" + indent + member + s[prev+1:]
}

// harnessUpsertJSON 在 root[key][name] 写入 valueJSON；缺哪层补哪层。
func harnessUpsertJSON(content, key, name, valueJSON string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "{\n  " + jsonQuote(key) + ": {\n    " + jsonQuote(name) + ": " + valueJSON + "\n  }\n}\n", nil
	}
	root := jsoncSkipWS(content, 0)
	if root >= len(content) || content[root] != '{' {
		return "", errors.New("根不是 JSON 对象")
	}
	open, close, ok := jsoncObjectBounds(content, root)
	if !ok {
		return "", errors.New("根对象没有闭合")
	}
	vs, _, found := jsoncFindMember(content, open, close, key)
	if !found {
		inner := "{\n    " + jsonQuote(name) + ": " + valueJSON + "\n  }"
		return jsoncInsertMember(content, open, close, key, inner), nil
	}
	if content[vs] != '{' {
		return "", fmt.Errorf("键 %q 已存在但不是对象", key)
	}
	_, c2, ok := jsoncObjectBounds(content, vs)
	if !ok {
		return "", fmt.Errorf("键 %q 的对象没有闭合", key)
	}
	if ns, ne, ok2 := jsoncFindMember(content, vs, c2, name); ok2 {
		// 已存在同名条目：整段替换，保留其余内容与注释。
		return content[:ns] + valueJSON + content[ne:], nil
	}
	return jsoncInsertMember(content, vs, c2, name, valueJSON), nil
}

// ---- TOML ----

func tomlTableKey(line string) string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return ""
	}
	if strings.HasPrefix(t, "[[") {
		return ""
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "["), "]"))
	return strings.ReplaceAll(inner, " ", "")
}

// harnessUpsertTOML 追加或替换一个表。表头永远从第 0 列开始，故边界好找。
func harnessUpsertTOML(content, table, name, body string) (string, error) {
	header := "[" + table + "." + name + "]"
	block := header + "\n" + body
	if strings.TrimSpace(content) == "" {
		return block, nil
	}
	lines := strings.Split(content, "\n")
	start, end := -1, len(lines)
	for i, ln := range lines {
		if tomlTableKey(ln) == table+"."+name {
			start = i
			break
		}
	}
	if start >= 0 {
		for j := start + 1; j < len(lines); j++ {
			if tomlTableKey(lines[j]) != "" {
				end = j
				break
			}
		}
		kept := append([]string{}, lines[:start]...)
		kept = append(kept, strings.Split(strings.TrimRight(block, "\n"), "\n")...)
		kept = append(kept, lines[end:]...)
		return strings.Join(kept, "\n"), nil
	}
	// 没有精确表头，但已经存在同名表（大小写/空格变体）→ 不再追加，交给人。
	if strings.Contains(strings.ReplaceAll(content, " ", ""), "["+table+"."+name+"]") {
		return "", fmt.Errorf("已存在 %s 表的非标准写法，请手工合并", header)
	}
	out := strings.TrimRight(content, "\n")
	if out != "" {
		out += "\n\n"
	}
	return out + block, nil
}

// ---- YAML ----

// harnessUpsertYAML 在 top-level 的 parent 映射里写入 name 块。
func harnessUpsertYAML(content, parent, name string, kvs []harnessKV) (string, error) {
	block := renderYAMLBlock(name, kvs, 2)
	if strings.TrimSpace(content) == "" {
		return parent + ":\n" + block, nil
	}
	lines := strings.Split(content, "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, parent+":") && (len(ln) == len(parent)+1 || ln[len(parent)+1] == ' ' || ln[len(parent)+1] == '\t' || ln[len(parent)+1] == '#') {
			start = i
			break
		}
	}
	if start < 0 {
		out := strings.TrimRight(content, "\n")
		return out + "\n" + parent + ":\n" + block, nil
	}
	// parent 的子块：到下一个第 0 列的非空非注释行为止。
	end := len(lines)
	for j := start + 1; j < len(lines); j++ {
		ln := lines[j]
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
			end = j
			break
		}
	}
	// 已有 name 子块就替换它。
	childStart, childEnd := -1, end
	for j := start + 1; j < end; j++ {
		ln := lines[j]
		if strings.HasPrefix(ln, "  "+name+":") {
			childStart = j
			for k := j + 1; k < end; k++ {
				nl := lines[k]
				if nl == "" {
					continue
				}
				if !strings.HasPrefix(nl, "   ") {
					childEnd = k
					break
				}
			}
			break
		}
	}
	blockLines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	var out []string
	if childStart >= 0 {
		out = append(out, lines[:childStart]...)
		out = append(out, blockLines...)
		out = append(out, lines[childEnd:]...)
	} else {
		// 插到 parent 子块末尾，且保证前面有个空行使 diff 好看。
		out = append(out, lines[:end]...)
		out = append(out, blockLines...)
		out = append(out, lines[end:]...)
	}
	return strings.Join(out, "\n"), nil
}

// ---- Cordis（DSH）----

// harnessCordisBlock 生成一条 Cordis 插件插入项。
//
// DSH 没有 mcpServers：MCP 是 @deepseek-ai/dsh-mcp-client 的一个实例，
// 一台 server 一个实例，靠 id 与 serverName 定位。
func harnessCordisBlock(in HarnessPlanInput) string {
	var b strings.Builder
	b.WriteString("- insert:\n")
	b.WriteString("    - id: mcp-artex\n")
	b.WriteString("      name: '@deepseek-ai/dsh-mcp-client'\n")
	b.WriteString("      config:\n")
	b.WriteString("        serverName: artex\n")
	b.WriteString("        transport: streamable-http\n")
	b.WriteString(fmt.Sprintf("        url: %s\n", strings.TrimRight(in.BaseURL, "/")+"/mcp"))
	b.WriteString("        headers:\n")
	b.WriteString(fmt.Sprintf("          Authorization: %s\n", yamlScalar("Bearer "+in.Token)))
	return b.String()
}

// harnessUpsertCordis 在 Cordis patch 文件末尾追加 insert 项。
//
// 同一个 id 在机器级与 profile 级各声明一次会让 loader 组合失败，所以
// 发现已存在就**不改**：宁可让用户手工更新，也不制造「profile 起不来」。
func harnessUpsertCordis(content string, in HarnessPlanInput) (string, error) {
	if strings.Contains(content, "id: mcp-artex") {
		return "", errors.New("已存在 id: mcp-artex，未改动（同 id 重复声明会让 profile 组合失败）")
	}
	block := harnessCordisBlock(in)
	if strings.TrimSpace(content) == "" {
		return block, nil
	}
	// patch 文件必须是序列；根是映射说明这不是 Cordis patch 文件。
	for _, ln := range strings.Split(content, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if !strings.HasPrefix(t, "-") {
			return "", errors.New("文件根节点不是 YAML 序列，疑似不是 cordis.patch.yml")
		}
		break
	}
	return strings.TrimRight(content, "\n") + "\n\n" + block, nil
}

// ---- plan / apply ----

// harnessDiff 只做「公共前后缀 + 中段替换」的朴素 diff。
//
// 一键配置的改动永远是「加一段」或「换一段」，用不着 Myers；朴素 diff 反而
// 更容易读：上下文在上，删除在下，新增在最后，长度也小。
func harnessDiff(before, after string) string {
	if before == after {
		return ""
	}
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	pre := 0
	for pre < len(b) && pre < len(a) && b[pre] == a[pre] {
		pre++
	}
	suf := 0
	for suf < len(b)-pre && suf < len(a)-pre && b[len(b)-1-suf] == a[len(a)-1-suf] {
		suf++
	}
	var out strings.Builder
	from := pre - 3
	if from < 0 {
		from = 0
	}
	out.WriteString(fmt.Sprintf("@@ -%d +%d @@\n", pre+1, pre+1))
	for _, ln := range b[from:pre] {
		out.WriteString(" " + ln + "\n")
	}
	for _, ln := range b[pre : len(b)-suf] {
		out.WriteString("-" + ln + "\n")
	}
	for _, ln := range a[pre : len(a)-suf] {
		out.WriteString("+" + ln + "\n")
	}
	to := len(b) - suf
	if to+3 < len(b) {
		to += 3
	} else {
		to = len(b)
	}
	for _, ln := range b[len(b)-suf : to] {
		out.WriteString(" " + ln + "\n")
	}
	return out.String()
}

// harnessPlanAll 只读：算出每个目标要做的改动，一个字节都不落盘。
func harnessPlanAll(in HarnessPlanInput) []HarnessChange {
	reg, err := HarnessRegistryData()
	if err != nil || reg == nil {
		return nil
	}
	changes := make([]HarnessChange, 0, len(in.Targets))
	for _, id := range in.Targets {
		c, ok := reg.Client(id)
		if !ok {
			changes = append(changes, HarnessChange{ClientID: id, Action: "skip", Reason: "注册表里没有这个客户端"})
			continue
		}
		changes = append(changes, harnessPlanClient(c, in)...)
	}
	return changes
}

func harnessPlanClient(c HarnessClient, in HarnessPlanInput) []HarnessChange {
	base := HarnessChange{ClientID: c.ID, ClientName: c.Name, Tier: c.Tier, NeedsVerify: c.NeedsVerify}

	// C 级与 manual：只给片段。
	if c.Artex == "manual" || c.Tier == "C" {
		base.Action = "manual"
		base.Reason = c.Notes
		base.Snippet = harnessSnippet(c, in)
		return []HarnessChange{base}
	}

	out := make([]HarnessChange, 0, len(c.Config))
	for _, cfg := range c.Config {
		if !harnessConfigForOS(cfg) {
			continue
		}
		ch := base
		ch.Scope, ch.Format, ch.Strategy, ch.Display = cfg.Scope, cfg.Format, cfg.Strategy, cfg.Path

		if cfg.Strategy == "manual" || cfg.Format == "ui" {
			ch.Action = "manual"
			ch.Reason = "该客户端只有 UI 入口，没有可写的配置文件"
			ch.Snippet = harnessSnippet(c, in)
			out = append(out, ch)
			continue
		}
		if cfg.Strategy == "cli-only" {
			ch.Action = "manual"
			ch.Reason = "该文件同时存放登录态，只能通过官方 CLI 写入"
			ch.Snippet = harnessSnippet(c, in)
			out = append(out, ch)
			continue
		}
		abs, ok := harnessResolveConfigPath(cfg, in.ProjectDir)
		if !ok {
			ch.Action = "manual"
			ch.Reason = "路径含未定义变量，无法定位"
			ch.Snippet = harnessSnippet(c, in)
			out = append(out, ch)
			continue
		}
		ch.Path = abs
		if !harnessPathAllowed(abs, in.ProjectDir) {
			ch.Action = "manual"
			ch.Reason = "目标不在用户目录或指定项目目录内，拒绝写入"
			ch.Snippet = harnessSnippet(c, in)
			out = append(out, ch)
			continue
		}
		if !harnessScopeWritable(cfg.Scope) && in.ProjectDir == "" {
			ch.Action = "manual"
			ch.Reason = "项目级配置：请先指定项目目录"
			ch.Snippet = harnessSnippet(c, in)
			out = append(out, ch)
			continue
		}

		before := ""
		existed := false
		if data, err := os.ReadFile(abs); err == nil {
			before = string(data)
			existed = true
		}
		after, err := harnessRender(cfg, c, in, before)
		if err == nil {
			err = harnessValidate(cfg.Format, after)
		}
		if err != nil {
			ch.Action = "manual"
			ch.Reason = err.Error()
			ch.Snippet = harnessSnippet(c, in)
			out = append(out, ch)
			continue
		}
		ch.afterFull = after
		ch.After = harnessTruncate(after)
		if after == before {
			ch.Action = "noop"
			ch.Reason = "配置里已经是这份内容"
			out = append(out, ch)
			continue
		}
		ch.Action = "create"
		if existed {
			ch.Action = "update"
		}
		ch.Diff = harnessDiff(before, after)
		out = append(out, ch)
	}
	if len(out) == 0 {
		base.Action = "skip"
		base.Reason = "没有适用于本平台的配置路径"
		out = append(out, base)
	}
	return out
}

// harnessValidate 在落盘前把「我们刚拼出来的这份内容」再解一遍。
//
// 为什么值得多这一步：一键配置是全自动写用户的配置文件，写坏一次用户就再也
// 不敢点第二次。JSON/JSONC 能就地验；TOML/YAML 没有标准库解析器，交给写入后
// 回扫（内容里能不能找到 artex）做最低限度的确认。
func harnessValidate(format, content string) error {
	switch format {
	case "json":
		var probe map[string]any
		if err := json.Unmarshal([]byte(content), &probe); err != nil {
			return fmt.Errorf("生成的 JSON 解不开，已放弃写入: %v", err)
		}
	case "jsonc":
		var probe map[string]any
		if err := json.Unmarshal([]byte(stripJSONC(content)), &probe); err != nil {
			return fmt.Errorf("生成的 JSONC 解不开，已放弃写入: %v", err)
		}
	}
	return nil
}

// stripJSONC 去掉注释与尾逗号，只为「能不能解开」这一件事。
//
// 它不是解析器，也不打算是：字符串里的 // 不动，注释里的引号不管。
// 用它做校验，宁可漏报（放过一个坏文件），也不误杀（把好文件判成坏）。
func stripJSONC(s string) string {
	var b strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr {
			b.WriteByte(ch)
			if ch == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			b.WriteByte(ch)
			continue
		}
		if ch == '/' && i+1 < len(s) {
			if s[i+1] == '/' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				if i < len(s) {
					b.WriteByte('\n')
				}
				continue
			}
			if s[i+1] == '*' {
				i += 2
				for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		b.WriteByte(ch)
	}
	return stripTrailingCommas(b.String())
}

// stripTrailingCommas 把 ,} 与 ,] 里的逗号去掉（同样只在字符串外动手）。
func stripTrailingCommas(s string) string {
	var b strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inStr {
			b.WriteByte(ch)
			if ch == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			b.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(s) && strings.ContainsRune(" \t\r\n", rune(s[j])) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue // 丢掉这个逗号
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// harnessRender 按方言把 entry 合进现有内容。
func harnessRender(cfg HarnessConfig, c HarnessClient, in HarnessPlanInput, before string) (string, error) {
	kvs := harnessEntry(c, in)
	switch cfg.Format {
	case "json", "jsonc":
		key := cfg.Key
		if key == "" {
			key = "mcpServers"
		}
		return harnessUpsertJSON(before, key, "artex", renderJSON(kvs))
	case "toml":
		table := "mcp_servers"
		if cfg.Key != "" {
			if trimmed := strings.Trim(cfg.Key, "[]"); trimmed != "" {
				table = strings.Split(trimmed, ".")[0]
			}
		}
		return harnessUpsertTOML(before, table, "artex", renderTOMLBody(kvs))
	case "yaml":
		parent := cfg.Key
		if parent == "" {
			parent = "extensions"
		}
		return harnessUpsertYAML(before, parent, "artex", kvs)
	case "cordis-yaml":
		return harnessUpsertCordis(before, in)
	}
	return "", fmt.Errorf("不支持的格式 %q", cfg.Format)
}

// harnessPathAllowed 是写入护栏：只允许写进用户目录或显式指定的项目目录。
//
// 没有这道闸，一个被改坏的注册表（或一次注入）就能让「一键配置」变成
// 「一键写 C:\Windows」。
func harnessPathAllowed(path, projectDir string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if projectDir != "" {
		if pj, err := filepath.Abs(projectDir); err == nil {
			rel, err := filepath.Rel(filepath.Clean(pj), clean)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(home), clean)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func harnessTruncate(s string) string {
	const max = 32 << 10
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…（预览已截断，写入的仍是完整内容）"
}

// harnessApplyAll 逐条落盘：备份 → 临时文件 → rename → 回读校验。
//
// 先写临时文件再 rename，是为了「写一半断电」不会留下半个 JSON。
// 回读比对 sha256，是为了「写成功但内容不对」不会静默通过。
func harnessApplyAll(changes []HarnessChange, projectDir string, backup bool) []HarnessChange {
	out := make([]HarnessChange, 0, len(changes))
	for _, ch := range changes {
		if ch.Action != "create" && ch.Action != "update" {
			out = append(out, ch)
			continue
		}
		if !harnessPathAllowed(ch.Path, projectDir) {
			ch.Error = "目标不在允许写入的范围内"
			out = append(out, ch)
			continue
		}
		// After 可能被截断过，这里重新渲染一份完整内容。
		full := ch.fullAfter()
		if full == "" {
			ch.Error = "内部错误：缺少待写入内容"
			out = append(out, ch)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(ch.Path), 0o700); err != nil {
			ch.Error = err.Error()
			out = append(out, ch)
			continue
		}
		if backup {
			if _, err := os.Stat(ch.Path); err == nil {
				bk := ch.Path + ".artex.bak." + time.Now().Format("20060102-150405")
				if err := copyFile(ch.Path, bk); err != nil {
					ch.Error = "备份失败：" + err.Error()
					out = append(out, ch)
					continue
				}
				ch.Backup = bk
			}
		}
		tmp := ch.Path + ".artex.tmp"
		if err := os.WriteFile(tmp, []byte(full), 0o600); err != nil {
			ch.Error = err.Error()
			out = append(out, ch)
			continue
		}
		if err := os.Rename(tmp, ch.Path); err != nil {
			_ = os.Remove(tmp)
			ch.Error = err.Error()
			out = append(out, ch)
			continue
		}
		got, err := os.ReadFile(ch.Path)
		if err != nil || sha256Hex(got) != sha256Hex([]byte(full)) {
			ch.Error = "写入后校验不一致"
			out = append(out, ch)
			continue
		}
		ch.Applied = true
		out = append(out, ch)
	}
	return out
}

// fullAfter 返回完整的新内容：plan 阶段把完整内容存在 afterFull 里，
// 对外 JSON 只给截断版。
func (c HarnessChange) fullAfter() string { return c.afterFull }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// harnessSnippet 给「写不了」的客户端一段可复制的配置。
func harnessSnippet(c HarnessClient, in HarnessPlanInput) string {
	url := strings.TrimRight(in.BaseURL, "/") + "/mcp"
	switch c.ID {
	case "claude-code":
		return fmt.Sprintf("claude mcp add --transport http artex %s \\\n  --header \"Authorization: Bearer %s\" --scope user", url, in.Token)
	case "codex":
		args := []string{"mcp", "serve", "--stdio", "--addr", strings.TrimRight(in.BaseURL, "/")}
		quoted := make([]string, 0, len(args))
		for _, a := range args {
			quoted = append(quoted, jsonQuote(a))
		}
		return fmt.Sprintf("[mcp_servers.artex]\ncommand = %s\nargs = [%s]\nenv = { ARTEX_MCP_TOKEN = %s }",
			jsonQuote(in.Exe), strings.Join(quoted, ", "), jsonQuote(in.Token))
	}
	kvs := harnessEntry(c, in)
	switch c.Config[0].Format {
	case "toml":
		return "[mcp_servers.artex]\n" + renderTOMLBody(kvs)
	case "yaml":
		return renderYAMLBlock("artex", kvs, 2)
	case "cordis-yaml":
		return harnessCordisBlock(in)
	}
	pretty, err := json.MarshalIndent(harmlessJSON(kvs), "", "  ")
	if err != nil {
		return renderJSON(kvs)
	}
	return string(pretty)
}

// harmlessJSON 把有序 KV 转回普通 map 只为格式化输出；片段里键序不重要。
func harmlessJSON(kvs []harnessKV) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		switch t := kv.Value.(type) {
		case []harnessKV:
			out[kv.Key] = harmlessJSON(t)
		case []string:
			out[kv.Key] = t
		default:
			out[kv.Key] = t
		}
	}
	return out
}

// sortedTargets 让 plan/apply 的输出顺序稳定，前端 diff 不会每次刷新都跳。
func sortedTargets(ids []string) []string {
	cp := append([]string{}, ids...)
	sort.Strings(cp)
	return cp
}
