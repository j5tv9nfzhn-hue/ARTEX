// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: 关键数据形状).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export type AnalysisDomain = "web_security" | "software_re";

export interface Task {
  analysis_domain?: AnalysisDomain;
  id: string;
  name?: string; // 可选任务名称;空/缺省=未命名,展示时回退到描述
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // 已登记漏洞数(按严重度分档)
  // 价值指标(仅展示,不参与引擎判定):
  first_finding_ms?: number; // 首个 finding 相对任务开始的毫秒差;0/缺省=尚未产出
  findings_per_hour?: number; // 产出速率 = findings 总数 ÷ 已运行小时(保留一位小数)
  pending_approvals?: number; // 等待人工决定的拦截审批条数;>0 时显示「等待审批」徽章
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  token_budget?: number; // 任务级 token 预算(0=不限)；用尽自动暂停，调高后可恢复
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // 资产覆盖度功能开关(创建时定,默认开)；false=不计算/不展示覆盖度
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // 预设分类；null/缺省=无
  intercept_rules?: AssetInterceptRuleInput[]; // 预设的任务级拦截/允许规则
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // 归属公司资产 id；空=未归属
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// 力导向「资产覆盖图」的一个节点。key 唯一：资产="a:<id>"、公司="c:<id>"、
// 无资产行的根域名="r:<domain>"。in_scope=false 的是仅用于连线的灰色上下文节点。
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// 某资产在本任务探索图里关联到的意图/事实/发现（覆盖图节点抽屉用）。
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  /** 目录为整棵子树的累计字节数；文件为自身大小。 */
  size: number;
  mtime: number; // unix millis
  /** 目录子树里的文件数（文件自身为 1）。 */
  files?: number;
  /** 目录子树里的子目录数。 */
  dirs?: number;
  /** true 表示 size 没统计完（超时或个别条目读不到），界面显示「≥」。 */
  size_partial?: boolean;
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
  /** 当前目录的合计大小 / 文件数 / 子目录数。 */
  total_size?: number;
  total_files?: number;
  total_dirs?: number;
  total_partial?: boolean;
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// 任务测试范围的一条（覆盖度分母 + 授权边界）。
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // 后端 JOIN companies 解析，仅 kind=company 有值
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// 新增企业时提交的结构化资产范围规则。
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// 资产范围写入的结果。errors 是本次提交里不合法的行；warnings 是与本次提交无关、
// 但会让归属结果不符合预期的既有数据问题（如 ip 字段存了主机名的资产）。
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// 公司资产范围规则的一条（归属唯一真值来源）。
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // kind=domain 时有值
  net?: string; // kind=ip|cidr 时有值
  value?: string; // kind=icp|keyword 时可能由后端直接返回
  raw: string; // 原始用户输入，用于显示和回填
  reason?: string;
}

// 企业：type=company 的资产节点 + 图标 + 资产计数 + 资产范围规则。
export interface Company {
  id: number;
  name: string;
  logo?: string; // 远程图标 URL；空则前端用名称首字母
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

// 未收口分支（完成闸门）：来自 GET /tasks/{id}/branches 的 blockers。
export interface TaskBlocker {
  intent_id: number;
  end_state: string;
  stop_reason: string;
  attempt: number;
  node_state: string;
  has_handoff: boolean;
  /** 已自动回炉次数（P2 策略机记账）。 */
  requeues?: number;
  /** 已转人工：不再自动重跑，等人决定「继续 / 收口 / 强制完成」。 */
  needs_human?: boolean;
  /** 退避重跑的下一次时间（RFC3339）。 */
  next_retry_at?: string;
}

/** 交接单 L2 主观层快照（永远只是建议）。 */
export interface HandoffOpenWorkSnapshot {
  next_actions?: string[];
  hypotheses?: string[];
  negative_results?: string[];
  advisory?: boolean;
}

/** 交接单 L1 事实层（系统机械抽取）。 */
export interface IntentHandoffRecord {
  schema?: string;
  trigger?: { kind?: string; detail?: string; at?: string };
  progress?: {
    steps_total?: number;
    written?: Record<string, number>;
    last_output?: string;
    tail?: { id: number; tool?: string; kind?: string; summary?: string; is_error?: boolean }[];
  };
  pointers?: { session_id?: string; trace_range?: number[] };
  open_work?: HandoffOpenWorkSnapshot;
  [k: string]: unknown;
}

/** 查看器一次拉回的全量数据。 */
export interface IntentHandoffView {
  intent_id: number;
  handoff: null | {
    id: number;
    run_id: number;
    schema: string;
    created_at: string;
    mech_sha256: string;
    summary: string;
    record: IntentHandoffRecord;
  };
  open_work: {
    id: number;
    intent_id: number;
    author?: string;
    next_actions?: string;
    hypotheses?: string;
    negative_results?: string;
    created_at: string;
  }[];
  closures: {
    id: number;
    intent_id: number;
    actor: string;
    action: string;
    reason_code: string;
    note: string;
    created_at: string;
  }[];
}

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // 意图假删除(state='deleted')时的删除原因
}

// 播报板一页:按创建顺序分页的节点 + 这一页涉及的边 + 边另一端的节点(refs,按 id 索引),
// 这样每条播报都能说清「从哪来、产出了什么」,而不用把整张图拉下来。
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // 节点 id → 该节点锚定的资产(播报板展开时顺带展示,含本页节点与其邻居)。
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// 目标管理卡片用的目标(后端已把 payload 拆成 text/vulnclass)。
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// 约束管理卡片用的操作约束(allow=允许 / deny=禁止)。
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// 漏洞处置状态:待处理 / 处理中 / 已确认 / 已处理 / 已修复 / 误报 / 忽略 / 重复 / 风险接受。
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset 是一个漏洞绑定的资产(已在后端预渲染 label)。
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

// 独立复核结论。与 status 正交：status 是「人怎么处置」，validation 是
// 「这条漏洞本身有没有被独立复核过」；只有 verified 才计入资产真值与报告正文。
export type FindingValidation = "unverified" | "verified" | "refuted";

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // 独立 findings 表的行 id,状态更新的句柄(任务内旧节点可能缺失)
  vulnclass: string;
  name?: string; // 漏洞名称;为空时展示回退到 vulnclass
  severity: Severity;
  status: FindingStatus;
  validation?: FindingValidation;
  summary: string;
  evidence: string;
  report?: string; // 详细报告(Markdown);仅详情接口返回,列表为空
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage 是发现列表的服务端分页响应。
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // 可选任务名称;空/缺省=未命名
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats 是发现全表聚合(统计卡 + 漏洞类型下拉),服务端计算,不受分页影响。
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption 是发现页「按任务」筛选下拉的一项:有漏洞的任务(描述为空表示任务已删除,
// 前端回退展示 id)及其漏洞条数。
export interface FindingTaskOption {
  id: string | number;
  name?: string; // 可选任务名称;空/缺省=未命名
  description: string;
  count: number;
}

// FindingQuery 是发现列表分页/筛选/排序参数。
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // 任务 id;"all"/空 = 不按任务筛选
  query?: string;
  sort?: "severity" | "time";
  // 资产树节点 key;选中一个节点 = 选中它的整棵子树。空 = 不按资产筛选。
  assetScope?: string;
}

// ---- Findings by asset (资产视图) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode 是资产树的一个节点。key 形如 a:<id>(资产)、c:<id>(企业)、
// r:<domain>(库里没有资产行的根域名)、__none__(未关联资产)。
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // 直接挂在该资产上的发现数
  total: number; // 含子孙、按发现去重
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET 与后端 db.FindingUnassignedAsset 对应。
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment 是一次上传的文件:path 相对该会话/任务工作目录(即 agent 的 CWD)。
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // 绝对路径(scope=staging 暂存上传时返回;建任务前把它写进描述)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
    // 工具行的结构性结论。中断、拦截这类状态只有后端知道，前端不再靠文案猜；
    // 老数据没有这个字段，前端退回文案标记匹配（见 lib/tool-status.ts）。
    tool_status?: "completed" | "failed" | "denied" | "stopped";
    // usage 行上的上下文容量：窗口是模型属性（后端在 capture 时写入），
    // 已用是本轮输入口径（input + cache 读写）。缺失则前端不显示容量环。
    context_window?: number;
    context_used?: number;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 调度，仅自定义 agent) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // 定时:每 N 秒(0=不定时;>0 时服务端要求最小 60)
  on_finding: boolean; // 任意任务发现 finding 时触发
  on_goal_met: boolean; // 任意任务达成目标时触发
  on_task_timeout: boolean; // 任意任务超时时触发
  on_tool_call: boolean; // 选中工具被调用(执行完成)时触发
  on_task_create: boolean; // 任意任务被创建时触发
  interval_message: string; // 各触发条件的独立用户消息
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // on_tool_call 选中的工具 key(至少一个)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface ActiveFindingVerification {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

// FindingVerification 是一次独立复核的记录（POST/GET .../verifications）。
export interface FindingVerification {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "verified" | "refuted" | "inconclusive";
  notes: string;
  // reason 记录为什么复核：auto:severity=high / auto:sample / manual。
  reason: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Checkpoints (批次 6：时间线检查点与回滚) ----
// 快照只存边界引用（活动 id / transcript 大小 / 图计数），不复制消息。
export interface CheckpointSnapshot {
  activity_id: number; // 快照包含的最大活动 id；回滚隐藏 id 大于它的步骤
  transcript_size?: number;
  transcript_path?: string;
  node_id?: number;
  counts?: Record<string, number>;
  intents?: Record<string, number[]>;
  compaction?: Record<string, unknown>;
}

export interface Checkpoint {
  id: number;
  scope: string; // "conv-<id>" | "task-<id>"
  seq: number; // scope 内单调时间线地址
  reason:
    | "user_message"
    | "turn_complete"
    | "admit"
    | "intent_settled"
    | "interrupt"
    | "compaction_recover"
    | "revert"
    | "manual"
    | (string & {});
  status: "active" | "superseded" | (string & {});
  summary: string;
  snapshot: CheckpointSnapshot;
  created_at: string;
}

export interface CheckpointRevertResult {
  status: string;
  hidden: number; // 被隐藏的步骤数
  superseded: number; // 作废的后续检查点数
  transcript_truncated: boolean;
  checkpoint?: Checkpoint | null; // 回滚标记检查点
  reopened?: number[]; // 检查点回滚重排为待执行的意图 id（消息级回滚不返回）
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// 批量改分类的逐任务结果。失败只可能是任务已被删除，分类本身的写入是原子的。
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  // 费用估算(USD)：后端按 settings.price_table 读时计算；未配价格表或模型未命中时省略。
  cost_usd?: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  // 费用估算(USD)，与 ProfileUsage.cost_usd 同源同口径；未配价格表时省略。
  cost_usd?: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  // 费用估算(USD)：后端按 settings.price_table(模型名子串、最长匹配)读时计算。
  // 未配价格表或模型未命中时省略；同一响应的 cost_total 是全任务合计。
  cost?: number;
}

// Per-role (agent 泳道) token usage for one task (GET /api/llm/records/by-worker),
// same always-on llm_usage ledger as ModelTokenStat. worker is the raw lane
// identifier llmrec resolves from the session prefix: worker / planner / main /
// goals / compactor / btw / (unknown).
export interface WorkerTokenStat {
  worker: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  call_id?: string; // intercept trace 的 ToolUseID；空 = 无法配对
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent 自动绑定流量证据，默认关闭；不影响人工绑定
  // 复核成本闸门(见 server/verify_policy.go)：severity 达到 verify_auto_threshold 的发现
  // 入库即自动派发独立复核；低于阈值的按 verify_sample_percent 抽查(0-100,0=不抽查)。
  // 阈值枚举 off | critical | high | medium | low，默认 high(=critical/high 强制)。
  // 选 off 则只保留在漏洞详情里手动发起复核。改完对之后入库的发现立即生效。
  verify_auto_threshold: string;
  verify_sample_percent: number;
  llm_record: boolean; // LLM 录制开关（默认关）；关闭时不记录任何 LLM 调用
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // 独立出口代理(http/https/socks5)，用于访问搜索端点；与记录流量的 MITM 代理无关。空=直连。
  web_search_proxy?: string;
  // 全局出口代理(http/https/socks5，可带 user:pass)，所有目标流量走它。开启流量捕获时作为
  // MITM 上游；关闭捕获时直接注入 agent 的 bash/WebFetch。空=直连。
  global_proxy?: string;
  python_interpreter?: string; // 自定义脚本工具的 python 解释器路径(空=运行时检测)
  workers?: number; // 并发工作 agent 数(默认3)；对之后启动的任务生效
  // 新建任务的默认 token 预算(input+output,0=不限)；仅在创建任务未显式给预算时取它。
  // 预算闸在每次 run 收尾按 llm_usage 累计 input+output 检查，用尽自动暂停任务。
  default_task_token_budget?: number;
  // 模型价目 JSON(USD/百万 token)：{"claude-opus":{"input":15,"output":75,
  // "cache_read":1.5,"cache_write":18.75}, ...}。key 是模型名子串(最长匹配优先)，
  // 空串=清除(不算钱)。读取接口据此在读时现算费用(历史数据即刻生效)。
  price_table?: string;
  // 任务并发上限:同时「运行中」的任务数上限。关闭=不限;开启后新建任务超限则排队,有空位自动启动。
  task_concurrency_enabled?: boolean; // 默认 false
  task_concurrency_limit?: number; // 开启后默认 5
  // LLM 轮询(故障转移)。默认关；开启后「未指定模型」的 agent 在当前配置不可用
  // （余额不足/key 失效/限流/服务异常）时自动切到下一个配置。
  llm_pool_enabled?: boolean; // 默认 false
  // 绑定了指定配置的 agent/任务失败时是否也回落到轮询链。默认 false = 绑定即独占。
  llm_pool_bind_fallback?: boolean;
  // 操作约束注入范围(默认都开):把任务的 allow/deny 约束拼进对应 agent 的系统提示。
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // 实验功能:noa 模型驱动上下文压缩(默认关)。开启后平台接入的四类 agent(planner/
  // worker/主 agent/对话)由 noa 接管上下文压缩,取代内置 compaction;每 run 读一次,对
  // 之后启动的 run 生效。
  noa_compaction?: boolean;
  // 实验功能:完成闸门（未收口分支不许判完成）。off=关 / shadow=只记录不拦 / enforce=真拦并回炉。
  // 切换即时生效（每次完成判定读一次），无需重启。
  branch_gate_mode?: "off" | "shadow" | "enforce" | string;
  // AI 自我进化沉淀：任务完成/删除时把可复用路径、证据与失败模式整理成技能。
  // skill_review_gate=true（默认）时先进入待审阅队列；auto_distill_apply=true 才直接落盘。
  skill_review_gate?: boolean;
  skill_auto_distill?: boolean;
  skill_auto_distill_apply?: boolean;
  // 版本更新偏好（存在服务端，多客户端共享）：
  // update_auto_check=true 定时检测新版本（默认开，只读版本清单，不下载不换装）；
  // update_auto_apply=true 检测到后自动下载并校验，下次启动生效（默认关）——
  // 渗透任务可能跑到一半，自动更新不替用户决定"现在就重启"。
  update_auto_check?: boolean;
  update_auto_apply?: boolean;
  // ---- 漏洞 IM 推送（渠道本身是独立资源，见 /api/notify/*，这里只有三项全局配置）----
  notify_enabled?: boolean; // 推送总开关，默认开；用于维护期一键止血
  notify_public_base_url?: string; // 漏洞详情回链的外部访问地址；空=消息不带回链
  notify_digest_interval_min?: number; // 摘要周期单位为分钟，默认 30。
  // 用户输出语言为 zh/en/es，旧 ko 兼容归一为 zh。模型报告、摘要、对话遵循此值；
  // 设置页语言选择器同时保存 UI 与模型输出偏好。
  output_language?: string;
}

// ---- 漏洞 IM 推送 ----

// NotificationFilter 是渠道的过滤条件，字段全部可选，缺省即不过滤。
// 后端对所有字段都不加校验：配置畸形时按「命中」处理（宁可多推不可漏推）。
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // 空=不限；非空则要求与漏洞所属任务有交集
  asset_ids?: number[]; // 空=不限；非空则要求与漏洞锚定资产有交集
  vulnclass_include?: string[]; // 空=全收；非空则要求漏洞类型命中任一关键词（大小写不敏感子串）
  vulnclass_exclude?: string[]; // 命中任一关键词即排除（排除优先于包含）
  on_status_change?: boolean; // 是否也接收漏洞处置状态变更事件
  on_task_status?: boolean; // 是否接收任务状态变化（完成/失败/超时/暂停）
  on_approval?: boolean; // 是否接收「等待人工审批」事件
}

// NotificationChannel 是一个渠道实例。config 的字段随 kind 而异，
// 且凭据字段在读取时被替换成 "__masked__" 开头的掩码值——原样回传即表示「不改」。
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys 由后端按渠道类型给出，前端据此渲染密码框与「留空即不改」提示，
  // 不硬编码任何渠道知识。
  secret_keys: string[];
}

// NotificationKind 是 /api/notify/meta 返回的渠道类型元数据。
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery 是一条投递记录，用于投递历史与失败重发。
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // ---- 能力探测（后端 llmprobe 自动填）----
  // 供应商端点上报的模型能力；没探测过时整个 caps 缺省。
  caps?: {
    context_tokens?: number;
    max_output_tokens?: number;
    input_modalities?: string[];
    output_modalities?: string[];
    reasoning?: boolean;
    reasoning_efforts?: string[];
    tools?: boolean;
    // provider=端点实测 | catalog=内建目录 | defaults=保守默认 | local=本机推理进程
    source?: string;
    model?: string;
    probed_at?: string;
    notes?: string[];
  };
  // 压缩与用量展示实际使用的窗口（K）：实测优先，与上面的声明值取小。
  effective_context_window_k?: number;
  context_window_detected?: boolean;
  // 思考开关(thinking.type): ""=不发送(默认) | "disabled"=关闭 | "enabled"=开启
  thinking_type?: string;
  // 思考强度: ""=不发送(默认) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // 轮询顺位：越大越先被选中。激活配置恒为链首，与本值无关。
  priority?: number;
  // true = 不作为故障转移目标（仍可被 agent/任务显式绑定使用）。
  pool_exclude?: boolean;
  // true（默认）= 流式(SSE) | false = 真·非流式(stream:false，一次性返回)。
  streaming?: boolean;
  // 单次回复的输出上限(token)。0 = 不发送该字段，由服务端默认值决定。
  // 注意与 context_window_k 区分：后者是模型总容量，只在本地用于压缩阈值。
  max_tokens?: number;
  // 上限用哪个请求字段名，仅 format="openai" 有意义：
  // ""=max_tokens(默认) | "max_completion_tokens"(OpenAI 推理模型只认它)
  max_tokens_field?: string;
  // 自定义会话头名：非空时每次请求带该 HTTP 头，头值=当前会话/意图的 session id。
  // ""=不发送。用于按 session-id 头做提示缓存/粘性路由的网关。
  session_header_key?: string;
  // 本配置对重试的覆盖（建连/空响应/同 provider 安全窗口）。留空/全 0 = 跟随全局策略。
  retry?: LLMRetryOverride;
  // 输入模态声明：逗号分隔的 text/image/audio/pdf。""（或省略）= 未声明，后端按
  // 模型名保守推断。text 始终隐含；保存时后端统一归一（补 text、规范顺序）。
  input_modalities?: string;
  // 运行时实际生效的模态（声明优先，否则按模型名推断）。文本始终包含，卡片上只
  // 展示图片/音频/PDF 这些扩展模态。
  effective_input_modalities?: string;
  // 所属供应商（任务 16）：端点 / 代理 / Key / 限速 / 会话头都住在这一层，
  // 模型只保留自己那份参数。字符串形式与其它 id 一致；"0"/缺省 = 未归属。
  provider_id?: string;
  provider_name?: string;
}

// 供应商 = 端点（format / base_url / proxy）+ 鉴权（Key）+ 端点级限速与会话头。
// Key 就存在供应商上：没有独立凭据池，也没有「Key 来自别处」的第二种形态。
// 列表接口一次带回名下模型，设置页据此渲染「卡片 + 模型行」，不必再发 N 个请求。
export interface LLMProvider {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  session_header_key?: string;
  model_count: number;
  models: LLMProfile[];
  // 非空 = 存库的 Key 读不出来（主密钥丢失/更换）。只影响显示，重填 Key 即可恢复。
  key_error?: string;
}

// ---- LLM 重试策略 ----
// 一层重试的两个旋钮。两者都是「0 = 未配置」：
//   attempts    0=用默认次数 | -1=关闭该层重试 | >0=重试次数
//   interval_ms 0=用默认的指数退避 | >0=改用这个固定毫秒间隔
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// 单个 LLM 配置能覆盖的三层（都是「跟着端点走」的重试）。
export interface LLMRetryOverride {
  connect: LLMRetryRule; // 建连重试：连接重置/超时/429/5xx，流开始前
  empty: LLMRetryRule; // 空响应重试：完成但没有任何内容（仅 openai 格式）
  stream: LLMRetryRule; // 同 provider 安全窗口重试：未交付输出前的断流重放
}

// 全局策略 = 上面三层的默认值 + 两层只有全局的：
//   breaker 轮询熔断（attempts=连续几次瞬时失败熔断，interval_ms=固定冷却时长）
//   intent  意图重跑（worker 以 model_error 收场后整条意图重跑）
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM 轮询（故障转移）----
// 一个配置在轮询链中的位置与健康状态。state:
//   ok       正常
//   degraded 有连续失败但未达熔断阈值
//   tripped  已熔断，冷却期内被跳过（cooldown_secs 为剩余秒数）
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  // 所属供应商（任务 16）。同一家供应商可能在链上连着出现多条模型，标出归属
  // 才能分清「换的是端点」还是「只是换个模型名」。
  provider_id?: string;
  provider_name?: string;
  priority: number;
  active: boolean; // 是否为当前激活配置（恒为链首）
  excluded: boolean; // pool_exclude：不参与轮询
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // 内置为 goals/planner/mainagent/worker；自定义为用户自定 key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // 绑定的 LLM 配置；null/absent = 跟随任务/会话/全局
  max_turns?: number; // 0 = 无限制
  run_seconds?: number; // worker 单次运行墙钟上限(秒)；0 = 无限制
  web_search?: boolean; // 是否启用网络搜索(受系统全局开关门控)
  interactive_shell?: boolean; // 是否启用交互式 shell(持久 PTY 会话工具族)
  // P3 触发后处理策略(仅自定义 agent 有意义)
  trigger_run_mode?: "serial" | "parallel"; // 串行排队 / 每次触发各自并发一个会话
  trigger_merge_mode?: "by_task" | "all" | "none"; // 仅 serial：同任务合并 / 全部合并 / 不合并
  trigger_max_parallel?: number; // 仅 parallel：每 agent 并发上限；0=不限
  // 绑定数量(仅列表接口返回)：可见 MCP / 可见 Skill / 绑定工具
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentLLMProfileOption {
  id: number;
  name: string;
  model: string;
  is_default: boolean;
  // 能力元数据（随批次 3）：备用链编辑器据此显示能力徽章与匹配建议，缺省=未知。
  context_window_k?: number;
  effective_input_modalities?: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // 可绑定的 LLM 配置候选(供「默认模型」下拉与备用链编辑器)；当前绑定见 agent.llm_profile_id
  llm_profiles?: AgentLLMProfileOption[];
  // 备用模型链尾(#139/#144)：按顺序的配置 id；链首 = agent.llm_profile_id（主绑定）。
  llm_chain_fallbacks?: number[];
  wrapup_prompt?: string; // 已保存的收尾提示词(空=用内置默认)
  wrapup_default?: string; // 内置默认收尾提示词(占位/恢复默认)
  wrapup_max_turns?: number; // 已保存的收尾轮数(0=用内置默认)
  wrapup_max_turns_default?: number; // 内置默认收尾轮数(供 "0=默认N" 提示)
  // 任务级超时收尾词(仅 worker/planner，task_timeout_wrapup_supported=true 时才显示该分区)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

// 对外 MCP 接入（系统 → MCP 卡片）。方向与上面的 MCPServer 相反：
// MCPServer 是 ARTEX 去连别人，MCPExposure 是别人来连 ARTEX。
export interface MCPExposureTier {
  id: string;
  label: string;
  enabled: boolean;
}

export interface MCPExposureTool {
  name: string;
  tier: string;
  description: string;
}

export interface MCPExposure {
  enabled: boolean;
  tiers: string[];
  all_tiers: MCPExposureTier[];
  endpoint: string;
  exe?: string;
  token: string;
  token_mask: string;
  tools: MCPExposureTool[];
  tool_count: number;
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // 调用统计（skill_usage 账本）。从未被调用过的 skill：calls=0、last_used 缺省。
  calls: number;
  tasks: number; // 加载过它的任务数（chat 会话不计入）
  usage_agents: string[]; // 加载过它的 agent key
  last_used?: string;
  // 来源（后端判定，见 server/skill_origin.go）。builtin=随版本发布的内置战法；
  // distilled/merged/report=任务结束后沉淀的复盘；user=自建或上传。
  // 可选：旧后端没有这个字段时前端按命名前缀兜底。
  origin?: "builtin" | "distilled" | "merged" | "report" | "user";
  report_derived?: boolean; // 内置，但由外部报告提炼
  out_of_sync?: boolean; // 与当前内置版本不一致（本地改过或版本落后）
  pending_proposals?: number; // 待审阅提议数（治理信号）
}

// SkillCall 是一次 Skill() 调用（单个 skill 的最近调用列表）。
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = 非任务场景（对话会话）
  session_id: string;
  args_len: number;
}

// MissingSkill 是被点名但不存在的 skill —— "想用但没有"的缺口。
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

export type SkillProposalStatus = "pending" | "applied" | "rejected";

export interface SkillProposal {
  id: string;
  skill: string;
  file: string;
  action: string;
  content: string;
  origin: string;
  evidence: string;
  lint: string;
  status: SkillProposalStatus;
  note: string;
  created_at: string;
  reviewed_at?: string;
}

export interface SkillProposalListResponse {
  proposals: SkillProposal[];
  counts: Record<string, number>;
  review_gate: boolean;
  auto_distill: boolean;
  auto_distill_apply: boolean;
}

export interface ReportImportResult {
  ok: boolean;
  skill: string;
  status: SkillProposalStatus;
  engine: "llm" | "deterministic";
  files_written: string[];
  proposals: SkillProposal[];
  proposal: SkillProposal | null;
  findings_count: number;
  pitfalls_count: number;
  chains_count: number;
}

export interface VerificationCapabilities {
  native_protocols: string[];
  fingerprint_helper: {
    state: "not_installed" | "configuration_invalid_or_missing" | "detected_not_handshaken" | string;
    provider: string;
    profiles: string[];
    optional_separate_module: boolean;
    ipc_version: number;
    handshake_before_network: boolean;
    platform: string;
    binary_name: string;
  };
  not_supported: string[];
  limits: Record<string, number>;
  models_bundled: boolean;
  automatic_download: boolean;
}

// ---- Tools (内置工具目录) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // 自定义工具类型
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // 自定义工具执行规格(kind!=builtin)
  deferred?: boolean; // schema 延迟(SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- 平台 shell 探测（/api/health.shell；SDK Bash 工具的实际执行器与语法引导） ----
export interface ShellCapability {
  os: string;
  executor: string;
  available?: string[]; // PATH 上探测到的 shell
  guidance?: string; // 中文引导（仅非 bash 平台非空）
}

// ---- Reliability metrics (/api/metrics；本次进程启动以来的健康度，非历史账本) ----
export interface ReliabilityToolStat {
  tool: string;
  calls: number;
  errors: number;
  stages?: Record<string, number>; // 按错误阶段细分（hook/parse/validate/exec/timeout）
}

export interface ReliabilityToolMetrics {
  calls: number;
  errors: number;
  error_rate: number; // 错误/调用；无调用时为 0
  by_stage: Record<string, number>;
  top_tools: ReliabilityToolStat[];
}

// 崩溃重放与中断恢复共用的形状：attempts=尝试数，pending=尚未到达终态。
export interface ReliabilityRecovery {
  attempts: number;
  succeeded: number;
  failed: number;
  pending: number;
  success_rate: number; // 成功/(成功+失败)；无结算时为 0
}

export interface ReliabilityProtocol {
  unpaired_results: number; // 无调用 ID、已丢弃的结果
  unknown_results: number; // 有 ID 但无对应 tool_use 的结果
  missing_results: number; // 执行结束未收到结果的调用
}

export interface ReliabilityMetrics {
  since: number; // unix 秒；计数起始时刻（进程启动）
  tools: ReliabilityToolMetrics;
  replay: ReliabilityRecovery;
  interrupts: ReliabilityRecovery;
  protocol: ReliabilityProtocol;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules（资产拦截：全局黑名单） ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action 仅用于任务级规则：block=拦截(禁止测试) allow=允许(白名单)。
export type AssetInterceptAction = "block" | "allow";

// 任务级资产拦截/允许规则的录入项（创建任务、任务详情编辑使用）。
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // 全局规则不带此字段（恒为拦截）；任务级规则区分 block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // 规则 message 或模型判定理由(模型判定带 [模型] 前缀)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: 模型兜底审批(仅当没有任何拦截规则命中时由模型判断)的全局配置。
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = 跟随激活/默认配置
  prompt: string; // 判定提示词;GET 未设置时后端回填内置模板全文
  timeout_seconds: number; // 模型调用超时
  fail_action: "allow" | "ask" | "deny"; // 模型出错/超时/不可解析时的回退
  ask_timeout_seconds: number; // 模型判 ask 转人工后的审批等待超时
  ask_timeout_action: "allow" | "deny"; // 审批超时后的默认动作
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── 资产同步 (ScopeSentry 数据源) ──────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// 单个工具的调用统计（/commands/stats）；errors 为其中失败的次数。
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
  // 费用估算(USD)：后端按 settings.price_table 读时计算；未配价格表或模型未命中时省略。
  cost?: number;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // provider 实际收发的 HTTP 原文：请求为 buildBody() 发出的完整 body（含工具
  // schema），响应为原始 SSE 帧。上面的 request_body/response_body 是归一化视图，
  // 丢弃了工具 schema 与 tool_use 块。旧记录为空。
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
  // 该任务已记录调用的费用估算(USD)；未配价格表或模型未命中时省略。
  cost?: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check —— 当前版本与 GitHub 最新正式版的比较结果。 */
export interface UpdateCheck {
  /** 当前运行的版本；开发构建为 "dev" 或 git describe 的带后缀形式。 */
  current: string;
  /** 运行形态。docker 下换装只作用于容器可写层，重建容器会退回镜像版本。 */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** 是否存在可回滚的上一版本（artex.old）。 */
  has_backup: boolean;
  /** 本次启动时自更新自举的结论（换装失败 / 已回滚等），无事发生时为空。 */
  boot_notice?: string;
  rolled_back?: boolean;
  /** 查询 GitHub 失败时给出原因，此时下面的字段都不会有。 */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** 当前平台对应的发布包名，以及该 Release 是否真的带了它。 */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** 双方版本号是否可比较；开发构建为 false，此时禁用一键更新。 */
  comparable?: boolean;
  /** comparable 为 false 时的说明。 */
  reason?: string;
  /** 是否由守护脚本（start.sh / start.bat）启动；false 时自动更新无法自动重启。 */
  /** 当前环境能否自动重启（进程内置自拉；以前等同于是否有 start.sh/start.bat 监护） */
  supervised?: boolean;
  /** 已下载并校验、等待下次启动生效的版本；空 = 没有暂存件。 */
  staged_version?: string;
  /** 自动检测开关（服务端存储，默认 true）。 */
  auto_check?: boolean;
  /** 自动下载开关（服务端存储，默认 false）。 */
  auto_apply?: boolean;
}

/** /api/update/stream 推送的一条更新进度。 */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** 仅下载阶段有意义（0-100）；其余阶段为 -1。 */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

/**
 * GET /api/update/status —— 只读缓存的轻量状态，绝不触发网络请求。
 * 顶栏红点用它：自动检测关掉时 checked 恒为 false，不会在后台替用户查 GitHub。
 */
export interface UpdateStatus {
  current: string;
  staged_version?: string;
  /** 后端是否已经查过（有缓存）；false 时下面的字段都没有。 */
  checked: boolean;
  checked_at?: string;
  error?: string;
  latest?: string;
  has_update?: boolean;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}

// Software analysis objects retain explicit sample/session/evidence ownership.
export interface RESample {
  sha256: string;
  name: string;
  size: number;
  metadata: Record<string, unknown>;
  created_at: string;
}
export interface REOperation {
  tool: string;
  input: Record<string, unknown>;
  rows_path?: string;
  columns?: Record<string, string>;
  address_kind?: "rva" | "va" | "file_offset" | "provider";
}
export interface REProviderConfig {
  mode: "open" | "attach";
  reviewed_static: boolean;
  session_key_path?: string;
  identity_hash_path?: string;
  operations: Record<string, REOperation>;
}
export interface RECatalogTool {
  name: string;
  description: string;
  input_schema: Record<string, unknown>;
}
export interface REProvider {
  id: number;
  mcp_id: number;
  name: string;
  kind: "rea" | "ghidra" | "ida";
  config: REProviderConfig;
  catalog: RECatalogTool[];
  catalog_hash: string;
  discovery: string;
  message: string;
  updated_at: string;
}
export interface RESession {
  id: number;
  task_id: number;
  sample_sha: string;
  provider_id: number;
  state: "opening" | "ready" | "busy" | "closing" | "closed" | "failed" | "interrupted";
  message: string;
  snapshot: REProvider;
  created_at: string;
  updated_at: string;
}
export interface REEvidence {
  id: number;
  session_id: number;
  sample_sha: string;
  operation: string;
  tool: string;
  input: Record<string, unknown>;
  output?: string;
  output_sha: string;
  retained_sha: string;
  diagnostic: string;
  is_error: boolean;
  truncated: boolean;
  created_at: string;
}
export interface REEntity {
  id: number;
  session_id: number;
  evidence_id: number;
  kind: string;
  identity: string;
  name: string;
  module: string;
  address: string;
  address_kind: string;
  payload: Record<string, unknown>;
}
export interface REEnginePreset {
  kind: "rea" | "ghidra" | "ida";
  title: string;
  description: string;
  config: REProviderConfig;
}
export interface RECapabilityReport {
  provider_id: number;
  kind: string;
  mode: string;
  reviewed_static: boolean;
  discovery: string;
  catalog_count: number;
  core_ready: boolean;
  gate_reason: string;
  ready_operations: string[];
  missing_tools?: string[];
  schema_mismatches?: string[];
  unmapped_operations?: string[];
}
export interface REOverview {
  samples: RESample[];
  providers: REProvider[];
  sessions: RESession[];
  capabilities?: Record<number, RECapabilityReport>;
  presets?: REEnginePreset[];
  limit: number;
  scope: string;
}
export interface REDataPage<T> {
  items: T[];
  has_more: boolean;
  next_before: number;
}
export interface REMCPCandidate {
  id: number;
  name: string;
  transport: string;
  enabled: boolean;
  cached_tools: string[] | null;
}

// ---- 外部 harness 接入（ARTEX 作为 MCP 服务端）----

export type HarnessConfigStatus = {
  scope: string;
  display: string;
  path: string;
  resolved: boolean;
  format: string;
  strategy: string;
  note?: string;
  exists: boolean;
  writable: boolean;
  has_artex: boolean;
  size_bytes: number;
  unreadable?: string;
};

export type HarnessClientStatus = {
  id: string;
  name: string;
  vendor: string;
  kind: string;
  src: string;
  mcp: string;
  tier: string;
  artex: string;
  restart: boolean;
  needs_verify: boolean;
  notes: string;
  doc: string;
  bin_path?: string;
  state: string;
  stale: boolean;
  configs: HarnessConfigStatus[];
};

export type HarnessScan = {
  version: number;
  generated: string;
  project_dir: string;
  endpoint: string;
  exe: string;
  token_mask: string;
  stats: Record<string, number>;
  clients: HarnessClientStatus[];
};

export type HarnessChange = {
  client_id: string;
  client_name: string;
  tier: string;
  needs_verify: boolean;
  scope: string;
  format: string;
  strategy: string;
  path: string;
  display_path: string;
  action: string;
  reason?: string;
  diff?: string;
  after?: string;
  snippet?: string;
  applied?: boolean;
  backup?: string;
  error?: string;
};

export type HarnessPlanResult = {
  changes: HarnessChange[];
  summary: Record<string, number>;
};

/**
 * GET /api/update/journal —— 落盘的升级台账与版本指针。
 *
 * 与 SSE 进度不同，它跨进程存活：进程换装、崩溃、被守护脚本重启都不影响。
 * 前端在"正在重启"超时后读它，就能把失败原因（暂存件被替换、冒烟不过、
 * 健康检查不过、已回滚）明确告诉用户，而不是永远转圈。
 */
export interface UpdateJournal {
  state: "idle" | "staging" | "staged" | "applying" | "swapped" | "confirmed" | "failed" | "rolled-back" | string;
  root?: string;
  last_error?: string;
  detail?: string;
  staged_version?: string;
  staged_path?: string;
  attempts?: number;
  to?: string;
  journal?: {
    state: string;
    mode?: string;
    from?: string;
    to?: string;
    attempts?: number;
    swapped_at?: number;
    confirmed_at?: number;
    last_error?: string;
    detail?: string;
  } | null;
  pointer?: {
    available: boolean;
    current?: string;
    previous?: string;
    updated_at?: number;
    versions?: string[];
  };
}


