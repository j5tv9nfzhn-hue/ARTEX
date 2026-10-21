package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
)

// 本文件是「系统 → 存储与清理」页面的后端。
//
// 三条边界，决定了这里**不做什么**：
//  1. 只处理「可再生成 / 可重建」的东西：数据库空闲页、进程内存、活动流、孤儿行。
//     漏洞与资产是交付物，删除只能走任务的级联删除（那里有备份、有审计），
//     在这个页面上「一键清空 findings」等于把证据链半删——不做。
//  2. 所有写操作都是 POST + JSON，先 dry-run 预览再执行；执行要显式 confirm。
//  3. 删除动作一律人工触发。唯一的例外是「活动流自动保留」：它默认关闭，只有用户
//     在页面上显式设了天数，后台才按批兜底清理（server/activity_retention.go）。

func (s *Server) storageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/storage/stats", s.storageStats)
	mux.HandleFunc("POST /api/storage/compact", s.storageCompact)
	mux.HandleFunc("POST /api/storage/gc", s.storageGC)
	mux.HandleFunc("POST /api/storage/notify/clear", s.storageNotifyClear)
	mux.HandleFunc("POST /api/storage/orphans/purge", s.storageOrphansPurge)
	mux.HandleFunc("POST /api/storage/activity/purge", s.storageActivityPurge)
	mux.HandleFunc("POST /api/storage/activity/retention", s.storageActivityRetention)
}

func (s *Server) storageReady(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.m == nil || s.m.pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "数据库未就绪")
		return false
	}
	return true
}

// storageGuardJSON 复用 retention 的「application/json + 同源」守卫。
//
// 威胁模型完全一致：服务只监听本机端口，浏览器里任何一个网页都能对本机
// 发跨站 POST（不需要读响应）。没有这道守卫，`/api/storage/compact` 就是
// 一个「让别人的库锁三分钟」的按钮，`/api/storage/notify/clear` 则是
// 「清掉别人的通知」的按钮。守卫逻辑（Origin 白名单 / 全环回放行）已在
// retention.go 里写死并测过，这里只复用，不复制。
func storageGuardJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeRetentionJSON(w, r, dst)
}

// --- 统计 ---------------------------------------------------------------------

func (s *Server) storageStats(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"db":                 storageDBStats(ctx, s.m.pg),
		"mem":                storageMemStats(s.m),
		"memory_db":          storageMemoryDBStats(ctx, s.m.pg),
		"activity_retention": s.activityRetentionInfo(),
	})
}

func storageFileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0
	}
	return st.Size()
}

func storagePragmaInt(ctx context.Context, d *db.DB, name string) int64 {
	var v int64
	if err := d.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0
	}
	return v
}

func storagePragmaText(ctx context.Context, d *db.DB, name string) string {
	var v string
	if err := d.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return ""
	}
	return v
}

// storageAutoVacuumLabel 把 pragma 整数翻成人能读的模式名。
func storageAutoVacuumLabel(v int64) string {
	switch v {
	case 1:
		return "FULL"
	case 2:
		return "INCREMENTAL"
	default:
		return "NONE"
	}
}

// storageSafeIdent 只放行 SQLite 标识符里可能出现的字符。表名来自
// sqlite_master（不是用户输入），但拼进 SQL 前仍然过一遍白名单——
// 一个带引号的表名就能让 COUNT 变成注入点。
func storageSafeIdent(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func storageDBStats(ctx context.Context, d *db.DB) map[string]any {
	path := config.SQLitePath()
	pageSize := storagePragmaInt(ctx, d, "page_size")
	pageCount := storagePragmaInt(ctx, d, "page_count")
	free := storagePragmaInt(ctx, d, "freelist_count")
	out := map[string]any{
		"path":         path,
		"size":         storageFileSize(path),
		"wal_size":     storageFileSize(path + "-wal"),
		"shm_size":     storageFileSize(path + "-shm"),
		"page_size":    pageSize,
		"page_count":   pageCount,
		"freelist":     free,
		"free_bytes":   free * pageSize,
		"used_bytes":   (pageCount - free) * pageSize,
		"auto_vacuum":  storageAutoVacuumLabel(storagePragmaInt(ctx, d, "auto_vacuum")),
		"journal_mode": storagePragmaText(ctx, d, "journal_mode"),
	}
	tables, partial := storageTableRows(ctx, d, 1500*time.Millisecond)
	out["tables"] = tables
	out["tables_partial"] = partial
	out["tables_total"] = len(tables)
	return out
}

// storageTableRows 逐表 COUNT(*)。66 张表在 20 MB 级的库上是毫秒级，但库可能
// 被外部工具撑到 GB 级，所以带 1.5 秒预算：超时就返回已量到的部分并打 partial，
// 绝不为了一个概览数字把请求拖死。
func storageTableRows(ctx context.Context, d *db.DB, budget time.Duration) ([]map[string]any, bool) {
	rows, err := d.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return []map[string]any{}, false
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && storageSafeIdent(n) {
			names = append(names, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return []map[string]any{}, false
	}

	out := make([]map[string]any, 0, len(names))
	partial := false
	deadline := time.Now().Add(budget)
	for _, name := range names {
		if time.Now().After(deadline) {
			partial = true
			break
		}
		var n int64
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+name+`"`).Scan(&n); err != nil {
			continue
		}
		out = append(out, map[string]any{"name": name, "rows": n})
	}
	sort.Slice(out, func(i, j int) bool {
		ri, _ := out[i]["rows"].(int64)
		rj, _ := out[j]["rows"].(int64)
		if ri != rj {
			return ri > rj
		}
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out, partial
}

// storageTransportCount 统计自定义工具连接池里缓存的 transport 数量。
func storageTransportCount() int {
	n := 0
	customHTTPTransportPool.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

func storageMemStats(m *Manager) map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	taskHandles := 0
	if m != nil {
		taskHandles = m.InMemoryTaskCount()
	}
	return map[string]any{
		"heap_alloc":      ms.HeapAlloc,
		"heap_inuse":      ms.HeapInuse,
		"heap_sys":        ms.HeapSys,
		"stack_inuse":     ms.StackInuse,
		"sys":             ms.Sys,
		"num_gc":          ms.NumGC,
		"last_gc_unix_ms": int64(ms.LastGC / 1e6),
		"goroutines":      runtime.NumGoroutine(),
		"notify_ring":     uiNotifyHub.size(),
		"notify_ring_cap": uiNotifyRingCap,
		"task_handles":    taskHandles,
		"http_transports": storageTransportCount(),
	}
}

func storageCount(ctx context.Context, d *db.DB, query string) int64 {
	var n int64
	// 统计是纯读：走只读池（query_only），不会和写事务抢同一批连接。
	if err := d.ReadDB().QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0
	}
	return n
}

// 孤儿 = 父行没了、自己还在。三类都是纯粹的历史垃圾：探索被删后残留的
// 图节点/边、以及挂在已不存在的探索上的活动流。
const (
	storageOrphanNodesSQL    = `SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id NOT IN (SELECT id FROM explorations)`
	storageOrphanEdgesSQL    = `SELECT COUNT(*) FROM exploration_edges WHERE exploration_id NOT IN (SELECT id FROM explorations) OR src_id NOT IN (SELECT id FROM exploration_nodes) OR dst_id NOT IN (SELECT id FROM exploration_nodes)`
	storageOrphanActivitySQL = `SELECT COUNT(*) FROM activity WHERE exploration_id IS NOT NULL AND exploration_id NOT IN (SELECT id FROM explorations)`
)

func storageMemoryDBStats(ctx context.Context, d *db.DB) map[string]any {
	return map[string]any{
		"facts":              storageCount(ctx, d, `SELECT COUNT(*) FROM exploration_nodes`),
		"edges":              storageCount(ctx, d, `SELECT COUNT(*) FROM exploration_edges`),
		"activity":           storageCount(ctx, d, `SELECT COUNT(*) FROM activity`),
		"findings":           storageCount(ctx, d, `SELECT COUNT(*) FROM findings`),
		"assets":             storageCount(ctx, d, `SELECT COUNT(*) FROM assets`),
		"links":              storageCount(ctx, d, `SELECT COUNT(*) FROM task_asset_links`),
		"orphan_facts":       storageCount(ctx, d, storageOrphanNodesSQL),
		"orphan_edges":       storageCount(ctx, d, storageOrphanEdgesSQL),
		"orphan_activity":    storageCount(ctx, d, storageOrphanActivitySQL),
		"activity_older_30d": storageCount(ctx, d, `SELECT COUNT(*) FROM activity WHERE datetime(created_at) < datetime('now','-30 days')`),
		// 单写协程的观测值：平均批量、平均等待、回滚次数。存储页直接展示，
		// 用来回答「批量提交到底省了多少」以及「有没有在悄悄回滚」。
		"write_queue": d.WriteStats(),
	}
}

// InMemoryTaskCount 返回 Manager 内存里持有的任务句柄数。只读、加锁，
// 只服务于存储页展示，不参与调度判断。
func (m *Manager) InMemoryTaskCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}

// size / clear 是通知 ring 的两个维护入口。
//
// clear 只清空 ring，**不回退 seq**：seq 是 EventSource 的 Last-Event-ID 游标，
// 一旦回退，断线重连的客户端会以为自己「已经看过更新的 id」，从而永远收不到
// 之后的事件。清空只是不再支持断线补洞，新事件仍然从更大的 id 继续。
func (h *uiNotifyHubT) size() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ring)
}

func (h *uiNotifyHubT) clear() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.ring)
	h.ring = nil
	return n
}

// --- 压缩 ---------------------------------------------------------------------

func (s *Server) storageCompact(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w) {
		return
	}
	var req struct{}
	if !storageGuardJSON(w, r, &req) {
		return
	}
	path := config.SQLitePath()
	before := storageFileSize(path)
	// VACUUM 会重写整个文件：库越大越久，且全程持有写锁。给 3 分钟，
	// 前端在按钮上明确写了「期间任务写入会排队」。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	if err := s.m.pg.CompactStorage(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "压缩失败："+err.Error())
		return
	}
	after := storageFileSize(path)
	freed := before - after
	if freed < 0 {
		freed = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"before":      before,
		"after":       after,
		"freed":       freed,
		"duration_ms": time.Since(start).Milliseconds(),
		"wal_size":    storageFileSize(path + "-wal"),
	})
}

func (s *Server) storageGC(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !storageGuardJSON(w, r, &req) {
		return
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	debug.FreeOSMemory()
	runtime.ReadMemStats(&after)
	writeJSON(w, http.StatusOK, map[string]any{
		"heap_before": before.HeapAlloc,
		"heap_after":  after.HeapAlloc,
		"sys_before":  before.Sys,
		"sys_after":   after.Sys,
		"goroutines":  runtime.NumGoroutine(),
	})
}

func (s *Server) storageNotifyClear(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !storageGuardJSON(w, r, &req) {
		return
	}
	cleared := uiNotifyHub.clear()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
}

// --- 记忆库清理 ----------------------------------------------------------------

func (s *Server) storageOrphansPurge(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w) {
		return
	}
	var req struct {
		DryRun  bool `json:"dry_run"`
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	counts := map[string]int64{
		"facts":    storageCount(ctx, s.m.pg, storageOrphanNodesSQL),
		"edges":    storageCount(ctx, s.m.pg, storageOrphanEdgesSQL),
		"activity": storageCount(ctx, s.m.pg, storageOrphanActivitySQL),
	}
	if req.DryRun || !req.Confirm {
		writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "deleted": false, "dry_run": true})
		return
	}
	path := config.SQLitePath()
	before := storageFileSize(path)
	// 顺序有讲究：**先删边，再删节点**。反过来，删节点会让更多边变成悬空，
	// 于是「预览 3 条、实删 9 条」——预览骗人。边在前，两者谓词同一时刻成立，
	// 预览数与实删数才对得上。
	deleted := map[string]int64{}
	for _, step := range []struct {
		key string
		sql string
	}{
		{"edges", `DELETE FROM exploration_edges WHERE exploration_id NOT IN (SELECT id FROM explorations) OR src_id NOT IN (SELECT id FROM exploration_nodes) OR dst_id NOT IN (SELECT id FROM exploration_nodes)`},
		{"facts", `DELETE FROM exploration_nodes WHERE exploration_id NOT IN (SELECT id FROM explorations)`},
		{"activity", `DELETE FROM activity WHERE exploration_id IS NOT NULL AND exploration_id NOT IN (SELECT id FROM explorations)`},
	} {
		res, err := s.m.pg.ExecContext(ctx, step.sql)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "清理失败："+err.Error())
			return
		}
		n, _ := res.RowsAffected()
		deleted[step.key] = n
	}
	// 删完立刻做一次有界回收（3 秒预算、失败只记日志），让文件大小当场收敛。
	s.m.pg.ReclaimFreePages()
	after := storageFileSize(path)
	freed := before - after
	if freed < 0 {
		freed = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "rows": deleted, "freed": freed})
}

func (s *Server) storageActivityPurge(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w) {
		return
	}
	var req struct {
		DryRun  bool `json:"dry_run"`
		Confirm bool `json:"confirm"`
		Days    int  `json:"days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败")
		return
	}
	days := req.Days
	if days < 1 {
		days = 30
	}
	if days > 3650 {
		days = 3650
	}
	// 用 SQLite 自己的 datetime() 做比较，避免 Go 时间格式与库里 TEXT 格式
	// 之间的时区/精度差异（_time_format 是钉死的，但历史行的写入路径不止一条）。
	modifier := "-" + strconv.Itoa(days) + " days"
	const countQ = `SELECT COUNT(*) FROM activity WHERE datetime(created_at) < datetime('now', ?)`
	var n int64
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.m.pg.QueryRowContext(ctx, countQ, modifier).Scan(&n); err != nil {
		writeErr(w, http.StatusInternalServerError, "统计失败："+err.Error())
		return
	}
	if req.DryRun || !req.Confirm {
		writeJSON(w, http.StatusOK, map[string]any{"days": days, "rows": n, "deleted": false})
		return
	}
	path := config.SQLitePath()
	before := storageFileSize(path)
	res, err := s.m.pg.ExecContext(ctx, `DELETE FROM activity WHERE datetime(created_at) < datetime('now', ?)`, modifier)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "清理失败："+err.Error())
		return
	}
	deleted, _ := res.RowsAffected()
	s.m.pg.ReclaimFreePages()
	after := storageFileSize(path)
	freed := before - after
	if freed < 0 {
		freed = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "deleted": true, "rows": deleted, "freed": freed})
}
