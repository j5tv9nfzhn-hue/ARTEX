package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	actool "github.com/Autumn-27/norma/tool"
)

// ── 工具面瘦身（架构 P3「工具按阶段裁剪」的第二、三条）─────────────────────────────
//
// 动态能力路由（tool_router.go）解决的是「不该给的工具别给」；这里解决第二层
// 体积：一个 worker 就算只拿十来个工具，每个工具的说明仍会整份进请求：实测
// 领域工具的 schema 合计 43 KB，其中 40% 是**嵌套参数**的说明文字，32% 是工具
// 顶层描述。上下文预算是固定的，被说明书吃掉的部分就是模型能用来思考的部分。
//
// 这里做两件事，都在「装配完成、即将交给模型」的那一层，不碰工具行为：
//
//  1. 瘦身：顶层描述保留第一句（≤72 字），顶层参数说明 ≤40 字，嵌套参数说明
//     ≤24 字。截断一律落在句子/子句边界上，并在末尾加 `…` 明示还有下文。
//  2. 兜底入口：新增常驻工具 get_tool_help(name)，按需取回**完整**描述与参数
//     schema（含嵌套细节）。所以瘦身不丢信息，只把它从「每轮都付」挪到
//     「真需要时再付一次」。
//
// 三条边界：
//   - 只改 Description/InputSchema。Prompt()（SDK 工具那套操作守则）与 Call 行为
//     完全不动——那是行为正确性，不是体积问题。
//   - 在 ToolResolve 之后才瘦身，所以库里被人工改过的说明同样会被瘦身，但库里
//     存的仍是原文（get_tool_help 读运行时记录，也就是模型本来会看到的那份）。
//   - ARTEX_TOOL_SCHEMA_LEAN=0 一键关掉；关掉后工具面与改造前逐字节一致。
const (
	// leanTopDescRunes 是工具顶层描述保留的字数上限（第一句/第一子句）。
	leanTopDescRunes = 72
	// leanParamDescRunes 是顶层参数说明的字数上限。
	leanParamDescRunes = 40
	// leanNestedDescRunes 是嵌套参数（数组元素、子对象的字段）说明的字数上限。
	// 只留一个短语（「目标 URL」「超时秒数」）：复杂工具的嵌套层常有十几个字段，
	// 每个都写满会把刚省下的体积又吃回去；细节留给 get_tool_help。
	leanNestedDescRunes = 16

	// leanDisableEnv 设为 0/false/off 时关闭瘦身（排障用，等价于回到改造前的行为）。
	leanDisableEnv = "ARTEX_TOOL_SCHEMA_LEAN"
	// ToolHelpToolName 是常驻的「按需取完整说明书」工具名。
	ToolHelpToolName = "get_tool_help"
)

// toolDoc 是瘦身前那份完整说明的存档，供 get_tool_help 按名字取回。
type toolDoc struct {
	Desc   string
	Prompt string
	Schema map[string]any
}

// toolDocs 是进程级存档：工具名 → 完整说明。同一份代码在不同 agent 下说明相同，
// 所以按名字存即可；并发跑多个会话时后写覆盖前写，内容一致，无需加锁。
var toolDocs sync.Map

// leanEnabled 判断瘦身是否开启（默认开启）。拼错的值按「开启」处理：
// 排障时的默认期望是「和正常版本一致」，而不是悄悄退回旧行为。
func leanEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(leanDisableEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// leanText 把一段说明压到 max 个字以内。
//
// 截断点优先落在句子/子句边界（。；\n 与中英文句号），其次是硬截。追加的 `…`
// 不只是排版：它告诉模型「这里还有下文」，模型才知道该去调 get_tool_help。
func leanText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	// 1) 第一句/第一子句：中文标点与换行才是句子边界。刻意不含 ASCII 的 '.'/';'
	//    ——「file.txt」「e.g.」会被误判成句末，那才是真正的语义损失。
	cut := -1
	for i := 0; i < max; i++ {
		switch rs[i] {
		case '。', '；', '！', '？', '\n':
			cut = i + 1
		}
		if cut > 0 {
			break
		}
	}
	// 2) 没有句子边界（英文说明常见）：退到最近的空格，避免把单词劈成两半。
	if cut < 0 {
		cut = max
		for i := max; i > max/2; i-- {
			if rs[i-1] == ' ' {
				cut = i - 1
				break
			}
		}
	}
	out := strings.TrimRight(string(rs[:cut]), "，、, ")
	if out == "" {
		return "…"
	}
	return out + "…"
}

// leanSchema 深拷贝一份 schema，只把 description 压短；结构（类型/枚举/必填/默认值）
// 一律原样保留——那些决定工具能不能被正确调用，不能为了省字节动它们。
func leanSchema(v any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if s, ok := val.(string); ok && k == "description" {
				if depth <= 1 {
					out[k] = leanText(s, leanParamDescRunes)
				} else {
					out[k] = leanText(s, leanNestedDescRunes)
				}
				continue
			}
			out[k] = leanSchema(val, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = leanSchema(val, depth+1)
		}
		return out
	}
	return v
}

// leanTool 是只瘦身「模型看得到的那两样东西」的装饰器：Name/Prompt/权限/调度标志
// 与 Call 全部委托给原工具，因此动态路由、DB 覆盖、缺省入参注入、审批与计量都
// 不受影响。
type leanTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (l *leanTool) Description() string         { return l.desc }
func (l *leanTool) InputSchema() map[string]any { return l.schema }

// AcceptsRawInput 必须显式委托：它是可选接口，不写就会在包装层被静默丢掉，
// 而外层 guardedTool 正要靠它决定「先纠偏入参还是先按 schema 校验」。
func (l *leanTool) AcceptsRawInput() bool {
	if r, ok := l.CoreTool.(interface{ AcceptsRawInput() bool }); ok {
		return r.AcceptsRawInput()
	}
	return false
}

// LeanSchemaEnabled 报告瘦身是否生效（开机日志与测试用，不改变行为）。
func LeanSchemaEnabled() bool { return leanEnabled() }

// leanToolList 对装配完成的工具列表做瘦身，并在末尾追加 get_tool_help。
//
// 追加发生在 ToolResolve 之后：这个工具没有 tools 表行，因此不会被「按 agent 绑定」
// 过滤掉，也不需要为它出一条迁移——它是每个 agent 的常驻能力，和 SDK 通用工具一样。
//
// 存档（完整说明）在瘦身之前写入，所以 get_tool_help 返回的正是模型原本会看到的
// 那份文本，不是另一份需要维护的「文档」。
func leanToolList(tools []actool.CoreTool) []actool.CoreTool {
	out := make([]actool.CoreTool, 0, len(tools)+1)
	hasHelp := false
	for _, t := range tools {
		if t == nil {
			continue
		}
		toolDocs.Store(t.Name(), toolDoc{Desc: t.Description(), Prompt: t.Prompt(), Schema: t.InputSchema()})
		if t.Name() == ToolHelpToolName {
			hasHelp = true
			out = append(out, t)
			continue
		}
		out = append(out, &leanTool{
			CoreTool: t,
			desc:     leanText(t.Description(), leanTopDescRunes),
			schema:   leanSchemaMap(t.InputSchema()),
		})
	}
	// 工具面被整体裁空时不追加 help：没有工具可查的时候再挂一个「查说明书」的入口
	// 只会让「这个 agent 没有工具」这件事变得不像回事。
	if !hasHelp && len(out) > 0 {
		// 清单必须是**本次挂载面**。以前这里给的是进程级存档（toolDocs 跨 agent、
		// 跨 run 累积），于是模型看到一堆本次根本没挂的工具，照着调就得到
		// unknown tool——实测就是这样白跑一轮再把同样的名字重试一次。
		// get_tool_help 的职责是「查本次能用的工具」，清单与报错都按本次装配生成。
		names := make([]string, 0, len(out))
		for _, t := range out {
			if t != nil {
				names = append(names, t.Name())
			}
		}
		names = append(names, ToolHelpToolName) // 它自己也是本次可用的工具
		out = append(out, newToolHelpTool(names))
	}
	return out
}

// leanSchemaMap 是 leanSchema 的强类型入口（InputSchema 返回 map[string]any）。
func leanSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if v, ok := leanSchema(in, 0).(map[string]any); ok {
		return v
	}
	return in
}

// NewToolHelpTool 返回常驻的 get_tool_help：把被瘦身掉的完整说明按名字取回来。
//
// 它自己也在存档里（见 leanToolList），所以模型可以查它自己——这句自指并不多余：
// 瘦身后的说明末尾都带 `…`，模型第一反应就是来找这里。
func NewToolHelpTool() actool.CoreTool { return newToolHelpTool(nil) }

// newToolHelpTool 返回绑定**本次装配面**的 get_tool_help。
//
// mounted 为空时退回存档清单（只有单测与离线工具会走这条分支）。非空时：
//   - 报错里列的是本次真实可用的工具；
//   - 存档里有、本次没挂的名字 → 明确说「存在但本次未挂载」，不给模型「再试一次
//     也许就行」的错觉；
//   - 名字记错时给一个最接近的候选，把「瞎拼名字」收敛成一次有效重试。
func newToolHelpTool(mounted []string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: ToolHelpToolName,
		Description: "按名字取回某个工具的完整说明书（完整描述 + 完整参数 schema，含嵌套字段与枚举）。" +
			"工具列表里的说明都被压成了一句话，末尾带 `…` 时说明还有下文，用这个工具取全文。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "工具名，如 http_sequence"},
			},
			"required": []any{"name"},
		},
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数必须是 {\"name\":\"<工具名>\"}"), nil
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return actool.Errorf("缺少 name：请传入工具名"), nil
			}
			if len(mounted) > 0 && !nameIn(name, mounted) {
				if _, archived := toolDocs.Load(name); archived {
					return actool.Errorf(fmt.Sprintf("工具 %q 存在但本次未挂载，调用不了。本次可用的工具有 %d 个：%s",
						name, len(mounted), strings.Join(mounted, ", "))), nil
				}
				return actool.Errorf(fmt.Sprintf("没有名为 %q 的工具。本次可用的工具有 %d 个：%s%s",
					name, len(mounted), strings.Join(mounted, ", "), closestToolHint(name, mounted))), nil
			}
			v, ok := toolDocs.Load(name)
			if !ok {
				names := toolDocNames()
				return actool.Errorf(fmt.Sprintf("没有名为 %q 的工具。当前可用的工具有 %d 个：%s",
					name, len(names), strings.Join(names, ", "))), nil
			}
			doc := v.(toolDoc)
			desc := doc.Desc
			if p := strings.TrimSpace(doc.Prompt); p != "" {
				desc += "\n\n" + p
			}
			schema, err := json.Marshal(doc.Schema)
			if err != nil {
				return actool.Errorf("序列化 schema 失败: " + err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("【%s】\n%s\n\n参数 schema：\n%s", name, desc, schema)), nil
		},
	})
}

// nameIn 报告 name 是否在 names 里（大小写不敏感：模型偶尔会把 Bash 写成 bash）。
func nameIn(name string, names []string) bool {
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// closestToolHint 给「名字记错」一个最接近的候选。
//
// 实测的浪费长这样：模型凭记忆拼出一个近似但不存在工具名，拿不到就换个名字再试
// 一次。把最接近的那个名字直接摆出来，一次就能收敛。
func closestToolHint(name string, names []string) string {
	best, bestScore := "", 1 // 低于 2 分不提示，免得给一个毫不相干的工具
	for _, n := range names {
		if s := toolNameScore(name, n); s > bestScore {
			best, bestScore = n, s
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("。最接近的是 %q——如果那才是你要找的，直接用这个名字", best)
}

// toolNameScore 是轻量相似度：按 _ - . 切词后的公共词加权，再加互相包含的加分。
// 够用来纠「记错/拼错」，不引第三方依赖。
func toolNameScore(a, b string) int {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la == lb {
		return 100
	}
	score := 0
	for _, tok := range strings.FieldsFunc(la, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if len(tok) >= 3 && strings.Contains(lb, tok) {
			score += len(tok)
		}
	}
	if strings.Contains(lb, la) || strings.Contains(la, lb) {
		score += 2
	}
	return score
}

// toolNameDiscipline 是给执行型 agent 的一条硬纪律：工具名只能来自本次工具列表。
//
// 提示词与工具面是两处独立维护的东西，一旦漂移，模型就会反复去调一个不存在的工具
// （实测：提示要求某个工具，工具面没挂，于是每一轮都试一次）。纪律解决「不知
// 道该不该信自己记忆」的问题，get_tool_help 的清单解决「到底有什么」的问题。
const toolNameDiscipline = "\n\n【工具名纪律】只调用工具列表里存在的工具。名字记不准时先用 `get_tool_help` 查" +
	"（它的报错会列出本次真实可用的全部工具），不要凭记忆拼名字——调用不存在的工具只会白费一轮。"

// toolDocNames 列出存档里的工具名（排序，便于模型与人工对照）。
func toolDocNames() []string {
	var names []string
	toolDocs.Range(func(k, _ any) bool {
		names = append(names, k.(string))
		return true
	})
	sort.Strings(names)
	return names
}

// ToolSchemaSize 是一组工具「交给模型前的体积」实测：瘦身前 / 瘦身后（字节）。
// 这是 P3 的唯一验收口径（单轮工具 schema 体积下降 40%），所以做成可被测试和
// 开机日志同时引用的一个函数，而不是各写一份统计。
type ToolSchemaSize struct {
	Agent  string `json:"agent"`
	Tools  int    `json:"tools"`
	Before int    `json:"before"`
	After  int    `json:"after"`
}

// SavedPercent 返回体积下降的百分比（0–100），向下取整到 0.1。
func (s ToolSchemaSize) SavedPercent() float64 {
	if s.Before <= 0 {
		return 0
	}
	return float64(int((1-float64(s.After)/float64(s.Before))*1000)) / 10
}

// schemaPayloadBytes 统计一组工具在请求里占的字节数：名字 + 描述（含 Prompt，
// 与 norma 的 ToolSchema 渲染方式一致）+ schema。
func schemaPayloadBytes(tools []actool.CoreTool) int {
	total := 0
	for _, t := range tools {
		if t == nil {
			continue
		}
		total += schemaOneBytes(t)
	}
	return total
}

func schemaOneBytes(t actool.CoreTool) int {
	desc := t.Description()
	if p := t.Prompt(); p != "" {
		desc += "\n\n" + p
	}
	b, err := json.Marshal(struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Schema      map[string]any `json:"input_schema"`
	}{t.Name(), desc, t.InputSchema()})
	if err != nil {
		return len(desc)
	}
	return len(b)
}

// leanBytes 估算一个工具瘦身后占的字节数（不改动任何全局状态）。
func leanBytes(t actool.CoreTool) int {
	if t == nil {
		return 0
	}
	desc := leanText(t.Description(), leanTopDescRunes)
	if p := t.Prompt(); p != "" {
		desc += "\n\n" + p
	}
	b, err := json.Marshal(struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Schema      map[string]any `json:"input_schema"`
	}{t.Name(), desc, leanSchemaMap(t.InputSchema())})
	if err != nil {
		return len(desc)
	}
	return len(b)
}

// ToolSchemaSizes 实测内置三套工具面的瘦身效果（含 SDK 通用工具，因为它们在真实
// 请求里同样占体积）。启动时打一行日志，测试里作为验收断言。
func ToolSchemaSizes() []ToolSchemaSize {
	sets := builtinToolsByAgent()
	var out []ToolSchemaSize
	for _, name := range []string{"mainagent", "planner", "worker"} {
		tools := append(append([]actool.CoreTool{}, sets[name]...), SDKDefaultTools()...)
		size := ToolSchemaSize{Agent: name, Tools: len(tools) + 1} // +1：常驻的 get_tool_help
		for _, t := range tools {
			size.Before += schemaOneBytes(t)
			size.After += leanBytes(t)
		}
		size.After += schemaOneBytes(NewToolHelpTool())
		out = append(out, size)
	}
	return out
}
