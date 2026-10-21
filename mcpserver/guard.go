package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultDeny 是即使 AllowAny 打开也拒绝读取的本机敏感文件。
//
// 理由不是「不信任调用方」，而是 MCP 客户端会把工具输出喂给模型：一旦外部
// harness 被网页提示词注入，strings_scan 就成了最省事的凭据读取器。这里按
// 后缀/路径片段做最小拦截，误伤面只落在密钥库与浏览器 cookie 上。
var defaultDeny = []string{
	"credential.key",
	"jwt.key",
	"id_rsa",
	"id_ed25519",
	".ssh",
	".aws",
	".netrc",
	".git-credentials",
	"cookies",
	"login data",
	"key4.db",
	"logins.json",
}

// FileGuard 约束 L0 工具能读到的路径。
//
// 零值表示不限制（仍会拦截 defaultDeny）。Root 非空时只允许 Root 下的文件；
// AllowAny 显式打开「任意路径」，用于分析散落在桌面/下载目录的样本。
type FileGuard struct {
	Root     string
	AllowAny bool
	// Deny 是额外的拒绝规则：绝对路径或 glob，与 defaultDeny 一起生效。
	Deny []string
}

// Check 校验一个待读取的路径；返回 nil 表示可以读。
func (g *FileGuard) Check(path string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return fmt.Errorf("path 为空")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return fmt.Errorf("路径无法解析: %v", err)
	}
	if err := g.checkDeny(abs); err != nil {
		return err
	}
	if g == nil || g.AllowAny || strings.TrimSpace(g.Root) == "" {
		return nil
	}
	root, err := filepath.Abs(g.Root)
	if err != nil {
		return fmt.Errorf("允许目录无法解析: %v", err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("路径 %s 不在允许目录 %s 内（可用 --allow-any-path 放开）", abs, root)
	}
	return nil
}

func (g *FileGuard) checkDeny(abs string) error {
	lower := strings.ToLower(filepath.ToSlash(abs))
	base := strings.ToLower(filepath.Base(abs))
	rules := defaultDeny
	if g != nil && len(g.Deny) > 0 {
		rules = append(append([]string{}, rules...), g.Deny...)
	}
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if strings.ContainsAny(r, "*?") {
			if ok, _ := filepath.Match(r, base); ok {
				return fmt.Errorf("路径命中拒绝规则 %q: %s", r, abs)
			}
			continue
		}
		if strings.ContainsAny(r, "/\\") {
			if strings.HasSuffix(lower, strings.ToLower(filepath.ToSlash(r))) || strings.Contains(lower, strings.ToLower(filepath.ToSlash(r))) {
				return fmt.Errorf("路径命中拒绝规则 %q: %s", r, abs)
			}
			continue
		}
		if base == r || strings.HasSuffix(base, "."+r) {
			return fmt.Errorf("路径命中拒绝规则 %q: %s", r, abs)
		}
	}
	return nil
}

// GuardedPath 是给工具描述用的提示语，说明当前允许的范围。
func (g *FileGuard) GuardedPath() string {
	if g == nil || g.AllowAny || strings.TrimSpace(g.Root) == "" {
		return "本机任意可读路径（密钥库与浏览器凭据除外）"
	}
	return "允许目录 " + g.Root + " 内的路径"
}

// Exists 是一个小工具，供冒烟用例判断样本是否可用。
func Exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
