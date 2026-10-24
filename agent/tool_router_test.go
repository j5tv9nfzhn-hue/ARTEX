package agent

import (
	"fmt"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// 这组测试钉住的是「路由判得对」这件事本身，而不是某个固定的工具组合。
// 换言之：断言全部是**否命题**（扫端口时不能挂注入引擎）与**不变量**
// （控制面永远在、判不出来就全量、顺序稳定），没有一条要求「意图 X 必须
// 得到 Y 这个清单」——那又会变成硬编码。

// routerFake 造一个只有名字/说明的工具：路由只看工具怎么描述自己。
func routerFake(name, desc string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        name,
		Description: desc,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	})
}

// routerCatalog 是「一次真实 run 会看到的工具面」：原生 Go 工具（说明取自
// 当前代码，不是测试里另抄一份）+ 仿真 MCP 工具（形态照 2026-10-24 的
// tool_usage：mcp__artex-webscan__http_send 是窗口内调用最多的工具）。
//
// 特意把 MCP 工具混进来：AugmentTools 正是把 MCP 工具并回基础面、把阶段裁剪
// 整体抵消的地方（见 tool_router.go 顶部注释），路由必须在合并后的完整面上
// 仍然判得对。
func routerCatalog(t *testing.T) []actool.CoreTool {
	t.Helper()
	ts := NewToolSet(nil, "worker")
	out := append([]actool.CoreTool{}, ts.allWorkerTools()...)
	out = append(out,
		routerFake("mcp__artex-webscan__sqli_probe",
			"SQL 注入探测：布尔盲注、时间盲注、报错注入与 WAF 绕过。参数 url、param、payload、technique、timeout。"),
		routerFake("mcp__artex-webscan__xss_probe",
			"跨站脚本探测：反射型与存储型 XSS，payload 编码与过滤器绕过。参数 url、param、payload。"),
		routerFake("mcp__artex-webscan__http_send",
			"发送任意 HTTP 请求并返回状态码、响应头与响应体。参数 url、method、headers、body、cookies。"),
		routerFake("mcp__artex-dns__dns_lookup",
			"DNS 记录查询与区域传送：A、AAAA、CNAME、TXT、MX、NS。参数 domain、record_type、nameserver。"),
		routerFake("mcp__artex-nmap__port_scan",
			"端口扫描与服务版本指纹识别，可加载 NSE 脚本。参数 host、ports、scripts、timing。"),
	)
	if len(out) <= toolRouteTopNDefault {
		t.Fatalf("测试目录只有 %d 个工具，不足以验证裁剪（需 > %d）", len(out), toolRouteTopNDefault)
	}
	return out
}

func routeNames(tools []actool.CoreTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

func routeIndexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

// routeFind 返回所有名字含 needle（小写比较）的工具名。
func routeFind(names []string, needle string) []string {
	var out []string
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), needle) {
			out = append(out, n)
		}
	}
	return out
}

func routeDescBytes(tools []actool.CoreTool) int {
	n := 0
	for _, t := range tools {
		n += len(t.Description())
	}
	return n
}

// routeDump 在断言失败时把完整排名打出来，省得再猜「到底挂上了谁」。
func routeDump(t *testing.T, intent string, catalog, got []actool.CoreTool) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "意图: %s\n下发的工具: %s\n排名前 25:\n", intent, strings.Join(routeNames(got), ", "))
	for i, h := range RankTools(intent, catalog) {
		if i >= 25 {
			break
		}
		fmt.Fprintf(&b, "  %-44s %.3f\n", h.name, h.score)
	}
	t.Log(b.String())
}

// TestRoutePortScanDoesNotAttachInjectionEngine 是用户的验收条件之一：
// 「扫描端口」不得挂 sqli_probe。
//
// 注意它同时是**双向**的：注入引擎必须真的被摘掉（否则路由等于没做事），
// 端口能力必须真的在（否则是过度裁剪，worker 会卡死）。
func TestRoutePortScanDoesNotAttachInjectionEngine(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	intent := "扫描 piax-api.piax.org 的 80/443/8443 端口，识别服务 Banner 与 TLS 证书 SAN"
	got := RouteToolFace(intent, catalog)
	names := routeNames(got)

	if routeIndexOf(names, "net_probe") < 0 {
		routeDump(t, intent, catalog, got)
		t.Fatalf("扫端口的意图没有挂上 net_probe：%v", names)
	}
	if bad := routeFind(names, "sqli"); len(bad) > 0 {
		routeDump(t, intent, catalog, got)
		t.Fatalf("扫端口的意图挂上了 SQL 注入引擎：%v", bad)
	}
	if len(got) >= len(catalog) {
		t.Fatalf("工具面没有被裁剪（%d/%d），路由没有生效", len(got), len(catalog))
	}
	if len(got) < toolRouteMinTools {
		t.Fatalf("裁得太狠：只剩 %d 个工具（下限 %d）：%v", len(got), toolRouteMinTools, names)
	}
}

// TestRouteAuthzTestDoesNotAttachDNSEngine 是第二条验收条件：
// 「测越权」不得挂 dns_probe。
func TestRouteAuthzTestDoesNotAttachDNSEngine(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	intent := "用 A 用户的 JWT 访问 B 用户的订单接口 /api/order/{id}，验证水平越权 IDOR"
	got := RouteToolFace(intent, catalog)
	names := routeNames(got)

	if bad := routeFind(names, "dns"); len(bad) > 0 {
		routeDump(t, intent, catalog, got)
		t.Fatalf("测越权的意图挂上了 DNS 工具：%v", bad)
	}
	// 越权验证的核心动作是「换一个身份再发一次同一个请求」。
	if routeIndexOf(names, "http_sequence") < 0 && routeIndexOf(names, "http_request") < 0 {
		routeDump(t, intent, catalog, got)
		t.Fatalf("测越权的意图连 HTTP 发包工具都没有：%v", names)
	}
	if len(got) >= len(catalog) {
		t.Fatalf("工具面没有被裁剪（%d/%d），路由没有生效", len(got), len(catalog))
	}
}

// TestRouteInducementToolsAreIntentScoped：诱导面工具只在「目标有 AI 交互面」
// 时挂上；扫端口时挂一个多轮对话工具是纯噪声（它连的还是错的入口）。
func TestRouteInducementToolsAreIntentScoped(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)

	chat := routeNames(RouteToolFace("目标站的智能客服/知识库问答，试着套出系统提示词与后端配置", catalog))
	if routeIndexOf(chat, "ai_chat_probe") < 0 {
		t.Fatalf("AI 对话意图没有挂 ai_chat_probe：%v", chat)
	}
	doc := routeNames(RouteToolFace("上传一份带隐藏指令的合同，看 AI 解析后会不会执行", catalog))
	if routeIndexOf(doc, "doc_inject") < 0 {
		t.Fatalf("文档解析意图没有挂 doc_inject：%v", doc)
	}

	scan := routeNames(RouteToolFace("扫描 80/443 端口与服务指纹", catalog))
	for _, bad := range []string{"ai_chat_probe", "doc_inject"} {
		if routeIndexOf(scan, bad) >= 0 {
			t.Fatalf("扫端口的意图挂上了诱导面工具 %s：%v", bad, scan)
		}
	}
}

// TestRouteKeepsFallbackBreadth：最小可用面。专用工具只有一个（sqli_probe）
// 时，至少还要留一个能换路子的发包工具——否则它一坏，worker 就彻底没招了。
func TestRouteKeepsFallbackBreadth(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	got := RouteToolFace("对 /login 做 SQL 注入布尔盲注", catalog)
	specialists := 0
	for _, name := range routeNames(got) {
		if _, isControl := controlPlane[name]; !isControl {
			specialists++
		}
	}
	if specialists < 2 {
		routeDump(t, "对 /login 做 SQL 注入布尔盲注", catalog, got)
		t.Fatalf("专用工具只剩 %d 个，没有退路可言：%v", specialists, routeNames(got))
	}
}

// TestRouteAlwaysKeepsControlPlane：少挂一个专用工具只是这一轮不方便，少挂
// record_fact / add_finding 就是「测出来了也交不出来」。控制面与意图无关。
func TestRouteAlwaysKeepsControlPlane(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	intents := []string{
		"扫描 80/443 端口与服务指纹",
		"用 A 用户 token 访问 B 用户订单接口测越权",
		"对 /login 做 SQL 注入布尔盲注",
		"dump 前端 JS 里的接口与硬编码密钥",
		"分析这份 JWT 的签名算法与 alg=none 可能性",
		"读一下 /etc/passwd",
	}
	var want []string
	for name := range controlPlane {
		for _, ct := range catalog {
			if ct.Name() == name {
				want = append(want, name)
				break
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("测试目录里一个控制面工具都没有，测试无效")
	}
	for _, intent := range intents {
		got := routeNames(RouteToolFace(intent, catalog))
		for _, name := range want {
			if routeIndexOf(got, name) < 0 {
				t.Fatalf("意图 %q 丢掉了控制面工具 %s", intent, name)
			}
		}
	}
}

// TestRouteEmptyIntentKeepsFullFace：判不出意图时一个都不摘。宁可多花 token，
// 也不能让 worker 在毫无提示的情况下失去能力。
func TestRouteEmptyIntentKeepsFullFace(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	for _, intent := range []string{"", "   ", "\n\t"} {
		got := RouteToolFace(intent, catalog)
		if len(got) != len(catalog) {
			t.Fatalf("空意图 %q 被裁剪了：%d/%d", intent, len(got), len(catalog))
		}
	}
}

// TestRouteUnrelatedIntentFallsBackToFullFace：与工具目录毫无关系的意图
// （判不出任何有意义的词项）必须退回全量，而不是下发一个近乎空的工具面。
func TestRouteUnrelatedIntentFallsBackToFullFace(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	got := RouteToolFace("把今天的天气写成一首五言绝句", catalog)
	if len(got) != len(catalog) {
		routeDump(t, "把今天的天气写成一首五言绝句", catalog, got)
		t.Fatalf("无关意图没有退回全量：%d/%d", len(got), len(catalog))
	}
}

// TestRouteSmallCatalogUntouched：目录本来就不大时裁剪没有收益，只有风险。
func TestRouteSmallCatalogUntouched(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	small := []actool.CoreTool{
		routerFake("net_probe", "端口扫描与服务 Banner 探测"),
		routerFake("dns_probe", "DNS 记录查询与子域枚举"),
		routerFake("add_finding", "写回漏洞"),
	}
	got := RouteToolFace("扫描端口", small)
	if len(got) != len(small) {
		t.Fatalf("小目录被裁剪了：%d/%d", len(got), len(small))
	}
}

// TestRouteDisabledByEnv：排障开关必须能一键回到旧行为（全量面）。
func TestRouteDisabledByEnv(t *testing.T) {
	for _, v := range []string{"0", "false", "off", "OFF", " no "} {
		t.Setenv("ARTEX_TOOL_ROUTE", v)
		catalog := routerCatalog(t)
		if got := RouteToolFace("扫描端口与 TLS 证书", catalog); len(got) != len(catalog) {
			t.Fatalf("ARTEX_TOOL_ROUTE=%q 时仍被裁剪：%d/%d", v, len(got), len(catalog))
		}
	}
}

// TestRouteKeepsCatalogOrder：顺序稳定才能保住 prompt cache 的前缀；
// 结果必须是原目录的子序列，不能重排。
func TestRouteKeepsCatalogOrder(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	got := RouteToolFace("对登录接口做 SQL 注入测试", catalog)
	prev := -1
	for _, ct := range got {
		idx := -1
		for i, c := range catalog {
			if c.Name() == ct.Name() {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("路由返回了目录里没有的工具 %s", ct.Name())
		}
		if idx <= prev {
			t.Fatalf("顺序被打乱：%s 出现在位置 %d（上一个 %d）", ct.Name(), idx, prev)
		}
		prev = idx
	}
}

// TestRouteCutsDescriptionVolume 量化收益：这是要解决的原始问题——工具定义
// 把上下文吃掉了（实测 MCP 工具定义曾占窗口 72%）。
func TestRouteCutsDescriptionVolume(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE", "")
	catalog := routerCatalog(t)
	intent := "扫描 80/443 端口并识别服务 Banner"
	got := RouteToolFace(intent, catalog)
	before, after := routeDescBytes(catalog), routeDescBytes(got)
	if after >= before*3/4 {
		t.Fatalf("工具定义体积只降了 %.0f%%（%d→%d 字节），路由没起到作用",
			100*(1-float64(after)/float64(before)), before, after)
	}
	t.Logf("工具面 %d→%d 个，定义体积 %d→%d 字节（-%.1f%%）",
		len(catalog), len(got), before, after, 100*(1-float64(after)/float64(before)))
}

// TestRouteTopNClamp：上界参数不得把工具面裁到不可用，也不得无限制膨胀。
func TestRouteTopNClamp(t *testing.T) {
	t.Setenv("ARTEX_TOOL_ROUTE_TOPN", "")
	if n := toolRouteTopN(); n != toolRouteTopNDefault {
		t.Fatalf("默认 TopN = %d，期望 %d", n, toolRouteTopNDefault)
	}
	t.Setenv("ARTEX_TOOL_ROUTE_TOPN", "1")
	if n := toolRouteTopN(); n != toolRouteMinTools {
		t.Fatalf("TopN 下限应为 %d，得到 %d", toolRouteMinTools, n)
	}
	t.Setenv("ARTEX_TOOL_ROUTE_TOPN", "9999")
	if n := toolRouteTopN(); n != 120 {
		t.Fatalf("TopN 上限应为 120，得到 %d", n)
	}
	t.Setenv("ARTEX_TOOL_ROUTE_TOPN", "不是数字")
	if n := toolRouteTopN(); n != toolRouteTopNDefault {
		t.Fatalf("非法 TopN 应退回默认值，得到 %d", n)
	}
}

// TestRouteTokens：中文没有空格分词，二元组是保住局部语义的最低成本做法；
// 拉丁词项按非字母数字切并以长度 2 起收，避免把 a / 的 这类噪音塞进向量。
func TestRouteTokens(t *testing.T) {
	got := routeTokens("端口扫描 port_scan A 80")
	want := map[string]bool{"端口": false, "口扫": false, "扫描": false, "port": false, "scan": false}
	for _, tk := range got {
		if _, ok := want[tk]; ok {
			want[tk] = true
		}
		if tk == "80" {
			continue
		}
		if len([]rune(tk)) == 1 {
			t.Fatalf("单字符词项混进了向量：%q（全部 %v）", tk, got)
		}
	}
	for tk, hit := range want {
		if !hit {
			t.Fatalf("缺少词项 %q（全部 %v）", tk, got)
		}
	}
}

// TestWithRouteIntentPropagates：意图靠 context 传到 AugmentTools，空串不覆盖。
func TestWithRouteIntentPropagates(t *testing.T) {
	if routeIntentFrom(nil) != "" {
		t.Fatal("nil ctx 不应带出意图")
	}
	if c := WithRouteIntent(nil, "   "); c != nil {
		t.Fatal("空白意图 + nil ctx 应原样返回 nil（不新建 context）")
	}
	ctx := WithRouteIntent(nil, "扫描端口") // 故意验证 nil ctx 不 panic
	if got := routeIntentFrom(ctx); got != "扫描端口" {
		t.Fatalf("意图未透传：%q", got)
	}
	if got := routeIntentFrom(WithRouteIntent(ctx, "   ")); got != "扫描端口" {
		t.Fatalf("空白意图不应覆盖已有意图，得到 %q", got)
	}
	if got := routeIntentFrom(WithRouteIntent(ctx, "测越权")); got != "测越权" {
		t.Fatalf("新意图应覆盖旧意图，得到 %q", got)
	}
}
