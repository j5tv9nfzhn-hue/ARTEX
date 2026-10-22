package main

import (
	"fmt"
	"net"
	"runtime"
	"strings"

	"github.com/Autumn-27/artex/config"
)

// 手机端（App）只能通过局域网访问服务，而服务默认只监听回环地址。
// 这个文件负责三件事：
//
//	1. 把“监听地址”的四层优先级（-addr > -lan > 环境变量/配置文件 > 回环默认）落到一个函数里；
//	2. 列出该地址在局域网里对应的 URL，直接打在启动横幅里，省得用户去 ipconfig 里抄地址；
//	3. 当监听仍在回环上时，明确告诉用户“手机连不上”以及怎么开。

// permitLanAddr 是 -lan 开关使用的地址：监听全部网卡（IPv4+IPv6）。
const permitLanAddr = ":8787"

// resolveListenAddr 把四层优先级压成最终监听地址。
//
//	-addr 显式传参 > -lan 开关 > config.json/env(ARTEX_LISTEN) > 127.0.0.1:8787
//
// 之所以把命令行放在最前面：临时开一次局域网不应该被配置文件里的旧值覆盖。
func resolveListenAddr(flagAddr string, lan bool) string {
	if v := strings.TrimSpace(flagAddr); v != "" {
		return v
	}
	if lan {
		return permitLanAddr
	}
	return config.ListenAddr()
}

// lanURLs 返回该监听地址在局域网里可访问的 URL。
//
// 回环地址返回空（本机之外没有入口）；
// 明确绑了某个网卡地址就只报它；
// 全接口（":8787" / "0.0.0.0:8787" / "[::]:8787"）则枚举网卡上的 IPv4 地址。
func lanURLs(addr string) []string {
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
			// 链路本地（169.254.x.x）不是局域网地址，填进 App 只会让人疑惑。
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

// printLanHint 把“手机该填什么”直接打在启动横幅下面。
// 不放进 printBanner：横幅本身有单测（TestPrintBannerLocalized）锁着格式。
func printLanHint(addr string) {
	urls := lanURLs(addr)
	if len(urls) == 0 {
		fmt.Printf("  手机端：当前只监听回环地址 %s，App 连不上。\n", addr)
		fmt.Print("          加 -lan（等于 -addr " + permitLanAddr + "）重启即可开放局域网。\n\n")
		return
	}
	fmt.Print("  手机端连接（把地址填进 App 的「服务器地址」，密码用 PC 端登录密码）\n")
	for _, u := range urls {
		fmt.Printf("    %s\n", u)
	}
	if runtime.GOOS == "windows" {
		fmt.Print("  防火墙：首次连不上先放行入站端口，例如\n")
		fmt.Print("    netsh advfirewall firewall add rule name=\"ARTEX-REA\" dir=in action=allow protocol=TCP localport=" + lanPort(addr) + "\n")
	}
	fmt.Print("  安全：只在可信局域网开放；隔离/公网场景请用 TLS 反向代理，不要把端口直接暴露到互联网。\n\n")
}

// lanPort 从监听地址中取端口，取不到就用 8787。
func lanPort(addr string) string {
	if _, port, err := net.SplitHostPort(strings.TrimSpace(addr)); err == nil && port != "" {
		return port
	}
	return "8787"
}
