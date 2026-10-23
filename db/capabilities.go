package db

import (
	"encoding/json"
	"strconv"

	"github.com/Autumn-27/artex/llmprobe"
)

// ---------- 模型能力快照（llm_profiles 的旁挂存储） ----------
//
// 为什么放 settings 而不是给 llm_profiles 加列：
//   - 能力是「探测缓存」，不是用户配置。加列会让 profileCols / scanProfile /
//     INSERT / UPDATE 四处同时改，任何一处漏掉就是脏读；
//   - settings 已经是现成的键值存储，且能容纳任意形状的 JSON（模态数组、
//     推理档位数组），以后加字段不用再迁移。
//
// 键名 llm_caps:<profileID>，值是 llmprobe.Capabilities 的 JSON。
// 读失败一律当作「没有探测过」，退回用户声明或内建目录——探测永远不能
// 变成配置读取的硬依赖。

func capSettingKey(profileID int64) string {
	return "llm_caps:" + strconv.FormatInt(profileID, 10)
}

// LoadModelCaps 读取某条 profile 上次探测到的能力。
// ok=false 表示没探测过（或数据损坏），调用方应回落到声明值/目录。
func (d *DB) LoadModelCaps(profileID int64) (llmprobe.Capabilities, bool) {
	if d == nil || profileID <= 0 {
		return llmprobe.Capabilities{}, false
	}
	raw, ok, err := d.GetSetting(capSettingKey(profileID))
	if err != nil || !ok || raw == "" {
		return llmprobe.Capabilities{}, false
	}
	var caps llmprobe.Capabilities
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return llmprobe.Capabilities{}, false
	}
	return caps, caps.Known()
}

// SaveModelCaps 落盘一次探测结果（覆盖旧值）。
func (d *DB) SaveModelCaps(profileID int64, caps llmprobe.Capabilities) error {
	if d == nil || profileID <= 0 {
		return nil
	}
	b, err := json.Marshal(caps)
	if err != nil {
		return err
	}
	return d.SetSetting(capSettingKey(profileID), string(b))
}

// ClearModelCaps 删掉某条 profile 的能力快照（改模型后由调用方触发）。
func (d *DB) ClearModelCaps(profileID int64) error {
	if d == nil || profileID <= 0 {
		return nil
	}
	return d.SetSetting(capSettingKey(profileID), "")
}

// attachCaps 在读出 profile 后补上能力快照。best-effort：读失败就当没有，
// 绝不影响 profile 本身的可用性。
func (d *DB) attachCaps(p *LLMProfile) {
	if p == nil {
		return
	}
	if caps, ok := d.LoadModelCaps(p.ID); ok {
		p.Caps = caps
	}
}

// EffectiveContextWindowK 返回「实际该按多大窗口算」的 K 值。
//
//	探测到的窗口 > 用户声明 > 默认（调用方兜底 200K）
//
// 两者都有时取小：用户声明的语义已经从「模型有多大」变成「我最多让它用多大」，
// 可以当上限用；但用户填得比实测大时（线上那条 1000K），实测值赢——这正是
// 我们要修的那个坑。
func (p *LLMProfile) EffectiveContextWindowK() int {
	if p == nil {
		return 0
	}
	detected := 0
	if p.Caps.ContextTokens > 0 {
		detected = p.Caps.ContextK()
	}
	switch {
	case detected > 0 && p.ContextWindowK > 0:
		return min(detected, p.ContextWindowK)
	case detected > 0:
		return detected
	default:
		return p.ContextWindowK
	}
}

// ContextWindowDetected 报告窗口是否来自实测（决定 agent 侧是否信任超过
// 声明上限的大窗口，例如 Gemini 的 1M）。
func (p *LLMProfile) ContextWindowDetected() bool {
	return p != nil && p.Caps.ContextTokens > 0
}
