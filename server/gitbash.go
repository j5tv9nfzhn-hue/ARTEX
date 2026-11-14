package server

// Git Bash 设置：Windows 裸机上给工具调用层一个真正的 POSIX shell。
//
// 背景与 python_interpreter 同构，所以这里沿用它的三件套：库里存路径、
// 一个"检测"端点、以及"用户设置 > 自动探测 > 兜底"的解析顺序。差别在于解析
// 发生在 norma 执行层（actool），而它每条命令都重新解析一次，因此：
//
//   - 保存后无需重启、无需重建 agent —— 下一条工具调用即用新的 shell；
//   - 路径后来失效（换机器、便携版被移走、卸载 Git）时执行层自动降级，
//     不会把每个 Bash 调用打死；降级链与理由会原样回显到本设置页。
//
// 非 Windows 平台本设置不生效（norma 仍是 `bash -c`，逐字节保持原行为），
// 库里存着的路径也只作为记录保留，不参与解析。

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

const settingGitBashPath = "git_bash_path"

const (
	// 一次"检测"最多实测几个候选：每个候选要真的起一个进程，太多会把请求拖长。
	gitBashProbeMax = 4
	// 单次请求的整体预算（norma 内部每个探测还有自己的 8s 上限）。
	gitBashDetectBudget = 25 * time.Second
	// 浏览目录时只回显这些可执行文件名，避免这个端点变成通用文件浏览器。
	gitBashMaxEntries = 400
)

// shellApplicable 报告本平台的工具调用层是否受该设置影响。
func shellApplicable() bool { return runtime.GOOS == "windows" }

// gitBashPath 读已保存的路径；""=自动探测。
func (s *Server) gitBashPath() string {
	v, _, _ := s.m.pg.GetSetting(settingGitBashPath)
	return strings.TrimSpace(v)
}

// applyGitBash 把库里的路径推给执行层。开机调一次，保存后再调一次。
func (s *Server) applyGitBash() {
	actool.SetShellPath(s.gitBashPath())
}

// shellStatus 回显"当前到底在用哪个 shell、以及为什么"。执行层的实时解析结果
// 和库里的配置都给出，操作者才能看出配置是否真的生效（例如路径写错时会显示
// 实际降级到了哪个兜底档）。
func (s *Server) shellStatus() map[string]any {
	sh := actool.CurrentShell()
	return map[string]any{
		"applicable": shellApplicable(),
		"platform":   runtime.GOOS,
		"configured": s.gitBashPath(),
		"path":       sh.Path,
		"family":     string(sh.Family),
		"source":     sh.Source,
		"reason":     sh.Reason,
	}
}

// cleanGitBashPath 归一化输入：去掉从资源管理器"复制文件地址"带进来的引号与
// 首尾空白。空串（含只有引号）一律视作"清空=自动探测"。
func cleanGitBashPath(p string) string {
	return strings.Trim(strings.TrimSpace(p), `"'`)
}

// validateGitBashPath 校验手填/粘贴的路径。允许清空；允许指向一个当时还不可用
// 的路径吗——不允许，因为"保存了却静默不生效"比报错更难排查；真正的失效留给你
// 保存之后（便携版被移走等），那种情况执行层自己降级，这里也照常回显。
func validateGitBashPath(p string) (string, error) {
	p = cleanGitBashPath(p)
	if p == "" {
		return "", nil
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("必须是绝对路径，例如 C:\\Program Files\\Git\\bin\\bash.exe（当前：%q）", p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("路径不可用：%v", err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("需要指向 shell 可执行文件本身（bash.exe），不是它所在的目录")
	}
	return p, nil
}

// pgDetectGitBash 是"一键检测"。带 path 时实测那个路径（手动选择/粘贴后的验证），
// 不带时对自动发现的候选按顺序实测，第一个通过的即为结果。
//
// 检测必须真的执行一条命令：文件存在不等于能用——WSL 的 bash.exe 存根、损坏的便携
// 版、以及名字看着像但其实不是 POSIX shell 的文件都能通过存在性检查，却会在任务里
// 一路报错。探测语句同时验证 POSIX 语法（&& 与命令替换），正是 PowerShell 5.1 的短板。
func (s *Server) pgDetectGitBash(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	// 空 body / 无 body 都合法（就是"自动检测"）。
	if r.Body != nil {
		if err := decode(r, &req); err != nil && !strings.Contains(err.Error(), "EOF") {
			writeErr(w, 400, err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), gitBashDetectBudget)
	defer cancel()

	payload := map[string]any{
		"applicable": shellApplicable(),
		"platform":   runtime.GOOS,
		"configured": s.gitBashPath(),
		"effective":  s.shellStatus(),
		"candidates": []map[string]any{},
	}

	// 指定路径：先实测它。这一步与平台无关——手动粘贴的路径在 Linux 上也该能验证
	// （检测语句验的是"这个文件是不是可用的 POSIX shell"，本身就是平台中立的问题）。
	if p := cleanGitBashPath(req.Path); p != "" {
		res := actool.ProbeShell(ctx, p)
		payload["ok"] = res.OK
		payload["shell"] = probeShellDTO(res.Shell)
		payload["version"] = res.Version
		payload["output"] = res.Output
		if res.Error != "" {
			payload["error"] = res.Error
		}
		if res.OK {
			payload["detail"] = "该路径可用；保存后下一条工具调用即生效"
		} else {
			payload["detail"] = "该路径不可用；执行层会沿用降级链（自动探测 → pwsh → PowerShell）"
		}
		writeJSON(w, 200, payload)
		return
	}

	// 非 Windows：没有"选哪个 shell"的问题——norma 在这些平台仍是 `bash -c`，
	// 存着的路径只作为记录保留，不参与解析。照实说明并给出当前解析结果。
	if !shellApplicable() {
		payload["ok"] = true
		payload["detail"] = fmt.Sprintf("当前平台 %s 直接使用系统 bash，本设置不参与解析（保存的路径仅作记录）", runtime.GOOS)
		writeJSON(w, 200, payload)
		return
	}

	// 自动检测：按降级顺序实测。
	cands := actool.DetectShells()
	probed := 0
	firstErr := ""
	for _, c := range cands {
		if probed >= gitBashProbeMax {
			break
		}
		probed++
		res := actool.ProbeShell(ctx, c.Path)
		dto := probeShellDTO(c)
		dto["ok"] = res.OK
		dto["version"] = res.Version
		if res.Error != "" {
			dto["error"] = res.Error
		}
		payload["candidates"] = append(payload["candidates"].([]map[string]any), dto)
		if res.OK {
			payload["ok"] = true
			payload["shell"] = probeShellDTO(res.Shell)
			payload["path"] = c.Path
			payload["version"] = res.Version
			payload["detail"] = fmt.Sprintf("检测到可用的 POSIX shell（共发现 %d 个候选，实测第 %d 个通过）", len(cands), probed)
			writeJSON(w, 200, payload)
			return
		}
		if firstErr == "" {
			firstErr = res.Error
		}
	}

	// 一个 POSIX shell 都没找到：如实报告，并说明将走哪条兜底。
	eff := actool.CurrentShell()
	payload["ok"] = false
	payload["path"] = ""
	detail := "未检测到可用的 Git Bash / POSIX shell"
	if probed > 0 {
		detail = fmt.Sprintf("发现的 %d 个候选都不可用", probed)
	}
	if firstErr != "" {
		payload["error"] = firstErr
	}
	if eff.Source == "fallback" {
		detail += "；当前降级为 PowerShell（POSIX 命令会失败，建议安装 Git for Windows 或手动指定路径）"
	}
	payload["detail"] = detail
	writeJSON(w, 200, payload)
}

func probeShellDTO(sh actool.Shell) map[string]any {
	return map[string]any{
		"path":   sh.Path,
		"family": string(sh.Family),
		"source": sh.Source,
		"reason": sh.Reason,
	}
}

// gitBashBrowse 给"手动选择文件位置"提供受限的目录浏览。
//
// 边界（刻意的）：仅 Windows、只列目录与固定几个 shell 可执行文件名、不返回其它
// 文件名、不读内容、跳过条目级的重解析点、拒绝相对路径与 ".."、限条数。目录本身
// 若经 junction/symbolic link 指向别处，不拒绝而是回显 resolved 目标——Windows 上
// 连 "C:\Program Files" 都可能带重解析点，硬拒会让最常见目录浏览不了。
// 鉴权沿用全站 JWT（非 /api/auth/* 与 /api/health 都要令牌）。
func (s *Server) gitBashBrowse(w http.ResponseWriter, r *http.Request) {
	if !shellApplicable() {
		writeErr(w, 400, "仅 Windows 裸机支持浏览文件系统选择 shell 路径")
		return
	}
	dir := cleanGitBashPath(r.URL.Query().Get("dir"))
	if dir == "" {
		// 没给目录就返回盘符列表，前端从这里往下钻。标准库没有枚举卷的跨平台
		// API，这里对 A-Z 做一次性 stat（本端点仅在 Windows 生效，26 次 stat 可忽略）。
		roots := []string{}
		for l := 'C'; l <= 'Z'; l++ {
			cand := string(rune(l)) + `:\`
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				roots = append(roots, cand)
			}
		}
		if len(roots) == 0 {
			roots = append(roots, `C:\`)
		}
		writeJSON(w, 200, map[string]any{"dir": "", "parent": "", "roots": roots, "dirs": []string{}, "shells": []map[string]any{}, "resolved": ""})
		return
	}
	if !filepath.IsAbs(dir) || strings.Contains(dir, "..") {
		writeErr(w, 400, "只接受不含 .. 的绝对目录路径")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, 400, fmt.Sprintf("无法读取该目录：%v", err))
		return
	}
	// 符号链接/junction 指向别处时不拒绝（Windows 上 "C:\Program Files" 这类路径
	// 本身就可能带重解析点，硬拒会让最常用的目录浏览不了），只如实标注：
	// 调用方看到的是"这个入口指向哪"，而不是一个莫名的 400。
	resolvedNote := ""
	if real, e := filepath.EvalSymlinks(dir); e == nil {
		if r, c := slashPath(real), slashPath(filepath.Clean(dir)); !strings.EqualFold(r, c) {
			resolvedNote = r
		}
	}

	wanted := map[string]bool{
		"bash.exe": true, "sh.exe": true, "busybox.exe": true,
		"pwsh.exe": true, "powershell.exe": true,
	}
	dirs := []string{}
	shells := []map[string]any{}
	for _, e := range entries {
		if len(dirs)+len(shells) >= gitBashMaxEntries {
			break
		}
		name := e.Name()
		full := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
			continue // 跳过重解析点/特殊项
		}
		if e.IsDir() {
			dirs = append(dirs, name)
			continue
		}
		if wanted[strings.ToLower(name)] {
			shells = append(shells, map[string]any{"name": name, "path": full, "bytes": info.Size()})
		}
	}
	writeJSON(w, 200, map[string]any{
		"dir":      filepath.Clean(dir),
		"parent":   parentDir(dir),
		"dirs":     dirs,
		"shells":   shells,
		"resolved": resolvedNote, // 非空=本目录经符号链接/junction 指向这里
	})
}

// parentDir 返回上一级；已到盘符根时返回空串（前端据此禁用"返回上级"）。
func parentDir(dir string) string {
	p := filepath.Dir(filepath.Clean(dir))
	if strings.EqualFold(p, dir) || p == string(filepath.Separator) || p == "." {
		return ""
	}
	return p
}

func slashPath(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), "\\", "/")
}
