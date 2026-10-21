package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/skill"
)

func TestSkillCanonicalContractAndLegacyAliases(t *testing.T) {
	reg := skill.NewRegistry(skill.Skill{Name: "api-recon", Description: "fixture", Instructions: "fixture guidance"})
	m := &meteredSkill{CoreTool: reg.Tool(), reg: reg}
	for _, raw := range []string{`{"name":"api-recon"}`, `{"skill":"api"}`, `{"skill_name":"API"}`} {
		name, normalized, err := m.resolveInput(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if name != "api-recon" {
			t.Fatal(name)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(normalized, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["skill"]; ok {
			t.Fatal("legacy field leaked to SDK")
		}
		result, err := m.CoreTool.Call(context.Background(), normalized, nil)
		if err != nil || result.IsError || len(result.Extra) != 1 {
			t.Fatalf("SDK Skill contract failed: %v / %s", err, result.Flatten())
		}
	}
	for _, raw := range []string{`null`, `{"name":"api-recon","skill":"camoufox"}`, `{"skill":null}`, `{"name":"api-recon","args":{}}`, `{"name":"api-recon","unknown":1}`} {
		if _, _, err := m.resolveInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid input accepted: %s", raw)
		}
	}
	if _, ok := m.InputSchema()["anyOf"]; !ok {
		t.Fatal("legacy compatibility is not advertised to validation")
	}
}

func TestSkillExactVisibleCustomNamePrecedesAlias(t *testing.T) {
	reg := skill.NewRegistry(skill.Skill{Name: "api", Instructions: "custom"}, skill.Skill{Name: "api-recon", Instructions: "builtin"})
	m := &meteredSkill{CoreTool: reg.Tool(), reg: reg}
	if name, _, err := m.resolveInput(json.RawMessage(`{"name":"api"}`)); err != nil || name != "api" {
		t.Fatalf("custom name shadowed: %q / %v", name, err)
	}
	if _, _, err := m.resolveInput(json.RawMessage(`{"name":"api","skill":"api-recon"}`)); err == nil {
		t.Fatal("distinct visible skills conflated")
	}
}
