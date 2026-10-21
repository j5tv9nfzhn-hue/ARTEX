package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/mcpserver"
)

// 能力注册表的验收：结构与内容都要能钉住。
//
// golden 测试的意义不是「证明现在对」，是「证明以后改错了会被发现」：
// 工具顺序、描述、schema、四个注解位，任何一个动了都必须显式更新 golden。

type toolDigest struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Tier        string         `json:"tier"`
	ReadOnly    bool           `json:"read_only"`
	Destructive bool           `json:"destructive"`
	Idempotent  bool           `json:"idempotent"`
	OpenWorld   bool           `json:"open_world"`
	Schema      map[string]any `json:"schema"`
}

func registryDigest(t *testing.T) []toolDigest {
	t.Helper()
	s := newMCPTestServer(t)
	reg := buildRegistry(s, mcpFileGuard())
	tools := reg.Tools(mcpserver.L3)
	out := make([]toolDigest, 0, len(tools))
	for _, tl := range tools {
		out = append(out, toolDigest{
			Name:        tl.Name,
			Title:       tl.Title,
			Description: tl.Description,
			Tier:        tl.Tier.String(),
			ReadOnly:    tl.ReadOnly,
			Destructive: tl.Destructive,
			Idempotent:  tl.Idempotent || tl.ReadOnly,
			OpenWorld:   tl.OpenWorld,
			Schema:      tl.Schema,
		})
	}
	return out
}

// TestCapabilityGolden：工具面一旦漂移就红。
//
// 改了这个测试覆盖的东西（顺序/措辞/schema/注解），跑
//
//	ARTEX_UPDATE_GOLDEN=1 go test ./server -run TestCapabilityGolden
//
// 重新生成，再在 commit 里说明为什么改。
func TestCapabilityGolden(t *testing.T) {
	got := registryDigest(t)
	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	raw = append(raw, '\n')

	path := filepath.Join("testdata", "mcp_tools.golden.json")
	if os.Getenv("ARTEX_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("写 golden 失败: %v", err)
		}
		t.Logf("已写入 %s（%d 个工具）", path, len(got))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 golden（先跑一次 ARTEX_UPDATE_GOLDEN=1）: %v", err)
	}
	if string(want) != string(raw) {
		t.Fatalf("工具面与 golden 不一致。\n如果这是有意的改动，跑 ARTEX_UPDATE_GOLDEN=1 go test ./server -run TestCapabilityGolden 更新。")
	}
}

// TestCapabilityCounts：档位边界不能变（11 / 21 / 27）。
func TestCapabilityCounts(t *testing.T) {
	s := newMCPTestServer(t)
	reg := buildRegistry(s, mcpFileGuard())
	cases := []struct {
		tier mcpserver.Tier
		want int
	}{
		{mcpserver.L0, 5},
		{mcpserver.L1, 11},
		{mcpserver.L2, 21},
		{mcpserver.L3, 27},
	}
	for _, c := range cases {
		if got := len(reg.Tools(c.tier)); got != c.want {
			t.Fatalf("%s 档位工具数=%d want %d", c.tier, got, c.want)
		}
	}
}

// TestCapabilityDescriptionsFit：Claude Code 默认把描述截到 2048 字符。
// 超过就等于「模型看到的是半句话」，必须在仓库里就拦下来。
func TestCapabilityDescriptionsFit(t *testing.T) {
	const limit = 2048
	for _, d := range registryDigest(t) {
		if n := len([]rune(d.Description)); n > limit {
			t.Fatalf("%s 的描述 %d 字符，超过宿主截断点 %d", d.Name, n, limit)
		}
	}
}

// TestCapabilityDeniedNeveRegistered：禁区里列的能力，不能以任何名字出现在工具面。
func TestCapabilityDeniedNeveRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, d := range registryDigest(t) {
		names[d.Name] = true
	}
	for _, d := range Denied() {
		if d.ID == "" {
			t.Fatal("禁区条目缺 ID")
		}
		if names[strings.TrimPrefix(d.ID, "mcp.")] {
			t.Fatalf("禁区能力 %s 竟然注册成了工具", d.ID)
		}
	}
	if len(Denied()) < 8 {
		t.Fatalf("禁区清单太短，疑似被删: %d", len(Denied()))
	}
}

// TestCapabilityManifestShape：manifest 要能解释「暴露与不暴露」，字段不能缺。
func TestCapabilityManifestShape(t *testing.T) {
	s := newMCPTestServer(t)
	reg := buildRegistry(s, mcpFileGuard())
	m := reg.Manifest()
	if len(m) != 27 {
		t.Fatalf("manifest 应有 27 条，实际 %d", len(m))
	}
	for _, c := range m {
		if c.ID == "" || c.MCPName == "" || c.Tier == "" {
			t.Fatalf("manifest 条目残缺: %+v", c)
		}
		if !c.Exposed {
			t.Fatalf("%s 应当暴露", c.ID)
		}
	}
}

// TestCapResultSpill：超过上限的结果落盘，并明说省略了多少。
func TestCapResultSpill(t *testing.T) {
	big := strings.Repeat("阿", defaultMaxResultChars+5000)
	wrapped := capResult(func(context.Context, json.RawMessage) (string, error) {
		return big, nil
	}, defaultMaxResultChars)

	out, err := wrapped(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "完整结果已落盘") {
		t.Fatalf("应提示落盘: %.200s", out)
	}
	if n := len([]rune(out)); n > defaultMaxResultChars+400 {
		t.Fatalf("包装后仍过长: %d", n)
	}
	idx := strings.Index(out, "完整结果已落盘：")
	if idx < 0 {
		t.Fatal("找不到路径")
	}
	rest := out[idx+len("完整结果已落盘："):]
	path := rest[:strings.Index(rest, "）")]
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("落盘文件不存在 %s: %v", path, err)
	}
	_ = os.Remove(path)
}

// TestCapResultSmallUntouched：不超过上限的结果必须原样返回（一个字都不许动）。
func TestCapResultSmallUntouched(t *testing.T) {
	orig := `{"ok":true,"items":[1,2,3]}`
	wrapped := capResult(func(context.Context, json.RawMessage) (string, error) {
		return orig, nil
	}, 1024)
	out, err := wrapped(context.Background(), json.RawMessage(`{}`))
	if err != nil || out != orig {
		t.Fatalf("小结果被改写: %q (err=%v)", out, err)
	}
}

// TestCapabilityOrderStable：顺序即 UI 顺序，必须是 L0→L1→L2→L3。
func TestCapabilityOrderStable(t *testing.T) {
	ds := registryDigest(t)
	tiers := make([]string, 0, len(ds))
	for _, d := range ds {
		tiers = append(tiers, d.Tier)
	}
	sorted := append([]string(nil), tiers...)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1] > sorted[i] {
			t.Fatalf("工具顺序不是按档位递增: %v", tiers)
		}
	}
	if !reflect.DeepEqual(tiers, sorted) {
		t.Fatalf("工具顺序被扰乱: %v", tiers)
	}
}
