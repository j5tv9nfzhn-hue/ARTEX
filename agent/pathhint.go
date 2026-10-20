package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// localPathError 把「文件不存在」变成一条能一次改对的提示：给出按工作目录解析后的真实
// 绝对路径、当前工作目录，以及同目录（或工作目录）下最接近的候选文件。
//
// 为什么需要：模型引用的文件路径常常是上一步 Write/Bash 产物的近似值——典型如
// data/tasks/<id>/cmd-output/output-<pid>-<n>.txt 里的自增编号错一位，或
// wordlist 写到别处却按相对路径引用。只回一句
// "The system cannot find the file specified" 时，模型只能反复猜同一个目录，
// 一次错误会连带烧掉好几轮。给出候选清单后，绝大多数情况下一轮即可改对。
func (t *ToolSet) localPathError(purpose, given, resolved string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s失败：路径不存在 %s", purpose, resolved)
	if strings.TrimSpace(given) != "" && given != resolved {
		fmt.Fprintf(&b, "（参数值 %q 为相对路径，已按工作目录解析）", given)
	}
	if t != nil && strings.TrimSpace(t.workDir) != "" {
		fmt.Fprintf(&b, "；工作目录：%s", t.workDir)
	}

	dir := filepath.Dir(resolved)
	base := filepath.Base(resolved)
	if cands := nearestFiles(dir, base, 6); len(cands) > 0 {
		fmt.Fprintf(&b, "；该目录下相近的文件：%s", strings.Join(cands, ", "))
	} else if entries := dirEntries(dir, 8); len(entries) > 0 {
		fmt.Fprintf(&b, "；该目录现有内容：%s", strings.Join(entries, ", "))
	} else if t != nil && strings.TrimSpace(t.workDir) != "" {
		if entries := dirEntries(t.workDir, 12); len(entries) > 0 {
			fmt.Fprintf(&b, "；工作目录现有内容：%s", strings.Join(entries, ", "))
		}
	}
	b.WriteString("。请先用 LS 或 Read 确认实际路径（相对路径按工作目录解析）后重试，不要重复提交同一路径。")
	return b.String()
}

// nearestFiles 返回 dir 下与 base 最相近的文件名（同前缀/同扩展名优先），最多 limit 个。
func nearestFiles(dir, base string, limit int) []string {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		return nil
	}
	lb := strings.ToLower(base)
	lbStem := strings.TrimSuffix(lb, strings.ToLower(filepath.Ext(lb)))
	lbExt := strings.ToLower(filepath.Ext(lb))

	type cand struct {
		name  string
		score int
	}
	var cands []cand
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		nStem := strings.TrimSuffix(n, strings.ToLower(filepath.Ext(n)))
		score := commonPrefixLen(nStem, lbStem)
		if lbStem != "" && (strings.HasPrefix(nStem, lbStem) || strings.HasPrefix(lbStem, nStem)) {
			score += 4
		}
		if lbExt != "" && strings.ToLower(filepath.Ext(n)) == lbExt {
			score += 2
		}
		// 阈值 4：至少要有「同名前缀」或够长的公共前缀才算相近；只共享一两个首字母的
		// 无关文件（如 other.log 与 output-…）不该混进候选里干扰模型判断。
		if score >= 4 {
			cands = append(cands, cand{e.Name(), score})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.name)
	}
	return out
}

// dirEntries 列出目录一级内容（目录名带尾斜杠），最多 limit 个。
func dirEntries(dir string, limit int) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
