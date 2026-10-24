package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"
)

// 第六阶段接线：复核（verifier）与复测（retester）的结论采集。
//
// 背景：get_finding_verification_context / record_finding_verification_result /
// get_finding_retest_context / record_finding_retest_result 四个工具已从工具面
// 物理删除。上下文改由引擎在会话开头注入，结论改由引擎在会话收敛时解析模型
// 最终回复里的 JSON 并落库——模型不再拥有任何写结论的工具。

// verdictPayload 是复核/复测统一约定的结论载荷。
type verdictPayload struct {
	Verdict  string `json:"verdict"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence"`
}

// parseVerdictPayload 从模型最终回复里抽出结论 JSON。
// 容忍 Markdown 围栏与前后说明文字：取第一个 '{' 到最后一个 '}' 之间解析。
func parseVerdictPayload(text string) (verdictPayload, bool) {
	var out verdictPayload
	text = strings.TrimSpace(text)
	if text == "" {
		return out, false
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return out, false
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return out, false
	}
	out.Verdict = strings.TrimSpace(out.Verdict)
	out.Summary = strings.TrimSpace(out.Summary)
	out.Evidence = strings.TrimSpace(out.Evidence)
	if out.Verdict == "" {
		return out, false
	}
	return out, true
}

// captureVerificationVerdict 在复核会话收敛时，把最终回复里的结论落库。
// 未输出合法结论时保持 pending 由 finishVerification 按失败/停止收口。
func (s *Server) captureVerificationVerdict(convID int64) {
	if s == nil || s.m == nil || s.m.pg == nil || convID <= 0 {
		return
	}
	text, err := s.m.pg.LastConversationResult(convID)
	if err != nil || strings.TrimSpace(text) == "" {
		return
	}
	p, ok := parseVerdictPayload(text)
	if !ok {
		log.Printf("[verify] conv %d 最终回复中未找到合法结论 JSON，保持未复核", convID)
		return
	}
	if err := s.m.pg.RecordFindingVerificationResult(context.Background(), convID, p.Verdict, p.Summary, p.Evidence); err != nil {
		log.Printf("[verify] conv %d 结论落库失败: %v", convID, err)
		return
	}
	log.Printf("[verify] conv %d 结论已由引擎落库: %s", convID, p.Verdict)
}

// captureRetestVerdict 在复测会话收敛时，把最终回复里的结论落库。
func (s *Server) captureRetestVerdict(convID int64) {
	if s == nil || s.m == nil || s.m.pg == nil || convID <= 0 {
		return
	}
	text, err := s.m.pg.LastConversationResult(convID)
	if err != nil || strings.TrimSpace(text) == "" {
		return
	}
	p, ok := parseVerdictPayload(text)
	if !ok {
		log.Printf("[retest] conv %d 最终回复中未找到合法结论 JSON，保持未复测", convID)
		return
	}
	if err := s.m.pg.RecordFindingRetestResult(context.Background(), convID, p.Verdict, p.Summary, p.Evidence); err != nil {
		log.Printf("[retest] conv %d 结论落库失败: %v", convID, err)
		return
	}
	log.Printf("[retest] conv %d 结论已由引擎落库: %s", convID, p.Verdict)
}
