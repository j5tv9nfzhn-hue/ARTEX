package server

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 现场问题：供应商建好、模型也建好，「测试连接」却报「未提供 API Key」。
//
// 原因是 Key 解析只认 profile id：供应商刚建好、名下还没有模型时，前端没有
// 任何 profile 可指，就落到「全局配置」那一档——全局为空直接报没 Key，全局
// 有值则拿**另一家供应商**的 Key 去测，得到的 401 指向完全错误的对象。
//
// 这些用例钉住三件事：
//  1. 只给 provider_id 就能拿到供应商行上的 Key（不再需要「名下第一条模型」）；
//  2. 给了 profile_id 时仍然按 COALESCE(供应商, 模型) 读到同一把 Key；
//  3. 供应商真的没填 Key 时，错误信息说的是「这家供应商没有填写」，而不是
//     含糊的「未提供 API Key」。
func TestResolveLLMKeyByProvider(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)

	// 供应商里的 Key 是唯一事实来源；模型行只在镜像列里留一份密文副本。
	const want = "sk-provider-abcdef123456"
	pid, err := m.pg.SaveProvider(&db.LLMProvider{
		Name:    "acme",
		Format:  "openai",
		BaseURL: "https://api.acme.test/v1",
		APIKey:  want,
	})
	if err != nil {
		t.Fatalf("建供应商失败：%v", err)
	}

	// ① 一个模型都没有：只有 provider_id 这一条路。
	src, err := s.resolveLLMKey("", nil, &pid)
	if err != nil {
		t.Fatalf("只给 provider_id 应当能解出 Key，实际报错：%v", err)
	}
	if src.Key != want {
		t.Fatalf("Key=%q，期望 %q", src.Key, want)
	}
	if !strings.Contains(src.Source, "供应商") {
		t.Fatalf("来源应说明是供应商，实际 %q", src.Source)
	}

	// ② 在其下建模型（不再单独填 Key），profile 分支读到的必须还是同一把。
	mid, err := m.pg.SaveProfile(&db.LLMProfile{Name: "m1", Model: "gpt-x", ProviderID: pid})
	if err != nil {
		t.Fatalf("建模型失败：%v", err)
	}
	p, err := m.pg.ProfileByID(mid)
	if err != nil {
		t.Fatalf("读模型失败：%v", err)
	}
	if p.APIKey != want {
		t.Fatalf("模型读出的 Key=%q，期望回落到供应商的 %q（COALESCE 失效）", p.APIKey, want)
	}
	src, err = s.resolveLLMKey("", &mid, nil)
	if err != nil || src.Key != want {
		t.Fatalf("按 profile 解析失败：key=%q err=%v", src.Key, err)
	}

	// ③ 表单里刚粘贴的 Key 优先级最高（「测我刚填的这把」是明确意图）。
	src, err = s.resolveLLMKey("sk-typed-in-form-999999", &mid, &pid)
	if err != nil || src.Key != "sk-typed-in-form-999999" {
		t.Fatalf("表单 Key 应当胜出：key=%q err=%v", src.Key, err)
	}
	if src.Source != "form" {
		t.Fatalf("来源应为 form，实际 %q", src.Source)
	}

	// ④ 供应商确实没填 Key：错误必须点名这家供应商，而不是「未提供 API Key」。
	emptyID, err := m.pg.SaveProvider(&db.LLMProvider{
		Name:    "no-key",
		Format:  "anthropic",
		BaseURL: "https://api.nokey.test",
	})
	if err != nil {
		t.Fatalf("建空 Key 供应商失败：%v", err)
	}
	_, err = s.resolveLLMKey("", nil, &emptyID)
	if err == nil {
		t.Fatal("空 Key 的供应商不该解析出 Key")
	}
	if !strings.Contains(err.Error(), "没有填写 API Key") {
		t.Fatalf("错误应说明这家供应商没填 Key，实际 %q", err.Error())
	}

	// ⑤ 供应商被删掉之后，不能静默降级去用全局 Key。
	gone := int64(999999)
	if _, err := s.resolveLLMKey("", nil, &gone); err == nil {
		t.Fatal("不存在的供应商应当报错")
	} else if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("错误应说明供应商不存在，实际 %q", err.Error())
	}
}
