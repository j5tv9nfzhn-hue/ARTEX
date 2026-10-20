package mcpbuiltin

// artex-recon：侦察类工具。纯 Go，不依赖 nmap/dig/openssl 等外部二进制。
// 定位是"先看清目标"，不做利用；输出为紧凑文本表，便于模型继续推理。

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/mcp"
)

func reconServer() Server {
	return Server{
		Name:    "artex-recon",
		Title:   "ARTEX 侦察（DNS / 端口 / TLS / 指纹）",
		Domain:  "pentest",
		Summary: "内置 Go 实现：域名解析与子域探测、TCP 端口探活与 banner、证书与协议细节、技术栈指纹。",
		Tools: []mcp.InProcessTool{
			{
				Name:        "dns_recon",
				Description: "解析域名的 A/AAAA/CNAME/MX/TXT/NS，可选常见子域爆破。用于确认资产归属与邮件/验证配置。",
				Schema: jsonSchema(map[string]any{
					"domain":     propStr("根域名，例如 example.com"),
					"brute":      propBool("是否用内置常见前缀做子域探测（默认 false，会发几十次 DNS 查询）"),
					"wordlist":   propArr("自定义子域前缀列表，给了就代替内置列表", "string"),
					"timeout_ms": propInt("单次解析超时毫秒，默认 3000"),
				}, "domain"),
				Handler: dnsRecon,
			},
			{
				Name:        "port_probe",
				Description: "TCP connect 探活（不是 SYN 扫描，不发原始包），可读首个 banner。支持 80,443,8000-8100 这种写法。",
				Schema: jsonSchema(map[string]any{
					"host":        propStr("主机名或 IP"),
					"ports":       propStr("端口表达式：80,443,1000-1010；默认 21,22,23,25,53,80,110,135,139,143,443,445,1433,1521,3306,3389,5432,6379,8080,8443,9000,9200,11211,27017"),
					"timeout_ms":  propInt("单个端口超时毫秒，默认 800"),
					"concurrency": propInt("并发数，默认 128，上限 512"),
					"banner":      propBool("是否读取首个 banner（默认 true）"),
				}, "host"),
				Handler: portProbe,
			},
			{
				Name:        "tls_inspect",
				Description: "握手并输出证书链（主体/签发者/SAN/有效期）、协议版本、加密套件、ALPN、是否自签名。排查证书泄露的子域与过期配置。",
				Schema: jsonSchema(map[string]any{
					"target":      propStr("host:port，端口缺省 443"),
					"insecure":    propBool("跳过证书校验（默认 true；需要看真实证书时用 false）"),
					"server_name": propStr("SNI，缺省用 host"),
					"timeout_ms":  propInt("超时毫秒，默认 8000"),
				}, "target"),
				Handler: tlsInspect,
			},
			{
				Name:        "tech_hints",
				Description: "抓取首页并按响应头/正文特征推断服务端、框架、CDN/WAF 与前端技术栈。只是指纹，不发攻击载荷。",
				Schema: jsonSchema(map[string]any{
					"url":      propStr("完整 URL，例如 https://example.com/"),
					"insecure": propBool("跳过证书校验，默认 true"),
				}, "url"),
				Handler: techHints,
			},
		},
	}
}

func dnsRecon(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Domain    string   `json:"domain"`
		Brute     bool     `json:"brute"`
		Wordlist  []string `json:"wordlist"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	domain := strings.TrimSpace(strings.TrimPrefix(in.Domain, "http://"))
	domain = strings.TrimPrefix(domain, "https://")
	if i := strings.IndexAny(domain, "/"); i >= 0 {
		domain = domain[:i]
	}
	if domain == "" {
		return "", fmt.Errorf("domain 不能为空")
	}
	timeout := time.Duration(defInt(in.TimeoutMS, 3000, 500, 15000)) * time.Millisecond
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var b strings.Builder
	fmt.Fprintf(&b, "目标：%s\n\n", domain)

	addrs, err := net.DefaultResolver.LookupHost(cctx, domain)
	if err != nil {
		fmt.Fprintf(&b, "A/AAAA：解析失败 %v\n", err)
	} else {
		fmt.Fprintf(&b, "A/AAAA：%s\n", strings.Join(addrs, ", "))
	}
	if cname, err := net.DefaultResolver.LookupCNAME(cctx, domain); err == nil {
		fmt.Fprintf(&b, "CNAME：%s\n", cname)
	}
	if ns, err := net.DefaultResolver.LookupNS(cctx, domain); err == nil {
		names := make([]string, 0, len(ns))
		for _, n := range ns {
			names = append(names, n.Host)
		}
		fmt.Fprintf(&b, "NS：%s\n", strings.Join(names, ", "))
	}
	if mx, err := net.DefaultResolver.LookupMX(cctx, domain); err == nil {
		parts := make([]string, 0, len(mx))
		for _, m := range mx {
			parts = append(parts, fmt.Sprintf("%s(pref=%d)", m.Host, m.Pref))
		}
		fmt.Fprintf(&b, "MX：%s\n", strings.Join(parts, ", "))
	}
	if txt, err := net.DefaultResolver.LookupTXT(cctx, domain); err == nil && len(txt) > 0 {
		fmt.Fprintf(&b, "TXT：\n")
		for _, t := range txt {
			fmt.Fprintf(&b, "  - %s\n", clip(t, 300))
		}
	}

	if !in.Brute {
		return b.String(), nil
	}
	words := in.Wordlist
	if len(words) == 0 {
		words = defaultSubWords
	}
	words = words[:clamp(len(words), 0, 200)]
	type hit struct{ host, ips string }
	var mu sync.Mutex
	var hits []hit
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(prefix string) {
			defer wg.Done()
			defer func() { <-sem }()
			host := prefix + "." + domain
			c, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ips, err := net.DefaultResolver.LookupHost(c, host)
			if err != nil || len(ips) == 0 {
				return
			}
			mu.Lock()
			hits = append(hits, hit{host: host, ips: strings.Join(ips, ",")})
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	sort.Slice(hits, func(i, j int) bool { return hits[i].host < hits[j].host })
	fmt.Fprintf(&b, "\n子域探测（%d 个前缀）：命中 %d 个\n", len(words), len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, "  %-40s %s\n", h.host, h.ips)
	}
	return b.String(), nil
}

var defaultSubWords = []string{
	"www", "api", "app", "admin", "test", "dev", "staging", "stage", "uat", "pre",
	"prod", "mail", "smtp", "imap", "pop", "webmail", "vpn", "ns1", "ns2", "dns",
	"ftp", "sftp", "git", "gitlab", "jenkins", "ci", "cd", "grafana", "kibana",
	"prometheus", "monitor", "status", "docs", "wiki", "blog", "shop", "pay",
	"payment", "oauth", "sso", "auth", "login", "id", "account", "m", "mobile",
	"static", "cdn", "img", "assets", "upload", "files", "download", "media",
	"db", "mysql", "redis", "mongo", "es", "search", "mq", "kafka", "internal",
}

func portProbe(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Host        string `json:"host"`
		Ports       string `json:"ports"`
		TimeoutMS   int    `json:"timeout_ms"`
		Concurrency int    `json:"concurrency"`
		Banner      *bool  `json:"banner"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	host := strings.TrimSpace(in.Host)
	if host == "" {
		return "", fmt.Errorf("host 不能为空")
	}
	spec := in.Ports
	if strings.TrimSpace(spec) == "" {
		spec = "21,22,23,25,53,80,110,135,139,143,443,445,1433,1521,3306,3389,5432,6379,8080,8443,9000,9200,11211,27017"
	}
	ports := parsePorts(spec)
	if len(ports) == 0 {
		return "", fmt.Errorf("ports 解析为空：%q", spec)
	}
	ports = ports[:clamp(len(ports), 0, 2048)]
	timeout := time.Duration(defInt(in.TimeoutMS, 800, 100, 10000)) * time.Millisecond
	conc := defInt(in.Concurrency, 128, 1, 512)
	if conc == 0 {
		conc = 128
	}
	readBanner := true
	if in.Banner != nil {
		readBanner = *in.Banner
	}

	type opened struct {
		Port   int
		Banner string
	}
	var mu sync.Mutex
	var found []opened
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	start := time.Now()
	for _, p := range ports {
		wg.Add(1)
		sem <- struct{}{}
		go func(port int) {
			defer wg.Done()
			defer func() { <-sem }()
			addr := net.JoinHostPort(host, strconv.Itoa(port))
			conn, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				return
			}
			defer conn.Close()
			banner := ""
			if readBanner {
				_ = conn.SetReadDeadline(time.Now().Add(600 * time.Millisecond))
				buf := make([]byte, 256)
				if n, _ := conn.Read(buf); n > 0 {
					banner = sanitizeBanner(buf[:n])
				}
			}
			mu.Lock()
			found = append(found, opened{Port: port, Banner: banner})
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].Port < found[j].Port })

	var b strings.Builder
	fmt.Fprintf(&b, "目标：%s  端口 %d 个  耗时 %s\n", host, len(ports), time.Since(start).Round(time.Millisecond))
	if len(found) == 0 {
		b.WriteString("没有开放端口（TCP connect 视角；防火墙可能丢包）。\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "开放 %d 个：\n", len(found))
	for _, f := range found {
		if f.Banner != "" {
			fmt.Fprintf(&b, "  %-6d %s\n", f.Port, clip(f.Banner, 120))
		} else {
			fmt.Fprintf(&b, "  %-6d\n", f.Port)
		}
	}
	return b.String(), nil
}

func parsePorts(spec string) []int {
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if p > 0 && p <= 65535 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			lo, hi, ok := strings.Cut(part, "-")
			if !ok {
				continue
			}
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			z, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || a > z {
				continue
			}
			for p := a; p <= z && p-a < 4096; p++ {
				add(p)
			}
			continue
		}
		if p, err := strconv.Atoi(part); err == nil {
			add(p)
		}
	}
	sort.Ints(out)
	return out
}

// sanitizeBanner 把不可打印字节换掉，保持一行。
func sanitizeBanner(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		switch {
		case c == '\r' || c == '\n':
			s.WriteByte(' ')
		case c >= 32 && c < 127:
			s.WriteByte(c)
		default:
			s.WriteByte('.')
		}
	}
	return strings.TrimSpace(s.String())
}

func tlsInspect(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Target     string `json:"target"`
		Insecure   *bool  `json:"insecure"`
		ServerName string `json:"server_name"`
		TimeoutMS  int    `json:"timeout_ms"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return "", fmt.Errorf("target 不能为空")
	}
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	host, _, _ := net.SplitHostPort(target)
	sni := in.ServerName
	if sni == "" {
		sni = host
	}
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	timeout := time.Duration(defInt(in.TimeoutMS, 8000, 1000, 30000)) * time.Millisecond
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", target, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: insecure, // #nosec G402 —— 侦察时默认跳过校验
	})
	if err != nil {
		return "", fmt.Errorf("TLS 握手失败：%w", err)
	}
	defer conn.Close()
	st := conn.ConnectionState()

	var b strings.Builder
	fmt.Fprintf(&b, "目标：%s  SNI：%s\n", target, sni)
	fmt.Fprintf(&b, "协议：%s  套件：%s  ALPN：%s\n", tls.VersionName(st.Version), tls.CipherSuiteName(st.CipherSuite), st.NegotiatedProtocol)
	fmt.Fprintf(&b, "证书链：%d 张\n", len(st.PeerCertificates))
	for i, c := range st.PeerCertificates {
		fmt.Fprintf(&b, "\n[%d] 主体：%s\n", i, c.Subject.String())
		fmt.Fprintf(&b, "    签发：%s\n", c.Issuer.String())
		fmt.Fprintf(&b, "    有效期：%s → %s（剩余 %d 天）\n", c.NotBefore.UTC().Format("2006-01-02"), c.NotAfter.UTC().Format("2006-01-02"), int(time.Until(c.NotAfter).Hours()/24))
		fmt.Fprintf(&b, "    算法：%s / %s   CA=%v\n", c.PublicKeyAlgorithm, c.SignatureAlgorithm, c.IsCA)
		if len(c.DNSNames) > 0 {
			fmt.Fprintf(&b, "    SAN-DNS：%s\n", strings.Join(c.DNSNames, ", "))
		}
		if len(c.IPAddresses) > 0 {
			ips := make([]string, 0, len(c.IPAddresses))
			for _, ip := range c.IPAddresses {
				ips = append(ips, ip.String())
			}
			fmt.Fprintf(&b, "    SAN-IP：%s\n", strings.Join(ips, ", "))
		}
		if err := c.CheckSignatureFrom(c); err == nil {
			fmt.Fprintf(&b, "    ⚠ 自签名证书\n")
		}
	}
	if insecure {
		b.WriteString("\n注：本次跳过证书校验（insecure=true），结论不代表浏览器会接受该证书。\n")
	}
	return b.String(), nil
}

func techHints(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		URL      string `json:"url"`
		Insecure *bool  `json:"insecure"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	target := strings.TrimSpace(in.URL)
	if target == "" {
		return "", fmt.Errorf("url 不能为空")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "https://" + target
	}
	insecure := true
	if in.Insecure != nil {
		insecure = *in.Insecure
	}
	c := newHTTPClient(15*time.Second, insecure, true)
	req, err := httpNewRequest(ctx, "GET", target)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36")
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败：%w", err)
	}
	body, _ := readAllLimited(resp, 512<<10)
	_ = resp.Body.Close()
	lower := strings.ToLower(string(body))

	var b strings.Builder
	fmt.Fprintf(&b, "URL：%s\n状态：%s  长度：%d\n\n", target, resp.Status, len(body))
	b.WriteString("响应头指纹：\n")
	hits := 0
	add := func(reason string) {
		if hits < 40 {
			fmt.Fprintf(&b, "  - %s\n", reason)
			hits++
		}
	}
	for _, h := range []string{"Server", "X-Powered-By", "Via", "CF-Ray", "X-Akamai-Request-ID", "X-Cache", "X-Sucuri-ID", "X-Varnish", "X-AspNet-Version", "X-Generator", "X-Drupal-Cache"} {
		if v := resp.Header.Get(h); v != "" {
			add(h + ": " + v)
		}
	}
	cookies := strings.ToLower(strings.Join(resp.Header.Values("Set-Cookie"), ";"))
	for name, sig := range map[string]string{
		"phpsessid": "PHP", "jsessionid": "Java/Servlet", "asp.net_sessionid": "ASP.NET",
		"laravel_session": "Laravel", "ci_session": "CodeIgniter", "django": "Django",
		"wp-settings": "WordPress", "connect.sid": "Express/Node",
	} {
		if strings.Contains(cookies, name) {
			add("Cookie 特征 → " + sig)
		}
	}
	for sig, name := range map[string]string{
		"wp-content": "WordPress", "wp-includes": "WordPress", "__next_data__": "Next.js",
		"_nuxt": "Nuxt", "ng-version": "Angular", "data-reactroot": "React",
		"csrfmiddlewaretoken": "Django", "__viewstate": "ASP.NET WebForms",
		"jsessionid": "Java", "shopify": "Shopify", "grafana": "Grafana",
		"<title>kibana": "Kibana", "jenkins": "Jenkins", "swagger-ui": "Swagger UI",
	} {
		if strings.Contains(lower, sig) {
			add("正文特征 → " + name)
		}
	}
	edge := ""
	for name, sig := range map[string]string{
		"cloudflare": "Cloudflare", "akamai": "Akamai", "imperva": "Imperva/Incapsula",
		"sucuri": "Sucuri", "awselb": "AWS ELB", "nginx": "Nginx", "openresty": "OpenResty",
	} {
		if strings.Contains(strings.ToLower(resp.Header.Get("Server"))+" "+strings.ToLower(strings.Join(resp.Header.Values("Via"), " "))+" "+cookies, name) {
			edge = sig
			break
		}
	}
	if edge != "" {
		fmt.Fprintf(&b, "\n边缘/WAF 线索：%s\n", edge)
	}
	if hits == 0 {
		b.WriteString("  （未命中已知特征）\n")
	}
	return b.String(), nil
}
