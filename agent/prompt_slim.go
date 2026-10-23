package agent

// 批次 E:自动注入 prompt 的瘦身工具(输入 token 削减)。
//
// 原则:工具取数路径(graph_overview / list_* / node_detail / list_assets)保持原有
// 全量能力不变——模型要细节随时按需查;被削的只有【每轮/每唤醒自动注入】的部分:
//   - planner 每轮 user 输入里的态势 JSON(renderGraphOverview);
//   - worker 首条 user 消息里的全局概览(renderWorkerGraphOverview);
//   - worker system 里的意图块(意图原文 + 锚定资产紧凑视图,见 worker.go)。
// 这些内容每轮/每 run 重复计费,是输入 token 的大头;列表 cap 与字节硬上限双保险,
// 防止未来字段膨胀时注入体量再次失控。

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	// plannerOverviewMaxBytes 是本轮态势 JSON 的硬上限(字节兜底):正常经下面的列表
	// cap 后远低于它;未来加字段/列表变长时挡住注入体量失控。
	plannerOverviewMaxBytes = 16 << 10
	// workerOverviewMaxBytes 是 worker 首消息概览 JSON 的硬上限(字节)。
	workerOverviewMaxBytes = 12 << 10
	// promptDigestBodyMaxRunes 是注入侧单条 cold digest 正文上限(字符,保留首尾)。
	// 生成侧已限 800 token(compaction.compress),这里再兜一层——digest 正文会被
	// planner 每轮 + 每个 worker 首消息重复携带,是全量 body 的主要放大点。
	promptDigestBodyMaxRunes = 600
	// promptTriggerLineMaxBytes 是 planner 注入里单条触发消息的长度上限(字节):
	// worker 输出/目标文本是外部数据,不设限时一条几十 KB 的结论会撑爆每轮 user 输入。
	// 只截正文,触发条数保持不变。
	promptTriggerLineMaxBytes = 2048
	// workerIntentBlockMaxBytes 是 worker system 里「意图原文 + 锚定资产」块的字节上限。
	workerIntentBlockMaxBytes = 8 << 10
	// workerAssetPromptMax 是意图锚定资产注入条数上限;更多资产/更全字段用资产工具查。
	workerAssetPromptMax = 30
)

// overviewTruncatedNote 是态势块经字节硬上限压缩后追加的说明(仅在触发上限时出现)。
const overviewTruncatedNote = "…（态势块超长已压缩:完整图、全部资产、更早明细等可随时用 graph_overview / list_facts / list_findings / list_assets / node_detail 等工具按需查询）"

// overviewOmittedKey 是注入块里「少给了你什么」的清单键。
//
// 以前超预算只是整键丢弃、末尾加一行笼统说明:模型并不知道自己缺了哪一段,常见的
// 反应是把 graph_overview 整个再调一遍——省下的输入 token 又花回去,还多一轮往返。
// 现在把被省略/被截断的键(以及少了多少条)逐条写进态势块,模型能直接判断「要不要
// 补、补哪一个」,一次 get_overview_slice 就够。
const (
	overviewOmittedKey = "overview_omitted"
	overviewSliceHint  = "以上键可用 get_overview_slice(key=…, filter=…) 取回完整内容"
)

// overviewManifest 收集「本次注入被削减」的键。列表 cap 与字节预算两条路径都往这里
// 登记,最后由 marshalOverviewCapped 合并成 overview_omitted(见下)。
type overviewManifest struct{ entries []map[string]any }

func (m *overviewManifest) add(key, reason string, kept, total int) {
	if m == nil {
		return
	}
	e := map[string]any{"key": key, "reason": reason}
	if total > 0 {
		e["kept"], e["total"] = kept, total
	}
	m.entries = append(m.entries, e)
}

// overviewMustKeep 是撞上字节上限时也必须先装填、并且各自保底
// overviewMustKeepFloor 的键:task 是「我是谁、在做什么」,open_intents 是「下一步
// 能干什么」。其余块(related_tasks / cold_digests / finding_list…)都能按需回查,
// 让位顺序不变。
var overviewMustKeep = []string{"task", "open_intents"}

const overviewMustKeepFloor = 2 << 10

// overviewIsMustKeep reports whether key is in overviewMustKeep.
func overviewIsMustKeep(key string) bool {
	for _, k := range overviewMustKeep {
		if k == key {
			return true
		}
	}
	return false
}

// overviewPromptCaps 是注入侧各列表的条数上限;0 = 沿用工具默认值(不再收紧)。
type overviewPromptCaps struct {
	openIntents int
	recentDone  int
	findingList int
	recentFacts int
	coldDigests int
	goals       int
	hints       int
}

// plannerPromptCaps 是 planner 每轮注入的收紧值。理由:planner 每图变更/心跳都唤醒,
// 这些列表每轮重复携带;截断的都是"最新一窗",更早/更全在工具里一查即得。
var plannerPromptCaps = overviewPromptCaps{
	openIntents: 30, // 与工具默认一致(优先级最高的前 N 条;真实总数另有 frontier_open)
	recentDone:  8,  // 12 → 8;更早的看 done_intents_total 计数 + node_detail
	findingList: 8,  // 10 → 8;全部漏洞用 list_findings
	recentFacts: 12, // 20 → 12;更早/更全用 list_facts 翻页
	coldDigests: 8,  // 15 → 8;被截的更旧 digest id 并入 cold_digests_more 仍可见
	goals:       30, // 100 → 30
	hints:       20, // 50 → 20
}

// workerPromptCaps 是 worker 首消息概览的收紧值:只收 cold digests(15 → 8),
// 其余列表维持工具默认以保住规划/避重的参考质量。
var workerPromptCaps = overviewPromptCaps{coldDigests: 8}

// graphOverviewForPrompt 返回「注入专用」的紧凑态势视图:以 graphOverviewData 的
// 全量数据为源,按 caps 收紧各列表、截断 digest 正文、防御性剥离完整图。
// 工具路径(graph_overview 工具本身)不经过这里,字段与条数保持全量。
func (t *ToolSet) graphOverviewForPrompt(caps overviewPromptCaps) map[string]any {
	data := t.graphOverviewData()
	// 完整血缘边没有进过 graphOverviewData 的返回体(内部只折叠成 parents/yields);
	// 防御性剥离:未来若把 raw edges 加进数据层,也不会自动灌进每轮注入。
	delete(data, "edges")
	m := &overviewManifest{}
	capOverviewList(data, "open_intents", caps.openIntents, m)
	capOverviewList(data, "recent_done_intents", caps.recentDone, m)
	capOverviewList(data, "finding_list", caps.findingList, m)
	capOverviewList(data, "recent_facts", caps.recentFacts, m)
	capOverviewList(data, "goals", caps.goals, m)
	capOverviewList(data, "hints", caps.hints, m)
	capColdDigests(data, caps.coldDigests, m)
	truncateDigestBodies(data, promptDigestBodyMaxRunes)
	if len(m.entries) > 0 {
		data[overviewOmittedKey] = map[string]any{"hint": overviewSliceHint, "items": m.entries}
	}
	return data
}

// capOverviewList 收紧某个列表键的条数(只影响本次注入的数据副本;n<=0 = 保持原样)。
func capOverviewList(data map[string]any, key string, n int, m *overviewManifest) {
	if n <= 0 {
		return
	}
	switch list := data[key].(type) {
	case []map[string]any:
		if len(list) > n {
			data[key] = list[:n]
			m.add(key, "list_cap", n, len(list))
		}
	case []any:
		if len(list) > n {
			data[key] = list[:n]
			m.add(key, "list_cap", n, len(list))
		}
	}
}

// capColdDigests 收紧 cold_digests 条数,并把被截掉的 digest id 并入 cold_digests_more
// ——即使不展示正文,规划者仍知道这些折叠区存在、可用 expand_digest(id) 展开。
func capColdDigests(data map[string]any, n int, m *overviewManifest) {
	if n <= 0 {
		return
	}
	list, ok := data["cold_digests"].([]map[string]any)
	if !ok || len(list) <= n {
		return
	}
	var more []int64
	if prev, ok := data["cold_digests_more"].([]int64); ok {
		more = append(more, prev...)
	}
	for _, d := range list[n:] {
		if id, ok := d["id"].(int64); ok {
			more = append(more, id)
		}
	}
	data["cold_digests"] = list[:n]
	m.add("cold_digests", "list_cap", n, len(list))
	if len(more) > 0 {
		data["cold_digests_more"] = more
	}
}

// truncateDigestBodies 截断注入视图里的 digest 正文(保留首尾、中段省略):完整 body
// 可 expand_digest(id) 取。当前任务的 cold_digests 与 related_tasks[*].cold_digests
// 都是每轮重复携带的正文,一起收。
func truncateDigestBodies(data map[string]any, max int) {
	if max <= 0 {
		return
	}
	truncList := func(list []map[string]any) {
		for _, d := range list {
			if body, ok := d["body"].(string); ok {
				d["body"] = truncHeadTail(body, max, "……（中略,完整 body 用 expand_digest(id) 展开）……")
			}
		}
	}
	if list, ok := data["cold_digests"].([]map[string]any); ok {
		truncList(list)
	}
	if rts, ok := data["related_tasks"].([]map[string]any); ok {
		for _, rt := range rts {
			if list, ok := rt["cold_digests"].([]map[string]any); ok {
				truncList(list)
			}
		}
	}
}

// truncHeadTail 保留首尾、中间省略:结论通常在开头、来源 id 注记常在结尾,丢中段
// 信息密度最高。按 rune 计(中文一字一字符)。
func truncHeadTail(s string, max int, note string) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	if tail < 0 {
		tail = 0
	}
	return string(r[:head]) + note + string(r[len(r)-tail:])
}

// cutUTF8 在字节上限处截断并向前回退到 rune 边界,保证不切出半个 UTF-8 字符。
func cutUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// capPromptLine 单条触发消息的硬上限:条数不变、只截正文。
func capPromptLine(line string, maxBytes int) string {
	if maxBytes <= 0 || len(line) <= maxBytes {
		return line
	}
	return cutUTF8(line, maxBytes) + " …（本条过长已截断;完整内容用 get_worker_output / node_detail 等工具查）"
}

// marshalOverviewCapped 把态势数据 marshal 成注入用 JSON;超过 maxBytes 时按优先级
// 逐键装填(装不下的键先"砍半列表"降配,再不行整键省略),末尾附一行截断说明。
// 为什么不是直接拦腰截断:map 序列化按 key 字母序,截尾会把 task/running_intents
// 这类规划关键字段整段丢掉;逐键装填保证输出始终是合法 JSON、关键字段优先在场,
// 而让位顺序也确定(related_tasks / cold_digests 等按需可查的块先让)。
func marshalOverviewCapped(data map[string]any, maxBytes int) string {
	b, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	if maxBytes <= 0 || len(b) <= maxBytes {
		return string(b)
	}
	// 优先级:规划决策字段在前,按需可查字段在后。
	//
	// finding_list(已确认漏洞)紧跟 running_intents:它直接决定"还该不该继续找、
	// 哪些已经报过"。以前它排在 recent_done_intents / frontier_open / facts 之后,
	// 图一变大就整键被字节预算挤掉,规划者于是看不见自己已报的漏洞,反复派意图
	// 去打同一个点——这正是"越跑越笨、效率越来越低"的体感来源之一。
	order := []string{
		"task", "goals", "open_intents", "running_intents", "finding_list", "findings_total",
		"recent_facts", "recent_done_intents", "frontier_open", "done_intents_total",
		"facts", "hints", "coverage", "cold_digests", "cold_digests_more", "related_tasks",
	}
	budget := maxBytes - len(overviewTruncatedNote) - 1 // 尾注另算,预留进预算
	if budget < 0 {
		budget = maxBytes
	}
	// 注入前的「已削减」清单(列表 cap / digest 截断)先收进来,再叠加这一次字节预算
	// 挤掉的键,最后一起写回 overview_omitted。
	omitted := []map[string]any{}
	if prev, ok := data[overviewOmittedKey].(map[string]any); ok {
		if items, ok := prev["items"].([]map[string]any); ok {
			omitted = append(omitted, items...)
		}
	}
	delete(data, overviewOmittedKey)
	pendingMustKeep := len(overviewMustKeep)
	var sb strings.Builder
	sb.WriteByte('{')
	wroteAny := false
	for _, k := range order {
		v, ok := data[k]
		if !ok {
			// 该键本轮根本不存在：不能把它的保底额度一直占着。
			if overviewIsMustKeep(k) {
				pendingMustKeep--
			}
			continue
		}
		// 必留键先装填,并为后面还没轮到的必留键各留一份保底额度:让 related_tasks
		// 这类可回查块把 task/open_intents 挤掉,等于让模型看不见自己在干什么。
		limit := budget
		if overviewIsMustKeep(k) {
			if pendingMustKeep > 1 {
				limit = budget - (pendingMustKeep-1)*overviewMustKeepFloor
			}
			if limit < sb.Len()+overviewMustKeepFloor {
				limit = sb.Len() + overviewMustKeepFloor
			}
			pendingMustKeep--
		} else if pendingMustKeep > 0 {
			limit = budget - pendingMustKeep*overviewMustKeepFloor
		}
		if limit > budget {
			limit = budget
		}
		val := v
		for {
			inner, ok := marshalEntry(k, val)
			need := sb.Len() + len(inner)
			if wroteAny {
				need++ // 逗号
			}
			if ok && need <= limit {
				if wroteAny {
					sb.WriteByte(',')
				}
				sb.WriteString(inner)
				wroteAny = true
				break
			}
			shrunk, can := shrinkOverviewValue(val)
			if !can {
				omitted = append(omitted, map[string]any{"key": k, "reason": "budget"})
				break // 整键省略,但清单里点名 → 可按需回查
			}
			val = shrunk
		}
	}
	// 清单本身也要占字节:从尾部逐条丢到塞得下为止;一条都塞不下就不写(仍是合法 JSON)。
	for len(omitted) > 0 {
		inner, ok := marshalEntry(overviewOmittedKey, map[string]any{"hint": overviewSliceHint, "items": omitted})
		if ok && sb.Len()+1+len(inner) <= budget {
			sb.WriteByte(',')
			sb.WriteString(inner)
			break
		}
		omitted = omitted[:len(omitted)-1]
	}
	sb.WriteByte('}')
	return sb.String() + "\n" + overviewTruncatedNote
}

// marshalEntry 渲染 JSON 里的 `"key":value` 片段。
func marshalEntry(key string, v any) (string, bool) {
	b, err := json.Marshal(map[string]any{key: v})
	if err != nil || len(b) < 2 {
		return "", false
	}
	return string(b[1 : len(b)-1]), true
}

// shrinkOverviewValue 给装不下的值降配:列表砍半、超长字符串保留头部、映射里的超长
// 字符串值砍半(如 task.description 被塞了超长文本);其余不可降配。
func shrinkOverviewValue(v any) (any, bool) {
	switch t := v.(type) {
	case []map[string]any:
		if len(t) > 1 {
			return t[:len(t)/2], true
		}
	case []any:
		if len(t) > 1 {
			return t[:len(t)/2], true
		}
	case []int64:
		if len(t) > 1 {
			return t[:len(t)/2], true
		}
	case string:
		if r := []rune(t); len(r) > 256 {
			return string(r[:len(r)/2]), true
		}
	case map[string]any:
		cp := make(map[string]any, len(t))
		changed := false
		for k, v := range t {
			cp[k] = v
			if s, ok := v.(string); ok {
				if r := []rune(s); len(r) > 64 {
					cp[k] = string(r[:len(r)/2])
					changed = true
				}
			}
		}
		if changed {
			return cp, true
		}
	}
	return v, false
}
