package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

type doFunc func(method, path string, body any) (int, map[string]any)

// newGitBashFixture spins the real mux over PG. Callers that need a database get
// a skip, matching the rest of this package's PG-dependent tests.
func newGitBashFixture(t *testing.T) doFunc {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable — skipping")
	}
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	h := s.Handler()
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return func(method, path string, body any) (int, map[string]any) {
		var r *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
}

// gitBashStatus 从 GET /api/settings 里取回显的 shell 状态。
func gitBashStatus(t *testing.T, do doFunc) map[string]any {
	t.Helper()
	code, out := do(http.MethodGet, "/api/settings", nil)
	if code != 200 {
		t.Fatalf("GET /api/settings=%d", code)
	}
	st, ok := out["git_bash"].(map[string]any)
	if !ok {
		t.Fatalf("settings 缺少 git_bash 字段: %T", out["git_bash"])
	}
	return st
}

// TestGitBashSettingsPersistence 覆盖设置面板的核心链路：保存 → 回显 → 生效，
// 以及三类非法输入必须报错而不是"保存成功但静默不生效"。
func TestGitBashSettingsPersistence(t *testing.T) {
	do := newGitBashFixture(t)
	t.Cleanup(func() { actool.SetShellPath("") })

	// 结构存在且字段齐备（前端按这些键渲染）。
	status := gitBashStatus(t, do)
	for _, k := range []string{"applicable", "platform", "configured", "path", "family", "source"} {
		if _, has := status[k]; !has {
			t.Errorf("git_bash 缺字段 %q", k)
		}
	}
	if status["platform"] != runtime.GOOS {
		t.Errorf("platform=%v, want %v", status["platform"], runtime.GOOS)
	}
	if status["applicable"] != (runtime.GOOS == "windows") {
		t.Errorf("applicable=%v, 应与平台一致", status["applicable"])
	}
	// 解析结果必须有值：即使什么都没配，也应回显当前真正在用的 shell 与来源，
	// 否则操作者无从判断"配置到底生效了没有"。
	if p, _ := status["path"].(string); p == "" {
		t.Error("git_bash.path 为空：执行层必须报告当前生效的 shell")
	}
	if src, _ := status["source"].(string); src == "" {
		t.Error("git_bash.source 为空：无法判断配置是否真的生效")
	}

	// 相对路径 / 不存在 / 目录：都必须是 400，不能静默接受。
	for _, bad := range []struct {
		name  string
		value string
	}{
		{"relative", `Git\bin\bash.exe`},
		{"missing", filepath.Join(t.TempDir(), "nope", "bash.exe")},
		{"directory", t.TempDir()},
	} {
		code, out := do(http.MethodPut, "/api/settings", map[string]any{"git_bash_path": bad.value})
		if code != http.StatusBadRequest {
			t.Errorf("%s 路径应 400，得到 %d (%v)", bad.name, code, out["error"])
		}
	}

	// 非法值不得污染已存配置。
	if got := gitBashStatus(t, do)["configured"]; got != "" {
		t.Fatalf("被拒绝的写入不应落库, configured=%v", got)
	}

	// 合法绝对路径：保存 → GET 回显 → 执行层拿到同一个值（即时生效，无需重启）。
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this host")
	}
	bashPath, _ = filepath.Abs(bashPath)

	code, out := do(http.MethodPut, "/api/settings", map[string]any{"git_bash_path": bashPath})
	if code != 200 {
		t.Fatalf("保存合法路径失败: %d (%v)", code, out["error"])
	}
	if got := gitBashStatus(t, do)["configured"]; got != bashPath {
		t.Fatalf("configured=%v, want %v", got, bashPath)
	}
	if got := waitShellPath(bashPath); got != bashPath {
		t.Fatalf("执行层未收到保存的路径: got=%q want=%q（保存后必须即时推送到 actool）", got, bashPath)
	}

	// 资源管理器"复制文件地址"带来的引号要被清洗后仍然保存成功。
	quoted := `"` + bashPath + `"`
	code, out = do(http.MethodPut, "/api/settings", map[string]any{"git_bash_path": quoted})
	if code != 200 {
		t.Fatalf("带引号路径应被清洗并接受: %d (%v)", code, out["error"])
	}
	if got := gitBashStatus(t, do)["configured"]; got != bashPath {
		t.Fatalf("引号未被清洗: configured=%v", got)
	}

	// 空串 = 清空回自动探测（与 python_interpreter 的语义一致）。
	code, out = do(http.MethodPut, "/api/settings", map[string]any{"git_bash_path": ""})
	if code != 200 {
		t.Fatalf("清空失败: %d (%v)", code, out["error"])
	}
	if got := gitBashStatus(t, do)["configured"]; got != "" {
		t.Fatalf("清空后 configured=%v, want 空串", got)
	}
	if got := waitShellPath(""); got != "" {
		t.Fatalf("清空未传导到执行层: %q", got)
	}
}

// waitShellPath 轮询执行层的显式路径设置。开机 applyGitBash 跑在启动协程里，
// 保存动作是另一条路径，两者都从同一个库读同一个值，所以只断言终态一致，
// 不假设先后顺序。
func waitShellPath(want string) string {
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := actool.ShellPath()
		if got == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestGitBashDetect 是"一键检测"端点：带 path 走手动验证，不带走自动探测。
// 断言只针对两端共有的结构，避免依赖具体机器的安装情况。
func TestGitBashDetect(t *testing.T) {
	do := newGitBashFixture(t)
	t.Cleanup(func() { actool.SetShellPath("") })

	// 自动检测。
	code, out := do(http.MethodPost, "/api/settings/git-bash/detect", map[string]any{})
	if code != 200 {
		t.Fatalf("detect=%d (%v)", code, out["error"])
	}
	if _, has := out["detail"]; !has {
		t.Error("检测结果必须给出可读结论")
	}
	if _, has := out["effective"]; !has {
		t.Error("检测结果必须回显当前生效的 shell")
	}
	if _, has := out["candidates"]; !has {
		t.Error("检测结果必须带候选清单（前端据此提供手动选择）")
	}
	if applicable, _ := out["applicable"].(bool); applicable != (runtime.GOOS == "windows") {
		t.Errorf("applicable=%v 与平台不符", applicable)
	}

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this host")
	}

	// 手动路径实测：真 bash 应通过并带出版本行。
	code, out = do(http.MethodPost, "/api/settings/git-bash/detect", map[string]any{"path": bashPath})
	if code != 200 {
		t.Fatalf("detect path=%d (%v)", code, out["error"])
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("对真实 bash 的探测应成功: %v", out)
	}
	if v, _ := out["version"].(string); strings.TrimSpace(v) == "" {
		t.Error("探测应带回 shell 版本信息")
	}

	// 不存在的路径：结论必须是不可用并附原因（而不是 500）。
	bad := filepath.Join(t.TempDir(), "ghost.exe")
	code, out = do(http.MethodPost, "/api/settings/git-bash/detect", map[string]any{"path": bad})
	if code != 200 {
		t.Fatalf("探测不存在的路径也应返回结论而非报错: %d", code)
	}
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("不存在的 shell 被判为可用: %v", out)
	}
	if e, _ := out["error"].(string); e == "" {
		t.Fatal("探测失败必须附带原因")
	}

	// 普通可执行文件也要被拒绝：存在性检查会放行，语法检查不会。
	notAShell := filepath.Join(t.TempDir(), "notashell")
	if err := writeExecForTest(notAShell); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	_, out = do(http.MethodPost, "/api/settings/git-bash/detect", map[string]any{"path": notAShell})
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("普通文件被判为可用 shell: %v", out)
	}
}

// TestGitBashBrowseIsWindowsOnlyAndPathGuarded 检查浏览端点的安全边界：
// 非 Windows 一律拒绝；Windows 上只接受不含 .. 的绝对目录。
func TestGitBashBrowseIsWindowsOnlyAndPathGuarded(t *testing.T) {
	do := newGitBashFixture(t)

	code, out := do(http.MethodGet, "/api/settings/git-bash/browse", nil)
	if runtime.GOOS != "windows" {
		if code != http.StatusBadRequest {
			t.Fatalf("非 Windows 浏览应 400，得到 %d (%v)", code, out["error"])
		}
		if e, _ := out["error"].(string); !strings.Contains(e, "Windows") {
			t.Errorf("拒绝理由应说明平台限制: %q", e)
		}
		return
	}
	if code != 200 {
		t.Fatalf("browse roots=%d (%v)", code, out["error"])
	}
	if roots, ok := out["roots"].([]any); !ok || len(roots) == 0 {
		t.Fatalf("应返回可用盘符: %v", out)
	}
	// 相对路径与 .. 越界都要拒绝。
	for _, bad := range []string{"Git/bin", `C:\Program Files\..\..\Windows`} {
		code, _ := do(http.MethodGet, "/api/settings/git-bash/browse?dir="+bad, nil)
		if code == 200 {
			t.Errorf("不应接受 dir=%q", bad)
		}
	}
}

// TestValidateGitBashPath 纯函数校验：引号清洗、相对路径拒绝、目录拒绝。
func TestValidateGitBashPath(t *testing.T) {
	if p, err := validateGitBashPath("  \"\" "); err != nil || p != "" {
		t.Errorf("空白应视作清空: p=%q err=%v", p, err)
	}
	if _, err := validateGitBashPath(`Git\bin\bash.exe`); err == nil {
		t.Error("相对路径必须拒绝")
	}
	if _, err := validateGitBashPath(t.TempDir()); err == nil {
		t.Error("目录必须拒绝")
	}
	f := filepath.Join(t.TempDir(), "bash.exe")
	if err := writeExecForTest(f); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	p, err := validateGitBashPath(`"` + f + `"`)
	if err != nil || p != f {
		t.Errorf("合法绝对路径应通过并去引号: p=%q err=%v", p, err)
	}
	if got := parentDir(`C:\Program Files\Git\bin`); runtime.GOOS == "windows" && got != `C:\Program Files\Git` {
		t.Errorf("parentDir=%q", got)
	}
}

func writeExecForTest(p string) error {
	// 0o755：让"这个文件不是 shell"判定走语法失败，而不是权限失败。
	return os.WriteFile(p, []byte("#!/bin/sh\necho not-a-real-shell\n"), 0o755)
}
