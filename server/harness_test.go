package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessRegistryLoads(t *testing.T) {
	reg, err := HarnessRegistryData()
	if err != nil {
		t.Fatalf("注册表加载失败: %v", err)
	}
	if len(reg.Clients) < 25 {
		t.Fatalf("客户端太少: %d", len(reg.Clients))
	}
	for _, id := range []string{"claude-code", "codex", "dsh", "cursor", "vscode-copilot", "xcode"} {
		if _, ok := reg.Client(id); !ok {
			t.Errorf("缺少客户端 %s", id)
		}
	}
	// A 级必须能自动写，C 级必须不能——这是分级的全部意义。
	// 注意 C 级的 artex 可以是 stdio：意思是「推荐用 stdio 片段」，不是「我们要写文件」。
	for _, c := range reg.Clients {
		if c.Tier == "A" && c.Artex == "manual" {
			t.Errorf("%s 是 A 级却只能手工", c.ID)
		}
		if c.Tier == "C" {
			for _, cfg := range c.Config {
				if cfg.Strategy != "manual" && cfg.Strategy != "" {
					t.Errorf("%s 是 C 级却声明了可写策略 %q", c.ID, cfg.Strategy)
				}
			}
		}
	}
}

func TestHarnessExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("TEST_HARNESS_HOME", filepath.Join(home, "custom"))

	cases := map[string]string{
		"~/.cursor/mcp.json":            filepath.Join(home, ".cursor", "mcp.json"),
		"$TEST_HARNESS_HOME/mcp.json":   filepath.Join(home, "custom", "mcp.json"),
		"${TEST_HARNESS_HOME}/a.json":   filepath.Join(home, "custom", "a.json"),
		"$DSH_HOME/cordis.patch.yml":    filepath.Join(home, ".dsh", "cordis.patch.yml"),
		"$DSH_HOME/profiles/<profile>/x": filepath.Join(home, ".dsh", "profiles", "web", "x"),
	}
	for raw, want := range cases {
		got, ok := harnessExpandPath(raw)
		if !ok {
			t.Errorf("%q 应可解析", raw)
			continue
		}
		if filepath.Clean(got) != filepath.Clean(want) {
			t.Errorf("%q → %q，期望 %q", raw, got, want)
		}
	}
	// 未定义变量必须拒绝，绝不能拿字面量当路径去写。
	if _, ok := harnessExpandPath("$NOT_DEFINED_XYZ/mcp.json"); ok {
		t.Error("未定义变量应返回 ok=false")
	}
}

func TestHarnessUpsertJSONPreservesComments(t *testing.T) {
	before := "{\n  // 保留这行注释\n  \"mcpServers\": {\n    \"other\": { \"url\": \"http://x\" }\n  },\n  \"theme\": \"dark\"\n}\n"
	after, err := harnessUpsertJSON(before, "mcpServers", "artex", `{"url":"http://127.0.0.1:8787/mcp"}`)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if !strings.Contains(after, "// 保留这行注释") {
		t.Error("注释被吃掉了")
	}
	if !strings.Contains(after, `"theme": "dark"`) {
		t.Error("无关键被破坏")
	}
	if !strings.Contains(after, `"artex": {"url":"http://127.0.0.1:8787/mcp"}`) {
		t.Errorf("没有写进 artex: %s", after)
	}
	// 再写一次必须幂等。
	again, err := harnessUpsertJSON(after, "mcpServers", "artex", `{"url":"http://127.0.0.1:8787/mcp"}`)
	if err != nil || again != after {
		t.Errorf("不幂等: err=%v", err)
	}
}

func TestHarnessUpsertJSONCreatesRootKey(t *testing.T) {
	after, err := harnessUpsertJSON("{\n  \"theme\": \"dark\"\n}\n", "servers", "artex", `{"type":"http"}`)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(after), &root); err != nil {
		t.Fatalf("结果不是合法 JSON: %v\n%s", err, after)
	}
	if _, ok := root["servers"]; !ok {
		t.Fatalf("没有建出 servers: %s", after)
	}
	if after, err := harnessUpsertJSON("", "mcpServers", "artex", `{"url":"u"}`); err != nil {
		t.Errorf("空文件失败: %v", err)
	} else if !strings.Contains(after, `"artex"`) {
		t.Errorf("空文件没写出内容: %s", after)
	}
}

func TestHarnessUpsertTOML(t *testing.T) {
	before := "model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"x\"\n"
	after, err := harnessUpsertTOML(before, "mcp_servers", "artex", "command = \"artex\"\n")
	if err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	if strings.Count(after, "[mcp_servers.artex]") != 1 {
		t.Fatalf("表头数量不对:\n%s", after)
	}
	if !strings.Contains(after, "[mcp_servers.other]") {
		t.Error("别的表被吃掉")
	}
	// 替换：再写一次不应产生第二张表。
	after2, err := harnessUpsertTOML(after, "mcp_servers", "artex", "command = \"artex2\"\n")
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if strings.Count(after2, "[mcp_servers.artex]") != 1 {
		t.Errorf("替换后出现重复表:\n%s", after2)
	}
	if !strings.Contains(after2, "artex2") {
		t.Error("替换没生效")
	}
}

func TestHarnessUpsertYAML(t *testing.T) {
	before := "GOOSE_PROVIDER: openai\nextensions:\n  developer:\n    enabled: true\n\nGOOSE_MODE: auto\n"
	kvs := []harnessKV{{"url", "http://127.0.0.1:8787/mcp"}, {"headers", []harnessKV{{"Authorization", "Bearer t"}}}}
	after, err := harnessUpsertYAML(before, "extensions", "artex", kvs)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// artex 必须落在 extensions 下面，而不是跑到 GOOSE_MODE 之后。
	idxExt := strings.Index(after, "extensions:")
	idxArtex := strings.Index(after, "  artex:")
	idxMode := strings.Index(after, "GOOSE_MODE:")
	if !(idxExt >= 0 && idxArtex > idxExt && idxMode > idxArtex) {
		t.Fatalf("插入位置不对:\n%s", after)
	}
	if !strings.Contains(after, "    Authorization: Bearer t") {
		t.Errorf("headers 缩进不对:\n%s", after)
	}
}

func TestHarnessUpsertCordis(t *testing.T) {
	in := HarnessPlanInput{BaseURL: "http://127.0.0.1:8787", Token: "artex_mcp_x"}
	out, err := harnessUpsertCordis("", in)
	if err != nil {
		t.Fatalf("空文件失败: %v", err)
	}
	for _, want := range []string{"- insert:", "id: mcp-artex", "@deepseek-ai/dsh-mcp-client", "serverName: artex", "streamable-http"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q:\n%s", want, out)
		}
	}
	// 已有同 id：必须拒绝，而不是追加第二条（重复 id 会让 profile 组合失败）。
	if _, err := harnessUpsertCordis(out, in); err == nil {
		t.Error("重复 id 应当报错")
	}
	// 根不是序列：拒绝。
	if _, err := harnessUpsertCordis("foo: bar\n", in); err == nil {
		t.Error("非序列文件应当报错")
	}
}

func TestHarnessPlanApplyRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	in := HarnessPlanInput{
		Targets: []string{"cursor"},
		BaseURL: "http://127.0.0.1:8787",
		Token:   "artex_mcp_testtoken",
		Exe:     `C:\ARTEX程序\artex-rea.exe`,
	}
	changes := harnessPlanAll(in)
	if len(changes) == 0 {
		t.Fatal("plan 没有产出")
	}
	var target *HarnessChange
	for i := range changes {
		if changes[i].Scope == "user" {
			target = &changes[i]
		}
	}
	if target == nil {
		t.Fatalf("没有 user 级改动: %+v", changes)
	}
	if target.Action != "create" {
		t.Fatalf("期望 create，得到 %s（%s）", target.Action, target.Reason)
	}
	if target.Diff == "" {
		t.Error("没有 diff")
	}

	applied := harnessApplyAll(changes, "", true)
	if !applied[0].Applied && applied[0].Error == "" {
		// 第一条可能是 project 级（被拒为 manual），找 user 级那条。
		for _, ch := range applied {
			if ch.Scope == "user" && !ch.Applied {
				t.Fatalf("user 级没写成功: %s", ch.Error)
			}
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatalf("文件没写出来: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("写出来的不是合法 JSON: %v\n%s", err, data)
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if _, ok := servers["artex"]; !ok {
		t.Fatalf("没有 artex 条目: %s", data)
	}

	// 第二次 plan 必须是 noop。
	again := harnessPlanAll(in)
	for _, ch := range again {
		if ch.Scope == "user" && ch.Action != "noop" {
			t.Errorf("重复 plan 应为 noop，得到 %s", ch.Action)
		}
	}
}

func TestHarnessPathGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	if harnessPathAllowed(filepath.Join(home, ".cursor", "mcp.json"), "") != true {
		t.Error("用户目录内应放行")
	}
	outside := filepath.Join(string(filepath.Separator), "Windows", "System32", "x.json")
	if harnessPathAllowed(outside, "") {
		t.Error("系统目录必须拒绝")
	}
	proj := t.TempDir()
	if !harnessPathAllowed(filepath.Join(proj, ".vscode", "mcp.json"), proj) {
		t.Error("指定项目目录内应放行")
	}
}

func TestHarnessPlanUnknownTarget(t *testing.T) {
	changes := harnessPlanAll(HarnessPlanInput{Targets: []string{"nope"}, BaseURL: "http://x"})
	if len(changes) != 1 || changes[0].Action != "skip" {
		t.Fatalf("未知目标应 skip: %+v", changes)
	}
}
