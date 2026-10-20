package agent

import "testing"

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"":        ModeGeneral,
		"general": ModeGeneral,
		"  DEV ":  ModeDev,
		"office":  ModeOffice,
		"pentest": ModePentest,
		"unknown": ModeGeneral,
		"PENTEST": ModePentest,
	}
	for in, want := range cases {
		if got := NormalizeMode(in); got != want {
			t.Fatalf("NormalizeMode(%q)=%q want %q", in, got, want)
		}
	}
}

// TestPentestPolicyInherits：渗透/逆向必须完全沿用现状——目录走既有设置、
// 危险命令与 git 写操作都不额外加闸。这条断言就是「升级不损坏渗透链路」的代码化约束。
func TestPentestPolicyInherits(t *testing.T) {
	p, ok := ModePolicyFor(ModePentest)
	if !ok {
		t.Fatal("pentest 策略缺失")
	}
	if p.WorkspacePick != WorkspacePickDataDir {
		t.Fatalf("渗透应沿用既有目录设置，实际 %q", p.WorkspacePick)
	}
	if p.DangerCmd != GateInherit || p.GitWrite != GateInherit {
		t.Fatalf("渗透不应新增闸门：danger=%q git=%q", p.DangerCmd, p.GitWrite)
	}
	if len(p.ExtraTools) != 0 {
		t.Fatalf("渗透不额外挂 SDK 工具，实际 %v", p.ExtraTools)
	}
	if !UsesDataDir(ModePentest) {
		t.Fatal("UsesDataDir(pentest) 应为 true")
	}
}

// TestWorkModesUsePickerAndApproval：正常使用（通用/开发/办公）走成熟 harness 惯例。
func TestWorkModesUsePickerAndApproval(t *testing.T) {
	for _, mode := range []string{ModeGeneral, ModeDev, ModeOffice} {
		p, ok := ModePolicyFor(mode)
		if !ok {
			t.Fatalf("%s 策略缺失", mode)
		}
		if p.WorkspacePick != WorkspacePickPicker {
			t.Fatalf("%s 应由用户选目录，实际 %q", mode, p.WorkspacePick)
		}
		if p.DangerCmd != GateApprove || p.GitWrite != GateApprove {
			t.Fatalf("%s 危险命令/git 写应走审批：danger=%q git=%q", mode, p.DangerCmd, p.GitWrite)
		}
		if UsesDataDir(mode) {
			t.Fatalf("%s 不应沿用数据目录", mode)
		}
	}
}

func TestModePoliciesComplete(t *testing.T) {
	list := ModePolicies()
	if len(list) != 4 {
		t.Fatalf("期望 4 个域，实际 %d", len(list))
	}
	seen := map[string]bool{}
	for _, p := range list {
		if p.Mode == "" || p.Label == "" {
			t.Fatalf("域缺字段: %+v", p)
		}
		if seen[p.Mode] {
			t.Fatalf("域重复: %s", p.Mode)
		}
		seen[p.Mode] = true
	}
}
