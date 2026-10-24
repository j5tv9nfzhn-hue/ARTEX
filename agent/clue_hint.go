package agent

import (
	"context"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 连续失败 → 主动查缓存 → 换分支。
//
// 这是模块 2 的另一半：前半段（cache_clue / query_clues）解决「材料存在哪」，
// 这一半解决「模型在失败时会不会想起来去用」。实测里 worker 的行为是
// 原地重试同一请求（loop_breaker 的 3-strike 就是为此加的），而失败模式的
// 根因不是「没工具」，是「不换分支」——MazeRunner 把它列为 depth-first trap
// 与 failure misattribution 两条。
//
// 所以失败到阈值时，不止拦住它，还要把它需要的东西递到眼前：缓存里关于这个
// 目标的既有材料 + 一句「不许原地重试、换一条分支」的硬指令。

type clueStoreKey struct{}

// WithClueStore 把线索缓存放进 ctx。worker 在装配工具前调用一次；planner 与
// 聊天会话没有（也没有必要有）。
func WithClueStore(ctx context.Context, s *db.ClueStore) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, clueStoreKey{}, s)
}

func clueStoreFrom(ctx context.Context) *db.ClueStore {
	if ctx == nil {
		return nil
	}
	if s, ok := ctx.Value(clueStoreKey{}).(*db.ClueStore); ok {
		return s
	}
	return nil
}

// clueSwitchHint 生成「换个分支」的提示块。返回空串表示没有可用材料（此时
// 提示里只会出现换分支的要求，不编造线索）。
//
// 检索顺序：先按失败目标的 label（主机/路径）找相关材料；找不到再退到「本任务
// 最近写进来的材料」——一个刚被别的 work 发现的端点，往往正是此刻该走的路。
func clueSwitchHint(ctx context.Context, label string, limit int) string {
	cs := clueStoreFrom(ctx)
	ri := RunInfoFrom(ctx)
	if cs == nil || ri.TaskID == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 6
	}
	var rows []db.Clue
	seen := map[int64]bool{}
	if label != "" {
		for _, token := range clueHintTokens(label) {
			part, err := cs.Query(ri.TaskID, "", token, limit)
			if err != nil {
				break
			}
			for _, c := range part {
				if seen[c.ID] {
					continue
				}
				seen[c.ID] = true
				rows = append(rows, c)
				if len(rows) >= limit {
					break
				}
			}
			if len(rows) >= limit {
				break
			}
		}
	}
	if len(rows) == 0 {
		if recent, err := cs.Query(ri.TaskID, "", "", limit); err == nil {
			rows = recent
		}
	}

	var b strings.Builder
	b.WriteString("\n[线索缓存 / 换分支]\n")
	if len(rows) == 0 {
		b.WriteString("本任务的线索缓存还是空的。不要再原地重试同一请求：把手头已看到的端点/参数/报错用 cache_clue 写进缓存，")
		b.WriteString("然后换一种输入思路（另一个入口、另一个身份、另一类漏洞），或如实记录这次尝试的负结果并结束。")
		return b.String()
	}
	b.WriteString("这些是**别的 work 或你自己之前**已经确认过的材料（不是你上一轮的猜测，都带来源）；")
	b.WriteString("它们没有过期，不需要重新发现：\n")
	for _, c := range rows {
		b.WriteString(" - ")
		b.WriteString(clueSummaryLine(c))
		b.WriteString("\n")
	}
	b.WriteString("禁止原地重试同一工具+同一请求：请基于上面的材料换一条攻击分支（另一个端点/参数/身份/漏洞类型），")
	b.WriteString("或先用 query_clues 按关键字再挖一层。若确实无路可走，写清负结果与证据后收尾。")
	return b.String()
}

// clueHintTokens 从失败目标里抽出可用于检索的片段：主机、路径段。
// 只取长度足够的片段，避免用一个 "a" 把整张缓存捞出来。
func clueHintTokens(label string) []string {
	label = strings.TrimSpace(strings.ToLower(label))
	if label == "" {
		return nil
	}
	var out []string
	// 去 scheme
	if i := strings.Index(label, "://"); i >= 0 {
		label = label[i+3:]
	}
	hostPath := label
	if i := strings.IndexAny(hostPath, "?#"); i >= 0 {
		hostPath = hostPath[:i]
	}
	if i := strings.IndexByte(hostPath, '/'); i > 0 {
		host := hostPath[:i]
		path := hostPath[i:]
		if len(host) >= 4 {
			out = append(out, host)
		}
		// 路径取前两段，够具体又不至于太长。
		segs := strings.Split(strings.Trim(path, "/"), "/")
		if len(segs) > 0 {
			p := "/" + segs[0]
			if len(segs) > 1 && segs[1] != "" {
				p += "/" + segs[1]
			}
			if len(p) >= 4 {
				out = append(out, p)
			}
		}
		return out
	}
	if len(hostPath) >= 4 {
		out = append(out, hostPath)
	}
	return out
}

// loopTargetLabel 把一次失败的调用压成可读的目标标识（主机+路径，不含查询值、
// 不含凭据），既用于日志也用于缓存检索。
func loopTargetLabel(name string, in map[string]any) string {
	if in == nil {
		return ""
	}
	raw := ""
	if v, ok := in["url"].(string); ok {
		raw = v
	}
	if raw == "" {
		if v, ok := in["url_template"].(string); ok {
			raw = v
		}
	}
	if raw == "" {
		return name
	}
	// 去掉 {{FUZZ}} 之类的模板占位符，保留结构。
	for _, ph := range []string{"{{FUZZ}}", "{{fuzz}}", "{{PAYLOAD}}", "{{payload}}"} {
		raw = strings.ReplaceAll(raw, ph, "")
	}
	return name + " " + raw
}
