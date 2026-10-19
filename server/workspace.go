package server

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/fsutil"
	"github.com/Autumn-27/artex/stageerr"
)

// Workspace file manager — browse / view / edit / download / upload / delete the
// shared work dir (s.m.dir), where all agents write their artifacts. Every path is
// confined to the work dir root (traversal via ".." is neutralised). All routes sit
// behind requireAuth (see Handler()).

const (
	maxWorkspaceRead   = 2 << 20   // 2 MiB: files bigger than this aren't inlined for view/edit (download instead)
	maxWorkspaceUpload = 512 << 20 // 512 MiB per upload request
)

// 工作空间的用户可见错误使用中文；路径、payload、底层 err.Error() 保持原文。
const (
	errWsIllegalPath      = "路径无效"
	errWsPathNotFound     = "路径不存在"
	errWsNotDir           = "不是目录"
	errWsFileNotFound     = "文件不存在"
	errWsIsDir            = "这是一个目录，无法作为文件读取"
	errWsTargetIsDir      = "目标路径是一个目录"
	errWsCannotDeleteRoot = "无法删除工作区根目录"
	errWsUploadDirMissing = "目标目录不存在"
	errWsUploadParse      = "上传失败或超出大小限制： " // 后拼接 err.Error()。
	errWsNoUploadFile     = "没有要上传的文件（表单字段 file）"
)

// wsResolve maps a user-supplied relative path to an absolute path INSIDE the work
// dir. It returns ok=false if the path would escape the root. filepath.Clean on a
// rooted copy collapses any ".." so nothing can climb above the root.
func (s *Server) wsResolve(rel string) (string, bool) {
	base := filepath.Clean(s.m.dir)
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	clean := filepath.Clean("/" + rel) // e.g. "/a/../../etc" → "/etc" (still rooted at "/")
	abs := filepath.Clean(filepath.Join(base, clean))
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}

// wsRel renders an absolute path back as a workspace-relative path (forward slashes).
func (s *Server) wsRel(abs string) string {
	base := filepath.Clean(s.m.dir)
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

type wsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix millis

	// Files / Dirs 是目录的递归条目数（文件自身恒为 1 / 0），删除确认要用它告诉
	// 用户「这一下会带走多少个文件」。
	Files int `json:"files"`
	Dirs  int `json:"dirs"`
	// SizePartial 表示 Size 没量完（预算用尽或有条目读不到），界面据此显示「≥」。
	SizePartial bool `json:"size_partial,omitempty"`
}

// GET /api/workspace/list?path=<rel>
func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, errWsPathNotFound)
		return
	}
	if !fi.IsDir() {
		writeErr(w, 400, errWsNotDir)
		return
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]wsEntry, 0, len(ents))
	// 目录要递归量，先记下下标，整表建完再并发统计。
	dirIdx := make([]int, 0, len(ents))
	dirAbs := make([]string, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		ent := wsEntry{
			Name:  e.Name(),
			Path:  s.wsRel(filepath.Join(abs, e.Name())),
			Dir:   e.IsDir(),
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
			Files: 1,
		}
		if ent.Dir {
			// 目录项自己的 Size 只是目录记录（Windows 上恒为 0），真正的占用来自
			// 子树累计，这里先清零，稍后由 wsUsageFor 填回。
			ent.Size = 0
			ent.Files = 0
			dirIdx = append(dirIdx, len(out))
			dirAbs = append(dirAbs, filepath.Join(abs, e.Name()))
		}
		out = append(out, ent)
	}
	// 并发统计：深目录不至于把整张表拖住；wsUsageFor 内部带缓存，反复展开同一层
	// 不会重复走盘。
	sem := make(chan struct{}, wsMeasureWorkers)
	var wg sync.WaitGroup
	for i, idx := range dirIdx {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u := wsUsageFor(p)
			out[idx].Size = u.Size
			out[idx].Files = u.Files
			out[idx].Dirs = u.Dirs
			out[idx].SizePartial = u.Partial
		}(idx, dirAbs[i])
	}
	wg.Wait()

	// 目录排在文件前，两组分别按名称排序。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	// 当前目录的汇总：文件管理器头部展示「共多大、多少个文件」。
	var totalSize int64
	var totalFiles, totalDirs int
	totalPartial := false
	for _, e := range out {
		totalSize += e.Size
		totalFiles += e.Files
		if e.Dir {
			totalDirs++
			totalDirs += e.Dirs
		}
		if e.SizePartial {
			totalPartial = true
		}
	}

	writeJSON(w, 200, map[string]any{
		"path":          s.wsRel(abs),
		"entries":       out,
		"total_size":    totalSize,
		"total_files":   totalFiles,
		"total_dirs":    totalDirs,
		"total_partial": totalPartial,
	})
}

// GET /api/workspace/read?path=<rel> — inline text for view/edit. Binary or oversize
// files return {binary:true}/{too_large:true} with no content (use download instead).
func (s *Server) wsRead(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, errWsFileNotFound)
		return
	}
	if fi.IsDir() {
		writeErr(w, 400, errWsIsDir)
		return
	}
	if fi.Size() > maxWorkspaceRead {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "size": fi.Size(), "too_large": true, "binary": true})
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "size": fi.Size(), "binary": true})
		return
	}
	writeJSON(w, 200, map[string]any{"path": s.wsRel(abs), "size": fi.Size(), "binary": false, "content": string(data)})
}

// POST /api/workspace/write  {path, content} — create/overwrite a text file.
func (s *Server) wsWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	abs, ok := s.wsResolve(req.Path)
	if !ok || abs == filepath.Clean(s.m.dir) {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		writeErr(w, 400, errWsTargetIsDir)
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		writeErr(w, 500, stageerr.Wrap(stageerr.StagePersist, "创建工作区目录失败", err).Error())
		return
	}
	if err := fsutil.WriteFileAtomic(abs, []byte(req.Content), 0o644); err != nil {
		writeErr(w, 500, stageerr.Wrap(stageerr.StagePersist, "写入工作区文件失败", err).Error())
		return
	}
	// 大小变了，连同所有祖先目录的缓存一起作废。
	s.wsInvalidateUsage(abs)
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(abs)})
}

// POST /api/workspace/mkdir  {path}
func (s *Server) wsMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	abs, ok := s.wsResolve(req.Path)
	if !ok || abs == filepath.Clean(s.m.dir) {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.wsInvalidateUsage(abs)
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(abs)})
}

// DELETE /api/workspace/delete?path=<rel> — removes a file or a directory tree
// (confined to the work dir; the root itself can't be deleted).
func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	if abs == filepath.Clean(s.m.dir) {
		writeErr(w, 400, errWsCannotDeleteRoot)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, errWsPathNotFound)
		return
	}
	// 先量再删：删完就没有东西可统计了，而界面需要把这个数字回报给用户——存储
	// 敏感的用户正是靠它判断「这一下值不值」。
	usage := wsUsageFor(abs)
	// contents=1 是「清空但保留目录」：程序和 agent 都按目录名定位路径，整目录删掉
	// 会让它们下次写入失败；只想回收空间时应该走这条。
	if r.URL.Query().Get("contents") == "1" && fi.IsDir() {
		children, rerr := os.ReadDir(abs)
		if rerr != nil {
			writeErr(w, 500, rerr.Error())
			return
		}
		for _, c := range children {
			if rerr := os.RemoveAll(filepath.Join(abs, c.Name())); rerr != nil {
				writeErr(w, 500, rerr.Error())
				return
			}
		}
	} else if err := os.RemoveAll(abs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.wsInvalidateUsage(abs)
	writeJSON(w, 200, map[string]any{"ok": true, "freed": usage.Size})
}

// GET /api/workspace/backup?path=<rel> — 把文件或整棵目录打成 ZIP 流式下载。
//
// 删除是不可逆的，而工作区里放的是 agent 的产物（transcript、traffic、sessions…），
// 顺手删掉再想找回来是没有地方可找的。备份因此做成「就地打包、边打边传」：不落
// 临时文件、不占双份磁盘，删除确认里的「删除前先下载备份」就是调它。
//
// 目录备份保留相对结构（顶层目录名 + 子树），解压即可原样放回。符号链接不打包。
func (s *Server) wsBackup(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, errWsPathNotFound)
		return
	}
	base := filepath.Base(abs)
	stem := base
	if !fi.IsDir() {
		if ext := filepath.Ext(base); ext != "" && len(ext) < len(base) {
			stem = strings.TrimSuffix(base, ext)
		}
	}
	// 时间戳写进文件名：同一个目录备份多次不会互相覆盖。
	name := stem + "-backup-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition",
		"attachment; filename=\""+sanitizeFilename(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))

	zw := zip.NewWriter(w)
	defer zw.Close() //nolint:errcheck

	// add 把一个文件写进压缩包；单个文件读不出来只跳过，不中断整包。
	add := func(path, name string) {
		src, err := os.Open(path)
		if err != nil {
			return
		}
		defer src.Close() //nolint:errcheck
		info, err := src.Stat()
		if err != nil {
			return
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return
		}
		_, _ = io.Copy(dst, src)
	}

	if !fi.IsDir() {
		add(abs, base)
		return
	}
	root := filepath.Clean(abs)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if d.IsDir() {
			return nil // 空目录不单独建条目，解压时由文件路径补齐
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		add(p, filepath.ToSlash(filepath.Join(base, rel)))
		return nil
	})
}

// GET /api/workspace/download?path=<rel> — stream a file as an attachment.
func (s *Server) wsDownload(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		writeErr(w, 404, errWsFileNotFound)
		return
	}
	name := filepath.Base(abs)
	// RFC 5987 filename* keeps non-ASCII names intact; plain filename is the fallback.
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeFile(w, r, abs)
}

// POST /api/workspace/upload?path=<dir> — multipart form field "file" (one or more).
func (s *Server) wsUpload(w http.ResponseWriter, r *http.Request) {
	dirAbs, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, errWsIllegalPath)
		return
	}
	if fi, err := os.Stat(dirAbs); err != nil || !fi.IsDir() {
		writeErr(w, 400, errWsUploadDirMissing)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, errWsUploadParse+err.Error())
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, 400, errWsNoUploadFile)
		return
	}
	saved := 0
	for _, hdr := range files {
		name := filepath.Base(hdr.Filename) // strip any path component
		if name == "" || name == "." || name == ".." {
			continue
		}
		destAbs, okd := s.wsResolve(filepath.Join(s.wsRel(dirAbs), name))
		if !okd {
			continue
		}
		if err := saveUpload(hdr, destAbs); err != nil {
			writeErr(w, 500, stageerr.Wrap(stageerr.StagePersist, "写入上传文件失败", err).Error())
			return
		}
		saved++
	}
	if saved > 0 {
		s.wsInvalidateUsage(dirAbs)
	}
	writeJSON(w, 200, map[string]any{"uploaded": saved})
}

func saveUpload(hdr *multipart.FileHeader, dest string) error {
	src, err := hdr.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	// 原子落盘：同目录临时文件写完再 rename。上传中断/失败不会在目标路径留下半截
	// 文件，上传进行中其它读者（agent/文件管理器）也不会读到不完整内容。
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		discard()
		return err
	}
	// CreateTemp 用 0600；与 wsWrite 的最终权限保持一致。
	if err := tmp.Chmod(0o644); err != nil {
		discard()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// sanitizeFilename strips characters unsafe for a Content-Disposition filename token.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	return name
}
