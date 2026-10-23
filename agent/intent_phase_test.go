package agent

import (
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/db"
)

// nodeWithPayload 构造一条只带 payload 的意图节点（够 intentPhase 用）。
func nodeWithPayload(payload string) *db.Node {
	return &db.Node{ID: 1, Payload: json.RawMessage(payload)}
}

func TestInferIntentPhase(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    string
	}{
		// 明确声明优先
		{"declared wins", `{"summary":"随便看看","phase":"verify"}`, "verify"},
		// exploit
		{"端口扫描+利用", `{"summary":"对 10.0.0.5 的 8080 做 Struts2 命令执行利用"}`, "exploit"},
		{"弱口令爆破", `{"summary":"对 /login 做后台弱口令爆破"}`, "exploit"},
		{"未授权访问", `{"summary":"验证 /api/admin 是否存在未授权访问"}`, "exploit"},
		{"上传绕过", `{"summary":"测试头像上传的扩展名绕过"}`, "exploit"},
		// recon
		{"全端口扫描", `{"summary":"对 10.0.0.5 做全端口扫描，识别服务 banner"}`, "recon"},
		{"子域枚举", `{"summary":"枚举 example.com 的子域名与存活主机"}`, "recon"},
		{"资产指纹", `{"summary":"整理已收集资产的指纹并补全服务信息"}`, "recon"},
		{"英文 recon", `{"summary":"scan ports and fingerprint services on the host"}`, "recon"},
		// verify（只在没有 exploit/recon 信号时才用）
		{"复现确认", `{"summary":"确认 CVE-2024-1234 在该版本上是否可以复现"}`, "verify"},
		{"排除误报", `{"summary":"复核上一条结论，排除误报"}`, "verify"},
		{"验证码", `{"summary":"处理登录验证码后继续测试"}`, "verify"},
		// 判不出 → 空（全量面）
		{"无文字", `{}`, ""},
		{"含糊", `{"summary":"看看这个目标"}`, ""},
		{"英文含糊", `{"summary":"take a look"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := intentPhase(nodeWithPayload(c.summary))
			if got != c.want {
				t.Fatalf("intentPhase(%s) = %q, want %q", c.summary, got, c.want)
			}
		})
	}
}

// 推断不出阶段时必须保持全量面——这是「不许因为猜错而少给工具」的兜底。
func TestIntentPhaseUnknownFallsBackToFullFace(t *testing.T) {
	ts := &ToolSet{}
	ts.SetPhase(intentPhase(nodeWithPayload(`{"summary":"看看这个目标"}`)))
	if len(ts.WorkerTools()) != len((&ToolSet{}).WorkerTools()) {
		t.Fatalf("unknown intent must keep the full tool face, got %d tools", len(ts.WorkerTools()))
	}
}

func toolNames(tools []actool.CoreTool) map[string]bool {
	m := map[string]bool{}
	for _, t := range tools {
		m[t.Name()] = true
	}
	return m
}

func TestPruneSpecialistTools(t *testing.T) {
	base := (&ToolSet{}).allWorkerTools()

	// 没有任何专用能力信号 → 四个专用工具全部摘掉，其余一个不少。
	pruned := pruneSpecialistTools(base, "对 10.0.0.1 做端口扫描")
	got := toolNames(pruned)
	for _, name := range []string{"http_fuzz", "codec_crypto", "extract_data", "binary_inspect"} {
		if got[name] {
			t.Fatalf("%s should be pruned without its signal", name)
		}
	}
	for _, name := range []string{"http_request", "http_sequence", "record_fact", "report_finding", "handoff_patch", "tactical_kb", "insert_assets"} {
		if !got[name] {
			t.Fatalf("%s must always survive pruning", name)
		}
	}

	// 有信号就保留对应工具。
	cases := map[string][]string{
		"对登录口做爆破，字典猜解":    {"http_fuzz"},
		"解密前端参数，jwt 签名伪造": {"codec_crypto"},
		"把响应里的字段提取出来":     {"extract_data"},
		"逆向这个 apk 看固件":    {"binary_inspect"},
	}
	for hint, want := range cases {
		names := toolNames(pruneSpecialistTools(base, hint))
		for _, n := range want {
			if !names[n] {
				t.Fatalf("hint %q should keep %s", hint, n)
			}
		}
	}

	// hint 为空 → 一律不裁（判断不了就不动）。
	if len(pruneSpecialistTools(base, "")) != len(base) {
		t.Fatal("empty hint must not prune anything")
	}
}

// faceBytes 复刻运行期体积口径：lean 瘦身 + 按名字去重后的 name/description/
// prompt/input_schema 的 JSON 总和。
func faceBytes(tools []actool.CoreTool) int {
	seen := map[string]bool{}
	total := 0
	for _, tl := range leanToolList(tools) {
		if seen[tl.Name()] {
			continue
		}
		seen[tl.Name()] = true
		b, _ := json.Marshal(map[string]any{
			"name": tl.Name(), "description": tl.Description(),
			"prompt": tl.Prompt(), "input_schema": tl.InputSchema(),
		})
		total += len(b)
	}
	return total
}

// 验收口径：worker 每次请求的「固定开销」（工具面 + 系统提示种子）必须 < 10K token。
// 按 3.2 B/token 折算 ≈ 32,000 字节。这条测试就是那条验收线。
func TestWorkerToolFaceBudget(t *testing.T) {
	const budgetBytes = 32000
	seed := len(BuiltinPromptSeeds()["worker"])
	sdk := defaultToolsExcept("MultiEdit", "LS", "Sleep", "Write", "Edit", "apply_patch")

	faces := []struct {
		name    string
		phase   string
		hint    string
		wantMax int
	}{
		{"recon", "recon", "枚举目标子域名与端口", 0},
		{"verify", "verify", "复核误报", 0},
		{"exploit 无专用信号", "exploit", "对管理后台做越权访问测试", 0},
		{"exploit 带 fuzz 信号", "exploit", "对登录口做弱口令爆破", 0},
		{"exploit 全部专用信号", "exploit", "爆破并解密提取二进制", 0},
	}
	for _, f := range faces {
		ts := &ToolSet{}
		ts.SetPhase(f.phase)
		ts.SetIntentHint(f.hint)
		tools := append(append([]actool.CoreTool{}, ts.WorkerTools()...), sdk...)
		got := faceBytes(tools) + seed
		if got >= budgetBytes {
			t.Fatalf("%s: face %d B ≈ %d tok, over the 10K budget (%d B)", f.name, got, got*10/32, budgetBytes)
		}
		t.Logf("%-24s tools+seed = %5d B (~%4d tok)", f.name, got, got*10/32)
	}
}

// 全量面（判不出阶段时的兜底）保持原样，不被本改造影响。
func TestUnknownPhaseFaceUnchanged(t *testing.T) {
	ts := &ToolSet{}
	got := len(ts.WorkerTools())
	want := len((&ToolSet{}).allWorkerTools())
	if got != want {
		t.Fatalf("full face changed: %d != %d", got, want)
	}
	if strings.TrimSpace(ts.intentHint) != "" {
		t.Fatal("hint should default to empty")
	}
}
