package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bundled skills are the library every fresh install starts with, and the
// only thing standing between "three skills" and "a usable knowledge base".
// They are loaded by directory scan, so a malformed frontmatter does not fail
// loudly -- it yields a skill with no name and no description, which the agent
// then has no reason to load.
//
// That exact failure shipped once: skills/scopesentry/SKILL.md opened its
// frontmatter, left a blank line, wrote "## name: ..." instead of "name: ...",
// and never closed the block. It parsed as a file and did nothing.

type skillFrontmatter struct {
	name        string
	description string
	body        string
}

// parseSkillFrontmatter reads the leading `---` block. It is deliberately
// strict: anything that is not exactly `key: value` lines inside one
// well-formed block is an error, because the alternative is a skill that
// silently loses its metadata.
func parseSkillFrontmatter(t *testing.T, path string) skillFrontmatter {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("第 1 行必须是 ---，实际 %q", lines[0])
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatal("frontmatter 没有闭合的 ---")
	}

	var fm skillFrontmatter
	for _, line := range lines[1:end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			t.Fatalf("frontmatter 内不接受标题行: %q（应为 name: value）", line)
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("frontmatter 行不是 key: value 形式: %q", line)
		}
		switch strings.TrimSpace(key) {
		case "name":
			fm.name = strings.TrimSpace(value)
		case "description":
			fm.description = strings.TrimSpace(value)
		case "allowed-tools":
			// Anthropic Agent Skills extension; passed through, not our concern.
		case "mcps", "mcp", "license", "compatibility":
			// norma/skill supported frontmatter keys (e.g. mcps gates skill-bound MCP servers).
		default:
			t.Fatalf("frontmatter 出现未知键 %q", strings.TrimSpace(key))
		}
	}
	fm.body = strings.Join(lines[end+1:], "\n")
	return fm
}

func TestBundledSkillsHaveUsableFrontmatter(t *testing.T) {
	dir := filepath.Join("..", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("技能目录不可读: %v", err)
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			t.Errorf("%s 缺少 SKILL.md", e.Name())
			continue
		}
		found++
		fm := parseSkillFrontmatter(t, skillFile)

		if fm.name == "" {
			t.Errorf("%s: frontmatter 缺少 name", e.Name())
		} else if fm.name != e.Name() {
			// The directory name is what the loader and the invocation command
			// use; a mismatch means /name never resolves.
			t.Errorf("%s: name=%q 与目录名不一致", e.Name(), fm.name)
		}
		if len(fm.description) < 10 {
			t.Errorf("%s: description 过短(%d 字)，不足以作为匹配依据", e.Name(), len(fm.description))
		}
		if len(strings.TrimSpace(fm.body)) < 40 {
			t.Errorf("%s: 正文过短，不构成可用技能", e.Name())
		}
	}
	if found == 0 {
		t.Skip("技能目录为空")
	}
	t.Logf("校验了 %d 个内置技能", found)
}

// TestSkillTemplateIsUsable pins the fallback create_skill writes when the
// agent supplies no instructions. The old one was a three-line empty shell,
// which satisfied "is a file" while being useless to a later run.
func TestSkillTemplateIsUsable(t *testing.T) {
	got := skillTemplate("example-skill", "示例技能")

	for _, want := range []string{
		"# example-skill",
		"示例技能",
		"## When to Use",
		"## Procedure",
		"## Pitfalls",
		"## Verification",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("模板缺少 %q", want)
		}
	}
	if len(got) < 300 {
		t.Errorf("模板过短(%d 字节)，达不到可用的下限", len(got))
	}
}
