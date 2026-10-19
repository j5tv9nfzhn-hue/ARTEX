package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestCuratorRedactsCredentialShapes(t *testing.T) {
	raw := "password=s3cr3t token: abcdef Authorization: Bearer abcdefghijklmnop " +
		"cookie=session123 Cookie: sid=1111; auth_token=2222 " +
		"https://admin:superpass123@example.com/api sk-abcdefgh12345678 " +
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEASecretKey\n-----END RSA PRIVATE KEY----- " +
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop"
	out := curatorRedact(raw)
	for _, secret := range []string{
		"s3cr3t", "abcdefghijklmnop", "session123", "auth_token=2222",
		"superpass123", "sk-abcdefgh12345678", "MIIEpAIBAAKCAQEASecretKey",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q survived redaction: %s", secret, out)
		}
	}
	if !strings.Contains(out, "[已脱敏]") {
		t.Fatalf("redaction marker missing: %s", out)
	}
}

func TestCuratorSkillNameIsValidAndDomainAware(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	for _, tc := range []struct {
		domain string
		want   string
	}{
		{"web_security", "case-web-42"},
		{"software_re", "case-re-42"},
		{"", "case-web-42"},
	} {
		name := curatorSkillName(&db.Task{ID: 42, AnalysisDomain: tc.domain})
		if name != tc.want || !re.MatchString(name) {
			t.Fatalf("domain %q -> %q (want %q, valid=%t)", tc.domain, name, tc.want, re.MatchString(name))
		}
	}
}

func TestCuratorSkillNameAddsSemanticSlug(t *testing.T) {
	name := curatorSkillName(&db.Task{ID: 42, AnalysisDomain: "web_security"}, "登录接口安全测试", "SQL Injection", "auth_gateway.elf")
	if name != "case-web-login-api-sql-injection-42" {
		t.Fatalf("semantic name = %q (want %q)", name, "case-web-login-api-sql-injection-42")
	}
	// 无语义信号时回退旧命名，保持与既有提议/磁盘目录兼容。
	if back := curatorSkillName(&db.Task{ID: 42, AnalysisDomain: "web_security"}); back != "case-web-42" {
		t.Fatalf("fallback name = %q (want %q)", back, "case-web-42")
	}
}

func TestCuratorSubstantiveFiltersThinTasks(t *testing.T) {
	if curatorSubstantive(&curatorData{Task: &db.Task{ID: 1}}) {
		t.Fatal("empty task must not be distilled")
	}
	thin := &curatorData{Task: &db.Task{ID: 1}, Intents: []*db.Node{{ID: 1}}}
	if curatorSubstantive(thin) {
		t.Fatal("single intent must not be distilled")
	}
	rich := &curatorData{Task: &db.Task{ID: 1}, Intents: []*db.Node{{ID: 1}, {ID: 2}}}
	if !curatorSubstantive(rich) {
		t.Fatal("two intents should be distillable")
	}
	reRich := &curatorData{
		Task:        &db.Task{ID: 2, AnalysisDomain: db.AnalysisDomainSoftwareRE},
		RESamples:   []db.RESample{{Name: "sample.bin", SHA256: "0123456789abcdef"}},
		REEvidences: []db.REEvidence{{Operation: "decompile", Tool: "ghidra_decompile_function"}},
	}
	if !curatorSubstantive(reRich) {
		t.Fatal("RE sample with static evidence should be distillable")
	}
}

func TestBuildCuratorSkillDocumentCarriesArtifactsAndPitfalls(t *testing.T) {
	node := func(id int64, state, payload string) *db.Node {
		return &db.Node{ID: id, State: state, Payload: json.RawMessage(payload)}
	}
	d := &curatorData{
		Task: &db.Task{
			ID:             42,
			AnalysisDomain: "web_security",
			Name:           "登录接口安全测试",
			Goal:           "验证登录接口的 SQL 注入与越权",
		},
		Goals: []*db.Node{node(1, "open", `{"text":"验证登录接口"}`)},
		Intents: []*db.Node{
			node(10, "done", `{"summary":"枚举登录接口参数"}`),
			node(11, "blocked", `{"summary":"构造布尔盲注"}`),
		},
		Facts: []*db.Node{
			node(20, "confirmed", `{"summary":"username 参数存在报错型注入","confidence":"observed","evidence":"payload: ' OR 1=1 --"}`),
			node(21, "confirmed", `{"summary":"登录响应可区分用户是否存在","confidence":"inferred"}`),
		},
		Findings: []*db.Node{
			node(30, "confirmed", `{"name":"登录表单 SQL 注入","vulnclass":"SQL Injection","severity":"high","summary":"username 参数可注入","evidence":{"poc":"' OR 1=1 --"}}`),
		},
		Retests: []curatorRetestRecord{
			{
				FindingName: "登录表单 SQL 注入",
				VulnClass:   "SQL Injection",
				Verdict:     "fixed",
				Summary:     "参数化查询已生效，单引号不再引发报错",
				Notes:       "v1.2.1 补丁验证",
			},
		},
		RESamples: []db.RESample{
			{Name: "auth_gateway.elf", SHA256: "abcdef0123456789abcdef0123456789", Size: 65536},
		},
		REEvidences: []db.REEvidence{
			{Operation: "decompile", Tool: "ghidra_decompile_function", Diagnostic: "定位 verify_token 函数签名逻辑"},
		},
		Hints: []*db.Node{node(40, "active", `{"text":"不要对生产账号做爆破"}`)},
		Activity: []db.Activity{
			{Kind: "tool_use", Tool: "http_request", Summary: `http_request {"method":"POST","url":"https://target/login"}`},
			{Kind: "tool_use", Tool: "http_request", Summary: `http_request {"method":"POST","url":"https://target/login"}`},
			{Kind: "tool_result", Tool: "http_request", IsError: true, Summary: "专家战术熔断提醒：同一请求已连续三次相同负反馈"},
			{Kind: "tool_use", Tool: "Bash", Summary: `Bash {"command":"curl -H 'Authorization: Bearer abcdefghijklmnop' https://target/login"}`},
		},
		Skills:           []db.SkillStat{{Skill: "web-vuln-tactics", Calls: 3}},
		Yields:           map[int64][]int64{10: {20, 21}},
		FindingsByIntent: map[int64]int{10: 1},
	}
	d.Intents[1].BlockedReason = "WAF 返回统一 403，需要切换编码与请求路径"

	doc := buildCuratorSkillDocument(d)
	for _, want := range []string{
		"---\nname: case-web-login-api-sql-injection-42\n",
		"# 登录接口安全测试 · 任务复盘手册",
		"## 何时使用",
		"## 已验证的有效路径",
		"## 关键事实与证据",
		"## 已确认漏洞与可利用点",
		"## 漏洞复测与修复验证",
		"## 逆向样本与静态分析证据",
		"## 工具与命令模式",
		"## 失败、误区与止损",
		"## 人工提示与决策",
		"## 复用步骤",
		"## 适用边界与验证要求",
		"枚举登录接口参数",
		"WAF 返回统一 403",
		"登录表单 SQL 注入",
		"[已修复] 登录表单 SQL 注入",
		"auth_gateway.elf",
		"3-Strike",
		"web-vuln-tactics",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document missing %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "abcdefghijklmnop") {
		t.Fatalf("bearer token leaked into document:\n%s", doc)
	}
	// The frontmatter description must stay a single YAML-safe line.
	front := strings.SplitN(strings.TrimPrefix(doc, "---\n"), "\n---\n", 2)
	if len(front) != 2 {
		t.Fatalf("frontmatter malformed:\n%s", doc)
	}
	for _, line := range strings.Split(front[0], "\n") {
		if strings.HasPrefix(line, "description:") && (strings.Contains(line, "\n") || strings.Count(line, ":") != 1) {
			t.Fatalf("description is not YAML-safe: %q", line)
		}
	}
}

func TestCuratorPersistStagesThenApplies(t *testing.T) {
	pg, err := db.Open("file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "curator.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}, skillDir: dir}
	if err := pg.SetBool(settingSkillReviewGate, true); err != nil {
		t.Fatal(err)
	}
	if err := pg.SetBool(settingSkillAutoDistillApply, false); err != nil {
		t.Fatal(err)
	}

	docA := buildSkillDocument("case-web-1", "任务复盘 A", "## 已验证路径\n\n- A")
	p, err := s.persistCuratorSkill(context.Background(), "case-web-1", docA, "task=1", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Status != db.SkillProposalPending || p.Origin != "curator" {
		t.Fatalf("expected pending curator proposal, got %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "case-web-1", "SKILL.md")); err == nil {
		t.Fatal("staged proposal must not write the skill file")
	}

	docB := buildSkillDocument("case-web-2", "任务复盘 B", "## 已验证路径\n\n- B")
	p2, err := s.persistCuratorSkill(context.Background(), "case-web-2", docB, "task=2", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p2 == nil || p2.Status != db.SkillProposalApplied {
		t.Fatalf("expected applied proposal, got %+v", p2)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "case-web-2", "SKILL.md"))
	if err != nil || string(raw) != docB {
		t.Fatalf("applied skill file mismatch: %v", err)
	}
}
