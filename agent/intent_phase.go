package agent

// 意图阶段推断 + 工具面按能力裁剪（第四阶段：工具分域 / 惰性挂载）。
//
// 背景：worker 的工具面此前只有两条路——「planner 在 add_intent 里显式写了
// phase」或「全量 25 个工具」。实测（lean 瘦身之后的口径）全量面是 24.8 KB
// 工具描述 + 3.2 KB SDK 工具 + 8.5 KB 系统提示 ≈ 11.4K token；而按阶段裁剪后
// 只有 7.9–9.3K。也就是说「按需给工具」这件事本身是对的，缺的只是**阶段经常
// 没被声明**：phase 是可选字段，模型大多不填，于是每次都退回全量面。
//
// 这里做的是纯代码层的补齐，**不动任何工具描述、不改系统提示、不加新工具**：
//
//  1. 推断：planner 没写 phase 时，从意图自己的文字（summary + expected_evidence）
//     里按关键词推断阶段。推断不出来就**保持全量**——宁可多花 token，也不能让
//     worker 因为猜错阶段而拿不到干活要用的工具。
//  2. 细化：exploit 阶段内部再按「专用能力」二次裁剪（fuzz / crypto / extract /
//     binary）。这些工具各自只服务一类动作，意图里没提就不挂；通用工具与写入类
//     工具（record_fact / report_finding / handoff_patch …）永远保留。
//
// 推断的优先级是 exploit > recon > verify，理由是**能力覆盖**：exploit 面同时
// 带 http_request/http_sequence/codec/extract/资产工具，猜成 exploit 最多是多花
// token；而 verify 面最窄（没有 net_probe/web_recon/http_sequence），猜错会真的
// 让 worker 干不了活。所以 verify 只在完全没有其它信号时才用。

import (
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// intentSignalText 把一条意图里能说明「它要干什么」的文字拼成一段小写文本。
//
// 只取 summary 与 expected_evidence：payload 里其余字段（asset_ids / parent_ids /
// priority）是结构性引用，不携带动作语义。两者都空 = 无从判断。
func intentSignalText(n *db.Node) string {
	if n == nil || len(n.Payload) == 0 {
		return ""
	}
	var p struct {
		Summary          string `json:"summary"`
		ExpectedEvidence string `json:"expected_evidence"`
	}
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p.Summary + " " + p.ExpectedEvidence))
}

// 关键词表。中文按「动词/动作名词」取，英文只取足够独特的词（避免 pe 命中 spec
// 这类子串误判）。命中即计数，不看位置。
var (
	// exploitSignals：一旦出现，说明这条意图要「打」而不是「看」。
	exploitSignals = []string{
		"利用", "攻击", "打点", "拿下", "拿权限", "getshell", "get shell", "落shell",
		"提权", "注入", "sqli", "sql 注入", "sql注入", "命令执行", "rce", "反序列化",
		"上传", "绕过", "越权", "idor", "ssrf", "xxe", "csrf", "爆破", "暴力破解",
		"弱口令", "撞库", "破解", "payload", "exploit", "poc", "横向", "打穿",
		"未授权访问", "任意文件", "文件读取", "jndi", "log4j", "fastjson", "shiro",
		"jwt 伪造", "伪造 token", "拿 flag", "拿flag", "提数据", "拖库",
	}
	// verifySignals：复核/复现/判断真伪。
	verifySignals = []string{
		"验证", "复核", "复测", "二次确认", "二次验证", "确认是否", "核实",
		"误报", "排除", "证实", "证伪", "确认漏洞", "复现", "验证码", "打码",
		"是否成立", "是否真实", "真伪",
	}
	// reconSignals：面测绘/枚举/收集。
	reconSignals = []string{
		"侦察", "信息收集", "收集", "枚举", "扫描", "探测", "子域", "端口",
		"目录", "指纹", "存活", "资产", "爬取", "抓取", "测绘", "banner",
		"服务识别", "whois", "dns 记录", "旁站", "c 段", "c段", "inventory",
		"recon", "scan", "enumerate", "discover", "fingerprint", "subdomain",
	}
)

func countSignals(text string, words []string) int {
	if text == "" {
		return 0
	}
	n := 0
	for _, w := range words {
		if strings.Contains(text, w) {
			n++
		}
	}
	return n
}

// inferIntentPhase 在 planner 没有声明 phase 时，从意图文字推断阶段。
//
// 返回空串表示「判断不了」——调用方（intentPhase）会退回全量工具面，与改造前
// 的行为完全一致。这里是**宁可判不出，也不要判错**：判错会让 worker 缺工具，
// 那比多花 token 严重得多。
func inferIntentPhase(n *db.Node) string {
	text := intentSignalText(n)
	if text == "" {
		return ""
	}
	// 优先级见文件头：能力覆盖从宽到窄。
	if countSignals(text, exploitSignals) > 0 {
		return workerPhaseExploit
	}
	if countSignals(text, reconSignals) > 0 {
		return workerPhaseRecon
	}
	if countSignals(text, verifySignals) > 0 {
		return workerPhaseVerify
	}
	return ""
}

// ---- exploit 阶段内的专用能力裁剪 ----

// 专用能力标签。每种能力对应一组关键词：意图里没有这类信号，就把只服务这类
// 动作的工具摘掉（它们的通用替代品 http_request / http_sequence / bash 都还在）。
const (
	specFuzz    = "fuzz"    // 参数爆破、字典枚举、ID 遍历
	specCrypto  = "crypto"  // 编解码、签名、JWT、加解密
	specExtract = "extract" // 结构化提取、正则抽取
	specBinary  = "binary"  // 二进制/固件/APK
)

var specialistSignals = map[string][]string{
	specFuzz: {
		"爆破", "暴力", "猜解", "字典", "fuzz", "枚举", "遍历", "弱口令", "撞库",
		"brute", "wordlist", "enum",
	},
	specCrypto: {
		"编码", "解码", "加密", "解密", "签名", "验签", "哈希", "摘要",
		"base64", "hex", "jwt", "token", "aes", "rsa", "cbc", "ecb", "sm4",
		"国密", "encode", "decode", "cipher", "sign", "hmac",
	},
	specExtract: {
		"提取", "抽取", "正则", "解析", "格式化", "抽取字段", "parse", "extract", "regex",
	},
	specBinary: {
		"二进制", "反编译", "逆向", "固件", "汇编", "脱壳", "elf", "apk", "pe 文件",
		"binwalk", "ida", "ghidra",
	},
}

// specialistTools 把「只服务单一能力的工具名」映射到能力标签。
// 不在表里的工具永远不会被本层摘掉。
var specialistTools = map[string]string{
	"http_fuzz":      specFuzz,
	"codec_crypto":   specCrypto,
	"extract_data":   specExtract,
	"binary_inspect": specBinary,
}

// pruneSpecialistTools 按意图文字裁剪 exploit 面里的专用工具。
//
// hint 为空（意图没有可读文字）时**原样返回**：判断不了就不裁，保持老行为。
func pruneSpecialistTools(tools []actool.CoreTool, hint string) []actool.CoreTool {
	if strings.TrimSpace(hint) == "" || len(tools) == 0 {
		return tools
	}
	keep := map[string]bool{}
	for spec, words := range specialistSignals {
		if countSignals(hint, words) > 0 {
			keep[spec] = true
		}
	}
	out := make([]actool.CoreTool, 0, len(tools))
	for _, t := range tools {
		spec, isSpecialist := specialistTools[t.Name()]
		if isSpecialist && !keep[spec] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ---- 可观测性 ----

// toolFaceLogged 让每个「不同的工具面」只打一次日志：工具面按阶段基本固定，
// 每个意图都打会刷屏，但对账时又必须能看见到底挂了哪些工具。
var toolFaceLogged sync.Map

// logToolFace 打印一次某阶段实际下发的工具清单（同一组合只打一次）。
func logToolFace(phase string, tools []actool.CoreTool) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	sort.Strings(names)
	label := phase
	if label == "" {
		label = "all(未判定)"
	}
	key := label + "#" + strings.Join(names, ",")
	if _, loaded := toolFaceLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[tools] worker 工具面 phase=%s n=%d: %s", label, len(names), strings.Join(names, ","))
}
