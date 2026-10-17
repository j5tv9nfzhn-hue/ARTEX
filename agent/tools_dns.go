package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func (t *ToolSet) dnsProbeTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "dns_probe",
		Description: "原生跨平台 DNS 记录解析与子域名枚举工具（彻底替代 Windows/Linux 差异巨大的 dig / nslookup / host 命令及 Python dnspython/socket 脚本）。支持查询 A / AAAA / CNAME / TXT(SPF/DMARC) / MX / NS / PTR / ALL 全量记录，并支持内置泛解析自动识别与剔除（wildcard detection）的并发子域名枚举（subdomain_enum）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"description": "操作模式：lookup（默认，单域名多记录查询）| ptr（反向 IP 查域名）| subdomain_enum（带泛解析过滤的并发子域名枚举）",
				},
				"domain":        map[string]any{"type": "string", "description": "目标域名（lookup / subdomain_enum 必填）或 IP 地址（ptr 模式）"},
				"record_type":   map[string]any{"type": "string", "description": "lookup 模式的记录类型：ALL（默认）| A | AAAA | CNAME | TXT | MX | NS | PTR"},
				"subdomains":    map[string]any{"type": "array", "description": "subdomain_enum 模式的候选子域名前缀列表（如 ['api', 'admin', 'dev', 'test']；不传则使用内置高频字典）", "items": map[string]any{"type": "string"}},
				"wordlist_file": map[string]any{"type": "string", "description": "subdomain_enum 模式可选的本地子域名字典文件路径"},
				"nameserver":    map[string]any{"type": "string", "description": "可选：自定义 DNS 服务器（如 '8.8.8.8:53' 或 '223.5.5.5:53'）"},
				"concurrency":   map[string]any{"type": "integer", "description": "subdomain_enum 并发数，默认 10，最大 16"},
				"timeout_ms":    map[string]any{"type": "integer", "description": "单次查询超时（毫秒），默认 4000"},
			},
			"required": []any{"domain"},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Mode         string   `json:"mode"`
				Domain       string   `json:"domain"`
				RecordType   string   `json:"record_type"`
				Subdomains   []string `json:"subdomains"`
				WordlistFile string   `json:"wordlist_file"`
				Nameserver   string   `json:"nameserver"`
				Concurrency  int      `json:"concurrency"`
				TimeoutMs    int      `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			domain := strings.TrimSpace(a.Domain)
			if domain == "" {
				return actool.Errorf("domain 为必填参数"), nil
			}
			domain = strings.TrimPrefix(strings.TrimPrefix(domain, "http://"), "https://")
			if idx := strings.IndexByte(domain, '/'); idx >= 0 {
				domain = domain[:idx]
			}
			if h, _, err := net.SplitHostPort(domain); err == nil && h != "" {
				domain = h
			}

			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			if mode == "" {
				mode = "lookup"
			}
			timeoutDur := time.Duration(defaultInt(a.TimeoutMs, 4000, 500, 15000)) * time.Millisecond
			resolver := buildDNSResolver(a.Nameserver, timeoutDur)

			switch mode {
			case "lookup":
				rtype := strings.ToUpper(strings.TrimSpace(a.RecordType))
				if rtype == "" {
					rtype = "ALL"
				}
				res := runDNSLookup(ctx, resolver, domain, rtype, timeoutDur)
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "ptr":
				rCtx, cancel := context.WithTimeout(ctx, timeoutDur)
				defer cancel()
				names, err := resolver.LookupAddr(rCtx, domain)
				out := map[string]any{
					"mode":  "ptr",
					"ip":    domain,
					"names": names,
				}
				if err != nil {
					out["error"] = err.Error()
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "subdomain_enum":
				var words []string
				words = append(words, a.Subdomains...)
				if wf := strings.TrimSpace(a.WordlistFile); wf != "" {
					raw, err := os.ReadFile(t.resolveLocalPath(wf))
					if err != nil {
						return actool.Errorf("读取 wordlist_file 失败: " + err.Error()), nil
					}
					for _, line := range strings.Split(string(raw), "\n") {
						w := strings.TrimSpace(line)
						if w != "" && !strings.HasPrefix(w, "#") {
							words = append(words, w)
						}
					}
				}
				if len(words) == 0 {
					words = defaultSubdomainWords()
				}
				if len(words) > 500 {
					words = words[:500]
				}
				concurrency := defaultInt(a.Concurrency, 10, 1, 16)
				res := runSubdomainEnum(ctx, resolver, domain, words, concurrency, timeoutDur)
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			default:
				return actool.Errorf("未知的 dns_probe mode: " + mode), nil
			}
		},
	})
}

func buildDNSResolver(nameserver string, timeout time.Duration) *net.Resolver {
	ns := strings.TrimSpace(nameserver)
	if ns == "" {
		return net.DefaultResolver
	}
	if !strings.Contains(ns, ":") {
		ns += ":53"
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, network, ns)
		},
	}
}

func runDNSLookup(ctx context.Context, r *net.Resolver, domain, rtype string, timeout time.Duration) map[string]any {
	out := map[string]any{
		"mode":        "lookup",
		"domain":      domain,
		"record_type": rtype,
	}

	if rtype == "ALL" || rtype == "A" || rtype == "AAAA" {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		ips, err := r.LookupIPAddr(rCtx, domain)
		cancel()
		if err == nil {
			var v4, v6 []string
			for _, ip := range ips {
				if ip.IP.To4() != nil {
					v4 = append(v4, ip.IP.String())
				} else {
					v6 = append(v6, ip.IP.String())
				}
			}
			if rtype == "ALL" || rtype == "A" {
				out["A"] = v4
			}
			if rtype == "ALL" || rtype == "AAAA" {
				out["AAAA"] = v6
			}
		}
	}

	if rtype == "ALL" || rtype == "CNAME" {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		cname, err := r.LookupCNAME(rCtx, domain)
		cancel()
		if err == nil && cname != "" {
			out["CNAME"] = cname
		}
	}

	if rtype == "ALL" || rtype == "TXT" {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		txts, err := r.LookupTXT(rCtx, domain)
		cancel()
		if err == nil && len(txts) > 0 {
			out["TXT"] = txts
		}
	}

	if rtype == "ALL" || rtype == "MX" {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		mxs, err := r.LookupMX(rCtx, domain)
		cancel()
		if err == nil && len(mxs) > 0 {
			var list []string
			for _, m := range mxs {
				list = append(list, m.Host)
			}
			out["MX"] = list
		}
	}

	if rtype == "ALL" || rtype == "NS" {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		nss, err := r.LookupNS(rCtx, domain)
		cancel()
		if err == nil && len(nss) > 0 {
			var list []string
			for _, n := range nss {
				list = append(list, n.Host)
			}
			out["NS"] = list
		}
	}

	if rtype == "PTR" {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		ptrs, err := r.LookupAddr(rCtx, domain)
		cancel()
		if err == nil {
			out["PTR"] = ptrs
		}
	}

	return out
}

func runSubdomainEnum(ctx context.Context, r *net.Resolver, baseDomain string, words []string, concurrency int, timeout time.Duration) map[string]any {
	// 1. 泛解析（Wildcard DNS）基线探测
	wildcardIPs := make(map[string]struct{})
	for _, probe := range []string{"artex-wildcard-9f8e7d6c", "artex-nonexistent-1a2b3c4d"} {
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		ips, err := r.LookupHost(rCtx, probe+"."+baseDomain)
		cancel()
		if err == nil {
			for _, ip := range ips {
				wildcardIPs[ip] = struct{}{}
			}
		}
	}

	type subHit struct {
		Subdomain string   `json:"subdomain"`
		CNAME     string   `json:"cname,omitempty"`
		IPs       []string `json:"ips"`
	}

	var (
		mu              sync.Mutex
		hits            []subHit
		wildcardSkipped int
		wg              sync.WaitGroup
		localSem        = make(chan struct{}, concurrency)
	)

	seenSub := make(map[string]struct{}, len(words))
	for _, w := range words {
		w = strings.Trim(strings.ToLower(strings.TrimSpace(w)), ".")
		if w == "" {
			continue
		}
		fqdn := w + "." + baseDomain
		if strings.HasSuffix(w, "."+baseDomain) {
			fqdn = w
		}
		if _, dup := seenSub[fqdn]; dup {
			continue
		}
		seenSub[fqdn] = struct{}{}

		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		localSem <- struct{}{}
		globalNetProbeSem <- struct{}{}
		go func(host string) {
			defer wg.Done()
			defer func() {
				<-globalNetProbeSem
				<-localSem
			}()
			if ctx.Err() != nil {
				return
			}
			rCtx, cancel := context.WithTimeout(ctx, timeout)
			ips, err := r.LookupHost(rCtx, host)
			cancel()
			if err != nil || len(ips) == 0 {
				return
			}
			allWildcard := len(wildcardIPs) > 0
			for _, ip := range ips {
				if _, ok := wildcardIPs[ip]; !ok {
					allWildcard = false
					break
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if allWildcard {
				wildcardSkipped++
				return
			}
			cCtx, cCancel := context.WithTimeout(ctx, timeout)
			cname, _ := r.LookupCNAME(cCtx, host)
			cCancel()
			cname = strings.TrimSuffix(cname, ".")
			if cname == host {
				cname = ""
			}
			sort.Strings(ips)
			hits = append(hits, subHit{
				Subdomain: host,
				CNAME:     cname,
				IPs:       ips,
			})
		}(fqdn)
	}
	wg.Wait()

	sort.Slice(hits, func(i, j int) bool { return hits[i].Subdomain < hits[j].Subdomain })
	var wcList []string
	for ip := range wildcardIPs {
		wcList = append(wcList, ip)
	}
	sort.Strings(wcList)

	return map[string]any{
		"mode":              "subdomain_enum",
		"domain":            baseDomain,
		"tested_count":      len(seenSub),
		"wildcard_detected": len(wcList) > 0,
		"wildcard_ips":      wcList,
		"wildcard_filtered": wildcardSkipped,
		"discovered_count":  len(hits),
		"subdomains":        hits,
	}
}

func defaultSubdomainWords() []string {
	return []string{
		"www", "api", "admin", "dev", "test", "staging", "stage", "uat", "beta", "prod",
		"internal", "intra", "corp", "oa", "sso", "auth", "login", "id", "oauth", "cas",
		"portal", "console", "manage", "manager", "dashboard", "panel", "backend", "cms",
		"crm", "erp", "hr", "mail", "smtp", "imap", "pop", "webmail", "vpn", "remote",
		"git", "gitlab", "github", "gitea", "svn", "jenkins", "ci", "cd", "build", "sonar",
		"nexus", "harbor", "registry", "docker", "k8s", "kube", "rancher", "grafana", "prometheus",
		"kibana", "elk", "log", "logs", "monitor", "status", "metrics", "apm", "sentry",
		"static", "assets", "cdn", "img", "image", "images", "media", "upload", "uploads",
		"download", "files", "file", "oss", "s3", "minio", "storage", "backup", "bak",
		"db", "mysql", "pg", "postgres", "redis", "mongo", "es", "elastic", "kafka", "mq",
		"gateway", "gw", "proxy", "edge", "waf", "ws", "wss", "socket", "push", "msg",
		"m", "mobile", "app", "h5", "mini", "mp", "wx", "wechat", "open", "openapi",
		"partner", "pay", "payment", "billing", "order", "shop", "store", "mall", "user",
		"account", "member", "vip", "help", "support", "doc", "docs", "wiki", "confluence",
		"jira", "redmine", "zabbix", "jump", "bastion", "jumpserver", "nacos", "apollo",
		"eureka", "consul", "xxl", "actuator", "swagger", "v1", "v2", "v3", "old", "new",
	}
}
