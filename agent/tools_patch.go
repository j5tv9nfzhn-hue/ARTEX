package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

const (
	// maxPatchFiles / maxPatchEdits 是单次 apply_patch 的硬上限：模型一次要改
	// 太多文件通常是「任务没想清楚」，而且失败时回滚代价更高。超限就让它拆两次。
	maxPatchFiles = 40
	maxPatchEdits = 200
)

// patchEdit 是「一处精确字符串替换」，语义与 SDK 的 Edit/MultiEdit 完全一致。
type patchEdit struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// patchFile 是一个目标文件及其按顺序应用的编辑。
type patchFile struct {
	FilePath string      `json:"file_path"`
	Edits    []patchEdit `json:"edits"`
}

// applyPatchInput 是 apply_patch 的入参。
type applyPatchInput struct {
	Patches []patchFile `json:"patches"`
}

// patchTarget 是校验/落盘阶段的工作副本（同一文件被写两次会合并成一项）。
type patchTarget struct {
	display string
	path    string
	key     string
	orig    []byte
	cur     string
	mode    os.FileMode
	edits   []patchEdit
	count   int
	changed bool
}

// newApplyPatchTool 构建 apply_patch：一次调用改多个文件。
//
// 与 SDK 的 MultiEdit 的差别只有一条，但很关键——MultiEdit 只能改一个文件，于是
// 「改 10 个文件」要 10 次调用、10 个模型往返。apply_patch 把 N 个文件塞进一次
// 调用：先在内存里把每一处编辑都校验并应用（任一失败则整个调用不落盘），全部
// 通过后再逐文件原子写入（中途写失败则回滚已写的）。模型看到的是「全部成功」
// 或「一处都没改 + 具体哪个文件第几处、为什么」，不会出现改一半的中间态。
//
// Concurrent 声明为 true 是安全的：它内部对全部目标路径按字典序取写锁，与
// Write/Edit 的按路径锁是同一张表，因此彼此互斥；不同路径仍可真并行。
func newApplyPatchTool() actool.CoreTool {
	// 用字段赋值而不是复合字面量：字面量里多行嵌套 map 的列对齐由 gofmt 决定，
	// 手写很容易与 gofmt 输出不一致（本仓 CI 会查 gofmt），字段赋值没有这个坑。
	spec := actool.Spec{}
	spec.Name = "apply_patch"
	spec.Description = "在**一次调用**里同时修改多个文件（每个文件可含多处精确字符串替换）。" +
		"所有编辑先全部在内存中校验，任一 old_string 匹配不上则整个调用不落盘、文件保持原样；全部通过后按文件原子写入，写失败自动回滚。" +
		"**跨文件改动优先用它，不要连发多个 Edit**；同一文件的多处修改放进同一个 edits 数组，不要拆成多次调用。"
	spec.Prompt = "patches 每项是 {file_path, edits:[{old_string,new_string,replace_all}]}。edits 按顺序应用，后一处必须匹配前一处的修改结果。" +
		"old_string 必须与文件逐字一致（含缩进与空行），默认要求唯一——多处命中会报错，此时多带上文或设 replace_all=true。" +
		"只修改已存在的文件；新建文件请用 Write。"
	spec.Schema = applyPatchSchema()
	spec.Concurrent = func(json.RawMessage) bool { return true }
	spec.Run = runApplyPatch
	return actool.Build(spec)
}

// applyPatchSchema 每个 map 字面量都写在一行内，避免多行嵌套 map 的 gofmt 列对齐
// 依赖手算（同样是为了与 gofmt 输出保持一致）。
func applyPatchSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	edit := map[string]any{"type": "object", "properties": map[string]any{"old_string": str("要替换的原文，必须与文件内容完全一致（含缩进与空行）。"), "new_string": str("替换后的文本。"), "replace_all": boolean("替换全部命中（默认要求全局唯一）。")}, "required": []any{"old_string", "new_string"}}
	file := map[string]any{"type": "object", "properties": map[string]any{"file_path": str("文件路径（绝对路径，或相对工作目录）。"), "edits": map[string]any{"type": "array", "description": "按顺序应用到该文件的多处编辑。", "items": edit}}, "required": []any{"file_path", "edits"}}
	return map[string]any{"type": "object", "properties": map[string]any{"patches": map[string]any{"type": "array", "description": "要修改的文件列表，彼此独立，可一次给多个。", "items": file}}, "required": []any{"patches"}}
}

func runApplyPatch(_ context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	var in applyPatchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return actool.Result{}, err
	}
	if len(in.Patches) == 0 {
		return actool.Errorf("错误：patches 为空。apply_patch 至少需要一项 {file_path, edits}。"), nil
	}
	if len(in.Patches) > maxPatchFiles {
		return actool.Errorf(fmt.Sprintf("错误：一次最多改 %d 个文件（收到 %d 项）。请拆成多次 apply_patch。", maxPatchFiles, len(in.Patches))), nil
	}

	// 归并：同一文件出现多次时按出现顺序合并编辑，避免「两次读同一底本」互相覆盖。
	targets := make([]*patchTarget, 0, len(in.Patches))
	index := make(map[string]*patchTarget, len(in.Patches))
	edits := 0
	for i, p := range in.Patches {
		if strings.TrimSpace(p.FilePath) == "" {
			return actool.Errorf(fmt.Sprintf("错误：第 %d 项的 file_path 为空。", i+1)), nil
		}
		if len(p.Edits) == 0 {
			return actool.Errorf(fmt.Sprintf("错误：%s 没有任何编辑项。", p.FilePath)), nil
		}
		edits += len(p.Edits)
		if edits > maxPatchEdits {
			return actool.Errorf(fmt.Sprintf("错误：一次最多 %d 处编辑（已收到 %d 处）。请拆成多次 apply_patch。", maxPatchEdits, edits)), nil
		}
		key := fsPathKey(tc, p.FilePath)
		if t, ok := index[key]; ok {
			t.edits = append(t.edits, p.Edits...)
			continue
		}
		t := &patchTarget{
			display: p.FilePath,
			path:    resolveToolPath(tc, p.FilePath),
			key:     key,
			edits:   append([]patchEdit{}, p.Edits...),
		}
		index[key] = t
		targets = append(targets, t)
	}

	// 目标路径的互斥由装配层统一负责：AugmentTools 的 lockResources 在 Call 之前
	// 已按 patches[].file_path 把全部路径加好写锁（见 tools_concurrency.go）。这里
	// 不再自己加锁——同一把 sync.RWMutex 不可重入，重复加锁会死锁。

	// 阶段一：全部读入 + 全部在内存里应用。任一处失败 → 直接返回，一个文件都不写。
	for _, t := range targets {
		info, err := os.Stat(t.path)
		if err != nil {
			return actool.Errorf(fmt.Sprintf("错误：无法读取 %s：%v（apply_patch 只改已存在的文件，新建请用 Write）。本次调用未修改任何文件。", t.display, err)), nil
		}
		if info.IsDir() {
			return actool.Errorf(fmt.Sprintf("错误：%s 是目录，不是文件。本次调用未修改任何文件。", t.display)), nil
		}
		data, err := os.ReadFile(t.path)
		if err != nil {
			return actool.Errorf(fmt.Sprintf("错误：无法读取 %s：%v。本次调用未修改任何文件。", t.display, err)), nil
		}
		t.orig = data
		t.mode = info.Mode().Perm()
		content := string(data)
		for j, e := range t.edits {
			where := fmt.Sprintf("%s 第 %d 处编辑", t.display, j+1)
			if e.OldString == e.NewString {
				return actool.Errorf(fmt.Sprintf("错误：%s 的 old_string 与 new_string 相同。本次调用未修改任何文件。", where)), nil
			}
			n := strings.Count(content, e.OldString)
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("错误：%s 的 old_string 在文件里找不到（须与文件内容逐字一致，含缩进与空行）。本次调用未修改任何文件。", where)), nil
			}
			if n > 1 && !e.ReplaceAll {
				return actool.Errorf(fmt.Sprintf("错误：%s 的 old_string 命中 %d 次、不唯一；请多带上文与下文，或设 replace_all=true。本次调用未修改任何文件。", where, n)), nil
			}
			if e.ReplaceAll {
				content = strings.ReplaceAll(content, e.OldString, e.NewString)
			} else {
				content = strings.Replace(content, e.OldString, e.NewString, 1)
			}
			t.count += n
		}
		t.cur = content
		t.changed = t.cur != string(t.orig)
	}

	// 阶段二：落盘（原子写）。中途失败 → 回滚已写的，回到「一处都没改」。
	written := make([]*patchTarget, 0, len(targets))
	for _, t := range targets {
		if !t.changed {
			continue
		}
		if err := writeFileAtomic(t.path, []byte(t.cur), t.mode); err != nil {
			for _, w := range written {
				_ = writeFileAtomic(w.path, w.orig, w.mode)
			}
			return actool.Errorf(fmt.Sprintf(
				"错误：写入 %s 失败：%v。已回滚此前写入的 %d 个文件，全部改动均未生效。",
				t.display, err, len(written))), nil
		}
		written = append(written, t)
	}

	// 结果只回「哪个文件几处」，不回正文——多文件场景下正文回显纯属烧 token。
	total := 0
	for _, t := range targets {
		total += t.count
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已提交 %d 个文件、共 %d 处编辑（原子写入，全部成功）：", len(written), total)
	for _, t := range targets {
		state := "无实际变化"
		if t.changed {
			state = fmt.Sprintf("%d 处", t.count)
		}
		fmt.Fprintf(&b, "\n- %s：%s", t.display, state)
	}
	return actool.Text(b.String()), nil
}

// resolveToolPath 与 SDK 文件工具的路径语义保持一致：相对路径按工具工作目录展开。
func resolveToolPath(tc *actool.ToolContext, p string) string {
	if filepath.IsAbs(p) || tc == nil || tc.WorkingDir == "" {
		return p
	}
	return filepath.Join(tc.WorkingDir, p)
}

// writeFileAtomic 先写同目录临时文件再 rename 覆盖目标：任何时刻目标路径上要么是
// 旧内容、要么是新内容，不会出现被截断写了一半的文件（工具被取消/进程被杀时尤其
// 重要）。Windows 上 rename 可能因目标被占用（杀软 / 编辑器 / 索引器）返回
// EBUSY / EPERM / EACCES，因此带退避重试。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".artex-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // rename 成功后是 no-op；失败路径上清掉临时文件
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if mode != 0 {
		_ = os.Chmod(tmp, mode) // 尽力保留原权限位；chmod 失败不影响内容
	}
	var lastErr error
	for i := 0; i < 20; i++ {
		if lastErr = os.Rename(tmp, path); lastErr == nil {
			return nil
		}
		time.Sleep(time.Duration(10+i*5) * time.Millisecond)
	}
	return lastErr
}

// toolBatchSpec 是段 [D]：工具调用批量规约。与 artifactSpec 同属「代码持有的
// 尾段」——即使 DB 里的正文被改得面目全非也一定存在。
//
// 为什么需要它：调度器能并行不等于模型会并行。SDK/ARTEX 都没有任何一句话告诉
// 模型「彼此独立的读取要放在一条消息里、多文件改动要一次提交」，于是模型按
// 「读一个 → 看一眼 → 再读一个」走，6 个文件就是 6 次完整往返——这才是多文件
// 任务慢的主因（本地调度并行只省墙钟，省不掉往返）。
func toolBatchSpec() string {
	return "\n\n**工具调用批量规约（效率红线）**：\n" +
		"1. **动手前先把这一轮要碰的东西想齐**：彼此独立的读取/检索（多个文件、多个 Glob/Grep）**放在同一条消息里一次发出**，能并行的一律并行——不要读一个、看一眼、再要下一个。\n" +
		"2. **跨文件改动用一次 `apply_patch`**：一次传入多个 {file_path, edits}，别连发多个 Edit；**同一文件的多处改动**放进同一个 edits 数组（或一次 `MultiEdit`），不要拆成多次调用。\n" +
		"3. 只有「下一步依赖上一步的结果」时才拆成多轮往返；串行是你自己造成的，没有任何好处。\n" +
		"4. `apply_patch` 是全有或全无的：任一编辑匹配不上则整个调用不落盘。因此可以放心一次提交多处改动；失败时按报错重读那一处、改好 old_string 再重试，不要退回「一次改一处」的老路。"
}
