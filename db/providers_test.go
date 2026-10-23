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
		_ = d.DeleteProvider(pid)
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

	// 4) 还有模型时不允许删供应商——删模型会级联到任务链与 Agent 绑定。
	if err := d.DeleteProvider(pid); !errors.Is(err, ErrProviderNotEmpty) {
		t.Fatalf("DeleteProvider = %v, want ErrProviderNotEmpty", err)
	}

	// 5) 把模型挪到另一家供应商（建错位置时的补救）。
	pid2, err := d.SaveProvider(&LLMProvider{Name: "zz-provider-b", Format: "anthropic", APIKey: "sk-provider-b-2222"})
	if err != nil {
		t.Fatalf("save provider b: %v", err)
	}
	defer func() { _ = d.DeleteProvider(pid2) }()
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
	_ = d.DeleteProvider(p.ProviderID)
}
