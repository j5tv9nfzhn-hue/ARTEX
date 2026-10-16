package notify

// Snapshot 是 notification_events.snapshot 这一 JSONB 列的契约。写方是 db 层的
// 漏洞落库事务，读方是 server 层的投递引擎与过滤匹配。定义放在本包是因为它是
// 「通知领域」的载荷：db 只负责序列化，不理解字段含义。
//
// 为什么冗余存漏洞字段而不在渲染时回查：漏洞事后会被改名、改级别、改状态，
// 而推送内容应当反映**事发当时**的结论——回查会得到「事后被改成 low」的
// 危险误导。另外 fan-out 与渲染因此不必 JOIN findings/tasks/assets 三张表。
type Snapshot struct {
	// 事件类型：finding_created / finding_status_changed /
	// task_status_changed / approval_pending
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// 仅 kind=finding_status_changed 时非空。
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`

	// 以下字段仅任务级事件（task_status_changed / approval_pending）使用。
	// 全部 omitempty：旧版写方不会带它们，新版写方按需带，读取端零值即缺省。
	// TaskTitle 是写事件时**尽力**回填的展示名，可能为空（任务已删/读失败）——
	// 渲染端必须自行退化为「任务 #N」，不能假设它非空。
	TaskTitle string `json:"task_title,omitempty"`
	// TaskStatus 取值 done / failed / timeout / paused（emit 侧白名单）。
	TaskStatus string `json:"task_status,omitempty"`
	// StatusReason 是人话原因（如暂停来源、预算耗尽明细）；空表示无需解释。
	StatusReason string `json:"status_reason,omitempty"`
	// ApprovalID 是 intercept_pending 行的 id，审批事件专用。
	ApprovalID int64  `json:"approval_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	// ConversationID 是发起审批的会话 id；0=无会话（后台任务）。
	ConversationID int64 `json:"conversation_id,omitempty"`
}

// Item 是一条待推送的条目，供渠道渲染。
//
// 历史包袱：Item 曾被命名为「一条待推送的漏洞」，任务级事件（task_status_changed /
// approval_pending）复用同一结构后，它实际是「一条待推送的事件」。判定入口统一
// 走 taskEvent(it)，不要在渲染器里直接比对 Kind。
type Item struct {
	// Kind 是事件类型（notification_events.kind 的镜像）。空值表示旧版漏洞条目
	// （旧快照没写 kind），渲染按漏洞语义兜底。
	Kind      string
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets 是解析后的资产展示名（如域名/IP）。由 server 层填充——
	// 本包不碰数据库，拿不到名字。
	Assets []string
	// DetailURL 是详情回链；为空表示未配 public_base_url，渲染时省略。
	// 目标页面随 Kind 变化（漏洞详情 / 任务详情 / 审批列表），由 server 层决定。
	DetailURL string
	// 状态变更事件专用；两项均非空时渲染成「待处理 → 已修复」。
	FromStatus string
	ToStatus   string
	// 任务级事件专用；漏洞条目保持零值。
	TaskID         int64
	TaskTitle      string
	TaskStatus     string
	StatusReason   string
	ApprovalID     int64
	ToolName       string
	ConversationID int64
}

// IsStatusChange 报告该条目是否为状态变更事件。
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title 返回条目的展示标题：优先人工命名的 name，回退漏洞类型 vulnclass，
// 两者都空时用一个占位符——绝不输出空标题。
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "（未命名漏洞）"
}

// Message 是一次渠道发送的完整内容。
type Message struct {
	// 单条推送时长度为 1；汇总推送（digest）时为一整批。
	// 空切片是非法的，调用方须保证至少一条。
	Items []Item
	// Batch=true 时按汇总消息渲染（换标题、带上时间窗与条数）。
	Batch bool
	// WindowMinutes 是汇总周期（分钟），仅 Batch=true 时用于文案「近 N 分钟」。
	// 刻意由配置显式传入而不是渲染时算 time.Since：渲染保持确定性，才好测。
	WindowMinutes int
	// HomeURL 是平台面板地址（全局 public_base_url）；空则不带面板入口。
	HomeURL string
}
