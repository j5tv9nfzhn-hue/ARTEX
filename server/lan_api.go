package server

// 「系统 → 局域网连接」页的后端（页面在 web/src/app/(main)/system/lan）。
//
// 这个页面要回答的问题只有一个：手机现在该填什么、扫什么。所以 GET /api/lan
// 把答案一次算完交出去——监听地址、可用 URL、二维码，前端只负责排版。
//
// 另一半是「怎么把它变成能连」：POST /api/lan/enable 把 listen 落进 config.json，
// 然后让进程自己重启一次（复用「更新完成后重启」那条既有路径）。
// 用户不需要敲命令、也不需要知道 start.bat 在哪；start-lan.bat 只是给
// 「窗口都没开、想直接用局域网模式启动」的场景留的入口。
//
// 防火墙同理：先自己尝试放行（Windows 上 netsh），只有在没有管理员权限、
// 系统拒绝时才把命令原样摆到页面上，让用户带着管理员身份执行一次。

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
)

// lanRoutes 注册局域网连接页用到的接口。
func (s *Server) lanRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/lan", s.lanInfo)
	mux.HandleFunc("POST /api/lan/enable", s.lanEnable)
	mux.HandleFunc("POST /api/lan/disable", s.lanDisable)
}

// SetListenAddr 记下 main 里真正拿去 ListenAndServe 的地址
// （-addr / -lan / config.json / 环境变量压完之后的结果）。
func (s *Server) SetListenAddr(addr string) {
	s.cfgMu.Lock()
	s.listenAddr = addr
	s.cfgMu.Unlock()
}

// ListenAddr 返回当前监听地址；没设置过时退回配置文件里的值。
func (s *Server) ListenAddr() string {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.listenAddr != "" {
		return s.listenAddr
	}
	return config.ListenAddr()
}

// lanInfo 返回局域网连接页需要的全部信息。
func (s *Server) lanInfo(w http.ResponseWriter, r *http.Request) {
	addr := s.ListenAddr()
	urls := LanURLs(addr)
	if urls == nil {
		urls = []string{}
	}
	port := LanPort(addr)
	primary := ""
	if len(urls) > 0 {
		primary = urls[0]
	}
	// 二维码编不出来不该拖垮整个页面：地址那一段照样能用，图留空即可。
	qrSVG := ""
	if primary != "" {
		if svg, err := QRCodeSVG(primary); err == nil {
			qrSVG = svg
		}
	}
	writeJSON(w, 200, map[string]any{
		"version":       BuildVersion,
		"listen_addr":   addr,
		"port":          port,
		"loopback_only": len(urls) == 0,
		"urls":          urls,
		"primary_url":   primary,
		"password_set":  s.cachedPassHash() != "",
		"qr_svg":        qrSVG,
		"restart_cmd":   lanRestartCommand(),
		"firewall_cmd":  lanFirewallCommand(port),
		"mobile_hint":   strings.TrimPrefix(primary, "http://"),
		"env_override":  lanEnvOverride(),
	})
}

// lanEnable 把服务切到局域网监听：写配置 → 自己重启一次。
func (s *Server) lanEnable(w http.ResponseWriter, r *http.Request) {
	s.lanSetExposure(w, r, true)
}

// lanDisable 改回只听本机。
func (s *Server) lanDisable(w http.ResponseWriter, r *http.Request) {
	s.lanSetExposure(w, r, false)
}

// lanSetExposure 是 enable / disable 的共同实现。
//
// 端口沿用当前监听端口，只改绑定范围：用户想换端口是另一件事，
// 顺手改掉会让「刚才那个地址为什么打不开」变成新的困惑。
func (s *Server) lanSetExposure(w http.ResponseWriter, r *http.Request, lan bool) {
	var req struct {
		Lan *bool `json:"lan"`
	}
	// 空 body 也要能点：按 URL 决定方向，body 只是给以后留的口子。
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "请求格式不正确")
			return
		}
		if req.Lan != nil {
			lan = *req.Lan
		}
	}

	port := LanPort(s.ListenAddr())
	addr := "127.0.0.1:" + port
	if lan {
		addr = ":" + port
	}
	path, err := config.SetListen(addr)
	if err != nil {
		writeErr(w, 500, "写入配置失败："+err.Error())
		return
	}

	fw := "skipped"
	if lan {
		fw = lanEnsureFirewall(port)
	}

	// 环境变量压着监听地址时，写进配置不会生效：如实告诉页面，不要假装成功。
	envOverride := lanEnvOverride()
	if envOverride {
		writeJSON(w, 200, map[string]any{
			"ok": true, "listen": addr, "config": path,
			"restart": false, "firewall": fw, "env_override": true,
		})
		return
	}

	writeJSON(w, 200, map[string]any{
		"ok": true, "listen": addr, "config": path,
		"restart": true, "firewall": fw, "env_override": false,
	})

	// 先把响应写回浏览器再重启，否则页面只会看到连接被重置。
	// 400ms 足够一次本地响应落盘，又不至于让用户觉得卡住。
	go func() {
		time.Sleep(400 * time.Millisecond)
		requestRestart()
	}()
}

// lanEnvOverride 报告是否有环境变量压着监听地址（ARTEX_LISTEN）。
func lanEnvOverride() bool {
	return strings.TrimSpace(os.Getenv(config.ListenEnv)) != ""
}

// lanEnsureFirewall 尝试自动放行监听端口。
//
// 返回值：ok 已放行 / manual 系统拒绝（多半是没有管理员权限）/ skipped 该平台不处理。
// 失败不是错误：调用方会把命令摆到页面上让用户自己执行一次。
func lanEnsureFirewall(port string) string {
	if runtime.GOOS != "windows" {
		return "skipped"
	}
	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name=ARTEX-REA", "dir=in", "action=allow", "protocol=TCP", "localport="+port)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "manual"
	}
	return "ok"
}

// lanRestartCommand 返回「下次按局域网监听启动」的入口脚本。
func lanRestartCommand() string {
	if runtime.GOOS == "windows" {
		return "start-lan.bat"
	}
	return "./start-lan.sh"
}

// lanFirewallCommand 返回放行监听端口的命令。
// Windows 之外没有统一的防火墙命令行（ufw / firewall-cmd 各家不同），留空让前端不显示。
func lanFirewallCommand(port string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return `netsh advfirewall firewall add rule name="ARTEX-REA" dir=in action=allow protocol=TCP localport=` + port
}
