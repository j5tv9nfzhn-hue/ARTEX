package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// 一键更新 HTTP 层仅负责认证、互斥、进度广播和通知 main 退出。
// 下载、校验、换装由 selfupdate 完成；暂存后以 ExitRestart 退出，
// 由 start.sh、start.bat 或 Docker ENTRYPOINT 重启，进程不自行重启。
// update-card 直接渲染 SSE progress.message、reason 与 writeErr 正文，故使用中文。
const (
	updateMsgPreparing          = "准备中…"
	updateMsgFailed             = "更新失败"
	updateMsgStaged             = "新版本已准备就绪，正在重启…"
	updateErrNotReleaseFmt      = "当前版本 %q 不是正式发布版本，无法使用一键更新"
	updateErrAlreadyLatestFmt   = "已是最新版本 (%s)"
	updateErrInProgress         = "已有更新正在进行"
	updateErrRollbackInProgress = "正在更新，无法回滚"
	// 换装要重启进程，重启由 start.sh / start.bat 的守护循环完成。
	// 没有守护者时点"立即更新"= 下载成功后进程退出再没人拉起 = 直接停服，
	// 所以这里必须拦在服务端，而不是只在前端把按钮置灰。
	updateErrUnsupervised   = "当前进程不是由 start.sh / start.bat 启动，换装后没有人把它重新拉起，会直接停服。请改用启动脚本运行，或手动停止服务后用脚本重启完成升级。"
	updateErrDockerOneClick = "容器内不支持一键换装（只改容器可写层，重建容器即回退），请拉取新镜像后重建容器。"
)

// 更新偏好的设置键。两个开关都存在 settings 表里（而不是浏览器 localStorage）：
// 自动检测跑在服务端，多个浏览器、多个客户端看到的是同一份状态。
const (
	settingUpdateAutoCheck = "update_auto_check" // 默认开：只查新版本，不改任何东西
	settingUpdateAutoApply = "update_auto_apply" // 默认关：查到后自动下载并暂存
)

const (
	// 启动后先等一会儿再查：别和首屏请求抢代理/带宽，也让"刚上线就发现新版"
	// 这种噪声晚一点出现。
	autoCheckFirstDelay = 90 * time.Second
	// 自动检测间隔。清单通道走网页别名，不计入 API 配额，所以 6 小时一次几乎没有成本。
	autoCheckInterval = 6 * time.Hour
)

// restartCh 在升级就绪或回滚完成后关闭，main 收到后以 ExitRestart 退出。
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested 返回一个在"请退出并让守护进程重新拉起我"时关闭的 channel。
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState 是本次启动时 selfupdate.Bootstrap 的结论（升级成功 / 刚回滚 /
// 暂存件被丢弃），由 main 注入，供 /api/update/check 如实告诉前端上一次升级的下场。
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState 由 main 在启动时调用一次。
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache 缓存 GitHub 的最新版本查询结果。
//
// 顶栏的"有新版本"提示会在每次整页加载时查一次，而未认证的 GitHub API 是
// 每 IP 每小时 60 次——不缓存的话，多开几个标签页或刷几次页面就把配额耗光了，
// 之后真想更新时反而查不动。用户显式点"检查更新"时可以 force 绕过缓存。
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch 是取数函数，仅为测试留的注入点；为 nil 时走真正的 GitHub 查询。
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// 失败结果也缓存一小会儿，否则 GitHub 不可达时每次页面加载都要干等一次超时；
	// 但 TTL 要短，网络恢复后很快就能自己好。
	releaseErrTTL = 2 * time.Minute
	// 查询用的超时。NewClient 的 30 分钟超时是给下载整包用的，查版本不能等那么久。
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get 返回最新 Release，命中缓存则不访问网络。
//
// 取数期间一直持有锁：并发请求会排队等同一次查询的结果，而不是各自去打 GitHub
// （页面刚加载时多个标签页同时来查，正是最容易触发限流的时刻）。
// snapshot 只读缓存，不触发任何网络请求（顶栏红点用）。
func (c *releaseCache) snapshot() (*selfupdate.Release, error, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rel, c.err, c.at
}

func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatestWithFallback
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// 请求被取消（用户关了标签页）不代表 GitHub 有问题，别把它写进缓存，
	// 否则下一个访客会拿到一条莫名其妙的"已取消"错误。
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress 是推给前端的一条进度。
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // 仅下载阶段有意义；其余为 -1
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub 持有一次升级的进度并广播给 SSE 订阅者。
//
// running 同时充当互斥：升级期间再次 POST /api/update/apply 直接 409，
// 避免两个 goroutine 同时往同一个 artex.new 写。
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin 抢占升级权限，已在进行中则返回 false。
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: updateMsgPreparing, Version: version}
	h.fanout(h.cur)
	return true
}

// finish 结束一次升级。err 为 nil 表示暂存成功，等待重启。
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: updateMsgFailed, Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: updateMsgStaged, Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout 必须在持有 h.mu 时调用。订阅者 channel 是有缓冲的，满了就丢——
// 进度是可丢弃的瞬时信息，绝不能让一个卡住的 SSE 连接阻塞升级本身。
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck 查询 GitHub 上的最新正式版并与当前版本比较。
//
// 前端也会直连 api.github.com（GitHub 的 CORS 是 *），但**以本接口为准**：
// 下载是后端做的，只有后端能访问 GitHub 才谈得上更新。浏览器能连、服务器连不上
// 的情况很常见（服务器在内网、或代理只配在浏览器上），那时点更新必然失败，
// 不如在检查这一步就如实报错。
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
		// 自动更新只在"有守护脚本"的部署里生效：没有守护者时换装后没人拉起进程。
		"supervised": selfupdate.Supervised(),
		// 已下载待重启的版本；非空时前端应提示"重启后生效"，而不是再让用户点更新。
		"staged_version": selfupdate.StagedVersion(),
		"auto_check":     s.updateAutoCheck(),
		"auto_apply":     s.updateAutoApply(),
	}

	// 把落盘的升级台账一并回传：它是"上一次升级到底怎么样了"的唯一权威来源。
	// 内存里的 bootState 与 SSE 都会随进程退出消失，台账不会。
	if snap, jerr := selfupdate.JournalSnapshot(); jerr == nil {
		out["journal_state"] = snap["state"]
		out["staged_path"] = snap["staged_path"]
		out["attempts"] = snap["attempts"]
		if st, _ := snap["state"].(string); st == string(selfupdate.StateFailed) ||
			st == string(selfupdate.StateRolledBack) {
			if msg, _ := snap["last_error"].(string); msg != "" {
				out["boot_notice"] = msg
				out["rolled_back"] = st == string(selfupdate.StateRolledBack)
			}
		}
		if ptr, ok := snap["pointer"].(map[string]any); ok {
			out["pointer"] = ptr
		}
	}

	// 顶栏提示走缓存（默认）；用户点"检查更新"时带 force=1 强制回源。
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// 开发构建（dev / git describe 带后缀）没有可比较的版本号。放行只会
		// 用正式版覆盖掉本地正在调试的二进制，所以直接不给更新。
		out["reason"] = fmt.Sprintf(updateErrNotReleaseFmt, current)
	}
	writeJSON(w, 200, out)
}

// updateApply 下载并暂存新版本，完成后让进程退出交给守护脚本重启。
//
// 立刻返回 202，实际工作在后台 goroutine 上跑：整包下载可能要几分钟，
// 挂在请求上会被反代超时掐断。进度走 /api/update/stream。
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// 能力门控（服务端强制，不依赖前端置灰）：
	// 换装之后进程要以 ExitRestart 退出，由守护脚本重新拉起；容器内换装只作用于
	// 容器可写层。两者都不该走到下载，否则用户会得到"更新成功但服务没了"。
	if !selfupdate.Supervised() {
		writeErr(w, 400, updateErrUnsupervised)
		return
	}
	if selfupdate.InDocker() {
		writeErr(w, 400, updateErrDockerOneClick)
		return
	}

	// 走缓存：确保装上的就是用户在界面上看到并确认的那个版本。
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf(updateErrNotReleaseFmt, current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf(updateErrAlreadyLatestFmt, current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, updateErrInProgress)
		return
	}

	// 自动更新可能已经把它下好并校验过了：直接重启生效，不重复下载。
	if stagedMatches(rel) {
		updHub.finish(nil)
		go func() {
			time.Sleep(1200 * time.Millisecond)
			requestRestart()
		}()
		writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName, "staged": true})
		return
	}

	go s.stageUpdate(client, rel, current, true)

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback 主动退回上一版本（换装前备份的 artex.old）。
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	// 先答"正在更新中"这个更具体的拒绝，再谈能力门控：
	// 前者是此刻的事实，后者是部署形态，混在一起的顺序会让真正的原因被盖掉。
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, updateErrRollbackInProgress)
		return
	}
	// 回滚同样是"换装 + 退出 75"，前提条件与一键更新完全一致。
	if !selfupdate.Supervised() {
		writeErr(w, 400, updateErrUnsupervised)
		return
	}
	if selfupdate.InDocker() {
		writeErr(w, 400, updateErrDockerOneClick)
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] 已手动回滚到上一版本，即将退出以完成切换")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// stageUpdate 下载 → 校验 SHA256 → 暂存。
//
// restart=true：用户点了「立即更新」，暂存完就退出换装（由守护脚本拉起新版本）。
// restart=false：自动更新，只把包下好、校验好放在磁盘上，换装留到下次启动——
// 渗透任务可能正跑到一半，自动更新绝不能替用户决定"现在就重启"。
func (s *Server) stageUpdate(client *http.Client, rel *selfupdate.Release, current string, restart bool) {
	// 刻意用 s.ctx 而不是某次请求的 ctx：HTTP 响应一返回请求就结束了，
	// 挂在它上面下载会立刻被取消。
	err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
		updHub.publish(ph, pct, msg)
	})
	if err == nil {
		// 暂存成功后立刻安装：把核心放进 versions/<ver>/ 并原子切换 pointer.json。
		// 全程不替换任何正在运行的文件，所以既不需要"谁来换装"这个角色，
		// 也不会遇到 Windows 上"运行中的 exe 不能覆盖/删除"的锁问题。
		err = selfupdate.Activate()
	}
	updHub.finish(err)
	if err != nil {
		log.Printf("[update] 更新失败：%v", err)
		return
	}
	if !restart {
		log.Printf("[update] %s → %s 已下载并校验完成，下次启动生效", current, rel.TagName)
		return
	}
	log.Printf("[update] %s → %s 已暂存，即将退出以完成换装", current, rel.TagName)
	// 留一点时间把最后一条进度推给前端，再触发退出。
	time.Sleep(1500 * time.Millisecond)
	requestRestart()
}

// stagedMatches 报告磁盘上的暂存件是不是 rel 这个版本。
//
// 注意两边的版本号写法不同：marker 里存的是去掉 v 的版本号（selfupdate.Stage
// 写入 "0.3.16"），而 Release 的 tag 是 "v0.3.16"。直接比较会永远不相等，
// 结果就是自动更新下好的包被「立即更新」重新下载一遍。
func stagedMatches(rel *selfupdate.Release) bool {
	if rel == nil {
		return false
	}
	return strings.TrimPrefix(rel.TagName, "v") == strings.TrimPrefix(selfupdate.StagedVersion(), "v")
}

// updateAutoCheck 读「自动检测新版本」开关（默认开）。
func (s *Server) updateAutoCheck() bool {
	return s.m.pg.GetBool(settingUpdateAutoCheck, true)
}

// updateAutoApply 读「自动更新」开关（默认关）。
func (s *Server) updateAutoApply() bool {
	return s.m.pg.GetBool(settingUpdateAutoApply, false)
}

// startUpdateWatcher 起自动检测循环。开关每拍现读，所以在设置页关掉之后，
// 下一拍就不再访问网络——不需要重启服务。
func (s *Server) startUpdateWatcher() {
	go func() {
		// sleepCtx 返回 true = 等待期间服务已退出，直接收摊。
		if sleepCtx(s.ctx, autoCheckFirstDelay) {
			return
		}
		ticker := time.NewTicker(autoCheckInterval)
		defer ticker.Stop()
		for {
			s.updateTick()
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// 注：sleepCtx 由 engine.go 提供（返回 true = 等待期间 ctx 被取消）。
// updateTick 是一次「自动检测」。检测本身只读远端版本号，不改本地任何东西。
func (s *Server) updateTick() {
	if !s.updateAutoCheck() {
		return
	}
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(s.ctx, client, true)
	if err != nil {
		log.Printf("[update] 自动检测失败：%v", err)
		return
	}
	if rel == nil {
		return
	}
	current := BuildVersion
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		// 开发构建（dev / git describe 带后缀）不参与自动更新，避免把调试中的
		// 二进制换成正式版。手动检查同样会被 updateCheck 拒掉。
		return
	}
	if cmp >= 0 {
		return
	}
	asset := selfupdate.AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	if _, ok := rel.FindAsset(asset); !ok {
		log.Printf("[update] 新版本 %s 未提供 %s/%s 的安装包，跳过自动更新", rel.TagName, runtime.GOOS, runtime.GOARCH)
		return
	}
	log.Printf("[update] 检测到新版本 %s（当前 %s）", rel.TagName, current)
	if !s.updateAutoApply() {
		return
	}
	if selfupdate.InDocker() {
		// 容器里换装只作用于可写层，重建容器就退回镜像版本，自动下载没有意义。
		return
	}
	if stagedMatches(rel) {
		return
	}
	if !updHub.begin(rel.TagName) {
		return
	}
	go s.stageUpdate(client, rel, current, false)
}

// updateStatus 只读缓存，绝不发网络请求。
//
// 顶栏红点用它：红点必须便宜、离线可用，而且不能因为界面上挂了个红点，就在
// 背后替用户去问 GitHub——自动检测关掉时，这里永远返回"还没查过"。
func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	rel, err, at := relCache.snapshot()
	out := map[string]any{
		"current":        BuildVersion,
		"staged_version": selfupdate.StagedVersion(),
	}
	if at.IsZero() {
		out["checked"] = false
		writeJSON(w, 200, out)
		return
	}
	out["checked"] = true
	out["checked_at"] = at.Format(time.RFC3339)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}
	if rel == nil {
		out["checked"] = false
		writeJSON(w, 200, out)
		return
	}
	out["latest"] = rel.TagName
	cmp, comparable := selfupdate.CompareVersions(BuildVersion, rel.TagName)
	out["has_update"] = comparable && cmp < 0
	writeJSON(w, 200, out)
}

// updateStream 以 SSE 推送更新进度。
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// 先补一条当前状态，页面刷新后能立刻看到进行中的升级。
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// updateJournal 只读 upgrade 台账与版本指针。
//
// 前端在"正在重启"超过看门狗时限后拉它：失败原因因此能摆到用户面前，
// 而不是让页面永远转圈（旧实现只有 SSE + 无限健康轮询，SSE 一断就再无信息）。
func (s *Server) updateJournal(w http.ResponseWriter, r *http.Request) {
	snap, err := selfupdate.JournalSnapshot()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, snap)
}
