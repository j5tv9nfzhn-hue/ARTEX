
package server

// ── 复核成本闸门（用户可配）────────────────────────────────────────────────────
//
// 改造前，自动复核的触发条件硬编码在 shouldAutoVerify 里：critical/high 一律复核，
// medium/low 按 ARTEX_VERIFY_SAMPLE_RATE 采样（默认 25%）。口径藏在代码与环境变量里，
// 界面上看不到「这条发现为什么会被复核」，用户也没法按自己的预算收紧。
//
// 现在拆成两项系统设置（系统设置页「自动复核闸门」卡片）：
//
//   verify_auto_threshold   等级达到该档 → 入库即强制复核；off = 只保留人工发起。
//                           取值 off | critical | high | medium | low
//   verify_sample_percent   低于阈值时按此比例抽查（0–100，0 = 不抽查）。
//
// 默认值 high + 25% 与旧硬编码口径逐条对应，升级后行为不变。两项都在「发现入库」
// 那一刻读取（enqueueAutoVerification），改完对之后入库的发现立即生效，不必重建
// agent；已进队列的发现按旧口径跑完。

import (
	"log"
	"math/rand"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

const (
	settingVerifyAutoThreshold = "verify_auto_threshold"
	settingVerifySamplePercent = "verify_sample_percent"
)

// 阈值枚举。off 不与任何等级比较：它直接关掉自动派发（人工复核不受影响）。
const (
	verifyThresholdOff      = "off"
	verifyThresholdCritical = "critical"
	verifyThresholdHigh     = "high"
	verifyThresholdMedium   = "medium"
	verifyThresholdLow      = "low"
)

// 默认口径 = 改造前的硬编码行为（critical/high 强制、medium/low 采样 25%）。
const (
	defaultVerifyThreshold     = verifyThresholdHigh
	defaultVerifySamplePercent = 25
)

// VerifyPolicy 由宿主注入（server/assembly.go 的 wireTools）：返回库里配置的阈值与
// 采样比例。ok=false 表示两项都没配，调用方回退默认值 + 环境变量（运维覆盖）。
// 抽成变量是为了让 shouldAutoVerify 保持纯函数形态，单测不必起库。
var VerifyPolicy func() (threshold string, samplePercent int, ok bool)

// verifySeverityRank 把严重度映射成可比较的档位；未知严重度 ok=false。
// 档位顺序与 db/findings.go 的排序口径一致（critical > high > medium > low）。
func verifySeverityRank(severity string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 4, true
	case "high":
		return 3, true
	case "medium":
		return 2, true
	case "low":
		return 1, true
	}
	return 0, false
}

// verifyThresholdRank 把阈值枚举映射成档位；off 与非法值都 ok=false（off 不是档位）。
func verifyThresholdRank(threshold string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(threshold)) {
	case verifyThresholdCritical:
		return 4, true
	case verifyThresholdHigh:
		return 3, true
	case verifyThresholdMedium:
		return 2, true
	case verifyThresholdLow:
		return 1, true
	}
	return 0, false
}

func clampVerifyPercent(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// currentVerifyPolicy 取生效口径：库里有配置就用库里的，否则用默认阈值 +
// 环境变量采样率（verifySampleRate 的语义是 0–1，这里换算成百分比）。
func currentVerifyPolicy() (string, float64) {
	if VerifyPolicy != nil {
		if threshold, percent, ok := VerifyPolicy(); ok {
			normalized := strings.ToLower(strings.TrimSpace(threshold))
			if normalized == verifyThresholdOff {
				return normalized, clampVerifyPercent(float64(percent))
			}
			if _, valid := verifyThresholdRank(normalized); valid {
				return normalized, clampVerifyPercent(float64(percent))
			}
			log.Printf("[verify] 配置的自动复核阈值 %q 无法识别，回退默认口径", threshold)
		}
	}
	return defaultVerifyThreshold, verifySampleRate() * 100
}

// autoVerifyDecision 是闸门的纯判定：给定严重度、阈值、采样比例，返回是否复核以及
// 写进 finding_verifications.reason 的原因（前端按前缀显示「强制 / 抽样」）。
//
// 三条边界与改造前一致：
//  1. 未知严重度不自动复核（严重度本身有问题时，先别扩大消耗）；
//  2. 达到阈值强制复核、不采样——漏一条高危的代价远大于复核一次；
//  3. 低于阈值才按比例抽查，避免每条低危都开一个会话把预算烧光。
func autoVerifyDecision(severity, threshold string, samplePercent float64) (bool, string) {
	rank, known := verifySeverityRank(severity)
	if !known {
		return false, ""
	}
	if strings.EqualFold(strings.TrimSpace(threshold), verifyThresholdOff) {
		return false, ""
	}
	thresholdRank, ok := verifyThresholdRank(threshold)
	if !ok {
		// 阈值存坏了按默认档处理：宁可多复核一条，也不要让闸门静默失效。
		thresholdRank, _ = verifyThresholdRank(defaultVerifyThreshold)
	}
	if rank >= thresholdRank {
		return true, "auto:severity=" + strings.ToLower(strings.TrimSpace(severity))
	}
	percent := clampVerifyPercent(samplePercent)
	if percent <= 0 {
		return false, ""
	}
	if percent >= 100 || rand.Float64()*100 < percent {
		return true, "auto:sample"
	}
	return false, ""
}

// shouldAutoVerify 读当前生效口径后判定。保持无参签名：单测因此能钉住「默认口径」
// 这一层，不必起库，也不必注入策略。
func shouldAutoVerify(severity string) (bool, string) {
	threshold, percent := currentVerifyPolicy()
	return autoVerifyDecision(severity, threshold, percent)
}

// verifyAutoPolicy 读库里的复核闸门配置。任一读取失败、或两项都没配 → ok=false，
// 由调用方回退默认值 + 环境变量。
func verifyAutoPolicy(pg *db.DB) (string, int, bool) {
	if pg == nil {
		return "", 0, false
	}
	threshold, hasThreshold, err := pg.GetSetting(settingVerifyAutoThreshold)
	if err != nil {
		log.Printf("[verify] 读取自动复核阈值失败，回退默认口径: %v", err)
		return "", 0, false
	}
	sampleRaw, hasSample, sampleErr := pg.GetSetting(settingVerifySamplePercent)
	if sampleErr != nil {
		log.Printf("[verify] 读取自动复核采样比例失败，回退默认口径: %v", sampleErr)
		return "", 0, false
	}
	if !hasThreshold && !hasSample {
		return "", 0, false
	}
	if !hasThreshold {
		threshold = defaultVerifyThreshold
	}
	percent := defaultVerifySamplePercent
	if hasSample {
		if n, convErr := strconv.Atoi(strings.TrimSpace(sampleRaw)); convErr == nil {
			percent = int(clampVerifyPercent(float64(n)))
		}
	}
	return strings.ToLower(strings.TrimSpace(threshold)), percent, true
}

// verifyAutoSettings 给界面回显「当前真正生效的」口径（含默认值与环境变量覆盖）。
// 否则选择框显示 high、实际却在按环境变量跑，用户会以为设置没生效。
func verifyAutoSettings(pg *db.DB) (string, int) {
	if threshold, percent, ok := verifyAutoPolicy(pg); ok {
		if threshold == verifyThresholdOff {
			return threshold, percent
		}
		if _, valid := verifyThresholdRank(threshold); valid {
			return threshold, percent
		}
	}
	return defaultVerifyThreshold, int(clampVerifyPercent(verifySampleRate()*100) + 0.5)
}
