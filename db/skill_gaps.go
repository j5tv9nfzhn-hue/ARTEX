package db

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// 技能沉淀的触发信号：skill_usage 里 found=false 的那些行。
//
// ── 为什么是它 ───────────────────────────────────────────────────────────────
//
// schema 里 skill_usage.found 的注释已经写明了设计意图：
//
//	false = 模型点名了一个不存在的 skill(未命中)。这类行同样保留：
//	它反映"想用但没有"的缺口，是补 skill 的依据。
//
// 也就是说，这个信号是**系统自己记下来的**，不是事后推测出来的。它比另外两个
// 常见候选都更硬：
//
//   - 「某个技能用得多」只说明技能有用，不构成新增技能的理由；
//   - 「复测结论 inconclusive」要结合 evidence 字段才能判断是流程问题还是
//     漏洞本身无法判定，噪声大得多；
//   - 未命中是二元事实：模型确实想要一个不存在的东西，且重复了 minMisses 次。
//
// 需求里明确不预设、不导入第三方技能库——那么触发源就只能是自己的使用记录。
// 这一条是唯一一条天然存在的记录。

// SkillGap 是一个「被反复请求但不存在」的技能。
type SkillGap struct {
	Skill string
	// Misses 是未命中次数。这是排序依据：次数低说明是偶发，次数高说明
	// 模型在多个场景下都缺这个东西。
	Misses int64
	// FirstSeen / LastSeen 让审阅者判断这是长期缺口还是刚出现的波动。
	FirstSeen time.Time
	LastSeen  time.Time
	// Agents / Tasks 是去重后的请求来源数量，用于判断缺口的广度：
	// 一个 agent 反复要，和三个 agent 都要，优先级完全不同。
	Agents int64
	Tasks  int64
	// AgentKeys 是请求过它的 agent 名单。写作技能时 "When to Use" 要写清楚
	// 什么时候该用，这���名单直接给出线索。
	AgentKeys []string
}

// DefaultSkillGapThreshold 是默认的最小未命中次数。
//
// 定 3 而不是 1：单次未命中更可能是模型的一次性措辞偏差（把近似名字记错、
// 临时造了个词），三次以上才说明它在稳定地指向一个缺失的能力。阈值也刻意
// 不做成配置项——它调高一两次没人察觉，调低则提议表被单次噪声淹没，而这正是
// 这套机制最需要避免的失败模式。
const DefaultSkillGapThreshold = 3

// SkillGaps 返回未命中次数达到 minMisses 的技能，按次数降序。
//
// minMisses <= 0 时用 DefaultSkillGapThreshold。
//
// 注意它**不判断**这些技能名是否已经存在于 skills/ 目录：数据层不知道部署布局，
// 把目录的存在性判断留给 server 层。
func (d *DB) SkillGaps(ctx context.Context, minMisses int) ([]SkillGap, error) {
	if minMisses <= 0 {
		minMisses = DefaultSkillGapThreshold
	}
	// found=false 而非 found=0：PG 侧该列是真 BOOLEAN，SQLite 侧虽然存成
	// INTEGER，但 3.23 起的 false 关键字两边都认。
	rows, err := d.QueryContext(ctx, `SELECT skill, count(*) misses, min(ts), max(ts),
 count(DISTINCT agent_key), count(DISTINCT task_id)
FROM skill_usage WHERE found=false
GROUP BY skill HAVING count(*) >= $1
ORDER BY count(*) DESC, skill`, minMisses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := []SkillGap{}
	for rows.Next() {
		var g SkillGap
		var firstRaw, lastRaw any
		if err := rows.Scan(&g.Skill, &g.Misses, &firstRaw, &lastRaw, &g.Agents, &g.Tasks); err != nil {
			return nil, err
		}
		if g.FirstSeen, err = aggregateTimestamp(firstRaw); err != nil {
			return nil, err
		}
		if g.LastSeen, err = aggregateTimestamp(lastRaw); err != nil {
			return nil, err
		}
		gaps = append(gaps, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(gaps) == 0 {
		return gaps, nil
	}
	if err := d.attachGapAgents(ctx, gaps); err != nil {
		return nil, err
	}
	return gaps, nil
}

// attachGapAgents 补上每个缺口的 agent 名单。
//
// 没有用 array_agg/json_group_array：PG 侧是 array_agg(DISTINCT x)，SQLite 侧是
// group_concat(DISTINCT x)，两边函数名与去重语义都不同，走方言助手会把这个文件
// 变成另一处需要维护的分叉点。改成多发一次查询在 Go 侧分组——数据量是「未命中」
// 的行数，比成功命中少一个数量级，这里换来的确定性是划算的。
func (d *DB) attachGapAgents(ctx context.Context, gaps []SkillGap) error {
	index := make(map[string]int, len(gaps))
	for i, g := range gaps {
		index[g.Skill] = i
	}
	query := `SELECT skill, agent_key FROM skill_usage
WHERE found=false AND agent_key IS NOT NULL
ORDER BY skill, agent_key`
	rows, err := d.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]bool)
	for rows.Next() {
		var skill, agent string
		if err := rows.Scan(&skill, &agent); err != nil {
			return err
		}
		i, ok := index[skill]
		if !ok || agent == "" {
			continue
		}
		key := skill + "\x00" + agent
		if seen[key] {
			continue
		}
		seen[key] = true
		gaps[i].AgentKeys = append(gaps[i].AgentKeys, agent)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range gaps {
		sort.Strings(gaps[i].AgentKeys)
	}
	return nil
}

// aggregateTimestamp 归一化 min()/max() 这类**聚合表达式**的时间返回值。
//
// PostgreSQL 的 min(timestamptz) 返回 timestamptz，驱动直接给 time.Time。
// SQLite 那边不一样：结果列没有声明类型，而 DSN 上的 _texttotime 正是按声明
// 类型决定要不要把文本读成 time.Time 的，于是驱动交出的是 string，直接 Scan 进
// *time.Time 会报 unsupported Scan。
//
// 所以这里统一接住两种形态再自己解析，而不是给某个引擎开特例。
func aggregateTimestamp(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case string:
		return parseSQLiteTimestamp(t)
	case []byte:
		return parseSQLiteTimestamp(string(t))
	default:
		return time.Time{}, fmt.Errorf("时间聚合结果既不是时间也不是文本，而是 %T", v)
	}
}

// SkillGapEvidence 把一个缺口渲染成提议的 evidence 串。
//
// 审阅者靠它判断「这条提议该不该批」：模型确实缺这个能力，被三个 agent 缺过
// 十二次，最近一次就在两小时前。没有这行字，提议就只是一段无法核对的散文。
func SkillGapEvidence(g SkillGap) string {
	agents := "无 agent 记录"
	if len(g.AgentKeys) > 0 {
		agents = fmt.Sprintf("agent=%v", g.AgentKeys)
	}
	return fmt.Sprintf("skill_usage.found=false x%d, agents=%d, tasks=%d, %s, last=%s",
		g.Misses, g.Agents, g.Tasks, agents, g.LastSeen.UTC().Format(time.RFC3339))
}
