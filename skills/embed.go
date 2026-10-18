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
//go:embed api-recon playwright-cli scopesentry-mcp web-vuln-tactics reverse-engineering exploit-kb-patterns expert-triage-pivot human-verification
var Bundled embed.FS

// EnsureBundled idempotently writes embedded built-in skill files into dstDir.
// Existing files on disk are NEVER overwritten so user edits via the Web UI
// ("技能包" page) survive restarts.
func EnsureBundled(dstDir string) error {
	if dstDir == "" {
		return nil
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
