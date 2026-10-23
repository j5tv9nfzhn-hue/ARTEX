package server

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmprobe"
)

// 模型能力探测：把「这个模型多大窗口、能吐多少、支持哪些模态与推理档位」
// 从用户手填变成供应商上报。
//
// 触发点有两个：
//   - 保存配置后异步探一次（模型没变且快照还新鲜就跳过）；
//   - 设置页「重新检测」按钮（POST /api/llm/profiles/{id}/probe）强制探。
//
// 探测结果写进 settings 的 llm_caps:<id>，再由 db.LLMProfile.Caps 旁挂到
// 读路径上；压缩阈值取「实测窗口」优先，用户声明只在更小时生效。

// probeTimeout 是一次能力探测的总预算。元数据端点都是小请求，12s 足够；
// 超时就退到内建目录，绝不让设置页被网络拖住。
const probeTimeout = 12 * time.Second

// capsFreshFor 是「多久之内不用重复探测」的窗口。保存配置很频繁（改个名字、
// 拨个开关都会走到这里），没必要每次都打一次外部请求。
const capsFreshFor = 24 * time.Hour

func probeConfigOf(p *db.LLMProfile) llmprobe.Config {
	return llmprobe.Config{
		Format:  p.Format,
		BaseURL: p.BaseURL,
		Model:   p.Model,
		APIKey:  p.APIKey,
		Proxy:   p.Proxy,
		Timeout: probeTimeout,
	}
}

// probeProfileCaps 探测一条配置并落库。返回的能力即使是「目录推测」也会
// 落库——它带着 Source，UI 会照实标注，下次探到真值就覆盖。
func (s *Server) probeProfileCaps(ctx context.Context, pg *db.DB, p *db.LLMProfile) (llmprobe.Capabilities, error) {
	caps, err := llmprobe.Probe(ctx, probeConfigOf(p))
	if err != nil {
		return caps, err
	}
	if caps.ContextTokens > 0 {
		if saveErr := pg.SaveModelCaps(p.ID, caps); saveErr != nil {
			log.Printf("[llm] profile #%d 能力快照落库失败: %v", p.ID, saveErr)
		}
	}
	log.Printf("[llm] profile #%d（%s）能力探测：窗口 %dK / 输出 %d / 输入模态 %v / 推理 %v %v（来源 %s）",
		p.ID, p.Model, caps.ContextK(), caps.MaxOutputTokens,
		caps.InputModalities, caps.Reasoning, caps.ReasoningEfforts, caps.Source)
	return caps, nil
}

// probeProfileAsync 在保存配置后异步探测。不阻塞保存、不推进任何 failover
// 状态；探测失败只是没有快照，压缩继续用声明值。
func (s *Server) probeProfileAsync(pg *db.DB, id int64) {
	if pg == nil || id <= 0 {
		return
	}
	go func() {
		p, err := pg.ProfileByID(id)
		if err != nil || p == nil || p.Model == "" {
			return
		}
		// 没有显式端点就只做目录兜底，不发任何网络请求：空 BaseURL 会被
		// llmprobe 补成 api.openai.com，那等于让「保存配置」偷偷打一次外网
		// （单元测试里尤其致命）。
		if strings.TrimSpace(p.BaseURL) == "" {
			if caps, ok := llmprobe.FromCatalog(p.Model); ok {
				_ = pg.SaveModelCaps(id, caps.Stamp(p.Model))
			}
			return
		}
		if prev, ok := pg.LoadModelCaps(id); ok && prev.Model == p.Model {
			if ts, perr := time.Parse(time.RFC3339, prev.ProbedAt); perr == nil && time.Since(ts) < capsFreshFor {
				return
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout+5*time.Second)
		defer cancel()
		_, _ = s.probeProfileCaps(ctx, pg, p)
	}()
}

// pgProbeProfile 手动触发一次能力探测。
//
// POST /api/llm/profiles/{id}/probe
// 返回 caps 与「实际会用的窗口」，让前端能把「声明 1000K / 实测 128K /
// 实际按 128K 压缩」三件事同时展示出来。
func (s *Server) pgProbeProfile(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的配置 id")
		return
	}
	p, err := pg.ProfileByID(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "配置不存在")
		return
	}
	caps, probeErr := s.probeProfileCaps(r.Context(), pg, p)
	if probeErr != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": probeErr.Error()})
		return
	}
	p.Caps = caps
	writeJSON(w, 200, map[string]any{
		"ok":                         true,
		"caps":                       caps,
		"effective_context_window_k": p.EffectiveContextWindowK(),
		"context_window_detected":    p.ContextWindowDetected(),
	})
}
