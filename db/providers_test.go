package db

import (
	"errors"
	"testing"
)

// 供应商是模型的上游端点：模型从它继承 format / base_url / 代理 / Key / 限速 /
// 会话头，自己只保留模型参数。这里钉住四条语义，它们任何一条坏了都会让「同一家
// 供应商开三个模型」退回到重复填三遍。
func TestProviderGroupsModels(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pid, err := d.SaveProvider(&LLMProvider{
		Name:          "zz-provider-a",
		Format:        "openai",
		BaseURL:       "https://a.example.com/v1",
		Proxy:         "http://127.0.0.1:8080",
		APIKey:        "sk-provider-a-1111",
		RatePerMinute: 60,
	})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM llm_profiles WHERE provider_id=$1`, pid)
		_, _ = d.DeleteProvider(pid)
	}()

	id1, err := d.SaveProfile(&LLMProfile{Name: "zz-prov-model-1", Model: "m-one", ProviderID: pid, ContextWindowK: 128})
	if err != nil {
		t.Fatalf("save model 1: %v", err)
	}
	id2, err := d.SaveProfile(&LLMProfile{Name: "zz-prov-model-2", Model: "m-two", ProviderID: pid, MaxTokens: 4096})
	if err != nil {
		t.Fatalf("save model 2: %v", err)
	}

	// 1) 模型继承端点与鉴权，而不是自己存一份。
	p, err := d.ProfileByID(id1)
	if err != nil || p == nil {
		t.Fatalf("profile 1: %v", err)
	}
	if p.ProviderID != pid || p.ProviderName != "zz-provider-a" {
		t.Fatalf("归属 = %d/%q, want %d/zz-provider-a", p.ProviderID, p.ProviderName, pid)
	}
	if p.Format != "openai" || p.BaseURL != "https://a.example.com/v1" || p.Proxy != "http://127.0.0.1:8080" {
		t.Fatalf("端点未继承: %+v", p)
	}
	if p.APIKey != "sk-provider-a-1111" {
		t.Fatalf("key = %q, want provider key", p.APIKey)
	}
	if p.RatePerMinute != 60 {
		t.Fatalf("rate = %v, want 60 (端点级限速)", p.RatePerMinute)
	}
	// 模型自己的参数不被供应商覆盖。
	if p.ContextWindowK != 128 {
		t.Fatalf("context_window_k = %d, want 128", p.ContextWindowK)
	}

	// 2) 列表按供应商分组，一次带回全部模型。
	providers, err := d.ListProviders()
	if err != nil {
		t.Fatal(err)
	}
	var got *LLMProvider
	for _, v := range providers {
		if v.ID == pid {
			got = v
		}
	}
	if got == nil {
		t.Fatal("provider 未出现在 ListProviders 中")
	}
	if got.ModelCount != 2 || len(got.Models) != 2 {
		t.Fatalf("models = %d/%d, want 2/2", got.ModelCount, len(got.Models))
	}

	// 3) 改供应商端点 → 名下模型同步（同一份事实来源）。
	got.BaseURL = "https://b.example.com/v1"
	if _, err := d.SaveProvider(got); err != nil {
		t.Fatal(err)
	}
	p2, _ := d.ProfileByID(id2)
	if p2.BaseURL != "https://b.example.com/v1" {
		t.Fatalf("改名后模型 base_url = %q, want 同步", p2.BaseURL)
	}

	// 4) 把模型挪到另一家供应商（建错位置时的补救）。
	//    「删供应商要不要先清空模型」不在这里测：级联语义见
	//    TestDeleteProviderCascadesModels。
	pid2, err := d.SaveProvider(&LLMProvider{Name: "zz-provider-b", Format: "anthropic", APIKey: "sk-provider-b-2222"})
	if err != nil {
		t.Fatalf("save provider b: %v", err)
	}
	defer func() { _, _ = d.DeleteProvider(pid2) }()
	if err := d.MoveProfileToProvider(id1, pid2); err != nil {
		t.Fatalf("move: %v", err)
	}
	moved, _ := d.ProfileByID(id1)
	if moved.ProviderID != pid2 || moved.Format != "anthropic" || moved.APIKey != "sk-provider-b-2222" {
		t.Fatalf("移动后 = %+v, want 归属 provider-b", moved)
	}

	_ = d.DeleteProfile(id1)
	_ = d.DeleteProfile(id2)
}

// 旧客户端（以及手机端）仍然只发端点字段，不带 provider_id。这条路径必须继续
// 工作，并且为它自动建出一条供应商——否则升级那一刻旧界面就存不了配置了。
func TestSaveProfileWithoutProviderStillWorks(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	id, err := d.SaveProfile(&LLMProfile{
		Name:    "zz-legacy-model",
		Format:  "openai",
		BaseURL: "https://legacy.example.com/v1",
		Model:   "legacy-1",
		APIKey:  "sk-legacy-3333",
	})
	if err != nil {
		t.Fatalf("legacy save: %v", err)
	}
	p, err := d.ProfileByID(id)
	if err != nil || p == nil {
		t.Fatalf("read back: %v", err)
	}
	if p.ProviderID == 0 {
		t.Fatal("旧式保存没有建出供应商")
	}
	if p.BaseURL != "https://legacy.example.com/v1" || p.APIKey != "sk-legacy-3333" {
		t.Fatalf("端点丢失: %+v", p)
	}
	// 再存一次（改成新地址）应当落在同一家供应商上，而不是又建一条。
	if _, err := d.SaveProfile(&LLMProfile{
		ID: id, Name: "zz-legacy-model", Format: "openai",
		BaseURL: "https://legacy2.example.com/v1", Model: "legacy-1",
	}); err != nil {
		t.Fatalf("legacy re-save: %v", err)
	}
	again, _ := d.ProfileByID(id)
	if again.ProviderID != p.ProviderID {
		t.Fatalf("重复保存换了供应商: %d → %d", p.ProviderID, again.ProviderID)
	}
	if again.BaseURL != "https://legacy2.example.com/v1" {
		t.Fatalf("端点未更新: %q", again.BaseURL)
	}

	_ = d.DeleteProfile(id)
	_, _ = d.DeleteProvider(p.ProviderID)
}

// DeleteProvider 必须一次带走名下全部模型：删供应商却留下孤儿模型，是重构前
// 「先删光模型才能删供应商」那条限制的反面——两种极端都不可接受。
func TestDeleteProviderCascadesModels(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pid, err := d.SaveProvider(&LLMProvider{Name: "zz-cascade-prov", Format: "openai", APIKey: "sk-cascade-0001"})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	ids := make([]int64, 0, 3)
	for _, name := range []string{"zz-cascade-1", "zz-cascade-2", "zz-cascade-3"} {
		id, err := d.SaveProfile(&LLMProfile{Name: name, Model: "m-" + name, ProviderID: pid})
		if err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
		ids = append(ids, id)
	}
	// 把其中一条设成激活：删供应商时它会被一起删掉，激活位必须自动交给别人。
	if err := d.SetActiveProfile(pid); err != nil {
		t.Fatalf("activate provider: %v", err)
	}

	n, err := d.DeleteProvider(pid)
	if err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if n != 3 {
		t.Fatalf("级联删除 %d 个模型，期望 3 个", n)
	}
	for _, id := range ids {
		if p, _ := d.ProfileByID(id); p != nil {
			t.Fatalf("模型 %d 未被删除", id)
		}
	}
	if v, _ := d.ProviderByID(pid); v != nil {
		t.Fatal("供应商本身未被删除")
	}
	var left int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_profiles WHERE id IN ($1,$2,$3)`, ids[0], ids[1], ids[2]).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("残留 %d 条模型行", left)
	}
	if _, err := d.DeleteProvider(pid); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("重复删除 = %v, want ErrProviderNotFound", err)
	}
}

// 激活中的模型也必须能删——用户在一个供应商下只剩一条模型时，那一条往往就是
// 激活的那条，旧逻辑会把人卡死。删完激活位自动交给下一个候选；一条不剩就留空。
func TestDeleteActiveProfileHandsOverDefault(t *testing.T) {
	// 用一次性库：这条用例要断言「激活位交给了谁」，共享套件库里别的用例
	// 留下的配置会让排序结果不确定。
	d, err := openSQLiteTemp(t.TempDir(), "handover.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pid, err := d.SaveProvider(&LLMProvider{Name: "zz-handover-prov", Format: "anthropic", APIKey: "sk-handover-0002"})
	if err != nil {
		t.Fatalf("save provider: %v", err)
	}
	defer func() { _, _ = d.DeleteProvider(pid) }()

	keep, err := d.SaveProfile(&LLMProfile{Name: "zz-handover-keep", Model: "keep", ProviderID: pid, Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	active, err := d.SaveProfile(&LLMProfile{Name: "zz-handover-active", Model: "active", ProviderID: pid, Priority: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.DeleteProfile(keep) }()
	// 把激活位指到「想删的那条」。
	if err := d.SetActiveProfile(active); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteProfile(active); err != nil {
		t.Fatalf("删除激活模型被拒：%v", err)
	}
	got, err := d.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != keep {
		t.Fatalf("激活位没有交接给剩下的模型：%+v", got)
	}

	// 最后一条也能删：删完没有任何激活配置，这是合法状态。
	if err := d.DeleteProfile(keep); err != nil {
		t.Fatalf("删除最后一条模型被拒：%v", err)
	}
	if got, _ := d.ActiveProfile(); got != nil {
		t.Fatalf("删空后仍有激活配置：%+v", got)
	}
}
