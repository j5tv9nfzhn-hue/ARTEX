package mcpbuiltin

// 这些用例的目的不是覆盖率，而是证明「工具真的能跑」——
// 每个断言都对应一条模型的真实调用路径：发请求、跑字典、比基线、解 JWT、读样本。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func call(t *testing.T, fn func(context.Context, json.RawMessage) (string, error), args any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := fn(context.Background(), raw)
	if err != nil {
		t.Fatalf("工具返回错误：%v", err)
	}
	return out
}

func TestBuiltinServersExposed(t *testing.T) {
	for _, name := range []string{"artex-recon", "artex-webscan", "artex-binscan"} {
		s, ok := ByName(name)
		if !ok {
			t.Fatalf("缺少内置服务器 %s", name)
		}
		if s.Domain == "" || s.Title == "" {
			t.Fatalf("%s 缺元数据", name)
		}
		for _, tl := range s.Tools {
			if tl.Name == "" || tl.Description == "" || tl.Schema == nil || tl.Handler == nil {
				t.Fatalf("%s/%s 定义不完整", name, tl.Name)
			}
		}
	}
}

func TestDirProbeFindsKnownPath(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte("admin panel"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	out := call(t, dirProbe, map[string]any{
		"url": srv.URL, "wordlist": []string{"admin", "not-there"}, "status": []int{200},
	})
	if !strings.Contains(out, "/admin") {
		t.Fatalf("应命中 /admin，实际输出：\n%s", out)
	}
	if strings.Contains(out, "/not-there") {
		t.Fatalf("404 不应出现在 200 过滤结果里：\n%s", out)
	}
}

func TestHTTPFuzzComparesBaseline(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exists") {
			_, _ = w.Write([]byte("yes"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	tpl := "GET /FUZZ HTTP/1.1\nHost: " + strings.TrimPrefix(srv.URL, "http://") + "\n\n"
	out := call(t, httpFuzz, map[string]any{
		"request": tpl, "payloads": []string{"exists", "missing"}, "base_url": srv.URL,
	})
	if !strings.Contains(out, "exists") || !strings.Contains(out, "200") {
		t.Fatalf("模糊测试结果不符合预期：\n%s", out)
	}
	if !strings.Contains(out, "404") {
		t.Fatalf("应同时报告 404 分支：\n%s", out)
	}
}

func TestJWTInspectFindsWeakSecret(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin","exp":9999999999}`))
	signing := header + "." + payload
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(signing))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	out := call(t, jwtInspect, map[string]any{"token": signing + "." + sig})
	if !strings.Contains(out, "弱密钥命中") {
		t.Fatalf("应识别出弱密钥 secret：\n%s", out)
	}
	if !strings.Contains(out, "admin") {
		t.Fatalf("应解出 payload：\n%s", out)
	}
}

func TestStringsAndIdentify(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sample.bin")
	body := []byte("HEADER\x00\x00ARTEX-MARKER-9f3c\x00http://evil.example/c2\x00")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	so := call(t, stringsScan, map[string]any{"path": p, "min_len": 4})
	if !strings.Contains(so, "ARTEX-MARKER-9f3c") {
		t.Fatalf("字符串扫描没有命中标记：\n%s", so)
	}
	io := call(t, fileIdentify, map[string]any{"path": p})
	if !strings.Contains(io, "SHA256") {
		t.Fatalf("file_identify 应给出哈希：\n%s", io)
	}
	ioc := call(t, iocExtract, map[string]any{"path": p})
	if !strings.Contains(ioc, "evil.example") {
		t.Fatalf("IOC 抽取应命中域名：\n%s", ioc)
	}
}

func TestPEInfoOnSelf(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上验证 PE")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("取不到自身路径：%v", err)
	}
	out, err := peInfo(context.Background(), mustJSON(map[string]any{"path": exe}))
	if err != nil {
		t.Fatalf("解析自身 PE 失败：%v", err)
	}
	if !strings.Contains(out, "架构") || !strings.Contains(out, "节：") {
		t.Fatalf("PE 报告缺关键段落：\n%s", out)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal: %v", err))
	}
	return b
}
