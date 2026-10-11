package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 自定义工具 CRUD、试运行错误的中文化回归测试。
// 复用 assertChineseError、decodeErrorField；工具结果与 schema 不在这些 HTTP 断言范围。
// TestCustomToolErrorConstantsLocalized 固定八类常量。
// errCustomToolKeyExists、errCustomToolEditCustomOnly 位于 pg.GetTool 数据库门禁之后，
// 无数据库时只断言常量，其余验证路径由下面的 HTTP 测试执行。
func TestCustomToolErrorConstantsLocalized(t *testing.T) {
	for _, c := range []struct{ name, msg string }{
		{"keyFormat", errCustomToolKeyFormat},
		{"kindInvalid", errCustomToolKindInvalid},
		{"httpSchemaRequired", errCustomToolHTTPSchemaRequired},
		{"keyExists", errCustomToolKeyExists},
		{"editCustomOnly", errCustomToolEditCustomOnly},
		{"badBody", errCustomToolBadBody},
		{"shellNoExec", errCustomToolShellNoExec},
		{"unknownKindPrefix", errCustomToolUnknownKindPrefix},
	} {
		assertChineseError(t, "customtool."+c.name, c.msg)
	}
}

// newCustomToolServer 通过 s.pg 门禁，但不建立真实数据库连接。
// &db.DB{} 的内部 *sql.DB 为 nil，外层指针非 nil，因此 pg() 不返回 503。
// 以下验证路径均在实际 pg.GetTool 等调用之前返回，不会解引用 nil 数据库。
func newCustomToolServer() *Server {
	return &Server{m: &Manager{pg: &db.DB{}}}
}

// TestCustomToolCreateValidationLocalized 真实调用三个创建输入验证分支，
// 它们都在 pg.GetTool 前返回，并检查中文 HTTP 正文。
func TestCustomToolCreateValidationLocalized(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"bad-key", `{"key":"BadKey","kind":"command"}`, errCustomToolKeyFormat},
		{"bad-kind", `{"key":"goodkey","kind":"bogus"}`, errCustomToolKindInvalid},
		{"http-no-schema", `{"key":"goodkey","kind":"http"}`, errCustomToolHTTPSchemaRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCustomToolServer()
			req := httptest.NewRequest(http.MethodPost, "/api/tools/custom", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.pgCreateCustomTool(rec, req)
			if rec.Code != 400 && rec.Code != 409 {
				t.Fatalf("预期验证错误状态码，实际为 %d（正文 %q）", rec.Code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.want {
				t.Fatalf("响应正文不匹配：got %q want %q", got, c.want)
			}
			assertChineseError(t, "customtool.create."+c.name, got)
		})
	}
}

// TestCustomToolTestRunValidationLocalized 检查三个无数据库试运行验证分支的中文响应。
func TestCustomToolTestRunValidationLocalized(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"bad-body", `not-json`, errCustomToolBadBody},
		{"shell", `{"kind":"shell"}`, errCustomToolShellNoExec},
		{"unknown-kind", `{"kind":"bogus"}`, errCustomToolUnknownKindPrefix + "bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newCustomToolServer()
			req := httptest.NewRequest(http.MethodPost, "/api/tools/custom/test", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.pgTestCustomTool(rec, req)
			if rec.Code != 400 {
				t.Fatalf("预期状态码 400，实际为 %d（正文 %q）", rec.Code, rec.Body.String())
			}
			got := decodeErrorField(t, rec.Body.Bytes())
			if got != c.want {
				t.Fatalf("响应正文不匹配：got %q want %q", got, c.want)
			}
			// unknown-kind 后拼接 req.Kind 的 ASCII 值 bogus，检查包装中文且无韩文即可。
			assertChineseError(t, "customtool.test."+c.name, errCustomToolUnknownKindPrefix)
		})
	}
}
