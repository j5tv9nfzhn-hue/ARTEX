package server

// software_re_engine.go 落实「选定单引擎 = REA」的逆向引擎深化契约：
// 能力分层（tier）、会话配额与清理、多模块地址纪律；完整导出见 software_re_export.go。
//
// 边界：本文件只在 reEvaluateCapabilities 的目录/schema 门控之上做分级与约束
// 报告，不修改既有门控判定；清理走 reEnd / RETransition 既有状态机（closed 为
// 终态），不引入第二套会话生命周期。

import (
	"net/http"
	"time"

	"github.com/Autumn-27/artex/db"
)

// REEngineContract 描述某一引擎家族的深化契约：核心操作集、扩展操作、
// 地址纪律与多模块约定。通用映射层（REProviderConfig）负责“工具怎么调”，
// 本契约负责“什么状态才算 ready、导出/配额/模块的纪律是什么”。
type REEngineContract struct {
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	CoreOps      []string `json:"core_ops"`
	ExtendedOps  []string `json:"extended_ops"`
	AddressKinds []string `json:"address_kinds"`
	Notes        []string `json:"notes"`
}

var reContractTitles = map[string]string{
	"rea":    "REA 逆向分析助手 · 深化静态契约（选定单引擎）",
	"ghidra": "Ghidra MCP · 通用只读静态契约",
	"ida":    "IDA Pro / Hex-Rays MCP · 通用只读静态契约",
}

// reContractCoreOps 是各家族进入 deep 层必须全部就绪的核心操作。
// REA 深化契约把 callgraph 也计入核心：调用关系是逆向叙事的骨架。
var reContractCoreOps = map[string][]string{
	"rea":    {"status", "functions", "decompile", "xrefs", "callgraph"},
	"ghidra": {"status", "functions", "decompile", "xrefs"},
	"ida":    {"identity", "status", "functions", "decompile", "xrefs"},
}

// reContractAddressKinds 记录家族的地址纪律：REA 行归一化用 RVA，字符串用
// file_offset；导出与证据必须标注 address_kind，禁止混用 ASLR 运行地址。
var reContractAddressKinds = map[string][]string{
	"rea":    {"rva", "file_offset"},
	"ghidra": {"va"},
	"ida":    {"va"},
}

func reEngineContract(kind string) REEngineContract {
	core := reContractCoreOps[kind]
	extended := make([]string, 0, len(reAllStaticOperations))
	for _, op := range reAllStaticOperations {
		inCore := false
		for _, c := range core {
			if c == op {
				inCore = true
				break
			}
		}
		if !inCore {
			extended = append(extended, op)
		}
	}
	notes := []string{"工具目录就绪不等于引擎或目标样本就绪；开会话仍以引擎反馈为准"}
	switch kind {
	case "rea":
		notes = append(notes,
			"会话以 RVA 寻址；strings 行为 file_offset，证据与导出必须标注 address_kind",
			"多模块样本先用 modules 建立模块清单，后续函数/引用查询用 module 列限定范围",
			"完整导出默认覆盖无地址操作；decompile/xrefs/callgraph 需显式提供地址",
		)
	}
	return REEngineContract{
		Kind:         kind,
		Title:        reContractTitles[kind],
		CoreOps:      core,
		ExtendedOps:  extended,
		AddressKinds: reContractAddressKinds[kind],
		Notes:        notes,
	}
}

// REContractReport 在 RECapabilityReport 之上给出契约分级。
type REContractReport struct {
	Capability RECapabilityReport `json:"capability"`
	Contract   REEngineContract   `json:"contract"`
	Tier       string             `json:"tier"`
	TierReason string             `json:"tier_reason"`
}

// reContractReport 对单个 Provider 给出契约层就绪分级：
//
//	unverified —— 通用门控未通过；
//	basic      —— 通用门控通过，但契约核心操作未全部就绪；
//	deep       —— 契约核心操作全部就绪，可按深化契约开展分析与导出。
//
// 分级只约束静态分析会话与导出，不构成对样本架构或运行时的就绪承诺。
func reContractReport(p db.REProvider) REContractReport {
	capability := reEvaluateCapabilities(p)
	contract := reEngineContract(p.Kind)
	out := REContractReport{Capability: capability, Contract: contract, Tier: "unverified", TierReason: capability.GateReason}
	if !capability.CoreReady {
		return out
	}
	ready := make(map[string]bool, len(capability.ReadyOperations))
	for _, op := range capability.ReadyOperations {
		ready[op] = true
	}
	missing := make([]string, 0)
	for _, op := range contract.CoreOps {
		if !ready[op] {
			missing = append(missing, op)
		}
	}
	if len(missing) > 0 {
		out.Tier = "basic"
		out.TierReason = "通用门控已通过，但契约核心操作未全部就绪：" + missing[0]
		return out
	}
	out.Tier = "deep"
	out.TierReason = "契约核心操作全部就绪；导出/配额/多模块纪律以本契约为准"
	return out
}

// reQuotaDefaults 是会话与导出配额的平台默认值。导出侧强制（见
// software_re_export.go）；会话数当前为报告阈值，reOpen 侧硬拒绝是后续接线点。
var reQuotaDefaults = map[string]int{
	"max_live_sessions_per_task": 4,
	"max_live_per_provider":      2,
	"max_export_sections":        reExportMaxSections,
	"max_export_bytes":           reExportMaxBytes,
	"max_export_query_limit":     reExportMaxLimit,
	"sweep_batch_limit":          200,
}

// REQuotaReport 是任务级会话配额与租约状态报告。
type REQuotaReport struct {
	TaskID            int64          `json:"task_id"`
	Policy            map[string]int `json:"policy"`
	LiveSessions      int            `json:"live_sessions"`
	UncertainSessions int            `json:"uncertain_sessions"`
	ClosedSessions    int            `json:"closed_sessions"`
	LiveByProvider    map[int64]int  `json:"live_by_provider"`
	OverBudget        bool           `json:"over_budget"`
	Notes             []string       `json:"notes"`
}

func reSessionLive(state string) bool {
	return state == "opening" || state == "ready" || state == "busy" || state == "closing"
}

// reQuotaStatus 统计任务内会话租约状态。interrupted 计为不确定租约，必须经
// reEnd（control/close）或运维清理（POST /api/re/sweep）确认后才能释放。
func reQuotaStatus(taskID int64, sessions []db.RESession) REQuotaReport {
	out := REQuotaReport{
		TaskID:         taskID,
		Policy:         reQuotaDefaults,
		LiveByProvider: map[int64]int{},
		Notes:          []string{},
	}
	for _, s := range sessions {
		switch {
		case reSessionLive(s.State):
			out.LiveSessions++
			out.LiveByProvider[s.ProviderID]++
		case s.State == "interrupted":
			out.UncertainSessions++
		default:
			out.ClosedSessions++
		}
	}
	if out.LiveSessions > reQuotaDefaults["max_live_sessions_per_task"] {
		out.OverBudget = true
		out.Notes = append(out.Notes, "任务活动会话超过配额，新会话应先关闭或清理旧租约")
	}
	for _, n := range out.LiveByProvider {
		if n > reQuotaDefaults["max_live_per_provider"] {
			out.OverBudget = true
			out.Notes = append(out.Notes, "同一 Provider 活动会话超过配额")
			break
		}
	}
	if out.UncertainSessions > 0 {
		out.Notes = append(out.Notes, "存在 interrupted 不确定租约：确认外部引擎状态后用 control/close 或 sweep 释放")
	}
	return out
}

// reContractHTTP 返回三家族契约定义、配额默认值与导出范围
// （GET /api/re/engine-contract）。
func (s *Server) reContractHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"contracts":    []REEngineContract{reEngineContract("rea"), reEngineContract("ghidra"), reEngineContract("ida")},
		"quota_policy": reQuotaDefaults,
		"export": map[string]any{
			"default_operations": reExportDefaultOps,
			"max_sections":       reExportMaxSections,
			"max_bytes":          reExportMaxBytes,
			"max_query_limit":    reExportMaxLimit,
		},
		"scope": "契约分级只约束静态分析会话与导出；不构成对样本架构或运行时的就绪承诺",
	})
}

// reProviderContractHTTP 返回单 Provider 的契约分级
// （GET /api/re/providers/{provider}/contract）。
func (s *Server) reProviderContractHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	p, err := s.m.pg.REProvider(reID(r.PathValue("provider")))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, reContractReport(*p))
}

// reQuotaHTTP 返回任务级会话配额报告（GET /api/tasks/{id}/re/quota）。
func (s *Server) reQuotaHTTP(w http.ResponseWriter, r *http.Request) {
	_, taskID, ok := s.reTask(w, r)
	if !ok {
		return
	}
	sessions, err := s.m.pg.RESessions(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, reQuotaStatus(taskID, sessions))
}

// reSweepHTTP 运维清理：确认后释放滞留的 interrupted 租约（POST /api/re/sweep）。
// 必须显式 confirmed=true；单批上限 reQuotaDefaults["sweep_batch_limit"]；
// 只处理 interrupted，不触碰活动会话（opening/ready/busy/closing）。
func (s *Server) reSweepHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.reInitialize(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var a struct {
		OlderThanHours int  `json:"older_than_hours"`
		Confirmed      bool `json:"confirmed"`
	}
	if !reDecode(w, r, &a) {
		return
	}
	if !a.Confirmed {
		writeErr(w, 400, "必须显式 confirmed=true 才会释放滞留会话租约")
		return
	}
	if a.OlderThanHours < 1 || a.OlderThanHours > 720 {
		writeErr(w, 400, "older_than_hours 需在 1..720 小时之间")
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(a.OlderThanHours) * time.Hour).Format("2006-01-02T15:04:05.000Z")
	rows, err := s.m.pg.Query(`SELECT id, task_id FROM re_sessions WHERE state='interrupted' AND updated_at < $1 ORDER BY id LIMIT $2`,
		cutoff, reQuotaDefaults["sweep_batch_limit"])
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	found := make([]struct {
		TaskID    int64 `json:"task_id"`
		SessionID int64 `json:"session_id"`
	}, 0)
	for rows.Next() {
		var v struct {
			TaskID    int64 `json:"task_id"`
			SessionID int64 `json:"session_id"`
		}
		if err := rows.Scan(&v.SessionID, &v.TaskID); err != nil {
			rows.Close()
			writeErr(w, 500, err.Error())
			return
		}
		found = append(found, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	released := make([]int64, 0, len(found))
	skipped := make([]map[string]any, 0)
	for _, v := range found {
		// 优先走既有 close 状态机；对无活动句柄的 interrupted 收敛不到
		// closed 时，按状态机终态显式释放租约。
		if err := s.reEnd(r.Context(), v.TaskID, v.SessionID, "close", true); err != nil {
			if terr := s.m.pg.RETransition(v.SessionID, "interrupted", "closed", "运维清理：确认后释放滞留租约"); terr != nil {
				skipped = append(skipped, map[string]any{"session_id": v.SessionID, "error": terr.Error()})
				continue
			}
		}
		released = append(released, v.SessionID)
	}
	writeJSON(w, 200, map[string]any{"cutoff": cutoff, "scanned": len(found), "released": released, "skipped": skipped})
}
