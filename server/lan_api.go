package server

// 「系统 → 局域网连接」页的后端（页面在 web/src/app/(main)/system/lan）。
//
// 这个页面要回答的问题只有一个：手机现在该填什么、扫什么。
// 所以接口把答案一次算完交出去——监听地址、可用 URL、二维码、以及没开局域网时
// 需要用户自己敲的那两条命令，前端只负责排版。
//
// 这里刻意不提供「一键改监听地址」的写接口：换监听地址必须重启进程，
// 而静默重启一个正在跑任务的 ARTEX（还会连带中断 agent）比让用户复制一条命令危险得多。

import (
	"net/http"
	"runtime"
	"strings"

	"github.com/Autumn-27/artex/config"
)

// lanRoutes 注册局域网连接页用到的接口。
func (s *Server) lanRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/lan", s.lanInfo)
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
	})
}

// lanRestartCommand 返回「下次按局域网监听启动」的命令，各平台入口脚本不一样。
func lanRestartCommand() string {
	if runtime.GOOS == "windows" {
		return "start.bat -lan"
	}
	return "./start.sh -lan"
}

// lanFirewallCommand 返回放行监听端口的命令。
// Windows 之外没有统一的防火墙命令行（ufw / firewall-cmd 各家不同），留空让前端不显示。
func lanFirewallCommand(port string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return `netsh advfirewall firewall add rule name="ARTEX-REA" dir=in action=allow protocol=TCP localport=` + port
}
