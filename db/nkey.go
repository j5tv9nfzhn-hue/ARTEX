package db

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// 归一化自然键 (nkey)：移植自旧 graph/id.go，去掉 StableID 哈希（PG 用 BIGSERIAL 主键 +
// UNIQUE(type, nkey) 去重）。子资产的 nkey 内嵌父资产的 int64 id，把层级编码进键。

func DomainKey(fqdn string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
}

func IPKey(ip string) string { return strings.TrimSpace(ip) }

// RootDomain returns the registrable domain (eTLD+1) for a host and whether the
// host itself IS that apex (§3.1). Edge cases (§3.1 边界处理): an IP literal or a
// host publicsuffix can't classify (localhost / internal / non-ICANN TLD) is
// returned unchanged as its own root with isApex=true — best-effort, never treated
// as a subdomain.
func RootDomain(host string) (root string, isApex bool) {
	h := DomainKey(host)
	if h == "" || net.ParseIP(h) != nil {
		return h, true
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil || etld1 == "" {
		return h, true
	}
	return etld1, h == etld1
}

func PortKey(ipID int64, proto string, port int) string {
	return itoa(ipID) + "|" + strings.ToLower(proto) + "|" + strconv.Itoa(port)
}

func ServiceKey(portID int64, svcName string) string {
	return itoa(portID) + "|" + strings.ToLower(svcName)
}

func SiteKey(scheme, host string, port int) string {
	return strings.ToLower(scheme) + "|" + strings.ToLower(host) + "|" + strconv.Itoa(port)
}

func EndpointKey(siteID int64, method, urlTemplate string) string {
	return itoa(siteID) + "|" + strings.ToUpper(method) + "|" + urlTemplate
}

func ParameterKey(endpointID int64, location, name string) string {
	return itoa(endpointID) + "|" + strings.ToLower(location) + "|" + name
}

// NormalizeParamName 归一化参数名（endpoint.params 元素的「相同引用」判定）。
// 规则：lower + trim，不做同义词合并(userId/user_id/uid 视为不同)。写入与查询共享此实现，
// 保证「按参数名查同公司接口」可复现。
func NormalizeParamName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func TechKey(name, version string) string {
	return strings.ToLower(name) + "|" + version
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TemplatePath collapses high-cardinality path segments into placeholders so the
// graph is not flooded by instances: /user/123 -> /user/{id}.
//
// 用字符扫描代替正则：这条路径在每个被抓取的请求上都会跑一遍（每个路径段
// 最多 4 次 regexp.MatchString）。正则引擎的固定开销在抓包热路径上是纯浪费，
// 而这里要判断的形态（纯数字、UUID、长十六进制、长 token）都能用 O(len) 的
// 逐字符判断等价表达，且不需要分配。
func TemplatePath(path string) string {
	if path == "" {
		return "/"
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "" {
			continue
		}
		switch {
		case isAllDigits(s):
			segs[i] = "{id}"
		case isUUID(s):
			segs[i] = "{uuid}"
		case len(s) >= 16 && isAllHex(s):
			segs[i] = "{hex}"
		case len(s) >= 24 && isTokenChars(s):
			segs[i] = "{token}"
		}
	}
	return strings.Join(segs, "/")
}

// isAllDigits reports whether s is one or more ASCII digits (reNumeric).
func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// isAllHex reports whether s is exclusively ASCII hex digits (reHex body).
func isAllHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return len(s) > 0
}

// isTokenChars reports whether s consists only of [A-Za-z0-9_-] (reLong body).
func isTokenChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-') {
			return false
		}
	}
	return len(s) > 0
}

// isUUID matches the 8-4-4-4-12 hex form without a regex.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// SplitURL parses a raw URL into scheme/host/port/urlTemplate/params for building
// site/endpoint/param keys.
func SplitURL(raw, method string) (scheme, host string, port int, urlTemplate string, params []string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, "", nil, err
	}
	scheme = strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	port = defaultPort(scheme, u.Port())
	urlTemplate = TemplatePath(u.EscapedPath())
	for k := range u.Query() {
		params = append(params, k)
	}
	return scheme, host, port, urlTemplate, params, nil
}

func defaultPort(scheme, p string) int {
	if p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	switch scheme {
	case "https":
		return 443
	case "http":
		return 80
	default:
		return 0
	}
}
