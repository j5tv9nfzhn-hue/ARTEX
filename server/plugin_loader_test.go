package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestPluginHelperProcess 是子进程模式的假插件（JSON-RPC over stdio）。
// 通过 GO_PLUGIN_HELPER=1 + -test.run=TestPluginHelperProcess 拉起。
func TestPluginHelperProcess(t *testing.T) {
	if os.Getenv("GO_PLUGIN_HELPER") != "1" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		switch req.Method {
		case "plugin.initialize":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"name": "helper", "version": "0.0.1",
					"tools": []map[string]any{{"name": "echo", "description": "echo"}}}})
		case "plugin.call_tool":
			var p struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			}
			_ = json.Unmarshal(req.Params, &p)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tool": p.Name, "args": p.Args}})
		default:
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "unknown method"}})
		}
	}
}

func writePlugin(t *testing.T, root, dir, manifest string) string {
	t.Helper()
	p := filepath.Join(root, dir)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManifestValidation(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "bad1", `{"id":"bad1","name":"x","version":"1","type":"weird"}`)
	writePlugin(t, root, "bad2", `{"id":"bad2","name":"x","version":"1","type":"external"}`)
	writePlugin(t, root, "ok1", `{"id":"ok1","name":"x","version":"1","type":"internal","tools":[{"name":"a"}]}`)

	reg := NewPluginRegistry()
	reg.SetRoot(root)
	loaded, failed, err := reg.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 1 || failed != 2 {
		t.Fatalf("loaded=%d failed=%d", loaded, failed)
	}
	st, ok := reg.Get("ok1")
	if !ok || st.State != PluginLoaded {
		t.Fatalf("ok1=%+v", st)
	}
}

func TestInternalBuiltinBinding(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "p", `{"id":"bp","name":"x","version":"1","type":"internal","tools":[{"name":"known_tool"}]}`)
	writePlugin(t, root, "q", `{"id":"bq","name":"y","version":"1","type":"internal","tools":[{"name":"unknown_tool"}]}`)

	reg := NewPluginRegistry()
	reg.SetRoot(root)
	reg.SetBuiltinToolNames([]string{"known_tool", "other"})
	loaded, failed, _ := reg.Scan(context.Background())
	if loaded != 1 || failed != 1 {
		t.Fatalf("loaded=%d failed=%d", loaded, failed)
	}
	res, err := reg.CallTool(context.Background(), "bp", "known_tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(res, &m); err != nil || m["bound"] != true {
		t.Fatalf("res=%s err=%v", res, err)
	}
}

func TestExternalProcessLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := t.TempDir()
	exe, err := exec.LookPath(os.Args[0])
	if err != nil {
		t.Skip("cannot locate test binary:", err)
	}
	mf, _ := json.Marshal(map[string]any{
		"id": "helper", "name": "helper", "version": "0.0.1", "type": "external",
		"tools": []map[string]any{{"name": "echo"}},
		"exec":  []string{exe, "-test.run=TestPluginHelperProcess"},
	})
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), mf, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_PLUGIN_HELPER", "1")

	reg := NewPluginRegistry()
	reg.SetRoot(root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	loaded, failed, err := reg.Scan(ctx)
	if err != nil || loaded != 1 || failed != 0 {
		t.Fatalf("loaded=%d failed=%d err=%v", loaded, failed, err)
	}
	st, _ := reg.Get("helper")
	if st.State != PluginLoaded {
		t.Fatalf("state=%s err=%s", st.State, st.Error)
	}

	res, err := reg.CallTool(ctx, "helper", "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(res, &m); err != nil || m["tool"] != "echo" {
		t.Fatalf("res=%s err=%v", res, err)
	}

	if err := reg.Disable("helper"); err != nil {
		t.Fatal(err)
	}
	if st, _ := reg.Get("helper"); st.State != PluginDisabled {
		t.Fatalf("state=%s", st.State)
	}
	if _, err := reg.CallTool(ctx, "helper", "echo", nil); err == nil {
		t.Fatal("disabled plugin must refuse call")
	}
	if err := reg.Enable(ctx, "helper"); err != nil {
		t.Fatal(err)
	}
	if st, _ := reg.Get("helper"); st.State != PluginLoaded {
		t.Fatalf("state=%s err=%s", st.State, st.Error)
	}
	if err := reg.Reload(ctx, "helper"); err != nil {
		t.Fatal(err)
	}
	if st, _ := reg.Get("helper"); st.State != PluginLoaded {
		t.Fatalf("after reload state=%s err=%s", st.State, st.Error)
	}
}

func TestNoPluginsDirIsEmptyNotError(t *testing.T) {
	reg := NewPluginRegistry()
	reg.SetRoot(filepath.Join(t.TempDir(), "nope"))
	loaded, failed, err := reg.Scan(context.Background())
	if err != nil || loaded != 0 || failed != 0 {
		t.Fatalf("loaded=%d failed=%d err=%v", loaded, failed, err)
	}
	if len(reg.List()) != 0 {
		t.Fatal("registry must stay empty")
	}
}
