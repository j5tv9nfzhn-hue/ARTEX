package server

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	skill "github.com/Autumn-27/artex/skills"
)

// 技能来源（provenance）。
//
// 技能包天然分两类：随二进制发布的内置战法（公开成熟技术与漏洞报告），以及任务
// 结束后沉淀的复盘记忆。二者差的不是"能力"——加载、可见性、计量完全一致——而是
// **生命周期**：内置随版本走，沉淀按部署累积。
//
// 此前这个区别只活在前端：一份硬编码的 14 个名字的 Set 加前缀猜测。于是三个问题
// 谁都回答不了：
//  1. 这个内置技能是出厂原样，还是被现场改过？（升级/排障第一问）
//  2. 这条技能属于知识库还是记忆库？（新人第一问）
//  3. 哪些沉淀正在被反复提议修改、哪些长期零调用？（该退役谁）
//
// 把来源变成后端判定的字段，前端只负责展示，新增内置技能不必再改前端。
const (
	// SkillOriginBuiltin 随二进制发布的内置战术（skills/embed.go）。
	SkillOriginBuiltin = "builtin"
	// SkillOriginDistilled 任务结束后沉淀的复盘（case-*）。
	SkillOriginDistilled = "distilled"
	// SkillOriginMerged 多条复盘合并提炼出的通用战法（merged-*）。
	SkillOriginMerged = "merged"
	// SkillOriginReport 外部报告导入提炼（report-*）。
	SkillOriginReport = "report"
	// SkillOriginUser 运维自建 / 上传 / 从 UI 新建。
	SkillOriginUser = "user"
)

// builtinSkillDirs 现场枚举 embed 文件系统，而不是再抄一份名单——抄一份就意味着
// 迟早和 skills/embed.go 的 //go:embed 列表漂移。
var builtinSkillDirs = func() map[string]bool {
	m := map[string]bool{}
	entries, err := fs.ReadDir(skill.Bundled, ".")
	if err != nil {
		return m
	}
	for _, e := range entries {
		if e.IsDir() {
			m[e.Name()] = true
		}
	}
	return m
}()

// builtinSkillFromReport 标出"内置，但由外部漏洞报告提炼而来"。
//
// 它们仍属知识库（随版本发布、走代码评审），只是来源是报告而非实战复盘——这个
// 细分决定了两类内置技能该由谁来维护，值得单独标出来。
var builtinSkillFromReport = map[string]bool{
	"ai-coding-sandbox-tactics": true,
	"wasm-cloud-saas-tactics":   true,
}

// classifySkillOrigin 判定技能来源。命名前缀是既有的约定（agent/skill_router.go
// 用同一套前缀区分内置路由与沉淀路由），这里沿用而不另造一套。
func classifySkillOrigin(name string) string {
	// 前缀优先：任务沉淀/合并/报告三类即使也被 //go:embed 打包（M15 起沉淀技能与
	// 上游技能同目录），仍应归到记忆库，由沉淀生命周期管理，而不是知识库。
	switch {
	case strings.HasPrefix(name, "case-"):
		return SkillOriginDistilled
	case strings.HasPrefix(name, "merged-"):
		return SkillOriginMerged
	case strings.HasPrefix(name, "report-"):
		return SkillOriginReport
	case builtinSkillDirs[name]:
		return SkillOriginBuiltin
	default:
		return SkillOriginUser
	}
}

// skillOutOfSync 报告某个内置技能包的内容是否与**当前二进制里的版本**不一致。
//
// 为什么需要它：skills.EnsureBundled 刻意"只补缺、不覆盖"，以免抹掉运维通过 UI
// 做的修改。代价是升级之后磁盘内容会停留在旧版本，而没有任何地方能看出来。
//
// 注意这个判据本身分不清两种成因：**本地改过** 与 **版本落后**（升级了但文件没被
// 覆盖）。所以它只回答"不一致"，文案也照这个说，不臆断成"你改过"。要真正区分，
// 需要在物化时记下基线哈希（交付说明里的 P1）；对用户而言两种情况的正确动作相同：
// 同步为内置版本。
func skillOutOfSync(dir, name string) bool {
	modified := false
	_ = fs.WalkDir(skill.Bundled, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || modified {
			return nil
		}
		want, rerr := skill.Bundled.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, name), "/")
		got, gerr := os.ReadFile(filepath.Join(dir, name, filepath.FromSlash(rel)))
		if gerr != nil || sha256.Sum256(got) != sha256.Sum256(want) {
			modified = true
			return fs.SkipAll
		}
		return nil
	})
	return modified
}

// restoreBuiltinSkill 把一个内置技能包恢复为出厂内容。
//
// 只覆盖 embed 里存在的文件：运维额外加的文件保留，不做删除——恢复出厂的语义是
// "回到出厂状态"，不是"清空这个目录"，后者会误伤本地补充的 references。
func restoreBuiltinSkill(name, dstRoot string) (int, error) {
	written := 0
	err := fs.WalkDir(skill.Bundled, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, name), "/")
		if rel == "" {
			return nil
		}
		if strings.Contains(rel, "..") {
			return nil // embed 路径理论上不含 ..，防御性跳过
		}
		target := filepath.Join(dstRoot, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		data, rerr := skill.Bundled.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if werr := os.WriteFile(target, data, 0o644); werr != nil {
			return werr
		}
		written++
		return nil
	})
	return written, err
}

// parseOriginFilter 解析 ?origin=builtin,distilled。空串表示不过滤。
func parseOriginFilter(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out[p] = true
		}
	}
	return out
}
