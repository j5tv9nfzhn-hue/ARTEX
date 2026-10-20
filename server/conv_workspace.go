package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// 本文件是 Harness「会话工作根」的配套 API：
//
//	GET  /api/fs/list                             目录浏览（新建会话时选项目根）
//	GET  /api/conversations/{id}/changes          工作根的未提交变更（git）
//	POST /api/conversations/{id}/changes/revert   回滚单个文件
//
// 信任级说明：这些接口服务于「本机单用户」控制台，权限等同已登录管理员，与既有的
// Bash 工具、/api/workspace/* 一致。不做额外的目录白名单，但所有路径都先绝对化再
// 做越界校验，绝不因为参数拼接逃出工作根。

type fsEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size,omitempty"`
	MTime string `json:"mtime,omitempty"`
}

// fsRoots 返回选择器的起点（Windows 列盘符，其他平台是 /）。
func fsRoots() []map[string]string {
	var out []map[string]string
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			p := string(c) + ":\\"
			if _, err := os.Stat(p); err == nil {
				out = append(out, map[string]string{"label": string(c) + ":", "path": p})
			}
		}
		return out
	}
	return append(out, map[string]string{"label": "/", "path": "/"})
}

// pgFsList 列目录。path 为空时只返回起点（盘符/根），供选择器渲染第一屏。
func (s *Server) pgFsList(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		writeJSON(w, 200, map[string]any{"path": "", "parent": "", "roots": fsRoots(), "entries": []fsEntry{}})
		return
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		writeErr(w, 400, "无法解析路径: "+err.Error())
		return
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		writeErr(w, 404, "目录不存在或不可访问: "+abs)
		return
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]fsEntry, 0, len(ents))
	for _, e := range ents {
		item := fsEntry{Name: e.Name(), Dir: e.IsDir()}
		if info, ierr := e.Info(); ierr == nil && !e.IsDir() {
			item.Size = info.Size()
			item.MTime = info.ModTime().Format("2006-01-02 15:04")
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	writeJSON(w, 200, map[string]any{"path": abs, "parent": parent, "entries": out})
}

// ---- git ----

func gitCmd(ctx context.Context, root string, args ...string) (string, string, error) {
	all := append([]string{"-C", root, "--no-pager"}, args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// isGitRoot 判断工作根是否位于某个 git 仓库内（允许用户选仓库的子目录）。
func isGitRoot(ctx context.Context, root string) bool {
	out, _, err := gitCmd(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return false
	}
	topAbs, _ := filepath.Abs(top)
	rootAbs, _ := filepath.Abs(root)
	topAbs = strings.ToLower(filepath.ToSlash(topAbs))
	rootAbs = strings.ToLower(filepath.ToSlash(rootAbs))
	return rootAbs == topAbs || strings.HasPrefix(rootAbs, strings.TrimSuffix(topAbs, "/")+"/")
}

type convChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // M / A / D / R / ??
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
	TooLarge  bool   `json:"too_large,omitempty"`
}

// parsePorcelainZ 解析 `git status --porcelain=v1 -z`。
// -z 下的重命名是 "R  new\0old\0"，所以遇到 R/C 需要多吃掉一个 token。
func parsePorcelainZ(s string) []convChange {
	toks := strings.Split(s, "\x00")
	out := make([]convChange, 0, len(toks))
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 {
			continue
		}
		xy := t[:2]
		path := t[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			if i+1 < len(toks) {
				i++
			}
		}
		out = append(out, convChange{Path: filepath.ToSlash(path), Status: strings.TrimSpace(xy)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func countDiffLines(patch string) (add, del int) {
	for _, ln := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
			continue
		case strings.HasPrefix(ln, "+"):
			add++
		case strings.HasPrefix(ln, "-"):
			del++
		}
	}
	return
}

// pgConvChanges 返回会话工作根的未提交变更。非 git / 无工作根都返回 supported=false + 原因，
// 让前端能给出可读的空态，而不是假装"没有变更"。
func (s *Server) pgConvChanges(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	root := strings.TrimSpace(c.WorkspaceRoot)
	if root == "" {
		writeJSON(w, 200, map[string]any{
			"supported": false,
			"reason":    "该会话还没有工作根：在页头点「选择目录」挑一个项目目录后再看变更。",
			"files":     []convChange{},
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if !isGitRoot(ctx, root) {
		writeJSON(w, 200, map[string]any{
			"supported": false,
			"root":      root,
			"reason":    "工作根不是 git 仓库；当前只对 git 仓库提供变更追踪（非 git 项目暂不伪造 diff）。",
			"files":     []convChange{},
		})
		return
	}
	porcelain, errOut, err := gitCmd(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		writeErr(w, 500, "git status 失败: "+err.Error()+" "+strings.TrimSpace(errOut))
		return
	}
	files := parsePorcelainZ(porcelain)
	const maxPatch = 200 << 10
	for i := range files {
		f := &files[i]
		if f.Status == "??" {
			continue // 未跟踪的新文件不生成 diff（前端显示为「新文件」）
		}
		patch, _, derr := gitCmd(ctx, root, "diff", "HEAD", "--unified=3", "--", f.Path)
		if derr != nil {
			continue
		}
		if len(patch) > maxPatch {
			f.TooLarge = true
			patch = patch[:maxPatch]
		}
		f.Patch = patch
		f.Additions, f.Deletions = countDiffLines(patch)
	}
	writeJSON(w, 200, map[string]any{"supported": true, "root": root, "vcs": "git", "files": files})
}

// pgConvRevertChange 回滚单个已跟踪文件到 HEAD。
// 未跟踪文件一律拒绝：删除磁盘内容是用户的决定，不应该由一次点击代劳。
func (s *Server) pgConvRevertChange(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	root := strings.TrimSpace(c.WorkspaceRoot)
	if root == "" {
		writeErr(w, 400, "该会话没有工作根")
		return
	}
	rel := filepath.Clean(strings.ReplaceAll(strings.TrimSpace(req.Path), "\\", "/"))
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		writeErr(w, 400, "非法路径: "+req.Path)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	status, _, err := gitCmd(ctx, root, "status", "--porcelain=v1", "--", rel)
	if err != nil {
		writeErr(w, 500, "git status 失败: "+err.Error())
		return
	}
	if strings.HasPrefix(strings.TrimSpace(status), "??") {
		writeErr(w, 409, "这是未纳入 git 的新文件，回滚会删除磁盘内容；请手动删除以免误删。")
		return
	}
	if _, errOut, err := gitCmd(ctx, root, "checkout", "HEAD", "--", rel); err != nil {
		writeErr(w, 500, "回滚失败: "+err.Error()+" "+strings.TrimSpace(errOut))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": rel})
}
