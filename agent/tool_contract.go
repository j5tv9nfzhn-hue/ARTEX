package agent

import "strings"

// CanonicalSkillAlias handles only explicit historical aliases. Unknown custom
// names are preserved, never matched by substring or an ambiguous prefix.
func CanonicalSkillAlias(name string) string {
	aliases := map[string]string{
		"api":                 "performing-api-inventory-and-discovery",
		"apirecon":            "performing-api-inventory-and-discovery",
		"api-recon":           "performing-api-inventory-and-discovery",
		"web-vuln":            "performing-web-application-penetration-test",
		"web":                 "performing-web-application-penetration-test",
		"web-vuln-tactics":    "performing-web-application-penetration-test",
		"reverse":             "reverse-engineering-malware-with-ghidra",
		"re":                  "reverse-engineering-malware-with-ghidra",
		"reverse-engineering": "reverse-engineering-malware-with-ghidra",
		"exploit":             "performing-cve-prioritization-with-kev-catalog",
		"exploit-kb":          "performing-cve-prioritization-with-kev-catalog",
		"exploit-kb-patterns": "performing-cve-prioritization-with-kev-catalog",
		"triage":              "performing-web-application-vulnerability-triage",
		"pivot":               "performing-web-application-vulnerability-triage",
		"expert-triage-pivot": "performing-web-application-vulnerability-triage",
		"sandbox":             "ai-coding-sandbox-tactics",
		"ai-sandbox":          "ai-coding-sandbox-tactics",
		"ai-coding":           "ai-coding-sandbox-tactics",
		"nextjs-sandbox":      "ai-coding-sandbox-tactics",
		"wasm":                "wasm-cloud-saas-tactics",
		"wasm-saas":           "wasm-cloud-saas-tactics",
		"cloud-saas":          "wasm-cloud-saas-tactics",
		"wasm-cloud":          "wasm-cloud-saas-tactics",
	}
	name = strings.TrimSpace(name)
	if v, ok := aliases[strings.ToLower(name)]; ok {
		return v
	}
	return name
}
