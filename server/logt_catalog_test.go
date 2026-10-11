package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/config"
)

// TestLogTTemplatesAreChinese pins that every logT("…") template in this package
// is Simplified Chinese with no leftover Hangul, and that every template resolves
// to itself under any language setting. 本仓库只提供简体中文一种语言，
// 因此这里不再检查其他语言译文，只保证调用点模板本身是中文且可安全回退。
func TestLogTTemplatesAreChinese(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var keys []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "logT" {
				return true
			}
			// A plain literal is the template itself. A computed argument (e.g. a
			// map lookup) is checked through every Hangul literal it contains.
			found := 0
			ast.Inspect(call.Args[0], func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
				}
				if strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
					keys = append(keys, s)
					found++
				}
				return true
			})
			if found == 0 {
				t.Errorf("%s: 未找到中文模板字面量 in logT argument", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if len(keys) == 0 {
		t.Fatal("found no logT call sites; the scan is broken")
	}
	// 模板必须是无韩文的中文，且在任何语言设置下都稳定返回自身。
	for _, lang := range []string{"zh", "ko", "en", "es", "fr", ""} {
		t.Setenv("ARTEX_LANG", lang)
		for _, k := range keys {
			if hasHangulRune(k) {
				t.Errorf("logT 模板仍有韩文残留：%q", k)
			}
			if got := config.T(k); got != k {
				t.Errorf("logT 模板在 %s 下应回退到中文源模板：got=%q want=%q", lang, got, k)
			}
		}
	}
}

func hasHangulRune(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) >= 0
}

// TestErrCatalogFormatVerbsMatch pins that translated API error templates keep the
// Simplified Chinese template's format verbs in order; trMsg wraps the template before
// fmt.Sprintf/Errorf fill it, so a mismatch would garble the response.
func TestErrCatalogFormatVerbsMatch(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for ko, tr := range errCatalog {
		want := strings.Join(verbs.FindAllString(ko, -1), " ")
		for lang, v := range tr {
			if got := strings.Join(verbs.FindAllString(v, -1), " "); got != want {
				t.Errorf("errCatalog[%q][%s] verbs = %q, want %q", ko, lang, got, want)
			}
		}
	}
}

// TestAuthErrorsAreChinese pins that every authErr* message the auth endpoints
// return is Simplified Chinese and survives any language setting unchanged.
func TestAuthErrorsAreChinese(t *testing.T) {
	t.Cleanup(func() { config.CurrentLanguage = nil })
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "auth.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "authErr") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					msgs = append(msgs, s)
				}
			}
		}
	}
	if len(msgs) == 0 {
		t.Fatal("found no authErr constants; the scan is broken")
	}
	for _, lang := range []string{"zh", "ko", "en", "es", "fr", ""} {
		config.CurrentLanguage = func() string { return lang }
		for _, m := range msgs {
			if hasHangulRune(m) {
				t.Errorf("认证错误仍有韩文残留：%q", m)
			}
			if got := trMsg(m); got != m {
				t.Errorf("认证错误在 %s 下应回退到中文源消息：got=%q want=%q", lang, got, m)
			}
		}
	}
}
