package skills

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// Bundled embeds all built-in skill packages so single-file binary releases
// (CI artifacts, build.sh ARTEX_PACKAGE=0, or self-updated binaries) always
// materialize the default skills into the runtime skills/ directory when missing.
//
//go:embed api-recon camoufox scopesentry-mcp web-vuln-tactics reverse-engineering exploit-kb-patterns expert-triage-pivot human-verification ai-coding-sandbox-tactics wasm-cloud-saas-tactics rea-mcp wpe64-mcp mitmproxy-traffic ai-gateway-logic case-web-10 case-web-13 case-web-6 case-web-7 case-web-9 case-web-business-logic-abuse-enablement-37 case-web-info-leak-ato-unauth-20 case-web-logic-flaw-bypass-payment-24 case-web-payment-sandbox-false-positive case-web-vpngap-matrix-payment-ato
var Bundled embed.FS

// retiredSkills 是从内置技能里**下线**的目录。升级安装时主动删除，
// 避免旧技能继续把流量导回已废弃的浏览器引擎（Chromium / playwright-cli）。
var retiredSkills = []string{"playwright-cli"}

// EnsureBundled idempotently writes embedded built-in skill files into dstDir.
// Existing files on disk are NEVER overwritten so user edits via the Web UI
// ("技能包" page) survive restarts.
func EnsureBundled(dstDir string) error {
	if dstDir == "" {
		return nil
	}
	for _, name := range retiredSkills {
		_ = os.RemoveAll(filepath.Join(dstDir, name))
	}
	return fs.WalkDir(Bundled, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dstDir, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, statErr := os.Stat(target); statErr == nil {
			return nil // preserve existing user-customized skill file
		}
		data, readErr := Bundled.ReadFile(rel)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(target, data, 0o644)
	})
}
