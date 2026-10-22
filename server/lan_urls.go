package server

// 监听地址 → 「手机能填的地址」这一层换算放在 server 包里，
// 让两个调用点共用同一份实现：
//
//	cmd/artex/lan.go  启动横幅里直接打印
//	server/lan_api.go 「系统 → 局域网连接」页的 /api/lan
//
// 两处各写一遍迟早会不一致（横幅说能连、页面说连不上），所以只留一份。

import (
	"net"
	"strings"
)

// LanURLs 返回该监听地址在局域网里能被访问到的 URL 列表。
//
// 回环地址返回空（局域网里没有入口）；
// 域名/具体 IP 只回一条；
// 全接口（":8787" / "0.0.0.0:8787" / "[::]:8787"）则枚举所有非回环 IPv4。
func LanURLs(addr string) []string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return nil
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		if !ip.IsUnspecified() {
			return []string{"http://" + net.JoinHostPort(host, port)}
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			// 链路本地（169.254.x.x）不是可用地址，手机连不上来的可能性极高。
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			u := "http://" + net.JoinHostPort(ip4.String(), port)
			if seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// LanPort 从监听地址里取端口，取不到就用默认的 8787。
func LanPort(addr string) string {
	if _, port, err := net.SplitHostPort(strings.TrimSpace(addr)); err == nil && port != "" {
		return port
	}
	return "8787"
}
