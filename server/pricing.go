package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 价格表(settings.price_table)与成本计算。
//
// 设计取舍(为什么不做内置目录/写入时计算):
//   - 不做内置模型价目:各渠道/账号折扣千差万别,维护内置目录会变成长期负担,
//     价格由用户在系统配置里填一个 JSON 即可;
//   - 读时计算、不落列:同一批历史 token 在价格调整后立即按新价重算,无需迁移,
//     也不引入"写入价"与"当前价"两套口径;
//   - 单位统一 USD / 百万 token(与各厂商价目页一致),换算集中在 CostOf 一处。
// settingPriceTable 常量与其它设置 key 一起定义在 server/manager.go。

// Price 是单个模型条目的价目，单位 USD / 百万 token。
// 缓存三档(input/output 之外)若缺失按 0 计——缺失等价"该渠道缓存不再单独计费"。
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// PriceTable 是解析排序后的价格表：匹配时在全部子串命中的 key 里取最长者
// (如 opus-4 与 claude-opus 同时命中 claude-opus-4-5 时取更具体的 claude-opus)。
type PriceTable struct {
	entries []priceEntry // 按 name 长度降序排列
}

type priceEntry struct {
	name  string
	price Price
}

// ParsePriceTable 解析 price_table 设置。空串(未配置)= (nil, nil)。
// 校验规则(与 PUT /api/settings 的 400 错误一一对应)：
//   - 顶层必须是 JSON 对象(模型名 → 价目对象)；
//   - 每个值必须是对象，且 input/output/cache_read/cache_write 若出现必须是非负数；
//   - 模型名 key 不能为空或纯空白。
//
// 未知字段忽略(向前兼容)；缺失字段按 0 计。
func ParsePriceTable(raw string) (*PriceTable, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("不是合法的 JSON 对象: %w", err)
	}
	table := &PriceTable{entries: make([]priceEntry, 0, len(decoded))}
	for name, body := range decoded {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, errors.New("存在空的模型名 key")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, fmt.Errorf("模型 %q 的价目必须是 JSON 对象(如 {\"input\":1.5,\"output\":7.5})", name)
		}
		var price Price
		for field, target := range map[string]*float64{
			"input":       &price.Input,
			"output":      &price.Output,
			"cache_read":  &price.CacheRead,
			"cache_write": &price.CacheWrite,
		} {
			rawValue, ok := fields[field]
			if !ok {
				continue // 缺失按 0 计
			}
			var value float64
			if err := json.Unmarshal(rawValue, &value); err != nil {
				return nil, fmt.Errorf("模型 %q 的 %s 必须是数字", name, field)
			}
			if value < 0 {
				return nil, fmt.Errorf("模型 %q 的 %s 不能为负数", name, field)
			}
			*target = value
		}
		table.entries = append(table.entries, priceEntry{name: trimmed, price: price})
	}
	// 最长子串匹配优先 → 按 key 长度降序；等长时按字典序保证结果稳定。
	sort.Slice(table.entries, func(i, j int) bool {
		li, lj := len(table.entries[i].name), len(table.entries[j].name)
		if li != lj {
			return li > lj
		}
		return table.entries[i].name < table.entries[j].name
	})
	return table, nil
}

// PriceFor 返回模型名命中的价目。匹配规则：key 是 model 的子串(大小写不敏感)，
// 多个命中时取最长 key(更具体的配置覆盖更宽的)。未命中 → (Price{}, false)。
func (t *PriceTable) PriceFor(model string) (Price, bool) {
	if t == nil || model == "" {
		return Price{}, false
	}
	lowered := strings.ToLower(model)
	for _, entry := range t.entries {
		if strings.Contains(lowered, strings.ToLower(entry.name)) {
			return entry.price, true
		}
	}
	return Price{}, false
}

// CostOf 按价格表计算一段聚合(没有 token 就传 0，与计价无关)的费用，单位 USD。
// 无价格表 / 模型未命中 → (0, false)。缓存价目缺失按 0 计。
func (t *PriceTable) CostOf(model string, input, output, cacheRead, cacheWrite int) (float64, bool) {
	price, ok := t.PriceFor(model)
	if !ok {
		return 0, false
	}
	// 价目单位是 USD/百万 token，除以 1e6 换算成美元。
	const perToken = 1.0 / 1e6
	return (float64(input)*price.Input +
		float64(output)*price.Output +
		float64(cacheRead)*price.CacheRead +
		float64(cacheWrite)*price.CacheWrite) * perToken, true
}

// pricingTable 读当前 price_table 设置并解析；未配置或解析失败 → nil(不算钱)。
// 解析失败按未配置处理：坏配置绝不能把已有的 token 视图打崩，PUT 时已拒绝脏数据。
func (s *Server) pricingTable() *PriceTable {
	if s == nil || s.m == nil {
		return nil
	}
	table, err := ParsePriceTable(s.m.PriceTable())
	if err != nil {
		return nil
	}
	return table
}
