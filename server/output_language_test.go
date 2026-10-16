package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/config"
)

// TestOutputLanguageCacheFollowsSettings pins that the cached output language
// (read by every writeErr and label) is refreshed by PUT /api/settings and stays
// Simplified Chinese: 本仓库只支持 zh，任何其他语言代码都必须被拒绝。
func TestOutputLanguageCacheFollowsSettings(t *testing.T) {
	t.Setenv("ARTEX_LANG", "")
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	prev, hadPrev, _ := m.pg.GetSetting(settingOutputLanguage)
	t.Cleanup(func() { config.CurrentLanguage = nil })
	// defer (not t.Cleanup) so the restore runs before the deferred m.Close.
	defer func() {
		if hadPrev {
			_ = m.pg.SetSetting(settingOutputLanguage, prev)
		} else {
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingOutputLanguage)
		}
	}()
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingOutputLanguage)

	s := New(context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	errOf := func(rec *httptest.ResponseRecorder) string {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v.Error
	}

	if got := s.outputLanguage(); got != "zh" {
		t.Fatalf("default output language = %q, want zh", got)
	}
	if got := errOf(do(http.MethodPut, "/api/settings", map[string]any{"output_language": "fr"})); got != errOutputLanguageInvalid {
		t.Fatalf("zh error = %q, want %q", got, errOutputLanguageInvalid)
	}

	// 非简体中文的取值必须被拒绝，且不得写入设置。
	for _, lang := range []string{"en", "es", "ko", "fr"} {
		rec := do(http.MethodPut, "/api/settings", map[string]any{"output_language": lang})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("set %s status=%d，预期 400（body %s）", lang, rec.Code, rec.Body.String())
		}
		if got := errOf(rec); got != errOutputLanguageInvalid {
			t.Errorf("set %s error = %q，预期 %q", lang, got, errOutputLanguageInvalid)
		}
	}

	// 显式设置简体中文应被接受，并回显为 zh。
	if rec := do(http.MethodPut, "/api/settings", map[string]any{"output_language": "zh"}); rec.Code != http.StatusOK {
		t.Fatalf("set zh status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.outputLanguage(); got != "zh" {
		t.Fatalf("写入 zh 后缓存语言 = %q，预期 zh", got)
	}
	var settings struct {
		OutputLanguage string `json:"output_language"`
	}
	_ = json.Unmarshal(do(http.MethodGet, "/api/settings", nil).Body.Bytes(), &settings)
	if settings.OutputLanguage != "zh" {
		t.Fatalf("GET /api/settings output_language = %q，预期 zh", settings.OutputLanguage)
	}
}
