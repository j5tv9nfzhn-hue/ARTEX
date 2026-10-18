package server

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestREEnginePresetsValidateAndRequireExplicitReview(t *testing.T) {
	presets := reEnginePresets()
	if len(presets) != 3 {
		t.Fatalf("expected 3 engine presets (ghidra, ida, rea), got %d", len(presets))
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		seen[preset.Kind] = true
		if preset.Config.ReviewedStatic {
			t.Fatalf("preset %s must not default to reviewed_static=true", preset.Kind)
		}
		p := db.REProvider{
			MCPID:  1,
			Name:   preset.Title,
			Kind:   preset.Kind,
			Config: preset.Config,
		}
		if err := reValidateConfig(p); err != nil {
			t.Fatalf("preset %s failed reValidateConfig: %v", preset.Kind, err)
		}
		for _, op := range []string{"functions", "decompile", "xrefs", "callgraph", "types", "modules", "imports", "exports", "strings"} {
			if _, ok := preset.Config.Operations[op]; !ok {
				t.Fatalf("preset %s missing standard static operation %q", preset.Kind, op)
			}
		}
	}
	for _, kind := range []string{"ghidra", "ida", "rea"} {
		if !seen[kind] {
			t.Fatalf("missing engine preset for %s", kind)
		}
	}
}

func TestRECapabilityGateEvaluatesSchemaAndLifecycle(t *testing.T) {
	presets := reEnginePresets()
	var ghidra db.REProviderConfig
	for _, p := range presets {
		if p.Kind == "ghidra" {
			ghidra = p.Config
			break
		}
	}

	catalog := make([]reCatalogTool, 0, len(ghidra.Operations))
	for _, op := range ghidra.Operations {
		req := make([]any, 0, len(op.Input))
		for k := range op.Input {
			req = append(req, k)
		}
		catalog = append(catalog, reCatalogTool{
			Name:        op.Tool,
			Description: "fixture tool",
			Schema:      map[string]any{"type": "object", "required": req},
		})
	}
	rawCat, _ := json.Marshal(catalog)

	// 1) Unreviewed provider is blocked even when catalog matches.
	prov := db.REProvider{
		ID:        10,
		MCPID:     1,
		Name:      "Ghidra Local",
		Kind:      "ghidra",
		Config:    ghidra,
		Catalog:   rawCat,
		Discovery: "catalog_verified",
	}
	rep := reEvaluateCapabilities(prov)
	if rep.CoreReady {
		t.Fatal("unreviewed provider must not be core_ready")
	}

	// 2) Reviewed + verified + matching required schema -> CoreReady.
	prov.Config.ReviewedStatic = true
	rep = reEvaluateCapabilities(prov)
	if !rep.CoreReady || len(rep.MissingTools) != 0 || len(rep.SchemaMismatches) != 0 {
		t.Fatalf("expected core_ready=true, got %+v", rep)
	}

	// 3) Missing required schema parameter blocks CoreReady.
	badCatalog := []reCatalogTool{
		{
			Name:   "ghidra_open_program",
			Schema: map[string]any{"type": "object", "required": []any{"file_path", "extra_required_param"}},
		},
	}
	badRaw, _ := json.Marshal(badCatalog)
	prov.Catalog = badRaw
	rep = reEvaluateCapabilities(prov)
	if rep.CoreReady || len(rep.SchemaMismatches) == 0 {
		t.Fatalf("expected schema mismatch to block readiness, got %+v", rep)
	}
}

func TestRENormalizeMultiModuleAndSymbolEntities(t *testing.T) {
	sampleSHA := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	modOp := db.REOperation{
		Tool:        "rea_list_modules",
		RowsPath:    "items",
		AddressKind: "rva",
		Columns:     map[string]string{"name": "name", "address": "base", "module": "name"},
	}
	mods := reNormalize("modules", modOp, `{"items":[{"name":"core.dll","base":"0x1000"}]}`, sampleSHA, 10)
	if len(mods) != 1 || mods[0].Kind != "modules" || mods[0].Name != "core.dll" || mods[0].Address != "0x1000" {
		t.Fatalf("unexpected normalized modules: %+v", mods)
	}

	impOp := db.REOperation{
		Tool:        "rea_list_imports",
		RowsPath:    "items",
		AddressKind: "rva",
		Columns:     map[string]string{"name": "symbol", "address": "rva", "module": "library"},
	}
	imps := reNormalize("imports", impOp, `{"items":[{"symbol":"CreateFileW","rva":"0x2040","library":"KERNEL32.dll"}]}`, sampleSHA, 10)
	if len(imps) != 1 || imps[0].Kind != "imports" || imps[0].Module != "KERNEL32.dll" || imps[0].Name != "CreateFileW" {
		t.Fatalf("unexpected normalized imports: %+v", imps)
	}

	strOp := db.REOperation{
		Tool:        "rea_list_strings",
		RowsPath:    "items",
		AddressKind: "file_offset",
		Columns:     map[string]string{"name": "value", "address": "offset", "module": "section"},
	}
	strs := reNormalize("strings", strOp, `{"items":[{"value":"https://api.internal","offset":"0x4a0","section":".rdata"}]}`, sampleSHA, 10)
	if len(strs) != 1 || strs[0].Kind != "strings" || strs[0].Address != "0x4a0" || strs[0].Name != "https://api.internal" {
		t.Fatalf("unexpected normalized strings: %+v", strs)
	}
}
