"use client";

import * as React from "react";

import {
  ArrowUpIcon,
  BrainIcon,
  ChevronDownIcon,
  CircleCheckIcon,
  CircleSlashIcon,
  CircleXIcon,
  ClockIcon,
  HistoryIcon,
  Loader2Icon,
  NavigationIcon,
  PaperclipIcon,
  PauseIcon,
  PlusIcon,
  RadioIcon,
  RotateCwIcon,
  ScaleIcon,
  ShieldAlertIcon,
  SquareIcon,
  Trash2Icon,
  UserIcon,
  WifiOffIcon,
  XIcon,
  ZapOffIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { AgentPhaseBadge } from "@/components/agent-phase-badge";
import { ApprovalExecutionFocus, useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { EmptyState } from "@/components/empty-state";
import { LiveDot } from "@/components/live-dot";
import { MentionTextarea } from "@/components/mention-textarea";
import { MessageRollbackDialog } from "@/components/message-rollback-dialog";
import { SideQuestionButton, SideQuestionWorkspace } from "@/components/side-question-workspace";
import { TodoPopover } from "@/components/todo-popover";
import { ContextUsageRing } from "@/components/transcript/context-usage";
import { parseUserBody, Transcript } from "@/components/transcript";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InputGroup, InputGroupAddon, InputGroupButton } from "@/components/ui/input-group";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { usePolling } from "@/hooks/use-polling";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { deriveAgentPhase } from "@/lib/agent-phase";
import { api, sseUrl } from "@/lib/api";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { MOCK } from "@/lib/mock/enabled";
import { isBtwCommand } from "@/lib/side-questions";
import type {
  Activity,
  ChatAttachment,
  IntentAsset,
  InterceptApprovalRow,
  LLMProfile,
  Session,
  SessionStatus,
  SessionTokenUsage,
  TaskLLMResolution,
  TaskLLMResolutions,
  ReviewerVerdict,
  TaskNode,
  TokenTotal,
} from "@/lib/types";
import { cn } from "@/lib/utils";

// resolutionLabel names the LLM a session runs on. The badge shows this alone and
// keeps the model id in its tooltip. Env-backed configs can arrive without a name,
// so fall back to the model id rather than rendering an empty badge.
function resolutionLabel(r: TaskLLMResolution, unnamed: string): string {
  return r.name || r.model || unnamed;
}

// fmtBytes renders a human file size for attachment chips (mirrors transcript.tsx).
function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// ── Reliability model (see docs/task-session-history-sse-remediation.md) ──────────
// The task's activity is NO LONGER one unbounded `allActivity` array replayed from
// SSE since=0. Instead:
//   • Each UI session (main | plan | intent:<id>) has its own lazily-loaded, reverse-
//     paginated cache (SessionState below). Opening a session loads only its latest
//     page; scrolling up pages older history in.
//   • A single task-level SSE (opened at since=snapshot_cursor from the first history
//     page) tails ALL agents' new activity; frames are dispatched by session_key.
//   • History + SSE meet gap-free at snapshot_cursor and are merged by seq (dedup),
//     so refresh / tab-switch / sleep / reconnect never drop the newest records.

const PAGE = 200; // history page size
const SYSTEM_SCAN_PAGE = 500; // generic activity pages scanned to recover sparse system audit events
const MAX_KEEP = 4000; // per-session in-memory cap; older pages re-fetched on scroll-up
const STREAM_WINDOW_MS = 5000; // "live" = activity seen within this window
const MAX_WORKER_MESSAGE_CHARS = 4000;

function newWorkerMessageRequestID(): string {
  return (
    globalThis.crypto?.randomUUID?.() ??
    `worker-message-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
  );
}

function workerMessageCharCount(value: string): number {
  return Array.from(value).length;
}

// One session's lazily-loaded, reverse-paginated cache. `lastTs`/`unread` are kept
// live even for sessions that were never opened, so the list shows liveness + unread
// without holding their full history.
type SessionState = {
  items: Activity[];
  loaded: boolean;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean; // older history remains above the loaded window
  earliestSeq: number; // earliest loaded id — reverse-pagination anchor
  unread: number;
  lastTs: string; // most-recent activity time (drives live badge; updated even when unloaded)
  error?: string;
};
type SessionStore = Record<string, SessionState>;

function emptyState(): SessionState {
  return {
    items: [],
    loaded: false,
    loading: false,
    loadingMore: false,
    hasMore: false,
    earliestSeq: 0,
    unread: 0,
    lastTs: "",
  };
}

// sessionKeyOf routes an activity to its stable session key. worker="planner" covers
// BOTH the Goal Agent's round-0 decomposition and the Planner (single Plan session).
function sessionKeyOf(a: Activity): string {
  if (a.worker === "system" || a.kind === "llm_switch" || a.kind === "llm_failover") return "system";
  if (a.worker === "mainagent") return `main:${a.main_seg ?? 0}`; // one key per conversation segment
  if (a.worker === "planner") return "plan";
  if (a.intent_id) return `intent:${a.intent_id}`;
  return "unknown";
}

// mergeBySeq unions two activity lists by seq (dedup) in ascending seq order. Every
// data source — latest page, older page, SSE compensation, live tail — goes through
// this, so history responses can never clobber live records received meanwhile.
function mergeBySeq(current: Activity[], incoming: Activity[]): Activity[] {
  if (!incoming.length) return current;
  const bySeq = new Map<number, Activity>();
  for (const a of current) bySeq.set(a.seq, a);
  for (const a of incoming) bySeq.set(a.seq, a);
  return [...bySeq.values()].sort((p, q) => p.seq - q.seq);
}

// statusIcon maps a session status to its icon. Worker terminal states are
// distinct & color-coded: 完成(绿勾圈) / 取消停止(琥珀斜杠圈) / 出错(红叉圈) /
// 步数耗尽(紫). running=蓝色转圈, pending(待领取)=灰时钟.
function statusIcon(status: SessionStatus) {
  switch (status) {
    case "running": // 执行中
      return <Loader2Icon className="size-3.5 animate-spin text-blue-500" />;
    case "paused":
      return <PauseIcon className="size-3.5 text-amber-500" />;
    case "pending": // 待领取(open intent)
      return <ClockIcon className="size-3.5 text-muted-foreground" />;
    case "done": // 完成
      return <CircleCheckIcon className="size-3.5 text-emerald-500" />;
    case "stopped": // 取消/停止(被 planner 终止)
      return <CircleSlashIcon className="size-3.5 text-amber-500" />;
    case "blocked": // 出错
      return <CircleXIcon className="size-3.5 text-red-500" />;
    case "exhausted": // 步数耗尽(撞 max_turns)
      return <ZapOffIcon className="size-3.5 text-violet-500" />;
    case "deleted": // 用户假删除
      return <CircleSlashIcon className="size-3.5 text-muted-foreground" />;
  }
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

const TokenMetrics = React.forwardRef<
  HTMLSpanElement,
  React.ComponentPropsWithoutRef<"span"> & {
    input: number;
    cache: number;
    output: number;
    labels?: "short" | "long";
  }
>(({ input, cache, output, labels = "short", className, ...props }, ref) => {
  const t = useTranslations("sessions");
  const names =
    labels === "short"
      ? [t("token.inShort"), t("token.cacheShort"), t("token.outShort")]
      : [t("token.inLong"), t("token.cacheLong"), t("token.outLong")];
  const values = [input, cache, output];
  return (
    <span
      ref={ref}
      className={cn("inline-flex min-w-0 flex-wrap items-center gap-1 text-muted-foreground", className)}
      {...props}
    >
      {values.map((value, index) => (
        <React.Fragment key={names[index]}>
          <span>{names[index]}</span>
          <Badge variant="secondary" className="h-5 px-1.5 font-mono tabular-nums">
            {fmtTokens(value)}
          </Badge>
        </React.Fragment>
      ))}
    </span>
  );
});
TokenMetrics.displayName = "TokenMetrics";

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

// ReviewerVerdictPanel 渲染一条判决的全部内容（只读）。
//
// 为什么不做成「会话/对话」形态：Reviewer 不是聊天 Agent，它一次 run 只产出一条
// 结构化判决，做成气泡流会伪造出一段并不存在的对话。这里按台账字段原样呈现，
// facts/clues/carry/negative 都是后端清洗过的 JSON 字符串，解析失败时就落回原文。
function ReviewerVerdictPanel({ v }: { v: ReviewerVerdict }) {
  const t = useTranslations("sessions");
  const listOf = (raw: string): string[] => {
    try {
      const parsed = JSON.parse(raw || "[]");
      if (!Array.isArray(parsed)) return [];
      return parsed.map((x) => (typeof x === "string" ? x : JSON.stringify(x)));
    } catch {
      return raw ? [raw] : [];
    }
  };
  const facts = listOf(v.facts_json);
  const clues = listOf(v.clues_json);
  const carry = listOf(v.carry_json);
  const negative = listOf(v.negative_json);
  const evidence = listOf(v.evidence_json);
  const verdictKey = ["advance", "pivot", "abandon", "escalate"].includes(v.verdict)
    ? v.verdict
    : "unknown";
  const verdictTone =
    v.verdict === "advance"
      ? "border-emerald-500/40 text-emerald-600 dark:text-emerald-400"
      : v.verdict === "abandon"
        ? "border-destructive/40 text-destructive"
        : "border-amber-500/40 text-amber-600 dark:text-amber-400";

  const Section = ({ title, items }: { title: string; items: string[] }) =>
    items.length ? (
      <div className="grid gap-1">
        <div className="font-medium text-muted-foreground text-xs">{title}</div>
        <ul className="grid gap-1">
          {items.map((it, i) => (
            <li
              key={`${title}-${i}-${it.slice(0, 12)}`}
              className="rounded-md border bg-muted/30 px-2 py-1 text-xs leading-relaxed"
            >
              {it}
            </li>
          ))}
        </ul>
      </div>
    ) : null;

  return (
    <div className="mx-auto grid w-full max-w-3xl gap-3 px-1 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className={cn("font-mono text-xs uppercase", verdictTone)}>
          {t(`reviewer.verdict.${verdictKey}`)}
        </Badge>
        <Badge variant="outline" className="font-normal text-xs">
          {t("reviewer.attribution")}: {v.attribution || "—"}
        </Badge>
        <Badge variant="outline" className="font-normal text-xs tabular-nums">
          {t("reviewer.confidence")}: {Math.round((v.confidence ?? 0) * 100)}%
        </Badge>
        <Badge variant="outline" className="font-normal text-xs">
          {t("reviewer.decidedBy")}: {v.decided_by || "—"}
        </Badge>
        {v.sanitized && (
          <Badge variant="outline" className="border-amber-500/40 font-normal text-amber-600 text-xs dark:text-amber-400">
            {t("reviewer.sanitized")}
          </Badge>
        )}
      </div>

      <div className="rounded-lg border bg-card px-3 py-2">
        <div className="mb-1 font-medium text-muted-foreground text-xs">{t("reviewer.summary")}</div>
        <p className="text-sm leading-relaxed">{v.summary || t("reviewer.noSummary")}</p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <Section title={t("reviewer.facts")} items={facts} />
        <Section title={t("reviewer.clues")} items={clues} />
        <Section title={t("reviewer.carry")} items={carry} />
        <Section title={t("reviewer.negative")} items={negative} />
      </div>

      {evidence.length > 0 && (
        <div className="grid gap-1">
          <div className="font-medium text-muted-foreground text-xs">{t("reviewer.evidence")}</div>
          <div className="flex flex-wrap gap-1">
            {evidence.map((e, i) => (
              <Badge key={`ev-${i}-${e}`} variant="outline" className="font-mono text-xs">
                ref {e}
              </Badge>
            ))}
          </div>
        </div>
      )}

      <div className="flex flex-wrap gap-3 text-muted-foreground text-xs">
        <span>{t("reviewer.intent", { id: v.intent_id })}</span>
        <span>{t("reviewer.run", { id: v.run_id })}</span>
        <span className="tabular-nums">{t("reviewer.rawBytes", { n: v.raw_bytes })}</span>
        <span>{v.created_at}</span>
      </div>
    </div>
  );
}

// Role labels moved to the `sessions.role.*` message catalog (resolved at render in
// SessionsTab); only the icon stays here since it is not a translatable string.
const roleMeta = {
  mainagent: { icon: UserIcon },
  planner: { icon: BrainIcon },
  worker: { icon: RadioIcon },
  system: { icon: HistoryIcon },
  reviewer: { icon: ScaleIcon },
} as const;

// The main-agent session is the interactive entry point of this tab and has no
// dedicated backend "sessions" endpoint — it is a fixed UI affordance whose
// transcript is the main-agent activity stream (worker="mainagent") for the task.
// A main-agent session is one resettable conversation segment. Segment 0 is the
// original session; "新建会话" creates further segments (seq 1,2,…) so the agent
// starts on a clean transcript while the task's graph/assets/goal stay shared. Each
// segment is a switchable UI session; only the current (highest) one is writable.
const mainSessionId = (seg: number) => `s-main-${seg}`;
const mainSessionKey = (seg: number) => `main:${seg}`;
// Session titles live in the `sessions.title.*` catalog and are resolved at render
// inside SessionsTab (module scope has no translator). These fixed sessions carry an
// empty title placeholder; every consumer overrides it with t("title.*").
const MAIN_ID = mainSessionId(0);
const MAIN_SESSION: Session = {
  id: MAIN_ID,
  role: "mainagent",
  title: "",
  status: "running",
  live: true,
  last_activity: "",
  seg: 0,
};

// The planner session is, like the main-agent session, a fixed UI affordance with
// no dedicated backend "sessions" endpoint — its transcript is every activity step
// the planner emits (worker === "planner", which also carries the Goal Agent's
// round-0 decomposition; the planner carries no intent_id since it generates intents).
const PLANNER_ID = "s-planner";
const PLANNER_SESSION: Session = {
  id: PLANNER_ID,
  role: "planner",
  title: "",
  status: "running",
  live: true,
  last_activity: "",
};

// LLM provider switches are task-level audit events rather than agent output. The
// history endpoint has no "system" filter, so this fixed session is populated by
// scanning the generic incremental activity endpoint and then tailed by task SSE.
const SYSTEM_ID = "s-system";
const SYSTEM_SESSION: Session = {
  id: SYSTEM_ID,
  role: "system",
  title: "",
  status: "done",
  live: false,
  last_activity: "",
};

// keyForSession maps a UI Session → its stable store key (main:<seg> | plan | intent:<id>).
function keyForSession(s: Session): string {
  if (s.role === "mainagent") return `main:${s.seg ?? 0}`;
  if (s.role === "planner") return "plan";
  if (s.role === "system") return "system";
  if (s.role === "reviewer") return `reviewer:${s.id}`;
  return `intent:${s.intent_id}`;
}

// Map an exploration intent (TaskNode) state → a session status the UI renders.
function intentStatus(state: string): SessionStatus {
  switch (state) {
    case "done":
      return "done";
    case "blocked":
      return "blocked";
    case "exhausted":
      return "exhausted";
    case "stopped":
      return "stopped";
    case "paused":
      return "paused";
    case "open": // 待领取，区别于执行中
      return "pending";
    case "deleted": // 用户假删除
      return "deleted";
    default: // running
      return "running";
  }
}

// Derive worker sessions from running/open intents — there is no backend
// sessions endpoint, so intents (≈ worker units) are the closest real source.
function intentToSession(n: TaskNode): Session {
  const label = (n.payload ?? "").trim();
  const state = intentStatus(n.state);
  return {
    id: n.id,
    role: "worker",
    title: label || `Intent ${n.id}`,
    status: state,
    live: !n.inherited && state === "running",
    last_activity: n.ts,
    intent_id: n.id,
    source_task_id: n.source_task_id,
    inherited: n.inherited,
  };
}

function SessionItem({
  s,
  active,
  displayTitle,
  hasPending,
  unread,
  onClick,
  onCancel,
  controlling,
  deleted,
}: {
  s: Session;
  active: boolean;
  displayTitle: string;
  hasPending?: boolean;
  unread?: number;
  onClick: () => void;
  onCancel?: () => void;
  controlling?: boolean;
  deleted?: boolean;
}) {
  const t = useTranslations("sessions");
  const icon = deleted ? (
    <Trash2Icon className="size-3.5 text-destructive" />
  ) : s.role === "worker" ? (
    statusIcon(s.status)
  ) : s.live ? (
    <Loader2Icon className="size-3.5 animate-spin text-blue-500" />
  ) : null;

  const cancellable =
    s.role === "worker" &&
    !s.inherited &&
    !deleted &&
    // pending = 待领(open)意图;连同运行中/已暂停都允许删除。
    (s.status === "running" || s.status === "paused" || s.status === "pending");
  let leftBorderClass = "border-l-transparent hover:bg-accent/45";
  if (active) {
    leftBorderClass = "border-l-primary bg-accent text-accent-foreground shadow-2xs";
  } else if (s.live) {
    leftBorderClass = "border-l-emerald-500/70 hover:bg-accent/45";
  } else if (s.status === "blocked") {
    leftBorderClass = "border-l-amber-500/70 hover:bg-accent/45";
  }
  return (
    <div
      className={cn("group/session flex w-full items-center rounded-lg border-l-2 transition-colors", leftBorderClass)}
    >
      <button
        type="button"
        onClick={onClick}
        className="flex min-w-0 flex-1 items-center gap-1.5 px-2.5 py-1.5 text-left text-sm"
      >
        {icon ?? <span className="size-3.5 shrink-0" />}
        {s.intent_id && (
          <span className="shrink-0 rounded bg-muted px-1 py-0.5 font-mono text-[10px] text-muted-foreground tabular-nums">
            #{s.intent_id}
          </span>
        )}
        {s.inherited && s.source_task_id && (
          <Badge variant="outline" className="shrink-0">
            {t("item.sourceTask", { id: s.source_task_id })}
          </Badge>
        )}
        <span
          className={cn("min-w-0 flex-1 truncate font-medium text-sm", deleted && "text-muted-foreground line-through")}
        >
          {displayTitle}
        </span>
        {deleted && (
          <Badge variant="outline" className="shrink-0 border-destructive/40 text-destructive">
            {t("item.deleted")}
          </Badge>
        )}
        {hasPending && <ShieldAlertIcon className="size-3.5 shrink-0 text-amber-500" />}
        {!active && unread ? (
          <span className="inline-flex min-w-4 items-center justify-center rounded-full bg-blue-500/15 px-1 font-medium text-[10px] text-blue-600 tabular-nums dark:text-blue-400">
            {unread > 99 ? "99+" : unread}
          </span>
        ) : null}
        {s.live && (
          <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 font-medium text-[10px] text-blue-600 dark:text-blue-400">
            <LiveDot className="size-1" />
            {t("item.live")}
          </span>
        )}
      </button>
      {cancellable && (
        <div className="flex shrink-0 items-center gap-0.5 pr-1 opacity-100 sm:opacity-0 sm:transition-opacity sm:group-hover/session:opacity-100 sm:group-focus-within/session:opacity-100">
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            onClick={onCancel}
            disabled={controlling}
            title={t("item.deleteTitle")}
            aria-label={t("item.deleteTitle")}
            className="text-destructive hover:text-destructive"
          >
            <Trash2Icon />
          </Button>
        </div>
      )}
    </div>
  );
}

function truncateWorkerAssetLabel(value: string, maxChars = 30): string {
  const chars = Array.from(value.trim());
  if (chars.length <= maxChars) return chars.join("");
  return `${chars.slice(0, Math.max(0, maxChars - 1)).join("")}…`;
}

function WorkerAssetBadge({ assets }: { assets: IntentAsset[] }) {
  const t = useTranslations("sessions");
  const displayAssets = assets.filter(
    (asset) => asset.type === "root_domain" || asset.type === "subdomain" || asset.type === "ip",
  );
  if (displayAssets.length === 0) return null;

  const first = displayAssets[0];
  const firstRawLabel = first.label.trim() || `#${first.asset_id}`;
  const firstLabel = truncateWorkerAssetLabel(firstRawLabel);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="outline" className="max-w-60 shrink-0 font-normal" title={firstRawLabel}>
          <span className="truncate">{t("asset.current", { label: firstLabel })}</span>
          {displayAssets.length > 1 && <span className="shrink-0 tabular-nums">+{displayAssets.length - 1}</span>}
        </Badge>
      </TooltipTrigger>
      <TooltipContent side="right" align="start" className="max-w-sm">
        <div className="flex flex-col gap-2">
          {displayAssets.map((asset) => (
            <div key={`${asset.intent_id}-${asset.asset_id}`} className="min-w-0">
              <div className="break-all font-mono text-xs">{asset.label.trim() || `#${asset.asset_id}`}</div>
              <div className="mt-0.5 text-muted-foreground text-xs">
                {t(`asset.type.${asset.type}`)} ·{" "}
                {t.has(`asset.source.${asset.source}`) ? t(`asset.source.${asset.source}`) : asset.source}
                {asset.inherited ? ` · ${t("asset.sourceTask", { id: asset.source_task_id })}` : ""}
              </div>
              <div className="mt-0.5 text-xs [overflow-wrap:anywhere]">{asset.source_summary}</div>
            </div>
          ))}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

export function SessionsTab({ taskId }: { taskId: string }) {
  const t = useTranslations("sessions");
  const tc = useTranslations("checkpoints");
  const approvalFocus = useApprovalFocus({ taskId });
  const [selectedSessionId, setActiveId] = React.useState(MAIN_ID);
  const focusSession = React.useMemo<Session | undefined>(() => {
    const source = approvalFocus.state?.source;
    if (!source) return undefined;
    if (source.session.startsWith("main:")) {
      const seg = Number(source.session.slice(5));
      return { ...MAIN_SESSION, id: mainSessionId(seg), seg, title: t("title.main", { n: seg + 1 }), live: false };
    }
    if (source.session === "plan") return { ...PLANNER_SESSION, title: t("title.planner"), live: false };
    if (source.session.startsWith("intent:")) {
      const id = source.session.slice(7);
      return {
        id,
        role: "worker",
        intent_id: id,
        title: `Worker #${id}`,
        status: "done",
        live: false,
        last_activity: source.items[0]?.ts ?? "",
      };
    }
    return undefined;
  }, [approvalFocus.state?.source, t]);
  // Keep a located archived/older session selectable after leaving focus mode.
  const [locatedSession, setLocatedSession] = React.useState<{ taskId: string; session: Session }>();
  React.useEffect(() => {
    if (focusSession) setLocatedSession({ taskId, session: focusSession });
  }, [taskId, focusSession]);
  const retainedSession = locatedSession?.taskId === taskId ? locatedSession.session : undefined;
  const activeId = focusSession?.id ?? selectedSessionId;
  // Main-agent conversation segments (newest-first); currentSeg is the writable one.
  const [mainSegs, setMainSegs] = React.useState<{ seq: number; created_at: string }[]>([{ seq: 0, created_at: "" }]);
  const [currentSeg, setCurrentSeg] = React.useState(0);
  const [creatingMain, setCreatingMain] = React.useState(false);
  const [confirmNewMain, setConfirmNewMain] = React.useState(false);
  // 手机端（<lg）会话列表默认折叠：屏幕高度本就紧张，列表若固定占掉 10~15rem，
  // 下方的会话记录会被挤到只剩标题与输入框。折叠后记录区拿到几乎全部高度，
  // 点标题栏可展开选会话，选完自动收起。桌面端不受影响（lg 起始终展开）。
  const [listOpen, setListOpen] = React.useState(false);
  const [workerFilter, setWorkerFilter] = React.useState<"all" | "running" | "paused" | "blocked">("all");
  // 回顾者判决：台账是只读的，按需轮询即可（不接 SSE——判决是低频事件）。
  const [reviewerVerdicts, setReviewerVerdicts] = React.useState<ReviewerVerdict[]>([]);
  const [reviewerExpanded, setReviewerExpanded] = React.useState(false);
  // 分区折叠：五栏（主/规划/回顾者/Worker/审计）在小屏上会把列表撑爆，
  // 所以每个分区头都可折叠，折叠状态按 role 记；默认全展开，行为与旧版一致。
  const [collapsedRoles, setCollapsedRoles] = React.useState<Set<string>>(() => new Set());
  const toggleRole = React.useCallback((role: string) => {
    setCollapsedRoles((prev) => {
      const next = new Set(prev);
      if (next.has(role)) next.delete(role);
      else next.add(role);
      return next;
    });
  }, []);
  // Per-session lazily-loaded caches, keyed by session_key (main | plan | intent:<id>).
  const [store, setStore] = React.useState<SessionStore>({});
  // Worker sessions derived from exploration intents (paged past the old 300 cap).
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  const [intentAssets, setIntentAssets] = React.useState<IntentAsset[]>([]);
  const [olderIntents, setOlderIntents] = React.useState<TaskNode[]>([]);
  const [firstIntentsHasMore, setFirstIntentsHasMore] = React.useState(false);
  const [olderIntentsHasMore, setOlderIntentsHasMore] = React.useState(false);
  const [hasLoadedOlderIntentsPage, setHasLoadedOlderIntentsPage] = React.useState(false);
  const [loadingOlderIntents, setLoadingOlderIntents] = React.useState(false);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  const [mainChatRunning, setMainChatRunning] = React.useState<boolean | null>(null);
  const [controllingIntent, setControllingIntent] = React.useState<string | null>(null);
  const [cancelIntent, setCancelIntent] = React.useState<Session | null>(null);
  const [cancelReason, setCancelReason] = React.useState("");
  // 删除模式:soft=假删除(默认,置 deleted + 记原因,保留数据)| hard=真删除(级联移除独占子孙)。
  const [deleteMode, setDeleteMode] = React.useState<"soft" | "hard">("soft");
  const [workerMessage, setWorkerMessage] = React.useState("");
  const [workerMessageRequestId, setWorkerMessageRequestId] = React.useState("");
  const [workerMessageSending, setWorkerMessageSending] = React.useState(false);
  // 引导（steer）运行中的意图：对话框里的目标会话 + 文本 + 提交中标记。
  // 只在 running 的 Worker 上开放：后端会把文本投进引擎 steer 队列，
  // 下一步工具调用前注入，不暂停、不重跑。
  const [steerTarget, setSteerTarget] = React.useState<Session | null>(null);
  const [steerText, setSteerText] = React.useState("");
  const [steerSending, setSteerSending] = React.useState(false);
  // 消息级回滚（总 AGENT 气泡箭头）：待确认的用户消息 + 提交中标记。确认后整任务
  // 回滚到该消息之前，消息原文回填输入框。
  const [rollbackTarget, setRollbackTarget] = React.useState<Activity | null>(null);
  const [rollbackBusy, setRollbackBusy] = React.useState(false);
  // 方式1 文件上传:选好的附件(已落到任务工作目录 uploads/),随下条消息一起发。
  const [attachments, setAttachments] = React.useState<ChatAttachment[]>([]);
  const [uploading, setUploading] = React.useState(false);
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  async function pickFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    setUploading(true);
    try {
      const r = await api.chatUpload("task", taskId, Array.from(files));
      setAttachments((prev) => [...prev, ...r.attachments]);
    } catch (e) {
      toast.error(t("toast.uploadFailed", { msg: (e as Error).message }));
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  }

  // Start a fresh main-agent session: only the segment counter advances — the task's
  // graph/assets/goal are untouched, so the agent continues over the same task with a
  // clean context. The old segment stays as read-only history you can switch back to.
  async function createMainSession() {
    if (creatingMain) return;
    setCreatingMain(true);
    try {
      const r = await api.newMainSession(taskId);
      setMainSegs((prev) => [{ seq: r.seq, created_at: r.created_at }, ...prev.filter((m) => m.seq !== r.seq)]);
      setCurrentSeg(r.current ?? r.seq);
      // Seed an empty, loaded state so the new (empty) session renders immediately.
      setStore((prev) => ({ ...prev, [mainSessionKey(r.seq)]: { ...emptyState(), loaded: true } }));
      setActiveId(mainSessionId(r.seq));
      setListOpen(false);
      setInput("");
    } catch (e) {
      toast.error(t("toast.createSessionFailed", { msg: (e as Error).message }));
    } finally {
      setCreatingMain(false);
      setConfirmNewMain(false);
    }
  }

  const patchIntentState = React.useCallback((intentId: string, state?: string) => {
    const patch = (rows: TaskNode[]) =>
      state
        ? rows.map((row) => (row.id === intentId ? { ...row, state } : row))
        : rows.filter((row) => row.id !== intentId);
    setIntents(patch);
    setOlderIntents(patch);
  }, []);

  const controlWorker = React.useCallback(
    async (session: Session, action: "pause" | "resume" | "cancel", reason?: string, mode?: "soft" | "hard") => {
      if (!session.intent_id || session.inherited || controllingIntent) return;
      if (action === "cancel" && !reason?.trim()) {
        toast.error(t("toast.reasonRequired"));
        return;
      }
      setControllingIntent(session.intent_id);
      try {
        const res = await api.controlIntent(taskId, session.intent_id, action, reason, mode);
        if (action === "pause") {
          patchIntentState(session.intent_id, "paused");
          toast.success(t("toast.workerPaused", { id: session.intent_id }));
        } else if (action === "resume") {
          patchIntentState(session.intent_id, "open");
          toast.success(t("toast.workerResumed", { id: session.intent_id }));
        } else if (mode === "hard") {
          // 真删除:意图及独占下游已物理移除,从列表剔除该行。
          patchIntentState(session.intent_id);
          const d = res.deleted;
          const extra = d
            ? t("toast.hardDeleteExtra", { intents: d.intents, facts: d.facts, findings: d.findings })
            : "";
          toast.success(t("toast.workerHardDeleted", { id: session.intent_id, extra }));
          setCancelReason("");
        } else {
          // 假删除:意图置 deleted、记录删除原因,保留节点与产出。
          patchIntentState(session.intent_id, "deleted");
          toast.success(t("toast.workerSoftDeleted", { id: session.intent_id }));
          setCancelReason("");
        }
      } catch (error) {
        toast.error(t("toast.workerActionFailed", { msg: (error as Error).message }));
      } finally {
        setControllingIntent(null);
        setCancelIntent(null);
      }
    },
    [controllingIntent, patchIntentState, taskId, t],
  );

  // SSE connection state — surfaced so a dropped realtime link is visible, never
  // silently shown as "no messages".
  const [sseLive, setSseLive] = React.useState(false);
  // Whole-task token total (all agents), polled from the backend aggregate.
  const [taskTokens, setTaskTokens] = React.useState<TokenTotal | null>(null);
  const [sessionTokens, setSessionTokens] = React.useState<Record<string, SessionTokenUsage>>({});
  const [llmResolutions, setLLMResolutions] = React.useState<TaskLLMResolutions | null>(null);
  // profile 列表只用于上下文环的窗口回退（老会话 usage 行没 metadata）。
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  // Pending intercept requests for this task — used to show warning icons on sessions.
  const [pendingIntercepts, setPendingIntercepts] = React.useState<InterceptApprovalRow[]>([]);
  // 实时增量（未落库）：新的 activity 一到就清空，避免和已落库的行重复显示。
  const [liveFrame, setLiveFrame] = React.useState<{
    kind: string;
    text?: string;
    tool?: string;
    worker?: string;
    intent_id?: number;
  } | null>(null);

  // Refs backing SSE/loading without re-render churn.
  const snapshotRef = React.useRef(0); // task-level snapshot cursor → SSE since=
  const esRef = React.useRef<EventSource | null>(null);
  const activeKeyRef = React.useRef(mainSessionKey(0)); // current session key (for SSE dispatch/unread)
  // Highest seq the SSE-down fallback poll has already merged, per session key —
  // lets it skip setStore when a poll brings nothing new (no re-render churn).
  const fallbackMaxRef = React.useRef<Record<string, number>>({});
  const atBottomRef = React.useRef(true); // transcript pinned to bottom?
  const llmToastSeqRef = React.useRef<Set<number>>(new Set());
  const chatStatusRequestRef = React.useRef(0);
  const firstIntentsRef = React.useRef<TaskNode[]>([]);
  // Per-key request token: a stale response for a key is ignored (guards fast
  // latest/older interleaving). Writes are ALWAYS keyed, so a late response can only
  // touch its own session cache — never the currently-viewed one (see §7.5).
  const reqTokenRef = React.useRef<Record<string, number>>({});
  // Keys with a latest-page load in flight — dedups the double trigger where the
  // first-load effect and the active-session effect both want "main" on mount (the
  // former also opens the SSE, so it must not be pre-empted).
  const loadingKeysRef = React.useRef<Set<string>>(new Set());

  // ── store helpers ──────────────────────────────────────────────────────────────
  const patchStore = React.useCallback((key: string, fn: (s: SessionState) => SessionState) => {
    setStore((prev) => ({ ...prev, [key]: fn(prev[key] ?? emptyState()) }));
  }, []);

  // Load a session's LATEST page (before=0) on first open. Keyed + request-token
  // guarded so a switch away can't corrupt the view.
  const loadSession = React.useCallback(
    (key: string) => {
      if (MOCK) return; // MOCK preloads everything up front
      if (loadingKeysRef.current.has(key)) return; // already in flight (e.g. main on mount)
      loadingKeysRef.current.add(key);
      const token = (reqTokenRef.current[key] ?? 0) + 1;
      reqTokenRef.current[key] = token;
      patchStore(key, (s) => ({ ...s, loading: true, error: undefined }));

      if (key === "system") {
        void (async () => {
          let since = 0;
          let systemItems: Activity[] = [];
          for (;;) {
            const page = await api.activity(taskId, { since, limit: SYSTEM_SCAN_PAGE });
            if (reqTokenRef.current[key] !== token) return;
            systemItems = mergeBySeq(
              systemItems,
              page.items.filter(
                (item) => item.worker === "system" || item.kind === "llm_switch" || item.kind === "llm_failover",
              ),
            );
            if (page.items.length < SYSTEM_SCAN_PAGE || page.cursor <= since) break;
            since = page.cursor;
          }
          patchStore(key, (s) => {
            const items = mergeBySeq(systemItems, s.items);
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: false,
              earliestSeq: items.length ? items[0].seq : 0,
              unread: 0,
              lastTs: items.length ? items[items.length - 1].ts : s.lastTs,
              error: undefined,
            };
          });
        })()
          .catch((error) => {
            if (reqTokenRef.current[key] !== token) return;
            patchStore(key, (s) => ({
              ...s,
              loading: false,
              error: (error as Error).message || t("toast.loadFailed"),
            }));
          })
          .finally(() => loadingKeysRef.current.delete(key));
        return;
      }

      api
        .activityHistory(taskId, key, 0, PAGE)
        .then((r) => {
          if (reqTokenRef.current[key] !== token) return; // superseded
          if (r.snapshotCursor > snapshotRef.current) snapshotRef.current = r.snapshotCursor;
          patchStore(key, (s) => {
            const items = mergeBySeq(r.items, s.items); // keep any live frames arrived meanwhile
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : 0,
              unread: 0,
              error: undefined,
            };
          });
        })
        .catch((e) => {
          if (reqTokenRef.current[key] !== token) return;
          patchStore(key, (s) => ({ ...s, loading: false, error: (e as Error).message || t("toast.loadFailed") }));
        })
        .finally(() => loadingKeysRef.current.delete(key));
    },
    [taskId, patchStore, t],
  );

  // Load one older page (scroll-up) for a session, preserving scroll position.
  const loadEarlier = React.useCallback(
    (key: string, viewport: () => HTMLElement | null) => {
      const st = store[key];
      if (!st || st.loadingMore || !st.hasMore || !st.earliestSeq) return;
      const vp = viewport();
      const prevH = vp?.scrollHeight ?? 0;
      const prevTop = vp?.scrollTop ?? 0;
      patchStore(key, (s) => ({ ...s, loadingMore: true }));
      api
        .activityHistory(taskId, key, st.earliestSeq, PAGE)
        .then((r) => {
          patchStore(key, (s) => {
            const items = mergeBySeq(r.items, s.items);
            return {
              ...s,
              items,
              loadingMore: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : s.earliestSeq,
            };
          });
          requestAnimationFrame(() => {
            const v = viewport();
            if (v) v.scrollTop = prevTop + (v.scrollHeight - prevH);
          });
        })
        .catch(() => {
          patchStore(key, (s) => ({ ...s, loadingMore: false }));
        });
    },
    [taskId, store, patchStore],
  );

  // ── task token total (whole task, all agents) ───────────────────────────────────
  usePolling(
    async () => {
      const r = await api.tokenStats(taskId);
      setTaskTokens(r.total);
      setSessionTokens(Object.fromEntries(r.sessions.map((item) => [item.session, item])));
    },
    { intervalMs: 5000, enabled: Boolean(taskId) },
  );

  React.useEffect(() => {
    setMainChatRunning(null);
  }, []);
  // chatStatus 是「主 Agent 是否在跑」的唯一权威来源：2s 间隔保持不变，
  // 由 usePolling 负责在途去重（原实现用 chatStatusRequestRef 手工挡重复响应，
  // 现在不需要了，因为同一时刻只有一轮在跑）。
  usePolling(
    async () => {
      const { running } = await api.chatStatus(taskId);
      setMainChatRunning(running);
    },
    { intervalMs: 2000, enabled: Boolean(taskId) },
  );

  React.useEffect(() => {
    let alive = true;
    api
      .llmProfiles()
      .then((rows) => {
        if (alive) setProfiles(rows);
      })
      .catch(() => {
        // 拿不到 profile 时环不渲染，不影响会话正文。
      });
    return () => {
      alive = false;
    };
  }, []);

  usePolling(
    async () => {
      const value = await api.taskLLMResolution(taskId);
      setLLMResolutions(value);
    },
    { intervalMs: 10_000, enabled: Boolean(taskId) },
  );

  usePolling(
    async () => {
      const rows = await api.interceptTask(taskId);
      setPendingIntercepts(rows.filter((r) => r.status === "pending"));
    },
    { intervalMs: 5000, enabled: Boolean(taskId) },
  );

  // 让「最后活动距今多久」这类相对时间文本自己走字。
  //
  // 原实现是固定 1.5s 的 setInterval：不管有没有新活动，整棵 session 树每秒
  // 重建两次。而它真正需要的只是把「3 秒前」更新成「8 秒前」——10 秒粒度对
  // 这个文案完全够用。这里改成 10s 一次的可见性感知时钟：页面在后台时停掉，
  // 切回来立即补一次。活动到达时的即时更新由上面的 SSE 负责，不依赖这个时钟。
  const [, setTick] = React.useState(0);
  usePolling(() => setTick((t) => t + 1), { intervalMs: 10_000 });

  // ── first load + single task SSE ────────────────────────────────────────────────
  // On task open: load Main's latest page, take the task-level snapshot cursor from
  // it, THEN open ONE task SSE at since=snapshot_cursor. History covers id≤cursor and
  // the SSE covers id>cursor with no gap. The SSE tails ALL agents; frames are routed
  // by session_key. EventSource auto-reconnects and (via our `id:` lines → Last-Event-
  // ID) resumes from the DB, so a dropped realtime link self-heals; seq-merge dedups.
  React.useEffect(() => {
    setStore({});
    setIntents([]);
    setIntentAssets([]);
    setOlderIntents([]);
    setFirstIntentsHasMore(false);
    setOlderIntentsHasMore(false);
    setHasLoadedOlderIntentsPage(false);
    setTaskTokens(null);
    setSessionTokens({});
    setLLMResolutions(null);
    setSseLive(false);
    setActiveId(MAIN_ID); // a stale worker id from the previous task must not leak in
    setMainSegs([{ seq: 0, created_at: "" }]);
    setCurrentSeg(0);
    snapshotRef.current = 0;
    llmToastSeqRef.current = new Set();
    reqTokenRef.current = {};
    // Reserve the initial main key so the active-session effect (which fires for main on
    // mount) won't double-load it and pre-empt the SSE opened here.
    loadingKeysRef.current = new Set([mainSessionKey(0)]);
    let alive = true;

    // MOCK demo: no SSE backend — pull one activity snapshot and bucket by session.
    if (MOCK) {
      api
        .activity(taskId)
        .then((r) => {
          if (!alive) return;
          const buckets: SessionStore = {};
          for (const a of r.items) {
            const k = sessionKeyOf(a);
            if (!buckets[k]) buckets[k] = emptyState();
            buckets[k].items.push(a);
          }
          for (const k of Object.keys(buckets)) {
            const st = buckets[k];
            st.items.sort((p, q) => p.seq - q.seq);
            st.loaded = true;
            st.hasMore = false;
            st.earliestSeq = st.items.length ? st.items[0].seq : 0;
            st.lastTs = st.items.length ? st.items[st.items.length - 1].ts : "";
          }
          buckets[mainSessionKey(0)] ??= { ...emptyState(), loaded: true };
          buckets.system ??= { ...emptyState(), loaded: true };
          setStore(buckets);
        })
        .catch(() =>
          setStore({
            [mainSessionKey(0)]: { ...emptyState(), loaded: true },
            system: { ...emptyState(), loaded: true },
          }),
        );
      return () => {
        alive = false;
      };
    }

    const token = (reqTokenRef.current.mainboot ?? 0) + 1;
    reqTokenRef.current.mainboot = token;
    let bootKey = mainSessionKey(0);
    // Resolve the main-agent segments first, then load the CURRENT segment's history and
    // open the SSE from its snapshot cursor. The SSE tails all segments and routes each
    // frame by session_key (main:<seg>), so switching segments needs no new stream.
    api
      .mainSessions(taskId)
      .then((ms) => {
        if (!alive || reqTokenRef.current.mainboot !== token) throw new Error("superseded");
        const segs = ms.sessions.length ? ms.sessions : [{ seq: 0, created_at: "" }];
        setMainSegs(segs);
        setCurrentSeg(ms.current);
        bootKey = mainSessionKey(ms.current);
        if (bootKey !== mainSessionKey(0)) loadingKeysRef.current.delete(mainSessionKey(0));
        loadingKeysRef.current.add(bootKey);
        setActiveId(mainSessionId(ms.current));
        patchStore(bootKey, (s) => ({ ...s, loading: true }));
        return api.activityHistory(taskId, bootKey, 0, PAGE);
      })
      .then((r) => {
        if (!alive || reqTokenRef.current.mainboot !== token) return;
        snapshotRef.current = r.snapshotCursor;
        patchStore(bootKey, (s) => {
          const items = mergeBySeq(r.items, s.items);
          return {
            ...s,
            items,
            loaded: true,
            loading: false,
            hasMore: r.hasMore,
            earliestSeq: items.length ? items[0].seq : 0,
            unread: 0,
          };
        });
        // Open the single task SSE from the snapshot cursor.
        const es = new EventSource(
          sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=${snapshotRef.current}`),
        );
        esRef.current = es;
        es.onopen = () => setSseLive(true);
        es.addEventListener("live", (ev) => {
          try {
            const d = JSON.parse((ev as MessageEvent).data) as {
              kind?: string;
              text?: string;
              tool?: string;
              worker?: string;
              intent_id?: number;
            };
            if (!d.kind) return;
            setLiveFrame({
              kind: d.kind,
              text: d.text,
              tool: d.tool,
              worker: d.worker,
              intent_id: d.intent_id,
            });
          } catch {
            // 忽略坏帧
          }
        });
        es.onerror = () => setSseLive(false); // EventSource auto-reconnects; DB compensates the gap
        es.onmessage = (e) => {
          let a: Activity;
          try {
            a = JSON.parse(e.data) as Activity;
          } catch {
            return; // ignore malformed frame
          }
          setLiveFrame(null); // 已落库的行到了，实时块让位
          if ((a.kind === "llm_switch" || a.kind === "llm_failover") && !llmToastSeqRef.current.has(a.seq)) {
            llmToastSeqRef.current.add(a.seq);
            const transition = a.metadata?.llm_transition;
            if (transition?.mode === "exhausted" || a.is_error) {
              toast.error(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
            } else if (transition?.mode === "automatic") {
              toast.success(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
            } else {
              toast.info(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
            }
            void api
              .taskLLMResolution(taskId)
              .then((value) => {
                if (alive) setLLMResolutions(value);
              })
              .catch(() => {
                // The periodic resolver poll will retry if this event-triggered refresh fails.
              });
          }
          const k = sessionKeyOf(a);
          setStore((prev) => {
            const cur = prev[k] ?? emptyState();
            const activeK = activeKeyRef.current;
            // Merge into sessions that are loaded, actively loading, or the current
            // view (so returning is instant + a frame that lands mid-load isn't lost).
            // A cold, inactive session also keeps a tiny accounting tail so the
            // sidebar can add the latest unfinished usage without loading history.
            if (!cur.loaded && !cur.loading && k !== activeK) {
              const accountingItems =
                a.kind === "usage" || a.kind === "result" ? mergeBySeq(cur.items, [a]).slice(-4) : cur.items;
              return {
                ...prev,
                [k]: { ...cur, items: accountingItems, lastTs: a.ts, unread: cur.unread + 1 },
              };
            }
            let items = mergeBySeq(cur.items, [a]);
            // Memory bound: trim oldest when over cap (older re-fetched on scroll-up),
            // but never while the user is reading this session's history (scrolled up).
            let hasMore = cur.hasMore;
            let earliestSeq = cur.earliestSeq;
            const trimmable = k !== activeK || atBottomRef.current;
            if (trimmable && items.length > MAX_KEEP) {
              items = items.slice(items.length - MAX_KEEP);
              hasMore = true;
              earliestSeq = items[0].seq;
            }
            const unread = k === activeK ? 0 : cur.unread + 1;
            return { ...prev, [k]: { ...cur, items, lastTs: a.ts, unread, hasMore, earliestSeq } };
          });
        };
      })
      .catch((err) => {
        if (!alive || reqTokenRef.current.mainboot !== token) return;
        if ((err as Error).message === "superseded") return;
        patchStore(bootKey, (s) => ({ ...s, loading: false, error: (err as Error).message || t("toast.loadFailed") }));
      })
      .finally(() => loadingKeysRef.current.delete(bootKey));

    return () => {
      alive = false;
      esRef.current?.close();
      esRef.current = null;
    };
  }, [taskId, patchStore, t]);

  // Fallback transcript poll — runs ONLY while the SSE stream is down. SSE is the
  // sole live source for the active session's transcript (steps + the human turn),
  // so when the stream never connects or gets buffered/dropped by an intermediary
  // (a reverse proxy that holds text/event-stream, a flaky link), newly sent
  // messages and their replies stay invisible until a manual refresh re-runs the
  // history fetch. This mirrors the chat console (which stays live via polling):
  // every 2s it re-fetches the active session's latest page and merges it, so the
  // transcript advances without a reload. It is inert whenever sseLive is true, so
  // the healthy SSE path carries no extra load.
  React.useEffect(() => {
    if (sseLive) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const key = activeKeyRef.current;
      try {
        const r = await api.activityHistory(taskId, key, 0, PAGE);
        if (!alive) return;
        const maxSeq = r.items.reduce((m, a) => Math.max(m, a.seq), -1);
        if (maxSeq > (fallbackMaxRef.current[key] ?? -1)) {
          fallbackMaxRef.current[key] = maxSeq;
          patchStore(key, (s) => ({ ...s, items: mergeBySeq(s.items, r.items) }));
        }
      } catch {
        // best-effort; the next tick retries, and SSE reconnection compensates.
      } finally {
        if (alive) timer = setTimeout(() => void poll(), 2000);
      }
    };
    timer = setTimeout(() => void poll(), 2000);
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [sseLive, taskId, patchStore]);

  React.useEffect(() => {
    let active = true;
    const load = () =>
      api
        .taskIntentAssets(taskId)
        .then((assets) => {
          if (active) setIntentAssets(assets);
        })
        .catch(() => {
          // The next poll retries; Worker controls and transcripts remain available.
        });
    void load();
    const timer = setInterval(load, 5000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [taskId]);

  // ── worker (intent) session list — paged, poll first page lightly ───────────────
  React.useEffect(() => {
    let active = true;
    firstIntentsRef.current = [];
    setIntents([]);
    setOlderIntents([]);
    setFirstIntentsHasMore(false);
    setOlderIntentsHasMore(false);
    const load = () =>
      api
        .intentsPage(taskId, 0, 300)
        .then((r) => {
          if (!active) return;
          const freshIds = new Set(r.items.map((item) => item.id));
          const oldestFreshId = r.items.reduce((minimum, item) => Math.min(minimum, Number(item.id)), Infinity);
          const displaced = r.hasMore
            ? firstIntentsRef.current.filter((item) => !freshIds.has(item.id) && Number(item.id) < oldestFreshId)
            : [];
          if (displaced.length > 0) {
            setOlderIntents((previous) => {
              const byId = new Map(previous.map((item) => [item.id, item]));
              for (const item of displaced) byId.set(item.id, item);
              return [...byId.values()];
            });
          }
          firstIntentsRef.current = r.items;
          setIntents(r.items);
          setFirstIntentsHasMore(r.hasMore);
        })
        .catch(() => {
          // Polling is best-effort; the next interval retries automatically.
        });
    void load();
    const t = setInterval(load, 5000);
    return () => {
      active = false;
      clearInterval(t);
    };
  }, [taskId]);

  const loadOlderIntents = React.useCallback(() => {
    if (loadingOlderIntents) return;
    const all = [...intents, ...olderIntents];
    const minId = all.reduce((m, n) => Math.min(m, Number(n.id)), Infinity);
    if (!Number.isFinite(minId)) return;
    setLoadingOlderIntents(true);
    api
      .intentsPage(taskId, minId, 300)
      .then((r) => {
        setOlderIntents((prev) => {
          const seen = new Set([...intents, ...prev].map((n) => n.id));
          return [...prev, ...r.items.filter((n) => !seen.has(n.id))];
        });
        setOlderIntentsHasMore(r.hasMore);
        setHasLoadedOlderIntentsPage(true);
      })
      .catch(() => {
        // A later manual retry can fetch this page again.
      })
      .finally(() => setLoadingOlderIntents(false));
  }, [taskId, intents, olderIntents, loadingOlderIntents]);

  // Combined, de-duplicated worker list (newest first page + older loaded pages).
  const allIntents = React.useMemo(() => {
    const byId = new Map<string, TaskNode>();
    for (const n of olderIntents) byId.set(n.id, n);
    for (const n of intents) byId.set(n.id, n); // fresh poll wins over older snapshot
    return [...byId.values()].sort((a, b) => Number(b.id) - Number(a.id));
  }, [intents, olderIntents]);
  const intentAssetsByID = React.useMemo(() => {
    const grouped = new Map<string, IntentAsset[]>();
    for (const asset of intentAssets) {
      const key = String(asset.intent_id);
      const current = grouped.get(key);
      if (current) current.push(asset);
      else grouped.set(key, [asset]);
    }
    return grouped;
  }, [intentAssets]);

  // ── 回顾者判决 ────────────────────────────────────────────────────────────────
  // 判决是低频事件（每个 worker run 一条），不进 SSE，轮询足够；
  // 失败静默保持上一批，不弹错——面板读不到判决不该打断任务操作。
  React.useEffect(() => {
    let alive = true;
    setReviewerVerdicts([]);
    const pull = () => {
      api
        .taskReviewerVerdicts(taskId)
        .then((rows) => {
          if (alive) setReviewerVerdicts(rows);
        })
        .catch(() => {});
    };
    pull();
    const timer = window.setInterval(pull, 15000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [taskId]);

  // 一条判决 = 一个只读会话；标题带意图号，状态跟随判决结论。
  const reviewerSessions = React.useMemo<Session[]>(
    () =>
      reviewerVerdicts.map((v) => ({
        id: `s-reviewer-${v.id}`,
        role: "reviewer",
        title: t("title.reviewer", { id: v.intent_id }),
        status:
          v.verdict === "advance" ? "done" : v.verdict === "abandon" ? "stopped" : "paused",
        live: false,
        last_activity: v.created_at,
      })),
    [reviewerVerdicts, t],
  );

  const workerSessions = React.useMemo(() => allIntents.map(intentToSession), [allIntents]);
  const intentsHasMore = hasLoadedOlderIntentsPage ? olderIntentsHasMore : firstIntentsHasMore;

  // For each worker session (intent), derive the display title from the intent
  // payload summary. Also store the full TaskNode for the hover-JSON tooltip.
  const sessionMeta = React.useMemo(() => {
    const map = new Map<string, { title: string; json: unknown; deleted: boolean; deleteReason: string }>();
    for (const node of allIntents) {
      let title = `Intent ${node.id}`;
      let parsedPayload: unknown = node.payload;
      // 假删除:意图 state='deleted',删除原因在独立字段 delete_reason 上。
      const deleted = node.state === "deleted";
      const deleteReason = node.delete_reason ?? "";
      if (node.payload) {
        try {
          const p = JSON.parse(node.payload);
          parsedPayload = p;
          if (p?.summary) title = String(p.summary);
        } catch {
          title = node.payload.trim() || title;
        }
      }
      const json = { ...node, payload: parsedPayload };
      map.set(node.id, { title, json, deleted, deleteReason });
    }
    return map;
  }, [allIntents]);

  // Returns true if any pending intercept belongs to this session.
  // Worker agent_name format: "work#N · #intentID". Main/planner match by role key.
  const hasPendingForSession = React.useCallback(
    (s: Session): boolean => {
      if (s.inherited) return false;
      if (!pendingIntercepts.length) return false;
      if (s.role === "mainagent") return pendingIntercepts.some((r) => r.agent_name === "mainagent");
      if (s.role === "planner") return pendingIntercepts.some((r) => r.agent_name === "planner");
      // Worker: extract intent ID from "work#N · #<intentID>"
      return pendingIntercepts.some((r) => {
        const m = r.agent_name.match(/·\s*#(\d+)$/);
        return m ? m[1] === s.intent_id : false;
      });
    },
    [pendingIntercepts],
  );

  // Liveness from each session's last-seen activity time (kept fresh by the 1.5s tick).
  const recentLive = React.useCallback(
    (key: string) => {
      const ts = store[key]?.lastTs;
      if (!ts) return false;
      const t = Date.parse(ts);
      return t > 0 && Date.now() - t < STREAM_WINDOW_MS;
    },
    [store],
  );
  const currentMainKey = mainSessionKey(currentSeg);
  const plannerLive = recentLive("plan");

  // Which main segment is streaming RIGHT NOW. A main turn is serialized per task, so at
  // most one segment is live. Gate on the real running flag (sending / mainChatRunning)
  // rather than "recent activity", and drop it the moment the turn's terminal record
  // (kind='result', or an error) lands — otherwise the badge lingers for STREAM_WINDOW_MS
  // after the agent already finished. null = nothing running.
  const liveMainSeg = React.useMemo<number | null>(() => {
    if (!(sending || mainChatRunning)) return null;
    // the streaming segment is the one with the freshest activity (incl. the just-sent turn)
    let seg = currentSeg;
    let bestTs = -1;
    for (const m of mainSegs) {
      const raw = store[mainSessionKey(m.seq)]?.lastTs;
      const ts = raw ? Date.parse(raw) : -1;
      if (ts > bestTs) {
        bestTs = ts;
        seg = m.seq;
      }
    }
    const items = store[mainSessionKey(seg)]?.items ?? [];
    const last = items[items.length - 1];
    if (last && (last.kind === "result" || (last.kind === "text" && last.is_error))) return null;
    return seg;
  }, [sending, mainChatRunning, mainSegs, store, currentSeg]);

  // Every main-agent segment is an independent, interactive session (like the top-level
  // chat conversations) — you can talk in any of them, newest-first.
  const mainSessions = React.useMemo<Session[]>(
    () =>
      mainSegs.map((m) => ({
        id: mainSessionId(m.seq),
        role: "mainagent",
        title: t("title.main", { n: m.seq + 1 }),
        status: "running",
        live: m.seq === liveMainSeg,
        last_activity: m.created_at,
        seg: m.seq,
      })),
    [mainSegs, liveMainSeg, t],
  );

  const sessions = React.useMemo(() => {
    const items = [
      ...mainSessions,
      { ...PLANNER_SESSION, title: t("title.planner"), live: plannerLive },
      ...reviewerSessions,
      ...workerSessions,
      { ...SYSTEM_SESSION, title: t("title.system") },
    ];
    const located = focusSession ?? retainedSession;
    if (located && !items.some((s) => s.id === located.id)) items.push(located);
    return items;
  }, [mainSessions, reviewerSessions, workerSessions, plannerLive, focusSession, retainedSession, t]);

  const grouped = {
    mainagent: sessions.filter((s) => s.role === "mainagent"),
    planner: sessions.filter((s) => s.role === "planner"),
    reviewer: sessions.filter((s) => s.role === "reviewer"),
    worker: sessions.filter((s) => s.role === "worker"),
    system: sessions.filter((s) => s.role === "system"),
  };

  const active = sessions.find((s) => s.id === activeId) ?? { ...MAIN_SESSION, title: t("title.main", { n: 1 }) };
  const side = useSideQuestions(
    active.role === "mainagent"
      ? `/api/tasks/${taskId}/chat`
      : active.role === "worker" && !active.inherited && active.intent_id
        ? `/api/tasks/${taskId}/intents/${active.intent_id}`
        : null,
  );
  const isMain = active.role === "mainagent";
  const isPlanner = active.role === "planner";
  const isSystem = active.role === "system";
  const isReviewer = active.role === "reviewer";
  const activeVerdict = isReviewer
    ? (reviewerVerdicts.find((v) => `s-reviewer-${v.id}` === active.id) ?? null)
    : null;
  const activeKey = keyForSession(active);
  // 只把属于当前会话的增量交给 Transcript：
  //   - worker 会话按 intent_id 对（后端 number / Session string，统一转字符串比）；
  //   - 主会话没有 intent_id，按 lane 名 `chat` 对，否则会把 worker 的增量画到主会话里。
  const liveIntentId = liveFrame?.intent_id != null ? String(liveFrame.intent_id) : "";
  const liveBelongs = !liveFrame
    ? false
    : active.intent_id
      ? liveIntentId === String(active.intent_id)
      : liveFrame.worker === "chat";
  const liveDeltaForActive = liveBelongs
    ? { kind: liveFrame!.kind, text: liveFrame!.text, tool: liveFrame!.tool }
    : undefined;
  const activeState = store[activeKey];
  // A main turn is serialized per task (chat lock), so "busy" is task-wide: while any
  // main segment is mid-turn the active composer is disabled. Clear it the moment the
  // active session's terminal record (kind='result', or an error) lands, so the input
  // re-enables immediately instead of waiting for the next chat-status poll.
  const activeItems = activeState?.items ?? [];
  const activeLast = activeItems[activeItems.length - 1];
  const activeSettled =
    !!activeLast && (activeLast.kind === "result" || (activeLast.kind === "text" && activeLast.is_error));
  const mainBusy = isMain && (sending || (!activeSettled && (mainChatRunning ?? recentLive(activeKey))));
  // 统一状态机（与对话页同一份 deriveAgentPhase）：实时帧优先于已落库活动，
  // 避免“推理 → 正文 → 又回到推理”的来回跳（Memo 01）。
  const livePhase = deriveAgentPhase({
    running: active.live,
    liveKind: liveDeltaForActive?.kind,
    lastKind: activeLast?.kind,
    lastIsError: activeLast?.is_error,
    waitingApproval: activeLast?.kind === "intercept_request",
  });
  // 折叠态（手机端）标题栏要替代整张列表：显示当前会话名 + 其它会话的未读合计，
  // 否则收起后既不知道自己在看哪个会话，也看不到别处有新消息。
  const activeDisplayTitle = (active.role === "worker" ? sessionMeta.get(active.id)?.title : "") || active.title;
  const hiddenUnread = React.useMemo(
    () => Object.entries(store).reduce((sum, [key, s]) => (key === activeKey ? sum : sum + s.unread), 0),
    [store, activeKey],
  );

  // Keep the SSE dispatcher's notion of the active session current, and lazily load
  // + clear unread whenever the active session changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: cache updates must not reactivate the current session.
  React.useEffect(() => {
    activeKeyRef.current = activeKey;
    const st = store[activeKey];
    if (active.role === "reviewer") {
      // 判决台账自带内容，没有活动流可拉；拉了只会得到一个空的 history 请求。
    } else if (!st || (!st.loaded && !st.loading)) {
      loadSession(activeKey);
    } else if (st.unread) {
      patchStore(activeKey, (s) => ({ ...s, unread: 0 }));
    }
  }, [activeKey]);

  const focusKey = approvalFocus.state?.source?.session;
  const loadFocusPage = React.useCallback(
    (before: number) => api.activityHistory(taskId, focusKey ?? "main", before, PAGE),
    [taskId, focusKey],
  );
  const mergeFocusPage = React.useCallback(
    (page: { items: Activity[]; hasMore: boolean }) => {
      if (!focusKey) return;
      patchStore(focusKey, (s) => {
        const items = mergeBySeq(page.items, s.items);
        return { ...s, items, hasMore: page.hasMore, earliestSeq: items[0]?.seq ?? s.earliestSeq };
      });
    },
    [focusKey, patchStore],
  );
  const focusHistory = useApprovalHistory(
    approvalFocus.state?.source,
    !!focusKey && !!store[focusKey]?.loaded,
    focusKey ? (store[focusKey]?.items ?? []) : [],
    loadFocusPage,
    mergeFocusPage,
  );
  React.useEffect(() => {
    if (approvalFocus.state) atBottomRef.current = false;
  }, [approvalFocus.state]);

  // Main agent is the human↔orchestrator CONSOLE: only the conversation (user msgs +
  // the main agent's own replies/steps). Planner session shows planner steps; a
  // worker session shows only its intent's activity, led by the intent objective.
  const activity = React.useMemo(() => {
    const items = activeState?.items ?? [];
    // The main-agent console renders purely from server data: the human turn is
    // persisted+broadcast by the backend BEFORE it returns, so it arrives over the
    // same SSE stream (worker="mainagent") as every agent step — no client-side
    // optimistic echo, hence no fabricated seq that could collide with real DB ids.
    if (isMain) return items;
    if (isPlanner || isSystem) return items;
    // Worker session: the intent leads the transcript as a right-aligned "user"-style
    // message (the task handed to this worker), followed by its execution steps.
    const intentTitle = sessionMeta.get(active.id)?.title ?? active.title;
    const intentMsg: Activity = {
      seq: -1, // sorts/leads before any real step (real seq ≥ 0)
      worker: items[0]?.worker ?? active.id, // reuse the lane so no worker chips appear
      ts: active.last_activity || "",
      kind: "intent", // LLM-generated objective — rendered as a distinct (non-human) bubble
      summary: intentTitle,
      source_task_id: active.source_task_id,
      inherited: active.inherited,
    };
    return [intentMsg, ...items];
  }, [
    isMain,
    isPlanner,
    isSystem,
    activeState,
    active.id,
    active.title,
    active.last_activity,
    active.source_task_id,
    active.inherited,
    sessionMeta,
  ]);

  // seq of this session's most-recent TodoWrite call — for the Todo popover.
  const latestTodoSeq = React.useMemo(() => {
    for (let i = activity.length - 1; i >= 0; i--) {
      const a = activity[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [activity]);

  // Persisted result totals come from the backend's full activity history. Only
  // the latest unfinished usage frame is added client-side, so partial history
  // pages cannot undercount completed runs or double-count the active one.
  const tokenForSession = React.useCallback(
    (key: string): TokenTotal => {
      const saved = sessionTokens[key];
      const total: TokenTotal = {
        input_tokens: saved?.input_tokens ?? 0,
        output_tokens: saved?.output_tokens ?? 0,
        cache_read_tokens: saved?.cache_read_tokens ?? 0,
        cache_write_tokens: saved?.cache_write_tokens ?? 0,
      };
      const items = store[key]?.items ?? [];
      for (let index = items.length - 1; index >= 0; index--) {
        const item = items[index];
        if (item.kind === "result") break;
        if (item.kind !== "usage") continue;
        total.input_tokens += item.input_tokens ?? 0;
        total.output_tokens += item.output_tokens ?? 0;
        total.cache_read_tokens += item.cache_read_tokens ?? 0;
        total.cache_write_tokens += item.cache_write_tokens ?? 0;
        break;
      }
      return total;
    },
    [sessionTokens, store],
  );

  // 上下文环只认「最近一条」的口径：input + cache 写入的累计值，不会算爆窗口。
  // 窗口优先取后端写入的 usage metadata（capture 时按模型窗口写入）；老会话的 usage
  // 行还没有 metadata（当时该角色未挂 WithContextWindow），回退到该角色解析到的 profile
  // 的 context_window_k（与 agent/provider.go CompactionWindow 同口径：K → token，
  // 缺省 200K，夹在 32K–1M）。两者都是后端配置值，前端不估算；都拿不到就不渲染环。
  const fallbackContextWindow = React.useMemo(() => {
    const resolved =
      active.role === "worker"
        ? llmResolutions?.worker
        : active.role === "planner"
          ? llmResolutions?.planner
          : active.role === "mainagent"
            ? llmResolutions?.mainagent
            : undefined;
    const profileId = resolved?.profile_id;
    const profile =
      profileId != null ? profiles.find((item) => Number(item.id) === Number(profileId)) : undefined;
    const k = profile?.context_window_k ?? 0;
    if (k <= 0) return 0;
    return Math.min(Math.max(k, 32), 1000) * 1000;
  }, [active.role, llmResolutions, profiles]);

  const contextUsage = React.useMemo(() => {
    const items = store[activeKey]?.items ?? [];
    for (let index = items.length - 1; index >= 0; index -= 1) {
      const item = items[index];
      if (item.kind !== "usage") continue;
      const size = item.metadata?.context_window ?? fallbackContextWindow;
      if (!size) return null;
      // input_tokens 已含 cache（后端按 prompt_tokens 口径归一），不能再加 cache 字段。
      const used = item.metadata?.context_used ?? (item.input_tokens ?? 0);
      return { size, used };
    }
    return null;
  }, [store, activeKey, fallbackContextWindow]);

  const activeTokens = tokenForSession(activeKey);
  const tokenTotal = {
    i: activeTokens.input_tokens,
    o: activeTokens.output_tokens,
    cr: activeTokens.cache_read_tokens,
    any:
      activeTokens.input_tokens +
        activeTokens.output_tokens +
        activeTokens.cache_read_tokens +
        activeTokens.cache_write_tokens >
      0,
  };

  // Run duration = span from this session's first step to its last (for a live
  // session, "now" so it ticks up — the 1.5s setTick above re-renders it).
  const runDuration = React.useMemo(() => {
    let min = Infinity,
      max = 0;
    for (const a of activity) {
      const t = Date.parse(a.ts);
      if (!Number.isFinite(t)) continue;
      if (t < min) min = t;
      if (t > max) max = t;
    }
    if (!Number.isFinite(min) || max === 0) return null;
    const end = active.live ? Date.now() : max;
    return Math.max(0, end - min);
  }, [activity, active.live]);

  // ---- transcript auto-scroll (open → bottom; stick to bottom unless scrolled up) ----
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const viewport = React.useCallback(
    () => (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ?? null,
    [],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally rebinds the scroll listener.
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      if (approvalFocus.state && !focusHistory.ready) return;
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
      if (vp.scrollTop <= 80) loadEarlier(activeKeyRef.current, viewport); // near top → older page
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, activeId, loadEarlier, approvalFocus.state, focusHistory.ready]);
  // open/switch a session → jump to the latest (bottom)
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally scrolls a newly selected session.
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp && !approvalFocus.state) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [activeId, viewport, approvalFocus.state]);
  // new activity → stick to bottom only if the user is already pinned there
  // biome-ignore lint/correctness/useExhaustiveDependencies: activity growth intentionally drives live-edge scrolling.
  React.useLayoutEffect(() => {
    if (approvalFocus.state || !atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [activity, viewport, approvalFocus.state]);
  // Lazy detail loads (AnswerBlock / ToolBlock / Markdown) grow the content AFTER the
  // activity array settles, WITHOUT changing its reference — so the layout effects
  // above never re-fire and a freshly opened session would leave its last message
  // scrolled partly off-screen (the final answer expands from a one-line summary to
  // full markdown below the fold). A ResizeObserver re-pins to the bottom on any
  // height change while the user is still at the bottom, so opening the main agent
  // lands on the last message fully shown. contentRef's div is always mounted, so the
  // observer catches the transcript mounting + each detail expanding.
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally rebinds the observer to the new session's content.
  React.useEffect(() => {
    const el = contentRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (approvalFocus.state || !atBottomRef.current) return;
      const vp = viewport();
      if (vp) vp.scrollTop = vp.scrollHeight;
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [activeId, viewport, approvalFocus.state]);

  function stop() {
    if (stopping) return;
    setStopping(true);
    chatStatusRequestRef.current++;
    void api
      .stopChat(taskId)
      .then(() => {
        chatStatusRequestRef.current++;
        setMainChatRunning(false);
      })
      .finally(() => setStopping(false));
  }

  function send() {
    const text = input.trim();
    const atts = attachments;
    if (side.handleCommand(text, () => setInput(""))) return;
    if ((!text && atts.length === 0) || sending || mainBusy) return;
    // No optimistic echo: the backend persists+broadcasts the human turn before it
    // returns, so it streams back over SSE (worker="mainagent") with its real DB
    // seq — the transcript renders it from server data like every other step. Clear
    // the composer eagerly for responsiveness; restore it if the send fails.
    setInput("");
    setAttachments([]);
    setSending(true);
    chatStatusRequestRef.current++;
    api
      .chat(text, taskId, atts.length > 0 ? atts : undefined, active.seg ?? 0)
      .then(({ mode }) => {
        chatStatusRequestRef.current++;
        setMainChatRunning(mode === "llm");
      })
      .catch((e) => {
        setInput(text); // restore so the user doesn't lose their text / attachments
        setAttachments(atts);
        toast.error(t("toast.sendFailed", { msg: (e as Error).message || t("toast.retryLater") }));
      })
      .finally(() => setSending(false));
  }

  // confirmMessageRollback 执行消息级回滚（总 AGENT 气泡箭头）：后端只回退这段
  // 对话到该消息之前（隐藏主 Agent 步骤；任务进度、意图状态与 Worker/规划器记录
  // 都不动），这里把消息原文连同附件回填输入框，丢弃会话缓存并按当前会话重载，
  // 被回滚的对话内容从列表消失。
  async function confirmMessageRollback() {
    const step = rollbackTarget;
    if (!step || rollbackBusy) return;
    setRollbackBusy(true);
    try {
      const detail = (await api.activityDetail(step.seq, taskId)).detail ?? "";
      const body = parseUserBody(detail || step.summary);
      const result = await api.rollbackTaskMessage(taskId, step.seq);
      setInput(body.text);
      setAttachments(body.attachments.map((a) => ({ name: a.name, path: a.path, size: a.size })));
      setRollbackTarget(null);
      // 缓存里含被隐藏步骤，merge 无法移除——整店清空，当前会话立即重载，其它会话
      // 在打开时惰性重载。MOCK 演示没有后端可重载（loadSession 空转），清店只会
      // 卡在「加载活动流…」，因此演示态保留原列表。
      if (!MOCK) setStore({});
      loadSession(activeKey);
      toast.success(tc("msgRollback.doneTask", { hidden: result.hidden }));
    } catch (e) {
      toast.error(tc("msgRollback.failed", { msg: (e as Error).message }));
    } finally {
      setRollbackBusy(false);
    }
  }

  function sendWorkerChat() {
    const intentId = active.intent_id;
    const message = workerMessage.trim();
    if (side.handleCommand(message, () => setWorkerMessage(""))) return;
    if (!intentId || active.inherited || active.status !== "paused" || workerMessageSending || !message) return;
    if (workerMessageCharCount(message) > MAX_WORKER_MESSAGE_CHARS) {
      toast.error(t("toast.messageTooLong", { max: MAX_WORKER_MESSAGE_CHARS }));
      return;
    }
    const requestId = workerMessageRequestId || newWorkerMessageRequestID();
    if (!workerMessageRequestId) setWorkerMessageRequestId(requestId);
    setWorkerMessageSending(true);
    api
      .sendWorkerMessage(taskId, intentId, message, requestId)
      .then((result) => {
        // The server records the user turn and streams the run over SSE, so there is
        // no optimistic insert: the message and the continuation arrive live.
        patchIntentState(intentId, result.state);
        setWorkerMessage("");
        setWorkerMessageRequestId("");
        toast.success(t("toast.workerMessageSent", { id: intentId }));
      })
      .catch((error) => {
        toast.error(t("toast.sendFailed", { msg: (error as Error).message || t("toast.retryLater") }));
      })
      .finally(() => setWorkerMessageSending(false));
  }

  // 引导运行中的 Worker：走 /intents/{id}/steer，不改变意图状态。成功只是"已递达
  // 到引擎队列"——若 Worker 恰在收尾或尚未注册内存 work，后端会回 409 原样透出。
  async function sendSteer() {
    const intentId = steerTarget?.intent_id;
    const text = steerText.trim();
    if (!intentId || !text || steerSending) return;
    if (workerMessageCharCount(text) > MAX_WORKER_MESSAGE_CHARS) {
      toast.error(t("toast.messageTooLong", { max: MAX_WORKER_MESSAGE_CHARS }));
      return;
    }
    setSteerSending(true);
    try {
      await api.steerIntent(taskId, intentId, text);
      toast.success(t("toast.steerSent"));
      setSteerTarget(null);
      setSteerText("");
    } catch (error) {
      toast.error(t("toast.steerFailed", { msg: (error as Error).message || t("toast.retryLater") }));
    } finally {
      setSteerSending(false);
    }
  }

  const mainLoaded = !!store[currentMainKey]?.loaded;
  // 发送键位由系统设置决定（localStorage），默认 Enter 发送。
  const sendMode = useChatSendMode();
  // What the transcript pane should show for the active session.
  const showLoader = !activeState || (activeState.loading && !activeState.loaded);
  const resolutionForSession = (session: Session): TaskLLMResolution | undefined => {
    if (!llmResolutions || session.inherited || session.role === "system") return undefined;
    if (session.role === "mainagent") return llmResolutions.mainagent;
    if (session.role === "planner") return llmResolutions.planner;
    return llmResolutions.worker;
  };
  const activeResolution = resolutionForSession(active);
  const activeAssets =
    active.role === "worker" && active.intent_id ? intentAssetsByID.get(active.intent_id) : undefined;

  return (
    <TooltipProvider delayDuration={300}>
      {/* 高度预留：页面头部（标题行 + 目标 + Tabs ≈ 7.5rem）+ 内容内边距。手机端 p-4、
        桌面端 lg:p-6，且桌面还要留出滚动余量，所以两档分别预留 10rem / 13rem —— 手机端
        沿用 13rem 会白白吃掉 3rem 的记录高度。 */}
      <div
        className={cn(
          "grid h-[calc(100svh-9.5rem)] min-h-0 grid-cols-1 overflow-hidden rounded-xl border border-border/80 bg-card shadow-xs",
          listOpen ? "grid-rows-[minmax(0,40svh)_minmax(0,1fr)]" : "grid-rows-[auto_minmax(0,1fr)]",
          "lg:h-[calc(100svh-10.5rem)] lg:grid-cols-[18.5rem_1fr] lg:grid-rows-[minmax(0,1fr)]",
        )}
      >
        {/* Left: session fleet rail */}
        <div className="flex min-h-0 flex-col overflow-hidden border-border/75 bg-sidebar/40 max-lg:border-b lg:border-r">
          <div className="border-b px-3 py-2.5">
            <div className="flex items-center justify-between gap-2">
              <button
                type="button"
                onClick={() => setListOpen((open) => !open)}
                aria-expanded={listOpen}
                aria-controls="session-list-panel"
                className="flex min-w-0 flex-1 items-center gap-1 text-left font-medium text-muted-foreground text-xs lg:pointer-events-none"
              >
                <ChevronDownIcon
                  className={cn("size-3.5 shrink-0 transition-transform lg:hidden", !listOpen && "-rotate-90")}
                />
                <span className="shrink-0">{t("list.title")}</span>
                {!listOpen && (
                  <>
                    <span className="min-w-0 truncate text-foreground lg:hidden" title={activeDisplayTitle}>
                      · {activeDisplayTitle}
                    </span>
                    {hiddenUnread > 0 && (
                      <span className="inline-flex min-w-4 shrink-0 items-center justify-center rounded-full bg-blue-500/15 px-1 font-medium text-[10px] text-blue-600 tabular-nums lg:hidden dark:text-blue-400">
                        {hiddenUnread > 99 ? "99+" : hiddenUnread}
                      </span>
                    )}
                  </>
                )}
              </button>
              {!MOCK && (
                <span
                  className={cn(
                    "inline-flex items-center gap-1 text-[10px]",
                    sseLive ? "text-emerald-500" : "text-amber-500",
                  )}
                  title={sseLive ? t("sse.titleLive") : t("sse.titleDown")}
                >
                  {sseLive ? (
                    <>
                      <LiveDot className="size-1 bg-emerald-500" />
                      {t("sse.live")}
                    </>
                  ) : (
                    <>
                      <WifiOffIcon className="size-3" />
                      {t("sse.reconnecting")}
                    </>
                  )}
                </span>
              )}
            </div>
            {taskTokens && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <div className="mt-2 flex min-w-0 flex-wrap items-center gap-1 text-muted-foreground text-xs">
                    <span>{t("taskTotal")}</span>
                    <span>·</span>
                    <TokenMetrics
                      input={taskTokens.input_tokens}
                      cache={taskTokens.cache_read_tokens}
                      output={taskTokens.output_tokens}
                    />
                  </div>
                </TooltipTrigger>
                <TooltipContent>
                  {t("tokens.full", {
                    input: taskTokens.input_tokens.toLocaleString(),
                    output: taskTokens.output_tokens.toLocaleString(),
                    cacheRead: taskTokens.cache_read_tokens.toLocaleString(),
                    cacheWrite: taskTokens.cache_write_tokens.toLocaleString(),
                  })}
                </TooltipContent>
              </Tooltip>
            )}
          </div>
          <ScrollArea
            id="session-list-panel"
            type="auto"
            className={cn(
              "[&_[data-slot=scroll-area-viewport]>div]:block! min-h-0 flex-1",
              !listOpen && "max-lg:hidden",
            )}
          >
            <div className="flex w-full flex-col gap-3 p-2">
              {(["mainagent", "planner", "reviewer", "worker", "system"] as const).map((role) => {
                const rawItems = grouped[role];
                if (!rawItems.length) return null;
                let items =
                  role === "worker" && workerFilter !== "all"
                    ? rawItems.filter((s) => s.id === activeId || s.status === workerFilter)
                    : rawItems;
                // 回顾者可能积累几十上百条判决，默认只展示最近 8 条，
                // 否则这个分区会把下面所有分区挤出可视区。
                if (role === "reviewer" && !reviewerExpanded && items.length > 8) {
                  items = items.slice(0, 8);
                }
                const Meta = roleMeta[role];
                const collapsed = collapsedRoles.has(role);
                return (
                  <div key={role} className="flex flex-col gap-0.5">
                    <div className="flex items-center gap-1.5 px-2 py-1 font-semibold text-muted-foreground text-xs tracking-tight">
                      <button
                        type="button"
                        onClick={() => toggleRole(role)}
                        aria-expanded={!collapsed}
                        aria-label={t("list.toggleAria", { role: t(`role.${role}`) })}
                        title={t("list.toggleAria", { role: t(`role.${role}`) })}
                        className="-ml-1 flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
                      >
                        <ChevronDownIcon
                          className={cn(
                            "size-3 transition-transform",
                            collapsed && "-rotate-90",
                          )}
                        />
                      </button>
                      <Meta.icon className="size-3.5 text-primary/80" />
                      <span>{t(`role.${role}`)}</span>
                      {role === "reviewer" && (
                        <span className="ml-auto rounded-full bg-muted px-1.5 py-0.2 font-mono text-[10px] text-muted-foreground tabular-nums">
                          {rawItems.length}
                        </span>
                      )}
                      {role === "worker" && (
                        <span className="ml-auto rounded-full bg-muted px-1.5 py-0.2 font-mono text-[10px] text-muted-foreground tabular-nums">
                          {rawItems.length}
                        </span>
                      )}
                      {role === "mainagent" && (
                        <button
                          type="button"
                          onClick={() => setConfirmNewMain(true)}
                          disabled={creatingMain}
                          title={t("newMain.title")}
                          aria-label={t("newMain.aria")}
                          className="ml-auto flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground disabled:opacity-50"
                        >
                          {creatingMain ? (
                            <Loader2Icon className="size-3.5 animate-spin" />
                          ) : (
                            <PlusIcon className="size-3.5" />
                          )}
                          {t("newMain.label")}
                        </button>
                      )}
                    </div>
                    {!collapsed && (
                      <>
                    {role === "worker" && rawItems.length > 3 && (
                      <div className="mb-1 flex flex-wrap items-center gap-1 px-2">
                        {(["all", "running", "paused", "blocked"] as const).map((fKey) => {
                          const count =
                            fKey === "all" ? rawItems.length : rawItems.filter((item) => item.status === fKey).length;
                          if (fKey !== "all" && count === 0 && workerFilter !== fKey) return null;
                          return (
                            <button
                              key={fKey}
                              type="button"
                              onClick={() => setWorkerFilter(fKey)}
                              className={cn(
                                "inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[10px] transition-colors",
                                workerFilter === fKey
                                  ? "border-primary/40 bg-primary/15 font-semibold text-primary"
                                  : "border-border/60 bg-muted/40 text-muted-foreground hover:bg-accent",
                              )}
                            >
                              <span>{fKey === "all" ? "ALL" : fKey.toUpperCase()}</span>
                              <span className="tabular-nums opacity-80">{count}</span>
                            </button>
                          );
                        })}
                      </div>
                    )}
                    {items.map((s) => {
                      const meta = s.role === "worker" ? sessionMeta.get(s.id) : undefined;
                      return (
                        <SessionItem
                          key={s.id}
                          s={s}
                          active={s.id === activeId}
                          displayTitle={meta?.title ?? s.title}
                          hasPending={hasPendingForSession(s)}
                          unread={store[keyForSession(s)]?.unread}
                          onClick={() => {
                            approvalFocus.close();
                            setActiveId(s.id);
                            setListOpen(false); // 手机端选完即收起，把高度还给会话记录
                            setWorkerMessage("");
                            setWorkerMessageRequestId("");
                          }}
                          controlling={controllingIntent === s.intent_id}
                          deleted={meta?.deleted}
                          onCancel={() => {
                            setCancelIntent(s);
                          }}
                        />
                      );
                    })}
                    {role === "worker" && intentsHasMore && (
                      <button
                        type="button"
                        onClick={loadOlderIntents}
                        disabled={loadingOlderIntents}
                        className="mt-0.5 flex items-center justify-center gap-1 rounded-md px-2 py-1 text-muted-foreground text-xs hover:bg-accent/50"
                      >
                        {loadingOlderIntents ? (
                          <Loader2Icon className="size-3.5 animate-spin" />
                        ) : (
                          <RotateCwIcon className="size-3.5" />
                        )}
                        {t("worker.loadOlder")}
                      </button>
                    )}
                    {role === "reviewer" && rawItems.length > 8 && (
                      <button
                        type="button"
                        onClick={() => setReviewerExpanded((v) => !v)}
                        className="mt-0.5 flex items-center justify-center gap-1 rounded-md px-2 py-1 text-muted-foreground text-xs hover:bg-accent/50"
                      >
                        <ChevronDownIcon
                          className={cn("size-3.5 transition-transform", reviewerExpanded && "rotate-180")}
                        />
                        {reviewerExpanded
                          ? t("reviewer.collapse")
                          : t("reviewer.showAll", { n: rawItems.length })}
                      </button>
                    )}
                      </>
                    )}
                  </div>
                );
              })}
              {mainLoaded && !workerSessions.length && (
                <div className="px-2 py-1 text-muted-foreground text-xs">{t("worker.noRunning")}</div>
              )}
            </div>
          </ScrollArea>
        </div>

        {/* Right: transcript */}
        <SideQuestionWorkspace
          side={side}
          label={active.role === "worker" ? `Worker #${active.intent_id} · ${activeDisplayTitle}` : activeDisplayTitle}
        >
          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-card">
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-b bg-background/35 px-3.5 py-2 backdrop-blur-xs sm:px-4 sm:py-2.5">
              {(() => {
                const isWorker = active.role === "worker";
                const meta = isWorker ? sessionMeta.get(active.id) : undefined;
                // Worker: the intent moved into the transcript as a message, so the
                // header shows a stable generic label (intent JSON stays on hover).
                const title = isWorker ? t("worker.execSession") : active.title;
                const titleEl = <span className="min-w-0 truncate font-medium text-sm">{title}</span>;
                return meta?.json ? (
                  <Tooltip>
                    <TooltipTrigger asChild>{titleEl}</TooltipTrigger>
                    <TooltipContent side="bottom" align="start" className="max-h-80 max-w-sm overflow-auto p-0">
                      <pre className="p-2 text-[10px] leading-relaxed">{JSON.stringify(meta.json, null, 2)}</pre>
                    </TooltipContent>
                  </Tooltip>
                ) : (
                  titleEl
                );
              })()}
              <SideQuestionButton side={side} />
              {activeResolution && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Badge
                      variant="outline"
                      className="max-w-28 shrink-0 font-normal"
                      aria-label={
                        activeResolution.available
                          ? t("resolution.current", {
                              label: resolutionLabel(activeResolution, t("resolution.unnamed")),
                            })
                          : t("resolution.unavailable")
                      }
                    >
                      <span className="truncate">
                        {activeResolution.available
                          ? resolutionLabel(activeResolution, t("resolution.unnamed"))
                          : t("resolution.unavailable")}
                      </span>
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent side="bottom" className="max-w-xs [overflow-wrap:anywhere]">
                    {activeResolution.available
                      ? [resolutionLabel(activeResolution, t("resolution.unnamed")), activeResolution.model]
                          .filter(Boolean)
                          .join(" / ")
                      : activeResolution.reason || t("resolution.noConfig")}
                  </TooltipContent>
                </Tooltip>
              )}
              {activeAssets && activeAssets.length > 0 && <WorkerAssetBadge assets={activeAssets} />}
              {active.inherited && active.source_task_id && (
                <Badge variant="outline">{t("header.sourceTask", { id: active.source_task_id })}</Badge>
              )}
              {active.live && <AgentPhaseBadge phase={livePhase} />}
              {activeState?.hasMore && (
                <span className="text-[10px] text-muted-foreground" title={t("header.olderHistoryTitle")}>
                  {t("header.olderHistoryLabel")}
                </span>
              )}
              <div className="ml-auto flex min-w-0 max-w-full items-center justify-end gap-x-3 gap-y-1 text-muted-foreground text-xs max-sm:w-full max-sm:flex-wrap">
                {tokenTotal.any && (
                  <Tooltip>
                    {/* 手机端用短标签（入/缓/出）：长标签会把这一行撑成两行，进一步压缩记录区。 */}
                    <TooltipTrigger asChild>
                      <span className="inline-flex min-w-0 items-center">
                        <TokenMetrics
                          input={tokenTotal.i}
                          cache={tokenTotal.cr}
                          output={tokenTotal.o}
                          labels="short"
                          className="sm:hidden"
                        />
                        <TokenMetrics
                          input={tokenTotal.i}
                          cache={tokenTotal.cr}
                          output={tokenTotal.o}
                          labels="long"
                          className="max-sm:hidden"
                        />
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      {t("tokens.full", {
                        input: activeTokens.input_tokens.toLocaleString(),
                        output: activeTokens.output_tokens.toLocaleString(),
                        cacheRead: activeTokens.cache_read_tokens.toLocaleString(),
                        cacheWrite: activeTokens.cache_write_tokens.toLocaleString(),
                      })}
                    </TooltipContent>
                  </Tooltip>
                )}
                {runDuration != null && (
                  <span className="inline-flex items-center gap-1" title={t("header.runDurationTitle")}>
                    <ClockIcon className="size-3" />
                    {fmtDuration(runDuration)}
                  </span>
                )}
                {isMain && <span>{t("header.interactive")}</span>}
              </div>
            </div>
            {(() => {
              const dm = active.role === "worker" ? sessionMeta.get(active.id) : undefined;
              if (!dm?.deleted) return null;
              return (
                <div className="flex items-start gap-2 border-destructive/30 border-b bg-destructive/5 px-4 py-2.5 text-xs">
                  <Trash2Icon className="mt-0.5 size-3.5 shrink-0 text-destructive" />
                  <div className="min-w-0">
                    <span className="font-medium text-destructive">{t("deletedNotice.heading")}</span>
                    <span className="text-muted-foreground">{t("deletedNotice.detail")}</span>
                    {dm.deleteReason && (
                      <p className="mt-1 break-words text-foreground">
                        <span className="text-muted-foreground">{t("deletedNotice.reason")}</span>
                        {dm.deleteReason}
                      </p>
                    )}
                  </div>
                </div>
              );
            })()}
            <ApprovalExecutionFocus
              focus={{
                ...approvalFocus,
                close: () => {
                  setActiveId(activeId);
                  approvalFocus.close();
                },
              }}
              history={focusHistory}
            />
            {/* Force Radix's internal viewport wrapper (display:table, sizes to content)
            to block so wide/unbreakable steps (long commands, code, URLs) can't blow
            out the width and defeat the truncation below — the transcript wraps to
            the panel instead of overflowing horizontally. */}
            <ScrollArea type="auto" className="[&_[data-slot=scroll-area-viewport]>div]:block! min-h-0 min-w-0 flex-1">
              <div className="mx-auto min-w-0 max-w-5xl p-4 md:px-6" ref={contentRef}>
                {activeState?.loadingMore && (
                  <div className="flex items-center justify-center gap-2 pb-2 text-muted-foreground text-xs">
                    <Loader2Icon className="size-3.5 animate-spin" />
                    {t("transcript.loadingMore")}
                  </div>
                )}
                {showLoader ? (
                  <div className="flex items-center gap-2 pl-9 text-muted-foreground text-xs">
                    <Loader2Icon className="size-3.5 animate-spin" />
                    {t("transcript.loadingActivity")}
                  </div>
                ) : activeState?.error ? (
                  <div className="flex items-center gap-2 pl-9 text-red-500 text-xs">
                    <CircleXIcon className="size-3.5" />
                    {t("transcript.loadError", { error: activeState.error })}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 px-2 text-xs"
                      onClick={() => loadSession(activeKey)}
                    >
                      {t("transcript.retry")}
                    </Button>
                  </div>
                ) : isReviewer ? (
                  activeVerdict ? (
                    <ReviewerVerdictPanel v={activeVerdict} />
                  ) : (
                    <EmptyState
                      compact
                      illustration="chat"
                      title={t("reviewer.empty")}
                    />
                  )
                ) : activity.length || liveDeltaForActive ? (
                  <Transcript
                    activity={activity}
                    liveDelta={liveDeltaForActive}
                    taskId={taskId}
                    chat={isMain}
                    focusedSeq={focusHistory.ready ? approvalFocus.state?.source?.seq : undefined}
                    rollback={
                      isMain
                        ? {
                            disabledReason: mainBusy ? tc("msgRollback.busyMain") : undefined,
                            onRollback: setRollbackTarget,
                          }
                        : undefined
                    }
                  />
                ) : (
                  <EmptyState
                    compact
                    illustration="chat"
                    title={isMain ? t("transcript.emptyMain") : t("transcript.emptyWorker")}
                  />
                )}
              </div>
            </ScrollArea>
            {isMain ? (
              <div className="border-t px-3 py-2.5 md:px-6">
                <div className="mx-auto w-full max-w-5xl">
                  {attachments.length > 0 && (
                    <div className="mb-2 flex flex-wrap gap-1.5">
                      {attachments.map((a) => (
                        <div
                          key={a.path}
                          className="flex items-center gap-1.5 rounded-md border bg-muted/50 px-2 py-1 text-xs"
                          title={a.path}
                        >
                          <PaperclipIcon className="size-3 shrink-0 text-primary" />
                          <span className="max-w-[160px] truncate">{a.name}</span>
                          <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
                          <button
                            type="button"
                            className="ml-0.5 text-muted-foreground hover:text-foreground"
                            onClick={() => setAttachments((p) => p.filter((x) => x.path !== a.path))}
                            title={t("composer.attachmentRemove")}
                          >
                            <XIcon className="size-3" />
                          </button>
                        </div>
                      ))}
                    </div>
                  )}
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    className="hidden"
                    onChange={(e) => void pickFiles(e.target.files)}
                  />
                  <InputGroup className="min-h-9 has-disabled:opacity-100">
                    <MentionTextarea
                      inputGroup
                      rows={1}
                      aria-label={t("composer.mainAria")}
                      placeholder={mainBusy ? t("composer.mainBusyPlaceholder") : t("composer.mainPlaceholder")}
                      value={input}
                      disabled={sending}
                      onValueChange={setInput}
                      onKeyDown={(e) => {
                        if (!shouldSubmitOnKey(e, sendMode)) return;
                        e.preventDefault();
                        send();
                      }}
                      className="max-h-36 min-h-9 overflow-y-auto"
                    />
                    <InputGroupAddon align="block-end">
                      <InputGroupButton
                        size="icon-xs"
                        variant="ghost"
                        onClick={() => fileInputRef.current?.click()}
                        disabled={mainBusy || uploading}
                        title={t("composer.uploadTitle")}
                        aria-label={t("composer.uploadTitle")}
                      >
                        {uploading ? <Loader2Icon className="animate-spin" /> : <PaperclipIcon />}
                      </InputGroupButton>
                      {mainBusy && isBtwCommand(input) && (
                        <InputGroupButton size="icon-xs" onClick={send} aria-label={t("composer.sideQuestion")}>
                          <ArrowUpIcon />
                        </InputGroupButton>
                      )}
                      {contextUsage && (
                        <ContextUsageRing
                          className="ml-auto"
                          size={contextUsage.size}
                          used={contextUsage.used}
                        />
                      )}
                      {mainBusy ? (
                        <InputGroupButton
                          size="icon-xs"
                          variant="destructive"
                          onClick={stop}
                          disabled={stopping}
                          title={t("composer.stopTitle")}
                          aria-label={t("composer.stopTitle")}
                        >
                          {stopping ? <Loader2Icon className="animate-spin" /> : <SquareIcon />}
                        </InputGroupButton>
                      ) : (
                        <InputGroupButton
                          size="icon-xs"
                          variant="default"
                          onClick={send}
                          disabled={(!input.trim() && attachments.length === 0) || sending}
                          title={t("composer.sendTitle")}
                          aria-label={t("composer.sendTitle")}
                        >
                          {sending ? <Loader2Icon className="animate-spin" /> : <ArrowUpIcon />}
                        </InputGroupButton>
                      )}
                    </InputGroupAddon>
                  </InputGroup>
                </div>
              </div>
            ) : active.role === "worker" &&
              !active.inherited &&
              (active.status === "running" || active.status === "paused") ? (
              <div className="border-t p-3">
                <InputGroup className="min-h-9 has-disabled:opacity-100">
                  <MentionTextarea
                    inputGroup
                    rows={1}
                    aria-label={t("composer.workerAria", { id: active.intent_id ?? "" })}
                    placeholder={t("composer.workerPlaceholder", { id: active.intent_id ?? "" })}
                    value={workerMessage}
                    onValueChange={(value) => {
                      setWorkerMessage(value);
                      setWorkerMessageRequestId("");
                    }}
                    onKeyDown={(event) => {
                      if (!shouldSubmitOnKey(event, sendMode)) return;
                      event.preventDefault();
                      sendWorkerChat();
                    }}
                    disabled={workerMessageSending}
                    aria-invalid={workerMessageCharCount(workerMessage) > MAX_WORKER_MESSAGE_CHARS}
                    className="max-h-36 min-h-9 overflow-y-auto"
                  />
                  <InputGroupAddon align="block-end">
                    <span
                      className={cn(
                        "px-1 text-[10px] text-muted-foreground tabular-nums",
                        workerMessageCharCount(workerMessage) > MAX_WORKER_MESSAGE_CHARS && "text-destructive",
                      )}
                    >
                      {workerMessageCharCount(workerMessage)}/{MAX_WORKER_MESSAGE_CHARS}
                    </span>
                    {active.status === "running" && isBtwCommand(workerMessage) && (
                      <InputGroupButton size="icon-xs" onClick={sendWorkerChat} aria-label={t("composer.sideQuestion")}>
                        <ArrowUpIcon />
                      </InputGroupButton>
                    )}
                    {active.status === "running" ? (
                      <>
                        <InputGroupButton
                          className="ml-auto"
                          size="xs"
                          variant="outline"
                          onClick={() => {
                            setSteerTarget(active);
                            setSteerText("");
                          }}
                          disabled={controllingIntent === active.intent_id || steerSending}
                          title={t("steer.buttonTitle")}
                          aria-label={t("steer.buttonAria", { id: active.intent_id ?? "" })}
                        >
                          <NavigationIcon data-icon="inline-start" />
                          {t("steer.button")}
                        </InputGroupButton>
                        <InputGroupButton
                          size="icon-xs"
                          variant="destructive"
                          onClick={() => void controlWorker(active, "pause")}
                          disabled={controllingIntent === active.intent_id}
                          title={t("composer.pauseTitle")}
                          aria-label={t("composer.pauseTitle")}
                        >
                          {controllingIntent === active.intent_id ? (
                            <Loader2Icon className="animate-spin" />
                          ) : (
                            <SquareIcon />
                          )}
                        </InputGroupButton>
                      </>
                    ) : (
                      <>
                        <InputGroupButton
                          className="ml-auto"
                          size="xs"
                          variant="ghost"
                          onClick={() => void controlWorker(active, "resume")}
                          disabled={controllingIntent === active.intent_id || workerMessageSending}
                          title={t("composer.resumeTitle")}
                          aria-label={t("composer.resumeAria")}
                        >
                          {controllingIntent === active.intent_id ? (
                            <Loader2Icon className="animate-spin" />
                          ) : (
                            t("composer.resumeLabel")
                          )}
                        </InputGroupButton>
                        <InputGroupButton
                          size="icon-xs"
                          variant="default"
                          onClick={sendWorkerChat}
                          disabled={
                            workerMessageSending ||
                            !workerMessage.trim() ||
                            workerMessageCharCount(workerMessage) > MAX_WORKER_MESSAGE_CHARS
                          }
                          title={t("composer.sendTitle")}
                          aria-label={t("composer.sendTitle")}
                        >
                          {workerMessageSending ? <Spinner /> : <ArrowUpIcon />}
                        </InputGroupButton>
                      </>
                    )}
                  </InputGroupAddon>
                </InputGroup>
              </div>
            ) : (
              <div className="flex items-center border-t px-4 py-2">
                <TodoPopover
                  seq={latestTodoSeq}
                  fetchDetail={(seq) => api.activityDetail(seq, taskId).then((r) => r.detail ?? "")}
                />
              </div>
            )}
          </div>
        </SideQuestionWorkspace>
        {/* 引导（steer）对话框：只对运行中的 Worker 开放。 */}
        <Dialog
          open={steerTarget !== null}
          onOpenChange={(open) => {
            if (!open && !steerSending) {
              setSteerTarget(null);
              setSteerText("");
            }
          }}
        >
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t("steer.title", { id: steerTarget?.intent_id ?? "" })}</DialogTitle>
              <DialogDescription className="whitespace-normal break-words">{t("steer.desc")}</DialogDescription>
            </DialogHeader>
            <Textarea
              value={steerText}
              onChange={(event) => setSteerText(event.target.value)}
              placeholder={t("steer.placeholder")}
              rows={3}
              autoFocus
              disabled={steerSending}
              aria-invalid={workerMessageCharCount(steerText) > MAX_WORKER_MESSAGE_CHARS}
            />
            <div className="flex items-center justify-between gap-2 text-muted-foreground text-xs">
              <span>{t("steer.hint")}</span>
              <span
                className={cn(
                  "shrink-0 tabular-nums",
                  workerMessageCharCount(steerText) > MAX_WORKER_MESSAGE_CHARS && "text-destructive",
                )}
              >
                {workerMessageCharCount(steerText)}/{MAX_WORKER_MESSAGE_CHARS}
              </span>
            </div>
            <DialogFooter>
              <Button
                variant="outline"
                disabled={steerSending}
                onClick={() => {
                  setSteerTarget(null);
                  setSteerText("");
                }}
              >
                {t("steer.cancel")}
              </Button>
              <Button
                onClick={() => void sendSteer()}
                disabled={
                  steerSending || !steerText.trim() || workerMessageCharCount(steerText) > MAX_WORKER_MESSAGE_CHARS
                }
              >
                {steerSending && <Spinner data-icon="inline-start" />}
                {t("steer.submit")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        <AlertDialog
          open={cancelIntent !== null}
          onOpenChange={(open) => {
            if (!open) {
              setCancelIntent(null);
              setCancelReason("");
              setDeleteMode("soft");
            }
          }}
        >
          <AlertDialogContent className="max-w-[min(32rem,calc(100vw-2rem))]">
            <AlertDialogHeader>
              <AlertDialogTitle>{t("deleteDialog.title", { id: cancelIntent?.intent_id ?? "" })}</AlertDialogTitle>
              <AlertDialogDescription className="whitespace-normal break-words">
                {deleteMode === "hard"
                  ? t.rich("deleteDialog.hardDesc", { s: (c) => <strong>{c}</strong> })
                  : t.rich("deleteDialog.softDesc", { s: (c) => <strong>{c}</strong> })}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className="grid gap-3 py-1">
              <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => setDeleteMode("soft")}
                  className={cn(
                    "rounded-md border px-3 py-2 text-left text-sm transition-colors",
                    deleteMode === "soft" ? "border-primary bg-primary/5" : "hover:bg-accent",
                  )}
                >
                  <div className="font-medium">{t("deleteDialog.softLabel")}</div>
                  <div className="text-muted-foreground text-xs">{t("deleteDialog.softHint")}</div>
                </button>
                <button
                  type="button"
                  onClick={() => setDeleteMode("hard")}
                  className={cn(
                    "rounded-md border px-3 py-2 text-left text-sm transition-colors",
                    deleteMode === "hard" ? "border-destructive bg-destructive/5" : "hover:bg-accent",
                  )}
                >
                  <div className="font-medium">{t("deleteDialog.hardLabel")}</div>
                  <div className="text-muted-foreground text-xs">{t("deleteDialog.hardHint")}</div>
                </button>
              </div>
              <div className="grid gap-2">
                <label htmlFor="cancel-reason" className="font-medium text-sm">
                  {t("deleteDialog.reasonLabel")}
                </label>
                <Textarea
                  id="cancel-reason"
                  value={cancelReason}
                  onChange={(e) => setCancelReason(e.target.value)}
                  placeholder={t("deleteDialog.reasonPlaceholder")}
                  rows={3}
                  autoFocus
                />
              </div>
            </div>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("deleteDialog.back")}</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                disabled={!cancelIntent || controllingIntent !== null || !cancelReason.trim()}
                onClick={() => cancelIntent && void controlWorker(cancelIntent, "cancel", cancelReason, deleteMode)}
              >
                {controllingIntent ? <Loader2Icon className="animate-spin" /> : <Trash2Icon />}
                {deleteMode === "hard" ? t("deleteDialog.confirmHard") : t("deleteDialog.confirmSoft")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <AlertDialog open={confirmNewMain} onOpenChange={(open) => !open && setConfirmNewMain(false)}>
          <AlertDialogContent className="max-w-[min(32rem,calc(100vw-2rem))]">
            <AlertDialogHeader>
              <AlertDialogTitle>{t("newSession.title")}</AlertDialogTitle>
              <AlertDialogDescription className="whitespace-normal break-words">
                {t("newSession.desc")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={creatingMain}>{t("newSession.cancel")}</AlertDialogCancel>
              <AlertDialogAction disabled={creatingMain} onClick={() => void createMainSession()}>
                {creatingMain ? t("newSession.starting") : t("newSession.start")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <MessageRollbackDialog
          task
          open={rollbackTarget !== null}
          busy={rollbackBusy}
          onOpenChange={(open) => {
            if (!open) setRollbackTarget(null);
          }}
          onConfirm={() => void confirmMessageRollback()}
        />
      </div>
    </TooltipProvider>
  );
}
