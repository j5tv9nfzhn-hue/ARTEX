package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
	"github.com/tidwall/gjson"
	xhtml "golang.org/x/net/html"
)

func (t *ToolSet) webReconTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "web_recon",
		Description: "前端、API 规范与站点侦察（100% 原生替代 api-recon 中的 harvest_static.py / spider_mpa.py / extract_route_map.py / build_perm_tree.py 及 GraphQL/OpenAPI/SourceMap/指纹探测脚本）。**枚举前端路由、JS 内接口、GraphQL/OpenAPI 时用本工具，不要用 Bash 跑 Python 侦察脚本**。支持 8 种模式：spa_harvest（拉取入口 HTML、递归解析 Webpack/Vite chunk manifest、受控并发下载全量 JS chunk 并提取 api_static.txt / paths_other.txt / routes.txt / chunkmap.txt）、mpa_spider（同源多页 BFS 爬虫，产出 pages.txt / forms.json / inline_apis.txt）、route_map（扫描 JS 提取前端路由表 route_map.json）、perm_tree（扫描 JS 逆向 userRouteAuth 权限树并更新 config.json stubs）、graphql（自动探测 GraphQL 端点并执行 __schema 内省查询与批量/别名支持检测，提取 Query/Mutation 列表）、openapi（自动探测并解析 Swagger/OpenAPI v2/v3 JSON，提取全量接口与参数）、sourcemap（解析 .js.map 文件或 URL，还原打包前的原始前端源码文件到 outdir/sources/）、fingerprint（探测站点技术栈、框架、Shiro/Spring/ThinkPHP 等特征、WAF 类型与安全响应头）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"description": "侦察模式：spa_harvest | mpa_spider | route_map | perm_tree | graphql | openapi | sourcemap | fingerprint",
				},
				"url":         map[string]any{"type": "string", "description": "目标起始 URL（spa_harvest / mpa_spider / graphql / openapi / fingerprint / sourcemap远程模式 使用）"},
				"file_path":   map[string]any{"type": "string", "description": "本地文件路径（openapi 或 sourcemap 本地文件解析时使用）"},
				"outdir":      map[string]any{"type": "string", "description": "输出目录路径（默认 'recon'）"},
				"jsdir":       map[string]any{"type": "string", "description": "JS 文件目录（route_map / perm_tree 使用，默认 '<outdir>/js'）"},
				"config_file": map[string]any{"type": "string", "description": "perm_tree 模式下可选自动打补丁的 config.json 路径"},
				"cookie":      map[string]any{"type": "string", "description": "可选：请求附带的 Cookie 字符串"},
				"max_pages":   map[string]any{"type": "integer", "description": "mpa_spider 最大抓取页面数，默认 150，最大 500"},
				"max_depth":   map[string]any{"type": "integer", "description": "mpa_spider 最大爬取深度，默认 4，最大 10"},
				"include":     map[string]any{"type": "string", "description": "mpa_spider URL 包含正则"},
				"exclude":     map[string]any{"type": "string", "description": "mpa_spider URL 排除正则（默认 'logout|signout|delete|remove|destroy'）"},
			},
			"required": []any{"mode"},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			t.steer.markTool("web_recon")
			var a struct {
				Mode       string `json:"mode"`
				URL        string `json:"url"`
				FilePath   string `json:"file_path"`
				OutDir     string `json:"outdir"`
				JSDir      string `json:"jsdir"`
				ConfigFile string `json:"config_file"`
				Cookie     string `json:"cookie"`
				MaxPages   int    `json:"max_pages"`
				MaxDepth   int    `json:"max_depth"`
				Include    string `json:"include"`
				Exclude    string `json:"exclude"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			if mode == "" {
				return actool.Errorf("mode 为必填参数（spa_harvest | mpa_spider | route_map | perm_tree | graphql | openapi | sourcemap | fingerprint）"), nil
			}
			outDir := strings.TrimSpace(a.OutDir)
			if outDir == "" {
				outDir = "recon"
			}
			outDir = t.resolveLocalPath(outDir)

			switch mode {
			case "spa_harvest":
				rawURL := strings.TrimSpace(a.URL)
				if rawURL == "" {
					return actool.Errorf("mode=spa_harvest 时 url 为必填参数"), nil
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					rawURL = "https://" + rawURL
				}
				res, err := t.runSPAHarvest(ctx, rawURL, outDir, a.Cookie)
				if err != nil {
					return actool.Errorf("spa_harvest 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "mpa_spider":
				rawURL := strings.TrimSpace(a.URL)
				if rawURL == "" {
					return actool.Errorf("mode=mpa_spider 时 url 为必填参数"), nil
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					rawURL = "https://" + rawURL
				}
				maxPages := defaultInt(a.MaxPages, 150, 1, 500)
				maxDepth := defaultInt(a.MaxDepth, 4, 1, 10)
				exclude := strings.TrimSpace(a.Exclude)
				if exclude == "" {
					exclude = "logout|signout|delete|remove|destroy"
				}
				res, err := t.runMPASpider(ctx, rawURL, outDir, a.Cookie, maxPages, maxDepth, a.Include, exclude)
				if err != nil {
					return actool.Errorf("mpa_spider 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "route_map":
				jsDir := strings.TrimSpace(a.JSDir)
				if jsDir == "" {
					jsDir = filepath.Join(outDir, "js")
				} else {
					jsDir = t.resolveLocalPath(jsDir)
				}
				res, err := runExtractRouteMap(jsDir, outDir)
				if err != nil {
					return actool.Errorf("route_map 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "perm_tree":
				jsDir := strings.TrimSpace(a.JSDir)
				if jsDir == "" {
					jsDir = filepath.Join(outDir, "js")
				} else {
					jsDir = t.resolveLocalPath(jsDir)
				}
				cfgPath := ""
				if strings.TrimSpace(a.ConfigFile) != "" {
					cfgPath = t.resolveLocalPath(a.ConfigFile)
				}
				res, err := runBuildPermTree(jsDir, outDir, cfgPath)
				if err != nil {
					return actool.Errorf("perm_tree 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "graphql":
				rawURL := strings.TrimSpace(a.URL)
				if rawURL == "" {
					return actool.Errorf("mode=graphql 时 url 为必填参数"), nil
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					rawURL = "https://" + rawURL
				}
				res, err := t.runGraphQLRecon(ctx, rawURL, a.Cookie)
				if err != nil {
					return actool.Errorf("graphql 探测失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "openapi":
				res, err := t.runOpenAPIRecon(ctx, strings.TrimSpace(a.URL), strings.TrimSpace(a.FilePath), a.Cookie)
				if err != nil {
					return actool.Errorf("openapi 解析失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "sourcemap":
				res, err := t.runSourceMapUnpack(ctx, strings.TrimSpace(a.URL), strings.TrimSpace(a.FilePath), outDir, a.Cookie)
				if err != nil {
					return actool.Errorf("sourcemap 还原失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "fingerprint":
				rawURL := strings.TrimSpace(a.URL)
				if rawURL == "" {
					return actool.Errorf("mode=fingerprint 时 url 为必填参数"), nil
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					rawURL = "https://" + rawURL
				}
				res, err := t.runWebFingerprint(ctx, rawURL, a.Cookie)
				if err != nil {
					return actool.Errorf("fingerprint 探测失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			default:
				return actool.Errorf("未知的 web_recon mode: " + mode), nil
			}
		},
	})
}

var (
	reconPublicPathRe = regexp.MustCompile(`(?:\.p|publicPath)\s*=\s*"([^"]*)"`)
	reconWebpackMapRe = regexp.MustCompile(`\}\[[A-Za-z_$]\]\s*\+\s*"\.js"`)
	reconChunkPairRe  = regexp.MustCompile(`([0-9A-Za-z_$]+)\s*:\s*"([0-9a-fA-F]{6,20})"`)
	reconViteAssetRe  = regexp.MustCompile(`"(assets/[^"]+\.js)"`)
	reconScriptSrcRe  = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+\.js[^"']*)["']`)
	reconAnyJSRefRe   = regexp.MustCompile(`(?i)(?:src|href)=["']([^"']*\.js)["']`)
	reconAPIHintRe    = regexp.MustCompile(`(?i)/(?:api|rest|service|services|gateway|graphql|v\d|web|admin|backend|open)\b`)
	reconAssetExtRe   = regexp.MustCompile(`(?i)\.(js|css|png|jpe?g|svg|gif|woff2?|ttf|ico|map|json|mp4|webp)(\?|$)`)
	reconQuotedPathRe = regexp.MustCompile("[\"'`](/[A-Za-z0-9_\\-./{}$:]+)[\"'`]")
	reconConcatPathRe = regexp.MustCompile(`["'](/[A-Za-z0-9_\-./]+/)["']\s*\+`)
	reconRouteKeyRe   = regexp.MustCompile(`(?:path|to|redirect|href)\s*:\s*["'](/[A-Za-z0-9_\-/:]*)["']`)
)

func matchedBraceBlock(s string, endBraceIdx int) string {
	depth := 0
	for j := endBraceIdx; j >= 0; j-- {
		if s[j] == '}' {
			depth++
		} else if s[j] == '{' {
			depth--
			if depth == 0 {
				return s[j : endBraceIdx+1]
			}
		}
	}
	return ""
}

func parseJSChunkURLs(baseURL, jsText string) []string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	var pubs []string
	for _, m := range reconPublicPathRe.FindAllStringSubmatch(jsText, 20) {
		if len(m) > 1 && m[1] != "" {
			pubs = append(pubs, m[1])
		}
	}
	if len(pubs) == 0 {
		pubs = []string{"/assets/", "/static/js/", "/js/", "/"}
	}

	seen := make(map[string]struct{})
	addURL := func(raw string) {
		if raw != "" {
			seen[raw] = struct{}{}
		}
	}

	for _, loc := range reconWebpackMapRe.FindAllStringIndex(jsText, 30) {
		body := matchedBraceBlock(jsText, loc[0])
		if body == "" {
			continue
		}
		pairs := reconChunkPairRe.FindAllStringSubmatch(body, 2000)
		for _, pair := range pairs {
			cid, hash := pair[1], pair[2]
			for _, pp := range pubs {
				var prefix string
				if strings.HasPrefix(pp, "http://") || strings.HasPrefix(pp, "https://") {
					prefix = pp
				} else {
					if !strings.HasPrefix(pp, "/") {
						pp = "/" + pp
					}
					prefix = origin + pp
				}
				if !strings.HasSuffix(prefix, "/") {
					prefix += "/"
				}
				addURL(prefix + cid + "." + hash + ".js")
			}
		}
	}

	for _, m := range reconViteAssetRe.FindAllStringSubmatch(jsText, 2000) {
		fn := m[1]
		if ref, err := url.Parse(fn); err == nil {
			addURL(u.ResolveReference(ref).String())
		}
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func extractStaticEndpointsAndRoutes(jsText string) (apiPaths, otherPaths, routePaths []string) {
	pathSet := make(map[string]struct{})
	for _, m := range reconQuotedPathRe.FindAllStringSubmatch(jsText, 5000) {
		pathSet[m[1]] = struct{}{}
	}
	for _, m := range reconConcatPathRe.FindAllStringSubmatch(jsText, 2000) {
		pathSet[m[1]] = struct{}{}
	}
	for p := range pathSet {
		if reconAssetExtRe.MatchString(p) {
			continue
		}
		if strings.Count(p, "/") < 2 && !reconAPIHintRe.MatchString(p) {
			continue
		}
		if reconAPIHintRe.MatchString(p) {
			apiPaths = append(apiPaths, p)
		} else {
			otherPaths = append(otherPaths, p)
		}
	}
	routeSet := make(map[string]struct{})
	for _, m := range reconRouteKeyRe.FindAllStringSubmatch(jsText, 2000) {
		p := m[1]
		if !reconAssetExtRe.MatchString(p) && !reconAPIHintRe.MatchString(p) {
			routeSet[p] = struct{}{}
		}
	}
	for r := range routeSet {
		routePaths = append(routePaths, r)
	}
	return apiPaths, otherPaths, routePaths
}

func (t *ToolSet) runSPAHarvest(ctx context.Context, baseURL, outDir, cookie string) (map[string]any, error) {
	jsDir := filepath.Join(outDir, "js")
	if err := os.MkdirAll(jsDir, 0o755); err != nil {
		return nil, err
	}
	tr := t.getPooledTransport("", "", true)
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	fetchURL := func(target string) ([]byte, int) {
		rCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(rCtx, "GET", target, nil)
		if err != nil {
			return nil, 0
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (spa-api-recon)")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
		return data, resp.StatusCode
	}

	htmlBytes, status := fetchURL(baseURL)
	htmlStr := string(htmlBytes)
	_ = os.WriteFile(filepath.Join(outDir, "index.html"), htmlBytes, 0o644)

	baseParsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	seedSet := make(map[string]struct{})
	for _, re := range []*regexp.Regexp{reconScriptSrcRe, reconAnyJSRefRe} {
		for _, m := range re.FindAllStringSubmatch(htmlStr, 500) {
			s := strings.TrimSpace(m[1])
			if s == "" {
				continue
			}
			if ref, err := url.Parse(s); err == nil {
				seedSet[baseParsed.ResolveReference(ref).String()] = struct{}{}
			}
		}
	}

	var (
		mu   sync.Mutex
		have = make(map[string]string) // url -> local path
	)
	downloadBatch := func(urls []string) []string {
		var newFiles []string
		var wg sync.WaitGroup
		localSem := make(chan struct{}, 8)
		for _, u := range urls {
			if ctx.Err() != nil {
				break
			}
			mu.Lock()
			_, exists := have[u]
			mu.Unlock()
			if exists {
				continue
			}
			wg.Add(1)
			localSem <- struct{}{}
			globalHTTPFuzzSem <- struct{}{}
			go func(target string) {
				defer wg.Done()
				defer func() {
					<-globalHTTPFuzzSem
					<-localSem
				}()
				pu, err := url.Parse(target)
				if err != nil {
					return
				}
				fn := filepath.Base(pu.Path)
				if !strings.HasSuffix(strings.ToLower(fn), ".js") {
					return
				}
				data, st := fetchURL(target)
				trimmed := bytes.TrimSpace(data)
				if st == 200 && len(trimmed) > 0 && !bytes.HasPrefix(trimmed, []byte("<")) {
					lp := filepath.Join(jsDir, fn)
					if err := os.WriteFile(lp, data, 0o644); err == nil {
						mu.Lock()
						have[target] = lp
						newFiles = append(newFiles, lp)
						mu.Unlock()
					}
				}
			}(u)
		}
		wg.Wait()
		return newFiles
	}

	seedList := make([]string, 0, len(seedSet))
	for u := range seedSet {
		seedList = append(seedList, u)
	}
	sort.Strings(seedList)
	frontier := downloadBatch(seedList)

	seenURLs := make(map[string]struct{}, len(seedList))
	for _, u := range seedList {
		seenURLs[u] = struct{}{}
	}
	allManifest := make(map[string]struct{})
	for round := 0; round < 6 && len(frontier) > 0; round++ {
		var nextURLs []string
		for _, lp := range frontier {
			raw, err := os.ReadFile(lp)
			if err != nil {
				continue
			}
			for _, cu := range parseJSChunkURLs(baseURL, string(raw)) {
				allManifest[cu] = struct{}{}
				if _, ok := seenURLs[cu]; !ok {
					seenURLs[cu] = struct{}{}
					nextURLs = append(nextURLs, cu)
				}
			}
		}
		if len(nextURLs) == 0 {
			break
		}
		frontier = downloadBatch(nextURLs)
	}

	apiSet := make(map[string]struct{})
	otherSet := make(map[string]struct{})
	routeSet := make(map[string]struct{})
	for _, lp := range have {
		raw, err := os.ReadFile(lp)
		if err != nil {
			continue
		}
		a, o, r := extractStaticEndpointsAndRoutes(string(raw))
		for _, x := range a {
			apiSet[x] = struct{}{}
		}
		for _, x := range o {
			otherSet[x] = struct{}{}
		}
		for _, x := range r {
			routeSet[x] = struct{}{}
		}
	}

	writeSorted := func(filename string, m map[string]struct{}) []string {
		list := make([]string, 0, len(m))
		for k := range m {
			list = append(list, k)
		}
		sort.Strings(list)
		_ = os.WriteFile(filepath.Join(outDir, filename), []byte(strings.Join(list, "\n")), 0o644)
		return list
	}
	apis := writeSorted("api_static.txt", apiSet)
	others := writeSorted("paths_other.txt", otherSet)
	routes := writeSorted("routes.txt", routeSet)
	chunkNames := make(map[string]struct{}, len(allManifest))
	for u := range allManifest {
		if pu, err := url.Parse(u); err == nil {
			chunkNames[filepath.Base(pu.Path)] = struct{}{}
		}
	}
	_ = writeSorted("chunkmap.txt", chunkNames)

	return map[string]any{
		"mode":                "spa_harvest",
		"entry_url":           baseURL,
		"entry_status":        status,
		"outdir":              outDir,
		"html_seed_scripts":   len(seedList),
		"manifest_chunks":     len(allManifest),
		"downloaded_js_count": len(have),
		"api_endpoints_count": len(apis),
		"other_paths_count":   len(others),
		"routes_count":        len(routes),
		"api_endpoints_sample": func() []string {
			if len(apis) > 50 {
				return apis[:50]
			}
			return apis
		}(),
		"routes_sample": func() []string {
			if len(routes) > 50 {
				return routes[:50]
			}
			return routes
		}(),
	}, nil
}

func (t *ToolSet) runMPASpider(ctx context.Context, baseURL, outDir, cookie string, maxPages, maxDepth int, incStr, excStr string) (map[string]any, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	baseParsed, err := url.Parse(baseURL)
	if err != nil || baseParsed.Host == "" {
		return nil, fmt.Errorf("无效的起始 URL")
	}
	origin := baseParsed.Scheme + "://" + baseParsed.Host
	var incRe, excRe *regexp.Regexp
	if strings.TrimSpace(incStr) != "" {
		if r, err := regexp.Compile(incStr); err == nil {
			incRe = r
		}
	}
	if strings.TrimSpace(excStr) != "" {
		if r, err := regexp.Compile("(?i)" + excStr); err == nil {
			excRe = r
		}
	}

	tr := t.getPooledTransport("", "", true)
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}

	type queueItem struct {
		u     string
		depth int
	}
	startClean := strings.Split(baseURL, "#")[0]
	queue := []queueItem{{u: startClean, depth: 0}}
	seen := map[string]struct{}{startClean: {}}
	pagesSet := make(map[string]struct{})
	inlineSet := make(map[string]struct{})
	formsMap := make(map[string]map[string]struct{})

	crawled := 0
	for len(queue) > 0 && crawled < maxPages {
		if ctx.Err() != nil {
			break
		}
		cur := queue[0]
		queue = queue[1:]
		crawled++

		rCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(rCtx, "GET", cur.u, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (mpa-api-recon)")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") && ct != "" {
			resp.Body.Close()
			cancel()
			continue
		}
		rawHTML, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		resp.Body.Close()
		cancel()
		htmlText := string(rawHTML)

		relPage := strings.TrimPrefix(cur.u, origin)
		if relPage == "" {
			relPage = "/"
		}
		pagesSet[relPage] = struct{}{}

		for _, m := range reconQuotedPathRe.FindAllStringSubmatch(htmlText, 1000) {
			if reconAPIHintRe.MatchString(m[1]) && !reconAssetExtRe.MatchString(m[1]) {
				inlineSet[m[1]] = struct{}{}
			}
		}

		curParsed, _ := url.Parse(cur.u)
		for _, f := range extractHTMLForms(htmlText, 100) {
			act := f.Action
			if curParsed != nil {
				if ref, err := url.Parse(act); err == nil {
					act = curParsed.ResolveReference(ref).String()
				}
			}
			key := f.Method + " " + strings.TrimPrefix(act, origin)
			if formsMap[key] == nil {
				formsMap[key] = make(map[string]struct{})
			}
			for _, inp := range f.Inputs {
				if inp.Name != "" {
					formsMap[key][inp.Name] = struct{}{}
				}
			}
		}

		if cur.depth < maxDepth && curParsed != nil {
			doc, err := xhtml.Parse(strings.NewReader(htmlText))
			if err == nil {
				var walk func(*xhtml.Node)
				walk = func(n *xhtml.Node) {
					if n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, "a") {
						for _, attr := range n.Attr {
							if strings.EqualFold(attr.Key, "href") {
								href := strings.TrimSpace(attr.Val)
								if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") {
									continue
								}
								if ref, err := url.Parse(href); err == nil {
									nxt := strings.Split(curParsed.ResolveReference(ref).String(), "#")[0]
									if !strings.HasPrefix(nxt, origin) || reconAssetExtRe.MatchString(nxt) {
										continue
									}
									if excRe != nil && excRe.MatchString(nxt) {
										continue
									}
									if incRe != nil && !incRe.MatchString(nxt) {
										continue
									}
									if _, ok := seen[nxt]; !ok {
										seen[nxt] = struct{}{}
										queue = append(queue, queueItem{u: nxt, depth: cur.depth + 1})
									}
								}
							}
						}
					}
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						walk(c)
					}
				}
				walk(doc)
			}
		}
	}

	pages := make([]string, 0, len(pagesSet))
	for p := range pagesSet {
		pages = append(pages, p)
	}
	sort.Strings(pages)
	_ = os.WriteFile(filepath.Join(outDir, "pages.txt"), []byte(strings.Join(pages, "\n")), 0o644)

	inlines := make([]string, 0, len(inlineSet))
	for s := range inlineSet {
		inlines = append(inlines, s)
	}
	sort.Strings(inlines)
	_ = os.WriteFile(filepath.Join(outDir, "inline_apis.txt"), []byte(strings.Join(inlines, "\n")), 0o644)

	formsOut := make(map[string][]string, len(formsMap))
	for k, pset := range formsMap {
		var plist []string
		for p := range pset {
			plist = append(plist, p)
		}
		sort.Strings(plist)
		formsOut[k] = plist
	}
	fb, _ := json.MarshalIndent(formsOut, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "forms.json"), fb, 0o644)

	return map[string]any{
		"mode":              "mpa_spider",
		"outdir":            outDir,
		"crawled_pages":     crawled,
		"unique_pages":      len(pages),
		"forms_count":       len(formsOut),
		"inline_apis_count": len(inlines),
		"pages":             pages,
		"forms":             formsOut,
		"inline_apis":       inlines,
	}, nil
}

var routeMapEntryRe = regexp.MustCompile(`([A-Z_][A-Z0-9_]*)\s*:\s*\{\s*name\s*:\s*"([^"]*)"\s*,\s*link\s*:\s*"([^"]+)"`)

func runExtractRouteMap(jsDir, outDir string) (map[string]any, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(jsDir)
	if err != nil {
		return nil, err
	}
	best := make(map[string]map[string]string)
	bestFile := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".js") {
			continue
		}
		fp := filepath.Join(jsDir, e.Name())
		raw, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		hits := routeMapEntryRe.FindAllStringSubmatch(string(raw), 5000)
		if len(hits) > len(best) {
			cur := make(map[string]map[string]string, len(hits))
			for _, h := range hits {
				cur[h[1]] = map[string]string{"name": h[2], "link": h[3]}
			}
			best = cur
			bestFile = e.Name()
		}
	}
	outPath := filepath.Join(outDir, "route_map.json")
	b, _ := json.MarshalIndent(best, "", "  ")
	_ = os.WriteFile(outPath, b, 0o644)
	return map[string]any{
		"mode":         "route_map",
		"source_file":  bestFile,
		"routes_count": len(best),
		"output_file":  outPath,
	}, nil
}

var (
	permAliasRe     = regexp.MustCompile(`([a-zA-Z_$][\w$]*)=o\.([A-Z_0-9]+)\b`)
	permRouteAuthRe = regexp.MustCompile(`([A-Z_0-9]+):\{url:([^,}]+)(?:,control:(\[.*?\]|[^,}]+))?\}`)
	permIdentRe     = regexp.MustCompile(`([A-Za-z_$][\w$]*)`)
)

func runBuildPermTree(jsDir, outDir, configPath string) (map[string]any, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	routeMapPath := filepath.Join(outDir, "route_map.json")
	routeMap := make(map[string]map[string]string)
	if raw, err := os.ReadFile(routeMapPath); err == nil {
		_ = json.Unmarshal(raw, &routeMap)
	} else {
		_, _ = runExtractRouteMap(jsDir, outDir)
		if raw2, err2 := os.ReadFile(routeMapPath); err2 == nil {
			_ = json.Unmarshal(raw2, &routeMap)
		}
	}

	entries, err := os.ReadDir(jsDir)
	if err != nil {
		return nil, err
	}
	var authTxt, authFile string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".js") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(jsDir, e.Name()))
		if err != nil {
			continue
		}
		txt := string(raw)
		if strings.Contains(txt, "userRouteAuth") {
			authTxt = txt
			authFile = e.Name()
			if strings.Contains(txt, "userRouteAuth={MONITOR:") {
				break
			}
		}
	}
	if authTxt == "" {
		return map[string]any{
			"mode":    "perm_tree",
			"found":   false,
			"message": "未在 JS 目录中发现 userRouteAuth 结构",
		}, nil
	}

	idx := strings.Index(authTxt, "userRouteAuth")
	window := authTxt[idx:]
	if len(window) > 35000 {
		window = window[:35000]
	}
	aliases := make(map[string]string)
	for _, m := range permAliasRe.FindAllStringSubmatch(window, 1000) {
		aliases[m[1]] = m[2]
	}

	type routeAuthItem struct {
		Code    string   `json:"code"`
		URL     string   `json:"url"`
		Control []string `json:"control"`
	}
	parsedEntries := make(map[string]routeAuthItem)
	for _, m := range permRouteAuthRe.FindAllStringSubmatch(window, 2000) {
		key := m[1]
		rawURLTok := strings.TrimSpace(m[2])
		resolvedURL := strings.Trim(rawURLTok, `"'`)
		if aliasKey, ok := aliases[rawURLTok]; ok {
			if rm, ok2 := routeMap[aliasKey]; ok2 && rm["link"] != "" {
				resolvedURL = rm["link"]
			} else {
				resolvedURL = aliasKey
			}
		}
		var controls []string
		if len(m) > 3 && strings.TrimSpace(m[3]) != "" {
			for _, vm := range permIdentRe.FindAllStringSubmatch(m[3], 50) {
				v := vm[1]
				if ak, ok := aliases[v]; ok {
					controls = append(controls, ak)
				} else {
					controls = append(controls, v)
				}
			}
		}
		parsedEntries[key] = routeAuthItem{Code: key, URL: resolvedURL, Control: controls}
	}

	var codes []string
	for k := range parsedEntries {
		codes = append(codes, k)
	}
	sort.Strings(codes)

	type permNode struct {
		Code     string     `json:"code"`
		Position string     `json:"position"`
		Name     string     `json:"name"`
		Children []permNode `json:"children,omitempty"`
	}
	var tree []permNode
	for _, c := range codes {
		tree = append(tree, permNode{Code: c, Position: "left", Name: c})
	}

	writeJSONFile := func(name string, v any) {
		b, _ := json.MarshalIndent(v, "", "  ")
		_ = os.WriteFile(filepath.Join(outDir, name), b, 0o644)
	}
	writeJSONFile("userRouteAuth.json", parsedEntries)
	writeJSONFile("permissions_tree.json", tree)
	permAllStub := map[string]any{"code": 0, "msg": "success", "data": tree}
	rolePermStub := map[string]any{"code": 0, "msg": "success", "data": map[string]any{"permissions": codes, "role_type": 1}}
	writeJSONFile("permissions_all_stub.json", permAllStub)
	writeJSONFile("role_permissions_stub.json", rolePermStub)

	patchedConfig := false
	if configPath != "" {
		if rawCfg, err := os.ReadFile(configPath); err == nil {
			var cfg map[string]any
			if json.Unmarshal(rawCfg, &cfg) == nil {
				stubs, _ := cfg["stubs"].(map[string]any)
				if stubs == nil {
					stubs = make(map[string]any)
				}
				stubs["permissions/all"] = permAllStub
				stubs["role_permissions"] = rolePermStub
				cfg["stubs"] = stubs
				if nb, err := json.MarshalIndent(cfg, "", "  "); err == nil {
					_ = os.WriteFile(configPath, nb, 0o644)
					patchedConfig = true
				}
			}
		}
	}

	return map[string]any{
		"mode":              "perm_tree",
		"found":             true,
		"source_file":       authFile,
		"permissions_count": len(codes),
		"patched_config":    patchedConfig,
		"outdir":            outDir,
	}, nil
}

const graphqlIntrospectionQuery = `{"query":"query IntrospectionQuery { __schema { queryType { name } mutationType { name } subscriptionType { name } types { kind name fields(includeDeprecated: true) { name args { name type { kind name ofType { kind name } } } } } } }"}`

func (t *ToolSet) runGraphQLRecon(ctx context.Context, rawURL, cookie string) (map[string]any, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("无效的 URL: %s", rawURL)
	}
	origin := u.Scheme + "://" + u.Host
	candidates := []string{rawURL}
	if u.Path == "" || u.Path == "/" {
		candidates = []string{
			origin + "/graphql",
			origin + "/api/graphql",
			origin + "/v1/graphql",
			origin + "/gql",
			origin + "/query",
		}
	}

	tr := t.getPooledTransport("", "", true)
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}

	for _, ep := range candidates {
		rCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(rCtx, "POST", ep, strings.NewReader(graphqlIntrospectionQuery))
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (graphql-recon)")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		resp.Body.Close()
		cancel()

		bodyStr := string(raw)
		if !gjson.Get(bodyStr, "data.__schema").Exists() {
			continue
		}
		var exID string
		if t != nil && t.trafficStore != nil {
			exID = t.trafficStore.RecordDirect("POST", ep, resp.Proto, req.Header, []byte(graphqlIntrospectionQuery), resp.StatusCode, resp.Header, raw)
		}

		qName := gjson.Get(bodyStr, "data.__schema.queryType.name").String()
		mName := gjson.Get(bodyStr, "data.__schema.mutationType.name").String()
		var queries, mutations []map[string]any
		gjson.Get(bodyStr, "data.__schema.types").ForEach(func(_, v gjson.Result) bool {
			tName := v.Get("name").String()
			if tName != qName && tName != mName {
				return true
			}
			v.Get("fields").ForEach(func(_, f gjson.Result) bool {
				var args []string
				f.Get("args").ForEach(func(_, a gjson.Result) bool {
					args = append(args, a.Get("name").String())
					return true
				})
				item := map[string]any{
					"name": f.Get("name").String(),
					"args": args,
				}
				if tName == qName {
					queries = append(queries, item)
				} else {
					mutations = append(mutations, item)
				}
				return true
			})
			return true
		})

		// 探测 Array Batching 支持
		batchSupported := false
		bCtx, bCancel := context.WithTimeout(ctx, 8*time.Second)
		bReq, err := http.NewRequestWithContext(bCtx, "POST", ep, strings.NewReader(`[{"query":"{__typename}"},{"query":"{__typename}"}]`))
		if err == nil {
			bReq.Header.Set("Content-Type", "application/json")
			if cookie != "" {
				bReq.Header.Set("Cookie", cookie)
			}
			if bResp, err := client.Do(bReq); err == nil {
				bRaw, _ := io.ReadAll(io.LimitReader(bResp.Body, 64*1024))
				bResp.Body.Close()
				batchSupported = strings.HasPrefix(strings.TrimSpace(string(bRaw)), "[")
			}
		}
		bCancel()

		return map[string]any{
			"mode":                  "graphql",
			"endpoint":              ep,
			"exchange_id":           exID,
			"introspection_enabled": true,
			"batching_supported":    batchSupported,
			"queries_count":         len(queries),
			"mutations_count":       len(mutations),
			"queries":               queries,
			"mutations":             mutations,
		}, nil
	}

	return map[string]any{
		"mode":                  "graphql",
		"tested_endpoints":      candidates,
		"introspection_enabled": false,
		"message":               "未发现开启内省(__schema)的 GraphQL 端点",
	}, nil
}

func (t *ToolSet) runOpenAPIRecon(ctx context.Context, rawURL, filePath, cookie string) (map[string]any, error) {
	var specText, specSource string

	if filePath != "" {
		fp := t.resolveLocalPath(filePath)
		raw, err := os.ReadFile(fp)
		if err != nil {
			return nil, err
		}
		specText = string(raw)
		specSource = fp
	} else {
		if rawURL == "" {
			return nil, fmt.Errorf("mode=openapi 时请提供 url 或 file_path")
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			rawURL = "https://" + rawURL
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("无效的 URL: %s", rawURL)
		}
		origin := u.Scheme + "://" + u.Host
		candidates := []string{rawURL}
		if u.Path == "" || u.Path == "/" {
			candidates = []string{
				origin + "/v3/api-docs",
				origin + "/v2/api-docs",
				origin + "/swagger.json",
				origin + "/openapi.json",
				origin + "/api/swagger.json",
				origin + "/api/openapi.json",
				origin + "/swagger/v1/swagger.json",
				origin + "/api-docs",
			}
		}
		tr := t.getPooledTransport("", "", true)
		client := &http.Client{Transport: tr, Timeout: 15 * time.Second}
		for _, c := range candidates {
			rCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			req, err := http.NewRequestWithContext(rCtx, "GET", c, nil)
			if err != nil {
				cancel()
				continue
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (openapi-recon)")
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}
			resp, err := client.Do(req)
			if err != nil {
				cancel()
				continue
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
			resp.Body.Close()
			cancel()
			txt := string(raw)
			if gjson.Get(txt, "paths").Exists() && (gjson.Get(txt, "openapi").Exists() || gjson.Get(txt, "swagger").Exists()) {
				specText = txt
				specSource = c
				if t != nil && t.trafficStore != nil {
					_ = t.trafficStore.RecordDirect("GET", c, resp.Proto, req.Header, nil, resp.StatusCode, resp.Header, raw)
				}
				break
			}
		}
	}

	if specText == "" {
		return map[string]any{
			"mode":    "openapi",
			"found":   false,
			"message": "未找到有效的 Swagger / OpenAPI JSON 规范文档",
		}, nil
	}

	type apiOp struct {
		Path    string   `json:"path"`
		Method  string   `json:"method"`
		Summary string   `json:"summary,omitempty"`
		Params  []string `json:"params,omitempty"`
	}
	var ops []apiOp
	gjson.Get(specText, "paths").ForEach(func(pathKey, methodsVal gjson.Result) bool {
		methodsVal.ForEach(func(mKey, opVal gjson.Result) bool {
			mUpper := strings.ToUpper(mKey.String())
			switch mUpper {
			case "GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "HEAD":
				var params []string
				opVal.Get("parameters").ForEach(func(_, p gjson.Result) bool {
					if pn := p.Get("name").String(); pn != "" {
						params = append(params, p.Get("in").String()+":"+pn)
					}
					return true
				})
				ops = append(ops, apiOp{
					Path:    pathKey.String(),
					Method:  mUpper,
					Summary: clipUTF8(opVal.Get("summary").String(), 120),
					Params:  params,
				})
			}
			return true
		})
		return true
	})

	return map[string]any{
		"mode":            "openapi",
		"found":           true,
		"source":          specSource,
		"version":         firstNonEmpty(gjson.Get(specText, "openapi").String(), gjson.Get(specText, "swagger").String()),
		"title":           gjson.Get(specText, "info.title").String(),
		"endpoints_count": len(ops),
		"endpoints":       ops,
	}, nil
}

func (t *ToolSet) runSourceMapUnpack(ctx context.Context, rawURL, filePath, outDir, cookie string) (map[string]any, error) {
	var rawMap []byte
	var srcLabel string
	if filePath != "" {
		fp := t.resolveLocalPath(filePath)
		b, err := os.ReadFile(fp)
		if err != nil {
			return nil, err
		}
		rawMap = b
		srcLabel = fp
	} else if rawURL != "" {
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			rawURL = "https://" + rawURL
		}
		tr := t.getPooledTransport("", "", true)
		client := &http.Client{Transport: tr, Timeout: 25 * time.Second}
		rCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(rCtx, "GET", rawURL, nil)
		if err != nil {
			return nil, err
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		rawMap, _ = io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
		srcLabel = rawURL
	} else {
		return nil, fmt.Errorf("mode=sourcemap 时请提供 file_path 或 url")
	}

	var sm struct {
		Sources        []string `json:"sources"`
		SourcesContent []string `json:"sourcesContent"`
	}
	if err := json.Unmarshal(rawMap, &sm); err != nil {
		return nil, fmt.Errorf("解析 SourceMap JSON 失败: %w", err)
	}

	destRoot := filepath.Join(outDir, "sources")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return nil, err
	}

	var written []string
	for i, srcPath := range sm.Sources {
		if i >= len(sm.SourcesContent) || sm.SourcesContent[i] == "" {
			continue
		}
		cleanRel := strings.TrimPrefix(srcPath, "webpack:///")
		cleanRel = strings.TrimPrefix(cleanRel, "vite:///")
		cleanRel = strings.ReplaceAll(cleanRel, "../", "")
		cleanRel = strings.TrimLeft(cleanRel, "/\\.")
		if cleanRel == "" || strings.Contains(cleanRel, "node_modules") {
			continue
		}
		target, err := safeJoinDest(destRoot, cleanRel)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(target, []byte(sm.SourcesContent[i]), 0o644); err == nil {
			written = append(written, target)
		}
		if len(written) >= 500 {
			break
		}
	}

	return map[string]any{
		"mode":           "sourcemap",
		"source":         srcLabel,
		"total_sources":  len(sm.Sources),
		"unpacked_count": len(written),
		"dest_dir":       destRoot,
		"unpacked_sample": func() []string {
			if len(written) > 50 {
				return written[:50]
			}
			return written
		}(),
	}, nil
}

func (t *ToolSet) runWebFingerprint(ctx context.Context, rawURL, cookie string) (map[string]any, error) {
	tr := t.getPooledTransport("", "", true)
	client := &http.Client{
		Transport: tr,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return nil
		},
	}

	rCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rCtx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Cookie", strings.TrimSpace("rememberMe=1; "+cookie))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	resp.Body.Close()
	bodyStr := string(raw)

	var exID string
	if t != nil && t.trafficStore != nil {
		exID = t.trafficStore.RecordDirect("GET", rawURL, resp.Proto, req.Header, nil, resp.StatusCode, resp.Header, raw)
	}

	var techs []string
	var wafs []string
	addTech := func(name string) {
		for _, x := range techs {
			if x == name {
				return
			}
		}
		techs = append(techs, name)
	}

	hdrStr := ""
	for k, vs := range resp.Header {
		hdrStr += strings.ToLower(k) + ": " + strings.ToLower(strings.Join(vs, "; ")) + "\n"
	}
	lb := strings.ToLower(bodyStr)

	if srv := resp.Header.Get("Server"); srv != "" {
		addTech("Server:" + srv)
	}
	if xpb := resp.Header.Get("X-Powered-By"); xpb != "" {
		addTech("X-Powered-By:" + xpb)
	}
	if strings.Contains(hdrStr, "rememberme=deleteme") || strings.Contains(hdrStr, "shiro") {
		addTech("Apache Shiro")
	}
	if strings.Contains(hdrStr, "jsessionid") || strings.Contains(lb, "whitelabel error page") {
		addTech("Java / Spring")
	}
	if strings.Contains(hdrStr, "phpsessid") {
		addTech("PHP")
	}
	if strings.Contains(hdrStr, "thinkphp") || strings.Contains(lb, "thinkphp") {
		addTech("ThinkPHP")
	}
	if strings.Contains(hdrStr, "laravel_session") {
		addTech("Laravel")
	}
	if strings.Contains(hdrStr, "csrftoken") && strings.Contains(hdrStr, "sessionid") {
		addTech("Django")
	}
	if strings.Contains(hdrStr, "asp.net") || strings.Contains(hdrStr, "aspsessionid") {
		addTech("ASP.NET")
	}
	if strings.Contains(lb, "__next_data__") || strings.Contains(lb, "/_next/static/") {
		addTech("Next.js (React)")
	}
	if strings.Contains(lb, "__nuxt") || strings.Contains(lb, "/_nuxt/") {
		addTech("Nuxt.js (Vue)")
	}
	if strings.Contains(lb, "vue") || strings.Contains(lb, "data-v-") {
		addTech("Vue.js")
	}
	if strings.Contains(lb, "react") || strings.Contains(lb, "data-reactroot") {
		addTech("React")
	}
	if strings.Contains(lb, "ng-version") || strings.Contains(lb, "ng-app") {
		addTech("Angular")
	}
	if strings.Contains(lb, "swagger-ui") {
		addTech("Swagger UI")
	}

	if strings.Contains(hdrStr, "cf-ray") || strings.Contains(hdrStr, "cloudflare") {
		wafs = append(wafs, "Cloudflare")
	}
	if strings.Contains(hdrStr, "x-powered-by-360wzb") || strings.Contains(hdrStr, "wzws-ray") {
		wafs = append(wafs, "360/WangZhanBao WAF")
	}
	if strings.Contains(hdrStr, "aliyun") || strings.Contains(lb, "errors.aliyun.com") {
		wafs = append(wafs, "Aliyun WAF")
	}
	if strings.Contains(hdrStr, "safeline") || strings.Contains(lb, "safeline") {
		wafs = append(wafs, "SafeLine WAF")
	}

	title := ""
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(bodyStr); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}

	return map[string]any{
		"mode":           "fingerprint",
		"url":            rawURL,
		"exchange_id":    exID,
		"status":         resp.StatusCode,
		"content_length": len(raw),
		"page_title":     title,
		"technologies":   techs,
		"waf_detected":   wafs,
		"missing_security_headers": func() []string {
			var missing []string
			for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "Strict-Transport-Security"} {
				if resp.Header.Get(h) == "" {
					missing = append(missing, h)
				}
			}
			return missing
		}(),
	}, nil
}
