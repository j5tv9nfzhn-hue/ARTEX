package server

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmprobe"
	"github.com/Autumn-27/artex/traffic"
)

// DTO/serialization layer: each handler emits EXACTLY the frontend's spec shapes
// (artex/web/src/lib/types.ts). These reshape db package structs so the
// internal DB model never leaks over the API. The db structs and the frontend are
// the canonical contracts; this file maps one onto the other.

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

// rawString stringifies a json.RawMessage, returning "" for empty/nil so omitempty
// fields drop out.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// ---- Task (frontend "Task") ---- created_at as RFC3339, plus a derived status.
type TaskDTO struct {
	AnalysisDomain string             `json:"analysis_domain"`
	ID             string             `json:"id"`
	ExplorationID  int64              `json:"exploration_id"`
	Name           string             `json:"name"` // 可选任务名称;空=未命名
	CategoryID     *int64             `json:"category_id,omitempty"`
	CategoryName   string             `json:"category_name,omitempty"`
	Pinned         bool               `json:"pinned"`
	PinnedAt       string             `json:"pinned_at,omitempty"`
	Description    string             `json:"description"`
	Goal           string             `json:"goal"`
	Status         string             `json:"status"` // created | running | paused | done | failed
	CreatedAt      string             `json:"created_at"`
	CreatedUnix    int64              `json:"created_unix"`       // created_at as unix seconds (for run-duration calc)
	CompletedAt    string             `json:"completed_at"`       // RFC3339 finish time (done/failed); "" if unfinished
	CompletedUnix  int64              `json:"completed_unix"`     // completed_at as unix seconds (0 if unfinished)
	LastActivity   int64              `json:"last_activity_unix"` // unix seconds of the last activity (0 if none)
	Paused         bool               `json:"paused"`
	Queued         bool               `json:"queued"`
	Tokens         TokenTotalDTO      `json:"tokens"`       // whole-task token consumption
	TokenBudget    int                `json:"token_budget"` // 任务级 token 预算(0=不限);用尽自动暂停
	GoalsTotal     int                `json:"goals_total"`
	GoalsMet       int                `json:"goals_met"`
	InFlight       int                `json:"in_flight"` // 运行中 Worker 数（state=running 的意图）
	Findings       FindingSeverityDTO `json:"findings"`  // 该任务已登记的漏洞数（findings 表，按严重度分档）
	// FirstFindingMS 首个 finding 相对任务开始（first_run_at，未运行过退回 created_at）的
	// 毫秒数；0 = 尚未产出 finding。仅作价值展示，不参与引擎判定。
	FirstFindingMS int64 `json:"first_finding_ms,omitempty"`
	// FindingsPerHour 产出速率 = findings 总数 ÷ 已运行小时（保留一位小数；无法计算时为 0）。
	FindingsPerHour float64 `json:"findings_per_hour,omitempty"`
	// PendingApprovals 等待人工决定的拦截审批条数；>0 时列表/详情显示「等待审批」徽章。
	PendingApprovals   int      `json:"pending_approvals,omitempty"`
	LLMProfileID       *int64   `json:"llm_profile_id,omitempty"` // LLM profile used for this task; nil = default
	LLMProfileIDs      []int64  `json:"llm_profile_ids"`
	ActiveLLMProfileID *int64   `json:"active_llm_profile_id,omitempty"`
	LLMFailoverState   string   `json:"llm_failover_state"`
	LLMFailoverReason  string   `json:"llm_failover_reason,omitempty"`
	SourceTaskIDs      []string `json:"source_task_ids"`
	ArchiveBlockedBy   string   `json:"archive_blocked_by_task_id,omitempty"`
	CompanyIDs         []int64  `json:"company_ids"`
	CoverageEnabled    bool     `json:"coverage_enabled"` // 资产覆盖度功能开关(创建时定)
}

// FindingSeverityDTO 是任务列表里按严重度分档的漏洞计数（严重/高/中/低）。
type FindingSeverityDTO struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

func applyTaskArchiveBlocker(dto *TaskDTO, blockers map[int64]int64) {
	if dto == nil || len(blockers) == 0 {
		return
	}
	taskID, err := strconv.ParseInt(dto.ID, 10, 64)
	if err != nil {
		return
	}
	if dependentID := blockers[taskID]; dependentID > 0 {
		dto.ArchiveBlockedBy = strconv.FormatInt(dependentID, 10)
	}
}

// TokenTotalDTO is a whole-task (all agents) token aggregate.
type TokenTotalDTO struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func tokenTotalDTO(u db.TokenUsage) TokenTotalDTO {
	return TokenTotalDTO{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

// findingsPerHour 把任务产出折算成「条/小时」：分母是已运行时间（非终态按 now、
// 终态按结束时间，见 db.TaskListMetrics.RunSeconds）。分母 <= 0（任务尚未启动或
// 时间列缺失）时返回 0，除零保护优先于展示——宁可速率字段缺省，也不显示一个被
// 极小分母放大的假速率。保留一位小数，前端直接用。
func findingsPerHour(findings int, runSeconds int64) float64 {
	if findings <= 0 || runSeconds <= 0 {
		return 0
	}
	return math.Round(float64(findings)*3600/float64(runSeconds)*10) / 10
}

func taskDTO(t *Task, status string) TaskDTO {
	lifecycle := t.lifecycleSnapshot()
	llmState := t.llmStateSnapshot()
	sourceIDs := make([]string, 0, len(lifecycle.SourceTaskIDs))
	for _, id := range lifecycle.SourceTaskIDs {
		sourceIDs = append(sourceIDs, i64s(id))
	}
	profileIDs := append(make([]int64, 0, len(llmState.ProfileIDs)), llmState.ProfileIDs...)
	return TaskDTO{
		AnalysisDomain:     t.AnalysisDomain,
		ID:                 t.ID,
		ExplorationID:      t.ExpID,
		Name:               lifecycle.Name,
		CategoryID:         lifecycle.CategoryID,
		CategoryName:       lifecycle.CategoryName,
		Pinned:             lifecycle.PinnedAt > 0,
		PinnedAt:           completedRFC(lifecycle.PinnedAt),
		Description:        t.Description,
		Goal:               t.Goal,
		Status:             status,
		CreatedAt:          rfc3339(time.Unix(t.CreatedAt, 0)),
		CreatedUnix:        t.CreatedAt,
		CompletedAt:        completedRFC(lifecycle.CompletedAt),
		CompletedUnix:      lifecycle.CompletedAt,
		Paused:             lifecycle.Paused,
		Queued:             lifecycle.Queued,
		TokenBudget:        lifecycle.TokenBudget,
		LLMProfileID:       llmState.ProfileID,
		LLMProfileIDs:      profileIDs,
		ActiveLLMProfileID: llmState.ActiveID,
		LLMFailoverState:   llmState.FailoverState,
		LLMFailoverReason:  llmState.FailoverReason,
		SourceTaskIDs:      sourceIDs,
		CompanyIDs:         lifecycle.CompanyIDs,
		CoverageEnabled:    t.CoverageEnabled,
	}
}

// completedRFC renders a unix completion time as RFC3339, or "" when unset (0).
func completedRFC(unix int64) string {
	if unix == 0 {
		return ""
	}
	return rfc3339(time.Unix(unix, 0))
}

// ---- TrafficExchange (frontend "TrafficExchange") ---- ts as RFC3339.
type TrafficExchangeDTO struct {
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	RespLen     int    `json:"resp_len"`
}

func trafficDTOs(ex []traffic.ExchangeMeta) []TrafficExchangeDTO {
	out := make([]TrafficExchangeDTO, 0, len(ex))
	for _, e := range ex {
		out = append(out, TrafficExchangeDTO{
			ID:          e.ID,
			TS:          rfc3339(time.Unix(e.TS, 0)),
			Host:        e.Host,
			Method:      e.Method,
			URL:         e.URL,
			Status:      e.Status,
			ContentType: e.ContentType,
			RespLen:     e.RespLen,
		})
	}
	return out
}

// ---- TaskNode (frontend "TaskNode") — frontier, intents, exploration graph nodes ----

type TaskNodeDTO struct {
	ID           string `json:"id"`
	Type         string `json:"type"` // db Node.Kind
	Payload      string `json:"payload,omitempty"`
	Priority     int    `json:"priority"`
	State        string `json:"state"`
	Origin       string `json:"origin"`
	TS           string `json:"ts"`
	SourceTaskID string `json:"source_task_id,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
	DeleteReason string `json:"delete_reason,omitempty"` // 意图假删除(state='deleted')时的删除原因
}

func taskNodeDTO(n *db.Node) TaskNodeDTO {
	d := TaskNodeDTO{
		ID:           i64s(n.ID),
		Type:         n.Kind,
		Payload:      rawString(n.Payload),
		DeleteReason: n.DeleteReason,
		Priority:     n.Priority,
		State:        n.State,
		Origin:       n.Origin,
		TS:           rfc3339(n.CreatedAt),
	}
	if n.SourceTaskID > 0 {
		d.SourceTaskID = i64s(n.SourceTaskID)
	}
	d.Inherited = n.Inherited
	return d
}

// GoalDTO is a goal node with its payload unpacked into text/vulnclass — the shape
// the 总览「目标管理」UI works with (vs TaskNodeDTO which carries raw payload JSON).
type GoalDTO struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
	State     string `json:"state"`
	Origin    string `json:"origin,omitempty"`
	TS        string `json:"ts"`
}

func goalDTO(n *db.Node) GoalDTO {
	var p struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return GoalDTO{
		ID:        i64s(n.ID),
		Text:      p.Text,
		VulnClass: p.VulnClass,
		State:     n.State,
		Origin:    n.Origin,
		TS:        rfc3339(n.CreatedAt),
	}
}

func goalDTOs(in []*db.Node) []GoalDTO {
	out := make([]GoalDTO, 0, len(in))
	for _, n := range in {
		out = append(out, goalDTO(n))
	}
	return out
}

// ConstraintDTO is one operation constraint (allow/deny) for the 总览「约束管理」UI.
type ConstraintDTO struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // allow | deny
	Text   string `json:"text"`
	Origin string `json:"origin,omitempty"`
	TS     string `json:"ts,omitempty"`
}

func taskNodeDTOs(in []*db.Node) []TaskNodeDTO {
	out := make([]TaskNodeDTO, 0, len(in))
	for _, n := range in {
		out = append(out, taskNodeDTO(n))
	}
	return out
}

// ---- Edge (frontend "Edge") — exploration edges and asset edges ----

type EdgeDTO struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
	Rel string `json:"rel"`
}

func edgeDTO(e db.Edge) EdgeDTO {
	return EdgeDTO{Src: i64s(e.From), Dst: i64s(e.To), Rel: e.Rel}
}

func edgeDTOs(in []db.Edge) []EdgeDTO {
	out := make([]EdgeDTO, 0, len(in))
	for _, e := range in {
		out = append(out, edgeDTO(e))
	}
	return out
}

// ---- Coverage asset references ----

type CoverageAssetRefDTO struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Summary      string `json:"summary"`
	SourceTaskID string `json:"source_task_id,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
}

func coverageAssetRefDTO(ref db.AssetRef) CoverageAssetRefDTO {
	out := CoverageAssetRefDTO{
		ID: ref.ID, Kind: ref.Kind, State: ref.State, Summary: ref.Summary, Inherited: ref.Inherited,
	}
	if ref.SourceTaskID > 0 {
		out.SourceTaskID = i64s(ref.SourceTaskID)
	}
	return out
}

// ---- Finding (frontend "Finding") ----

type FindingDTO struct {
	TrafficCount          int                        `json:"traffic_count"`
	EvidenceVersion       int64                      `json:"evidence_version"`
	ReportEvidenceVersion int64                      `json:"report_evidence_version"`
	ReportStale           bool                       `json:"report_stale"`
	TrafficBindings       []db.FindingTrafficBinding `json:"traffic_bindings,omitempty"`

	ID        string `json:"id"`
	FindingID string `json:"finding_id,omitempty"` // standalone findings-table id — the handle for status updates
	VulnClass string `json:"vulnclass"`
	Name      string `json:"name,omitempty"` // 漏洞名称;为空时前端回退展示 vulnclass
	Severity  string `json:"severity"`       // critical | high | medium | low
	Status    string `json:"status"`         // pending | in_progress | confirmed | resolved | fixed | false_positive | ignored | duplicate | risk_accepted
	// Validation 是独立复核态(unverified | verified | refuted)。与 status 正交:
	// 前者是「人怎么处置」,后者是「有没有被独立确认过」。前端用它区分真值与线索。
	Validation string `json:"validation"`
	Summary    string `json:"summary"`
	Evidence   string `json:"evidence"`
	Report     string `json:"report,omitempty"` // 详细报告(Markdown);仅详情接口返回,列表为空

	IntentID        string            `json:"intent_id,omitempty"`
	ParamID         string            `json:"param_id,omitempty"`
	TaskID          string            `json:"task_id,omitempty"`
	TaskDescription string            `json:"task_description,omitempty"`
	SourceTaskID    string            `json:"source_task_id,omitempty"`
	Inherited       bool              `json:"inherited,omitempty"`
	Assets          []FindingAssetDTO `json:"assets,omitempty"`
	TS              string            `json:"ts"`
}

// FindingAssetDTO is one asset a finding is anchored to, pre-labelled for display.
type FindingAssetDTO struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// assetLabel renders an asset's most identifying field for compact display.
func assetLabel(a *db.Asset) string {
	switch {
	case a.URL != "":
		if a.Method != "" {
			return a.Method + " " + a.URL
		}
		return a.URL
	case a.Domain != "":
		if a.Port != nil && *a.Port > 0 {
			return fmt.Sprintf("%s:%d", a.Domain, *a.Port)
		}
		return a.Domain
	case a.IP != "":
		if a.Port != nil && *a.Port > 0 {
			return fmt.Sprintf("%s:%d", a.IP, *a.Port)
		}
		return a.IP
	case a.AppName != "":
		return a.AppName
	case a.BundleID != "":
		return a.BundleID
	case a.ServiceName != "":
		return a.ServiceName
	case a.RootDomain != "":
		return a.RootDomain
	default:
		return "#" + i64s(a.ID)
	}
}

// findingPayload mirrors the JSON written by the worker's report_finding tool
// (agent/tools.go addFinding): {vulnclass, severity, summary, evidence:{by,poc}}.
type findingPayload struct {
	VulnClass string          `json:"vulnclass"`
	Name      string          `json:"name"`
	Severity  string          `json:"severity"`
	Summary   string          `json:"summary"`
	Evidence  json.RawMessage `json:"evidence"`
}

func findingDTO(n *db.Node) FindingDTO {
	var p findingPayload
	_ = json.Unmarshal(n.Payload, &p)
	d := FindingDTO{
		ID:        i64s(n.ID),
		VulnClass: p.VulnClass,
		Name:      p.Name,
		Severity:  p.Severity,
		Status:    db.FindingPending,
		// 节点级发现没有独立带复核态的来源时一律按未复核展示：把空串漏给前端，
		// 徽标会退化成英文原值。
		Validation: db.FindingUnverified,
		Summary:    p.Summary,
		Evidence:   rawString(p.Evidence),
		TS:         rfc3339(n.CreatedAt),
	}
	if n.SourceTaskID > 0 {
		d.SourceTaskID = i64s(n.SourceTaskID)
	}
	d.Inherited = n.Inherited
	return d
}

// findingDTOsForTask converts a task's finding nodes to DTOs, stamping each with
// the owning task's id/description so the global 发现 page can group across tasks.
// meta maps node id → the standalone findings row (id + status + asset ids), so the
// per-task view shows the same triage state and anchored assets as the global page;
// nodes with no row keep the 'pending' default and no finding_id (not editable).
// assets pre-resolves the anchored asset rows for label rendering.
func findingDTOsForTask(t *Task, in []*db.Node, meta map[int64]db.FindingMeta, assets map[int64]*db.Asset) []FindingDTO {
	return findingDTOsForOwner(t.ID, t.Description, in, meta, assets)
}

func findingDTOsForOwner(taskID, description string, in []*db.Node, meta map[int64]db.FindingMeta, assets map[int64]*db.Asset) []FindingDTO {
	out := make([]FindingDTO, 0, len(in))
	for _, n := range in {
		d := findingDTO(n)
		d.TaskID = taskID
		d.TaskDescription = description
		if m, ok := meta[n.ID]; ok {
			d.FindingID = i64s(m.ID)
			d.Status = m.Status
			// 任务内的「发现」Tab 与全局发现页共用一个 DTO，复核态也要一致：
			// 否则同一条发现在两个页面会显示成不同的真值口径。
			d.Validation = findingValidation(m.Validation)
			d.TrafficCount = m.TrafficCount
			d.Assets = findingAssetDTOs(m.AssetIDs, assets)
		}
		out = append(out, d)
	}
	return out
}

// findingAssetDTOs maps anchored asset ids to display DTOs, skipping ids whose
// asset row is missing (e.g. deleted).
func findingAssetDTOs(ids []int64, assets map[int64]*db.Asset) []FindingAssetDTO {
	var out []FindingAssetDTO
	for _, aid := range ids {
		if a := assets[aid]; a != nil {
			out = append(out, FindingAssetDTO{ID: i64s(a.ID), Type: a.Type, Label: assetLabel(a)})
		}
	}
	return out
}

// findingValidation 归一复核态:空值或未知值一律按 unverified 返回,前端因此永远
// 拿到三个已知值之一,不必在渲染处再兜一次空串。
func findingValidation(v string) string {
	if db.ValidFindingValidation(v) {
		return v
	}
	return db.FindingUnverified
}

// findingFromDB converts a standalone DBFinding row to a FindingDTO. task_id and
// task_description are empty when the originating task has been deleted (NULL).
func findingFromDB(f *db.DBFinding, assets map[int64]*db.Asset) FindingDTO {
	status := f.Status
	if status == "" {
		status = db.FindingPending
	}
	d := FindingDTO{
		ID:           i64s(f.ID),
		FindingID:    i64s(f.ID),
		TrafficCount: f.TrafficCount, EvidenceVersion: f.EvidenceVersion, ReportEvidenceVersion: f.ReportEvidenceVersion,
		ReportStale: f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion, TrafficBindings: f.TrafficBindings,
		VulnClass:  f.VulnClass,
		Name:       f.Name,
		Severity:   f.Severity,
		Status:     status,
		Validation: findingValidation(f.Validation),
		Summary:    f.Summary,
		Evidence:   f.Evidence,
		Report:     f.Report,
		TS:         rfc3339(f.CreatedAt),
	}
	d.Assets = findingAssetDTOs(f.AssetIDs, assets)
	if f.TaskID != nil {
		d.TaskID = i64s(*f.TaskID)
		d.TaskDescription = f.TaskDescription
	}
	if f.NodeID != nil {
		d.IntentID = "" // node_id is the finding node, not the intent; keep IntentID empty
	}
	return d
}

// ---- Activity (frontend "Activity") ----

type ActivityDTO struct {
	Seq          int64           `json:"seq"`                 // db Activity.ID
	IntentID     string          `json:"intent_id,omitempty"` // db NodeID
	Worker       string          `json:"worker"`
	TS           string          `json:"ts"` // db CreatedAt
	Kind         string          `json:"kind"`
	Tool         string          `json:"tool,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	IsError      bool            `json:"is_error"`
	Summary      string          `json:"summary"`
	Detail       string          `json:"detail,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	SourceTaskID string          `json:"source_task_id,omitempty"`
	Inherited    bool            `json:"inherited,omitempty"`
	MainSeg      *int            `json:"main_seg,omitempty"` // main-agent conversation segment (nil for non-mainagent rows)
	// token usage (set only on kind='result'); used for per-session token totals.
	InputTokens      *int `json:"input_tokens,omitempty"`
	OutputTokens     *int `json:"output_tokens,omitempty"`
	CacheReadTokens  *int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

func activityDTO(a db.Activity) ActivityDTO {
	intent := ""
	if a.NodeID != nil {
		intent = i64s(*a.NodeID)
	}
	d := ActivityDTO{
		Seq:              a.ID,
		IntentID:         intent,
		Worker:           a.Worker,
		TS:               rfc3339(a.CreatedAt),
		Kind:             a.Kind,
		Tool:             a.Tool,
		ToolUseID:        a.ToolUseID,
		IsError:          a.IsError,
		Summary:          a.Summary,
		Detail:           a.Detail, // list endpoint leaves this empty (lazy)
		Metadata:         a.Metadata,
		InputTokens:      a.InputTokens,
		OutputTokens:     a.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens,
		MainSeg:          a.MainSeg,
	}
	if a.SourceTaskID > 0 {
		d.SourceTaskID = i64s(a.SourceTaskID)
	}
	d.Inherited = a.Inherited
	return d
}

func activityDTOs(in []db.Activity) []ActivityDTO {
	out := make([]ActivityDTO, 0, len(in))
	for _, a := range in {
		out = append(out, activityDTO(a))
	}
	return out
}

// ---- Agent (frontend "Agent") — string id ----

type AgentDTO struct {
	ID               string `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Role             string `json:"role"`
	Builtin          bool   `json:"builtin"`
	Enabled          bool   `json:"enabled"`
	MaxTurns         int    `json:"max_turns"`
	RunSecs          int    `json:"run_seconds"`
	WebSearch        bool   `json:"web_search"`
	InteractiveShell bool   `json:"interactive_shell"`
	LLMProfileID     *int64 `json:"llm_profile_id"` // 绑定的 LLM 配置;null=跟随任务/全局
	// P3 触发后处理策略(仅自定义 agent 有意义)。
	TriggerRunMode     string `json:"trigger_run_mode"`
	TriggerMergeMode   string `json:"trigger_merge_mode"`
	TriggerMaxParallel int    `json:"trigger_max_parallel"`
	// binding counts (populated only by the list endpoint) — shown on agent cards.
	McpCount   int `json:"mcp_count"`
	SkillCount int `json:"skill_count"`
	ToolCount  int `json:"tool_count"`
}

func agentDTO(a *db.Agent) AgentDTO {
	return AgentDTO{
		ID:                 i64s(a.ID),
		Key:                a.Key,
		Name:               a.Name,
		Description:        a.Description,
		Role:               a.Role,
		Builtin:            a.Builtin,
		Enabled:            a.Enabled,
		MaxTurns:           a.MaxTurns,
		RunSecs:            a.RunSecs,
		WebSearch:          a.WebSearch,
		InteractiveShell:   a.InteractiveShell,
		LLMProfileID:       a.LLMProfileID,
		TriggerRunMode:     a.TriggerRunMode,
		TriggerMergeMode:   a.TriggerMergeMode,
		TriggerMaxParallel: a.TriggerMaxParallel,
	}
}

func agentDTOs(in []*db.Agent) []AgentDTO {
	out := make([]AgentDTO, 0, len(in))
	for _, a := range in {
		out = append(out, agentDTO(a))
	}
	return out
}

// ---- LLMProfile (frontend "LLMProfile") — string id ----

type LLMProfileDTO struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Format          string  `json:"format"`
	BaseURL         string  `json:"base_url,omitempty"`
	Proxy           string  `json:"proxy,omitempty"`
	Model           string  `json:"model"`
	APIKeyHint      string  `json:"api_key_hint,omitempty"`
	RatePerSecond   float64 `json:"rate_per_second"`
	RatePerMinute   float64 `json:"rate_per_minute"`
	ContextWindowK  int     `json:"context_window_k"`
	ThinkingType    string  `json:"thinking_type"`
	ReasoningEffort string  `json:"reasoning_effort"`
	IsDefault       bool    `json:"is_default"`
	// 轮询(故障转移)参数：priority 越大越先被选中(激活配置恒为链首)；
	// pool_exclude=true 则不作为故障转移目标，但仍可被 agent/任务显式绑定。
	Priority    int  `json:"priority"`
	PoolExclude bool `json:"pool_exclude"`
	// 收发模式：true=流式(SSE) | false=非流式。没有 omitempty —— false 必须出现在
	// 响应里，否则前端读不到「非流式」，开关会回落成默认的流式。
	Streaming bool `json:"streaming"`
	// 单次回复输出上限(0=不发送，由服务端默认值决定)，以及它用哪个请求字段名
	// (''=max_tokens | 'max_completion_tokens'，仅 openai 格式有意义)。
	MaxTokens      int    `json:"max_tokens"`
	MaxTokensField string `json:"max_tokens_field"`
	// 自定义会话头名：非空时每次请求带该 HTTP 头，头值=当前会话/意图的 session id。
	// ''=不发送。用于按 session-id 头做提示缓存/粘性路由的网关。
	SessionHeaderKey string `json:"session_header_key"`
	// 本配置对重试的覆盖(建连/空响应/同 provider 安全窗口)。每项 attempts:
	// 0=继承全局策略 | -1=关闭该层重试 | >0=次数；interval_ms: 0=用默认指数退避 |
	// >0=改用该固定毫秒间隔。全 0 = 完全跟随全局，即历史行为。
	Retry db.RetryOverride `json:"retry"`
	// 输入模态声明(计划 75–96)：逗号分隔的 text/image/audio/pdf；"" = 未声明。
	// Text 始终隐含；保存时后端统一归一(db.NormalizeModalities)。
	InputModalities string `json:"input_modalities"`
	// 运行时实际生效的能力 = 声明优先，未声明按模型名保守推断
	// (db.LLMProfile.EffectiveInputModalities)。前端据此显示「实际支持」徽章。
	EffectiveInputModalities string `json:"effective_input_modalities"`
	// 凭据绑定（计划 46–47）：非空 = Key 以 credentials 行为准（api_key_hint 也
	// 来自该凭据）。credential_name 只读，供界面显示「Key 来自凭据 X」。
	CredentialID   string `json:"credential_id,omitempty"`
	CredentialName string `json:"credential_name,omitempty"`
	// ---- 能力探测（llmprobe）----
	// Caps 是最近一次探测到的模型能力（窗口/输出/模态/推理档位）；
	// 零值 = 没探测过，前端不显示徽章。
	Caps llmprobe.Capabilities `json:"caps,omitempty"`
	// EffectiveContextWindowK 是压缩与用量展示实际使用的窗口（K）：
	// 实测值优先，与用户声明取小。和 context_window_k（用户声明）分开显示，
	// 才能一眼看出「填的数」和「实际用的数」不是一回事。
	EffectiveContextWindowK int  `json:"effective_context_window_k"`
	ContextWindowDetected   bool `json:"context_window_detected"`
	// ---- 供应商归属（重构后）----
	// 界面按供应商分组时用这两个字段；老客户端忽略它们即可，行为不受影响。
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
}

func llmProfileDTO(p *db.LLMProfile) LLMProfileDTO {
	credID := ""
	if p.CredentialID != nil {
		credID = i64s(*p.CredentialID)
	}
	return LLMProfileDTO{
		ID:               i64s(p.ID),
		Name:             p.Name,
		Format:           p.Format,
		BaseURL:          p.BaseURL,
		Proxy:            p.Proxy,
		Model:            p.Model,
		APIKeyHint:       p.APIKeyHint,
		RatePerSecond:    p.RatePerSecond,
		RatePerMinute:    p.RatePerMinute,
		ContextWindowK:   p.ContextWindowK,
		ThinkingType:     p.ThinkingType,
		ReasoningEffort:  p.ReasoningEffort,
		IsDefault:        p.IsDefault,
		Priority:         p.Priority,
		PoolExclude:      p.PoolExclude,
		Streaming:        p.Streaming,
		MaxTokens:        p.MaxTokens,
		MaxTokensField:   p.MaxTokensField,
		SessionHeaderKey: p.SessionHeaderKey,
		Retry:            p.Retry,

		InputModalities:          p.InputModalities,
		EffectiveInputModalities: p.EffectiveInputModalities(),
		CredentialID:             credID,
		CredentialName:           p.CredentialName,

		Caps:                    p.Caps,
		EffectiveContextWindowK: p.EffectiveContextWindowK(),
		ContextWindowDetected:   p.ContextWindowDetected(),

		ProviderID:   providerIDString(p.ProviderID),
		ProviderName: p.ProviderName,
	}
}

// providerIDString 把内部 id 转成界面用的字符串 id；0 保持空（= 未归属）。
func providerIDString(id int64) string {
	if id <= 0 {
		return ""
	}
	return i64s(id)
}

// LLMProviderDTO 是一家供应商及其名下模型。模型复用 LLMProfileDTO——前端拿到的
// 每一条仍然是「能直接发请求的完整配置」，只是多了一层分组关系。
type LLMProviderDTO struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Format           string          `json:"format"`
	BaseURL          string          `json:"base_url,omitempty"`
	Proxy            string          `json:"proxy,omitempty"`
	APIKeyHint       string          `json:"api_key_hint,omitempty"`
	CredentialID     string          `json:"credential_id,omitempty"`
	CredentialName   string          `json:"credential_name,omitempty"`
	RatePerSecond    float64         `json:"rate_per_second"`
	RatePerMinute    float64         `json:"rate_per_minute"`
	SessionHeaderKey string          `json:"session_header_key,omitempty"`
	ModelCount       int             `json:"model_count"`
	Models           []LLMProfileDTO `json:"models"`
	// KeyError 非空 = 这一行的 Key 存库里读不出来（主密钥丢失/更换）。界面据此
	// 直说原因，而不是显示成「未配置 Key」让人反复重填。
	KeyError string `json:"key_error,omitempty"`
}

func llmProviderDTOs(in []*db.LLMProvider) []LLMProviderDTO {
	out := make([]LLMProviderDTO, 0, len(in))
	for _, v := range in {
		dto := LLMProviderDTO{
			ID:               i64s(v.ID),
			Name:             v.Name,
			Format:           v.Format,
			BaseURL:          v.BaseURL,
			Proxy:            v.Proxy,
			APIKeyHint:       v.APIKeyHint,
			RatePerSecond:    v.RatePerSecond,
			RatePerMinute:    v.RatePerMinute,
			SessionHeaderKey: v.SessionHeaderKey,
			ModelCount:       v.ModelCount,
			Models:           llmProfileDTOs(v.Models),
			KeyError:         v.KeyError,
		}
		if v.CredentialID != nil {
			dto.CredentialID = i64s(*v.CredentialID)
		}
		dto.CredentialName = v.CredentialName
		out = append(out, dto)
	}
	return out
}

func llmProfileDTOs(in []*db.LLMProfile) []LLMProfileDTO {
	out := make([]LLMProfileDTO, 0, len(in))
	for _, p := range in {
		out = append(out, llmProfileDTO(p))
	}
	return out
}

// idStrings formats a slice of int64 ids as strings (for visibility agent ids).
func idStrings(in []int64) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, i64s(v))
	}
	return out
}
