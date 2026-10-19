package agent

import "strings"

// CanonicalSkillAlias handles only explicit historical aliases. Unknown custom
// names are preserved, never matched by substring or an ambiguous prefix.
func CanonicalSkillAlias(name string) string {
	aliases := map[string]string{
		"scopesentry":               "scopesentry-mcp",
		"scope-sentry":              "scopesentry-mcp",
		"playwright":                "playwright-cli",
		"browser":                   "playwright-cli",
		"api":                       "api-recon",
		"apirecon":                  "api-recon",
		"web-vuln":                  "web-vuln-tactics",
		"web":                       "web-vuln-tactics",
		"reverse":                   "reverse-engineering",
		"re":                        "reverse-engineering",
		"exploit":                   "exploit-kb-patterns",
		"exploit-kb":                "exploit-kb-patterns",
		"triage":                    "expert-triage-pivot",
		"pivot":                     "expert-triage-pivot",
		"captcha":                   "human-verification",
		"human-verification-triage": "human-verification",
		"sandbox":                   "ai-coding-sandbox-tactics",
		"ai-sandbox":                "ai-coding-sandbox-tactics",
		"ai-coding":                 "ai-coding-sandbox-tactics",
		"nextjs-sandbox":            "ai-coding-sandbox-tactics",
		"wasm":                      "wasm-cloud-saas-tactics",
		"wasm-saas":                 "wasm-cloud-saas-tactics",
		"cloud-saas":                "wasm-cloud-saas-tactics",
		"wasm-cloud":                "wasm-cloud-saas-tactics",
		"rea":                       "rea-mcp",
		"rea-engine":                "rea-mcp",
		"wpe":                       "wpe64-mcp",
		"wpe64":                     "wpe64-mcp",
		"mitmproxy":                 "mitmproxy-traffic",
		"traffic-capture":           "mitmproxy-traffic",
	}
	name = strings.TrimSpace(name)
	if v, ok := aliases[strings.ToLower(name)]; ok {
		return v
	}
	return name
}
