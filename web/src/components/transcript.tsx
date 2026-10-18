"use client";

import * as React from "react";

import {
  BookOpenIcon,
  BrainCircuitIcon,
  CheckIcon,
  ChevronDown,
  ChevronRight,
  ChevronsDownUpIcon,
  ChevronsUpDownIcon,
  CodeIcon,
  CopyIcon,
  CpuIcon,
  CrosshairIcon,
  Flag,
  GlobeIcon,
  KeyRoundIcon,
  ListOrderedIcon,
  Loader2Icon,
  MessageSquare,
  NetworkIcon,
  PaperclipIcon,
  PlugIcon,
  RadarIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  Terminal,
  Undo2Icon,
  UserIcon,
  WrapTextIcon,
  Wrench,
  XIcon,
  ZapIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { ApprovalDetail } from "@/components/approval-records";
import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { api } from "@/lib/api";
import { parseApprovalSummary } from "@/lib/approval-summary";
import type { Activity, InterceptPending } from "@/lib/types";
import { cn } from "@/lib/utils";

// ---- per-agent lane color (planner + work#1/#2/#3 …) ---------------------------
const workerColors = ["bg-sky-600", "bg-violet-600", "bg-teal-600", "bg-pink-600", "bg-orange-600"];
function workerColor(name: string): string {
  if (name === "planner") return "bg-amber-600"; // the intent generator, distinct
  if (name === "mainagent") return "bg-primary";
  const m = /#(\d+)/.exec(name);
  const i = m ? (Number.parseInt(m[1], 10) - 1) % workerColors.length : 0;
  return workerColors[Math.max(0, i)];
}

const chip = (worker: string) =>
  `mt-0.5 shrink-0 rounded px-1.5 py-0.5 text-[9px] font-medium text-white ${workerColor(worker)}`;

// Semantic tool category metadata for progressive disclosure trace rows.
function getToolCategoryMeta(toolName: string): {
  tag: string;
  displayName: string;
  Icon: React.ComponentType<{ className?: string }>;
  pillClass: string;
} {
  if (toolName.startsWith("mcp__")) {
    const rest = toolName.slice(5);
    const sep = rest.indexOf("__");
    const srv = sep > 0 ? rest.slice(0, sep) : "MCP";
    const sub = sep > 0 ? rest.slice(sep + 2) : rest;
    return {
      tag: `MCP:${srv}`,
      displayName: sub || toolName,
      Icon: PlugIcon,
      pillClass:
        "border-blue-500/30 bg-blue-500/10 text-blue-600 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-200",
    };
  }
  if (toolName === "http_request" || toolName === "http_fuzz" || toolName === "http_sequence") {
    return {
      tag: "HTTP",
      displayName: toolName,
      Icon: GlobeIcon,
      pillClass:
        "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300",
    };
  }
  if (toolName === "web_recon" || toolName === "dns_probe" || toolName === "net_probe") {
    return {
      tag: "RECON",
      displayName: toolName,
      Icon: RadarIcon,
      pillClass:
        "border-indigo-500/30 bg-indigo-500/10 text-indigo-600 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-300",
    };
  }
  if (
    toolName === "human_verification" ||
    toolName === "human_verification_solve" ||
    toolName === "human_verification_capabilities" ||
    toolName === "human_verification_flow"
  ) {
    return {
      tag: "VERIFY",
      displayName: toolName,
      Icon: ShieldCheckIcon,
      pillClass:
        "border-cyan-500/30 bg-cyan-500/10 text-cyan-700 dark:border-cyan-500/30 dark:bg-cyan-500/10 dark:text-cyan-300",
    };
  }
  if (toolName === "binary_inspect" || toolName === "software_analysis") {
    return {
      tag: "RE",
      displayName: toolName,
      Icon: CpuIcon,
      pillClass:
        "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300",
    };
  }
  if (
    toolName === "record_fact" ||
    toolName === "report_finding" ||
    toolName === "add_finding" ||
    toolName === "insert_assets" ||
    toolName === "list_assets" ||
    toolName === "graph_overview" ||
    toolName === "node_detail" ||
    toolName === "add_intent" ||
    toolName === "prove_goal" ||
    toolName === "set_goals" ||
    toolName === "set_constraints" ||
    toolName === "add_hint" ||
    toolName === "expand_digest"
  ) {
    return {
      tag: "GRAPH",
      displayName: toolName,
      Icon: NetworkIcon,
      pillClass:
        "border-blue-500/30 bg-blue-500/10 text-blue-700 dark:border-zinc-700 dark:bg-zinc-800/90 dark:text-zinc-200",
    };
  }
  if (
    toolName === "search_all_worker_traces" ||
    toolName === "list_worker_traces" ||
    toolName === "get_worker_trace" ||
    toolName === "TodoWrite" ||
    toolName === "steer_work" ||
    toolName === "kill_work"
  ) {
    return {
      tag: "TRACE",
      displayName: toolName,
      Icon: ListOrderedIcon,
      pillClass:
        "border-fuchsia-500/30 bg-fuchsia-500/10 text-fuchsia-700 dark:border-fuchsia-500/30 dark:bg-fuchsia-500/10 dark:text-fuchsia-300",
    };
  }
  if (toolName === "tactical_kb" || toolName === "Skill") {
    return {
      tag: "KB",
      displayName: toolName,
      Icon: BookOpenIcon,
      pillClass:
        "border-violet-500/30 bg-violet-500/10 text-violet-600 dark:border-violet-500/30 dark:bg-violet-500/10 dark:text-violet-300",
    };
  }
  if (toolName === "codec_crypto" || toolName === "extract_data") {
    return {
      tag: "CRYPTO",
      displayName: toolName,
      Icon: KeyRoundIcon,
      pillClass:
        "border-teal-500/30 bg-teal-500/10 text-teal-600 dark:border-teal-500/30 dark:bg-teal-500/10 dark:text-teal-300",
    };
  }
  if (
    toolName === "Bash" ||
    toolName === "shell_open" ||
    toolName === "shell_send" ||
    toolName === "shell_read" ||
    toolName === "shell_close" ||
    toolName === "Glob" ||
    toolName === "Grep" ||
    toolName === "Read" ||
    toolName === "Write" ||
    toolName === "Edit" ||
    toolName === "MultiEdit" ||
    toolName === "LS"
  ) {
    return {
      tag: "EXEC",
      displayName: toolName,
      Icon: Terminal,
      pillClass:
        "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300",
    };
  }
  return {
    tag: "TOOL",
    displayName: toolName,
    Icon: Wrench,
    pillClass:
      "border-border bg-muted/60 text-muted-foreground dark:border-zinc-700 dark:bg-zinc-800/80 dark:text-zinc-300",
  };
}

// useInView latches true when the ref'd element first comes within `rootMargin` of
// the enclosing scroll viewport. Blocks that always show their full body (user
// bubbles, answers) use it to defer fetching that body until they're about to be
// seen — so opening a long thread doesn't fire a detail request for every off-
// screen step. Observes the ScrollArea viewport (falls back to eager load when
// IntersectionObserver is unavailable, e.g. SSR).
function useInView(rootMargin = "400px"): [React.RefObject<HTMLDivElement | null>, boolean] {
  const ref = React.useRef<HTMLDivElement | null>(null);
  const [inView, setInView] = React.useState(false);
  React.useEffect(() => {
    if (inView) return; // latch: once seen, stop observing
    const el = ref.current;
    if (!el) return;
    if (typeof IntersectionObserver === "undefined") {
      setInView(true);
      return;
    }
    const root = el.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null;
    const ob = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) setInView(true);
      },
      { root, rootMargin },
    );
    ob.observe(el);
    return () => ob.disconnect();
  }, [inView, rootMargin]);
  return [ref, inView];
}

// A tool group pairs a tool_use with its matching tool_result (by tool_use_id);
// a run of consecutive conversational steps (text/thinking/result) from the same
// agent is one "message".
type Group =
  | { type: "user"; key: number; step: Activity; intent?: boolean }
  | { type: "answer"; key: number; step: Activity }
  | { type: "round"; key: number; label: string }
  | {
      type: "tool";
      key: number;
      worker: string;
      use?: Activity;
      result?: Activity;
    }
  | { type: "msg"; key: number; worker: string; steps: Activity[] }
  | { type: "intercept"; key: number; step: Activity };

function groupSteps(steps: Activity[], chat: boolean): Group[] {
  const out: Group[] = [];
  const byToolId = new Map<string, Extract<Group, { type: "tool" }>>();
  for (const s of steps) {
    if (s.kind === "usage") continue; // live token-usage marker — not a rendered step
    if (s.kind === "round") {
      out.push({ type: "round", key: s.seq, label: s.summary || "" }); // planner round boundary (empty → localized fallback at render)
      continue;
    }
    if (s.kind === "intercept_request") {
      out.push({ type: "intercept", key: s.seq, step: s });
      continue;
    }
    if (s.kind === "user" || s.kind === "intent") {
      // human turn OR the LLM-generated intent leading a worker session — both are
      // right-aligned bubbles; `intent` swaps the avatar to a non-human icon.
      out.push({
        type: "user",
        key: s.seq,
        step: s,
        intent: s.kind === "intent",
      });
      continue;
    }
    // In a chat (main agent) the assistant's text/result IS the answer — render it
    // full (markdown), never collapsed. thinking still folds into a compact block.
    if (s.kind === "result" || (chat && s.kind === "text")) {
      out.push({ type: "answer", key: s.seq, step: s });
      continue;
    }
    if (s.kind === "tool_use") {
      const g: Extract<Group, { type: "tool" }> = {
        type: "tool",
        key: s.seq,
        worker: s.worker,
        use: s,
      };
      if (s.tool_use_id) byToolId.set(s.tool_use_id, g);
      out.push(g);
      continue;
    }
    if (s.kind === "tool_result") {
      // bind to its tool_use by id (NOT adjacency — tools can run in parallel)
      const g = s.tool_use_id ? byToolId.get(s.tool_use_id) : undefined;
      if (g && !g.result) {
        g.result = s;
      } else {
        out.push({ type: "tool", key: s.seq, worker: s.worker, result: s }); // orphan result
      }
      continue;
    }
    const last = out[out.length - 1];
    if (last && last.type === "msg" && last.worker === s.worker) {
      last.steps.push(s);
    } else {
      out.push({ type: "msg", key: s.seq, worker: s.worker, steps: [s] });
    }
  }
  return out;
}

// maps a step kind to its translation sub-key under transcript.kind.* (label resolved at render)
function kindKey(k: string): "thinking" | "result" | "note" {
  if (k === "thinking") return "thinking";
  if (k === "result") return "result";
  return "note";
}

function ActivityTime({ ts }: { ts: string }) {
  const date = new Date(ts);
  if (!ts || Number.isNaN(date.getTime())) return null;
  return (
    <time
      dateTime={date.toISOString()}
      title={date.toLocaleString("zh-CN")}
      className="text-[10px] text-muted-foreground tabular-nums"
      suppressHydrationWarning
    >
      {date.toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })}
    </time>
  );
}

// CopyTextButton renders a compact inline copy button with transient check feedback.
function CopyTextButton({ text }: { text: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        if (!text) return;
        void navigator.clipboard?.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
      className="inline-flex size-5 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      aria-label="Copy"
    >
      {copied ? <CheckIcon className="size-3 text-emerald-500" /> : <CopyIcon className="size-3" />}
    </button>
  );
}

// toolInputText renders a tool_use input for display. For Bash it pulls the shell
// command out of the raw input JSON ({"command":…,"description":…}) so the UI shows
// the command itself instead of JSON; other tools fall back to the raw text.
function pickFirstString(o: Record<string, unknown>, keys: string[]): string {
  for (const k of keys) {
    const v = o[k];
    if (typeof v === "string" && v.length > 0) return v;
  }
  return "";
}

function formatNativeToolInput(tool: string, raw: string): string | null {
  try {
    const o = JSON.parse(raw) as Record<string, unknown>;
    if (!o || typeof o !== "object") return null;
    if (tool === "http_request" && typeof o.url === "string") {
      const m = typeof o.method === "string" ? o.method.toUpperCase() : "GET";
      return `${m} ${o.url}`;
    }
    if (
      (tool === "http_sequence" || tool === "web_recon" || tool === "net_probe" || tool === "dns_probe") &&
      typeof o.mode === "string"
    ) {
      const target = pickFirstString(o, ["url", "host", "domain"]);
      return target ? `[${o.mode}] ${target}` : `[${o.mode}]`;
    }
    if (tool === "http_fuzz" && typeof o.url === "string") {
      const m = typeof o.method === "string" ? o.method.toUpperCase() : "GET";
      return `FUZZ ${m} ${o.url}`;
    }
    if (tool === "codec_crypto" && typeof o.operation === "string") {
      const inp = typeof o.input === "string" ? o.input.slice(0, 48) : "";
      return inp ? `[${o.operation}] ${inp}` : `[${o.operation}]`;
    }
    if (tool === "extract_data" && typeof o.mode === "string") {
      const src = pickFirstString(o, ["file_path", "pattern"]);
      return src ? `[${o.mode}] ${src}` : `[${o.mode}]`;
    }
    if (tool === "tactical_kb" && typeof o.mode === "string") {
      const q = pickFirstString(o, ["query", "category"]);
      return q ? `[${o.mode}] ${q}` : `[${o.mode}]`;
    }
    if (tool === "Skill") {
      const skillName = pickFirstString(o, ["name", "skill", "skill_name"]);
      if (skillName) return skillName;
    }
    if (tool === "human_verification" || tool === "human_verification_solve" || tool === "human_verification_flow") {
      const mode = pickFirstString(o, ["mode", "protocol", "algorithm"]);
      const target = pickFirstString(o, ["target_url", "challenge_url", "submit_url"]);
      if (mode && target) return `[${mode}] ${target}`;
      if (mode) return `[${mode}]`;
      if (target) return target;
    }
    if (tool === "binary_inspect" || tool === "software_analysis") {
      const mode = pickFirstString(o, ["mode", "operation"]);
      const target = pickFirstString(o, ["file_path", "address", "query", "sample_sha"]);
      if (mode && target) return `[${mode}] ${target}`;
      if (mode) return `[${mode}]`;
      if (target) return target;
    }
    if (tool === "record_fact" || tool === "report_finding" || tool === "add_finding" || tool === "add_intent") {
      const summary = pickFirstString(o, ["summary", "name", "vulnclass"]);
      if (summary) return summary;
    }
    if (tool === "Read" || tool === "Write" || tool === "Edit" || tool === "MultiEdit" || tool === "LS") {
      const p = pickFirstString(o, ["file_path", "path"]);
      if (p) return p;
    }
    if (tool === "Grep" || tool === "Glob") {
      const pat = pickFirstString(o, ["pattern"]);
      const p = pickFirstString(o, ["path"]);
      if (pat && p) return `${pat} (${p})`;
      if (pat) return pat;
    }
    if (tool === "search_all_worker_traces") {
      const q = pickFirstString(o, ["query", "q"]);
      if (q) return q;
    }
  } catch {
    return null;
  }
  return null;
}

function toolInputText(tool: string, raw: string): string {
  if (tool !== "Bash") return formatNativeToolInput(tool, raw) ?? raw;
  // full input (expanded detail): parse and pull the command out.
  try {
    const o = JSON.parse(raw);
    if (o && typeof o.command === "string") return o.command;
  } catch {
    // collapsed-row summaries are truncated to ~200 chars by the backend, so the
    // JSON tail is cut off and JSON.parse fails — fall through and extract the
    // "command" field by hand, tolerating the missing closing quote.
  }
  const m = raw.match(/"command"\s*:\s*"((?:\\.|[^"\\])*)/);
  if (m) {
    try {
      // re-wrap the captured body and parse to unescape \n, \", \\, etc.
      return JSON.parse(`"${m[1]}"`);
    } catch {
      // truncated mid-escape — unescape the common sequences best-effort.
      return m[1].replace(/\\(["\\/nrt])/g, (_s, c) => {
        if (c === "n") return "\n";
        if (c === "r") return "\r";
        if (c === "t") return "\t";
        return c;
      });
    }
  }
  return raw;
}

// InterceptCard 渲染 intercept_request 审批卡片。
// 从“工具 X 审批请求 (#N)”摘要解析 pending_id，无需等待详情加载即可显示按钮。
function InterceptCard({ step, getDetail }: { step: Activity; getDetail: (seq: number) => Promise<string> }) {
  const t = useTranslations("transcript");
  // 解析审批摘要中的 (#42) 与工具名，保持前后端格式一致。
  const { pendingId, toolName } = React.useMemo(() => parseApprovalSummary(step.summary), [step.summary]);

  const [detail, setDetail] = React.useState<Record<string, unknown> | null>(null);
  const [decided, setDecided] = React.useState<"allowed" | "denied" | "timeout" | null>(null);
  const [deciding, setDeciding] = React.useState(false);
  const [expanded, setExpanded] = React.useState(false);
  const [pending, setPending] = React.useState<InterceptPending | null>(null);
  const [statusError, setStatusError] = React.useState("");
  const [retry, setRetry] = React.useState(0);

  // Load persisted detail JSON + check the real current status from the backend
  // so that a page refresh shows the already-decided state instead of re-offering buttons.
  React.useEffect(() => {
    let live = true;
    getDetail(step.seq)
      .then((raw) => {
        if (!live || !raw) return;
        try {
          setDetail(JSON.parse(raw));
        } catch {
          /* ignore */
        }
      })
      .catch(() => {
        /* ignore */
      });
    return () => {
      live = false;
    };
  }, [step.seq, getDetail]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: Retry explicitly reloads the same approval after a failed request.
  React.useEffect(() => {
    if (!pendingId) return;
    let live = true;
    api
      .interceptGetOne(pendingId)
      .then((p) => {
        if (!live) return;
        setPending(p);
        setStatusError("");
        if (p.status !== "pending") setDecided(p.status as "allowed" | "denied" | "timeout");
      })
      .catch((error) => {
        if (live) setStatusError((error as Error).message || t("approval.loadError"));
      });
    return () => {
      live = false;
    };
  }, [pendingId, retry]);

  async function decide(decision: "allowed" | "denied") {
    if (step.inherited || !pendingId || deciding) return;
    setDeciding(true);
    try {
      await api.interceptDecide(pendingId, decision);
      setDecided(decision);
      toast.success(decision === "allowed" ? t("approval.allowedToast") : t("approval.deniedToast"));
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setDeciding(false);
    }
  }

  const inputStr = detail?.input ? JSON.stringify(detail.input).slice(0, 200) : null;

  const row = pending
    ? {
        ...pending,
        status: decided ?? pending.status,
        conv_title: "",
        conv_agent_key: "",
        rule_name: "",
      }
    : null;

  let decidedBadgeClass = "bg-red-500/15 text-red-600 dark:text-red-400";
  let decidedLabel = t("approval.denied");
  if (decided === "allowed") {
    decidedBadgeClass = "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400";
    decidedLabel = t("approval.allowed");
  } else if (decided === "timeout") {
    decidedBadgeClass = "bg-zinc-500/15 text-zinc-600 dark:text-zinc-400";
    decidedLabel = t("approval.timeout");
  }

  return (
    <div className="my-2 rounded-xl border border-amber-500/40 bg-amber-500/8 p-3 text-xs shadow-2xs">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2">
          <ShieldAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-500" />
          <div className="min-w-0 space-y-0.5">
            <div className="flex items-center gap-1.5 font-medium">
              <span className="text-amber-700 dark:text-amber-300">{t("approval.request")}</span>
              <code className="rounded bg-amber-500/20 px-1.5 py-0.5 font-mono text-amber-800 dark:text-amber-200">
                {toolName}
              </code>
              {pendingId && <span className="text-muted-foreground">#{pendingId}</span>}
            </div>
            {inputStr && <p className="truncate font-mono text-muted-foreground">{inputStr}</p>}
          </div>
        </div>

        {step.inherited ? (
          <Badge variant="outline">{t("approval.historyReadonly")}</Badge>
        ) : decided ? (
          <span className={cn("shrink-0 rounded-md px-2 py-0.5 font-medium text-[11px]", decidedBadgeClass)}>
            {decidedLabel}
          </span>
        ) : (
          <div className="flex shrink-0 gap-1.5">
            <Button
              size="sm"
              className="h-6 px-2.5 text-xs"
              disabled={deciding || !pendingId}
              onClick={() => void decide("allowed")}
            >
              <CheckIcon className="size-3" />
              {t("approval.allow")}
            </Button>
            <Button
              size="sm"
              variant="destructive"
              className="h-6 px-2.5 text-xs"
              disabled={deciding || !pendingId}
              onClick={() => void decide("denied")}
            >
              <XIcon className="size-3" />
              {t("approval.deny")}
            </Button>
          </div>
        )}
      </div>
      {pendingId ? (
        <Collapsible open={expanded} onOpenChange={setExpanded} className="mt-2 min-w-0">
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" aria-label={expanded ? t("approval.collapse") : t("approval.expand")}>
              {expanded ? <ChevronDown data-icon="inline-start" /> : <ChevronRight data-icon="inline-start" />}
              {expanded ? t("approval.collapse") : t("approval.expand")}
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            {expanded && row ? (
              <ApprovalDetail
                row={row}
                busy={deciding}
                decide={(_id, decision) => decide(decision)}
                revision={retry}
                readOnly={Boolean(step.inherited)}
                defaultExpanded
                onResolved={setDecided}
              />
            ) : statusError ? (
              <div className="flex flex-wrap items-center gap-2 p-3" role="alert">
                <span>{statusError}</span>
                <Button variant="outline" size="sm" onClick={() => setRetry((value) => value + 1)}>
                  {t("approval.retry")}
                </Button>
              </div>
            ) : (
              <p className="p-3 text-muted-foreground">{t("approval.loading")}</p>
            )}
          </CollapsibleContent>
        </Collapsible>
      ) : null}
    </div>
  );
}

function tryPrettyJson(raw: string): string | null {
  const trimmed = raw.trim();
  if (!trimmed || (trimmed[0] !== "{" && trimmed[0] !== "[")) return null;
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return null;
  }
}

function countLines(text: string): number {
  if (!text) return 0;
  let n = 1;
  for (let i = 0; i < text.length; i++) {
    if (text.charCodeAt(i) === 10) n++;
  }
  return n;
}

// ToolBlock renders ONE tool call using 3-tier progressive disclosure:
// Tier 1 (collapsed): Timeline left rail + semantic category pill + tool name + primary target + status badge.
// Tier 2 (expanded): Split Command/Input & Output/Response inspector with JSON formatting, copy, and line-wrap.
function ToolBlock({
  group,
  getDetail,
  showWorker,
  focused = false,
  expandSignal,
}: {
  group: Extract<Group, { type: "tool" }>;
  getDetail: (seq: number) => Promise<string>;
  showWorker?: boolean;
  focused?: boolean;
  expandSignal?: { version: number; open: boolean };
}) {
  const t = useTranslations("transcript");
  const [open, setOpen] = React.useState(focused);
  const [wrapLines, setWrapLines] = React.useState(true);
  const [prettyJson, setPrettyJson] = React.useState(true);
  const targetRef = React.useRef<HTMLElement>(null);
  const [sections, setSections] = React.useState<
    { label: string; content: string; isCmd?: boolean; isError?: boolean }[] | null
  >(null);
  const loadedKey = React.useRef<string | null>(null);
  const { use, result } = group;
  const toolName = use?.tool || result?.tool || t("tool.name");
  const cat = getToolCategoryMeta(toolName);
  const ToolIcon = cat.Icon;
  const running = !result;
  const ok = !!result && !result.is_error;
  let statusPillClass =
    "border-red-500/35 bg-red-500/10 text-red-600 dark:border-red-500/35 dark:bg-red-500/15 dark:text-red-300";
  if (running) {
    statusPillClass =
      "border-sky-500/40 bg-sky-500/12 text-sky-700 dark:border-sky-500/35 dark:bg-sky-500/15 dark:text-sky-300";
  } else if (ok) {
    statusPillClass =
      "border-emerald-500/35 bg-emerald-500/10 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/12 dark:text-emerald-300";
  }

  const rawCmd = use?.summary.startsWith(toolName)
    ? use.summary.slice(toolName.length).trimStart()
    : (use?.summary ?? "");
  const cmd = toolInputText(toolName, rawCmd);
  const loopTripped = Boolean(
    result?.summary?.includes("专家战术熔断提醒") || sections?.some((s) => s.content.includes("专家战术熔断提醒")),
  );
  if (loopTripped) {
    statusPillClass =
      "border-amber-500/45 bg-amber-500/15 text-amber-700 dark:border-amber-500/40 dark:bg-amber-500/15 dark:text-amber-300";
  }

  let statusText = ok ? t("tool.ok") : t("tool.failed");
  if (loopTripped) statusText = "⚡ 3-Strike";
  if (running) statusText = t("tool.running");

  const seqNum = use?.seq ?? result?.seq;
  const stepTs = result?.ts || use?.ts || "";

  React.useEffect(() => {
    if (expandSignal && expandSignal.version > 0 && !focused) {
      setOpen(expandSignal.open);
    }
  }, [expandSignal, focused]);

  const detailKey = `${use?.seq ?? ""}:${result?.seq ?? ""}`;
  React.useEffect(() => {
    if (!open || loadedKey.current === detailKey) return;
    let live = true;
    const segs: {
      label: string;
      seq: number;
      cmd?: boolean;
      isError?: boolean;
    }[] = [];
    if (use) segs.push({ label: t("tool.command"), seq: use.seq, cmd: true });
    if (result) {
      segs.push({
        label: t("tool.output") + (result.is_error ? " ✕" : " ✓"),
        seq: result.seq,
        isError: result.is_error,
      });
    }
    void Promise.all(
      segs.map((x) =>
        getDetail(x.seq)
          .then((d) => d || t("tool.empty"))
          .catch(() => t("tool.loadFailed")),
      ),
    ).then((parts) => {
      if (!live) return;
      setSections(
        segs.map((x, i) => ({
          label: x.label,
          content: x.cmd ? toolInputText(toolName, parts[i]) : parts[i],
          isCmd: x.cmd,
          isError: x.isError,
        })),
      );
      loadedKey.current = detailKey;
    });
    return () => {
      live = false;
    };
  }, [open, detailKey, use, result, getDetail, toolName, t]);

  const scrolledRef = React.useRef(false);
  React.useEffect(() => {
    scrolledRef.current = false;
    if (focused) setOpen(true);
  }, [focused]);
  React.useEffect(() => {
    if (!focused || !open || sections === null || scrolledRef.current) return;
    const el = targetRef.current;
    const viewport = el?.closest('[data-slot="scroll-area-viewport"]');
    if (!el || !viewport) return;
    scrolledRef.current = true;
    const center = () => {
      const rect = el.getBoundingClientRect();
      const bounds = viewport.getBoundingClientRect();
      viewport.scrollTop += rect.top + rect.height / 2 - bounds.top - bounds.height / 2;
    };
    const observer = new ResizeObserver(center);
    observer.observe(el.parentElement ?? el);
    observer.observe(viewport);
    const stop = () => observer.disconnect();
    viewport.addEventListener("wheel", stop, { passive: true, once: true });
    viewport.addEventListener("touchstart", stop, {
      passive: true,
      once: true,
    });
    viewport.addEventListener("pointerdown", stop, { once: true });
    const frame = requestAnimationFrame(center);
    const timer = setTimeout(stop, 2000);
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(timer);
      stop();
      viewport.removeEventListener("wheel", stop);
      viewport.removeEventListener("touchstart", stop);
      viewport.removeEventListener("pointerdown", stop);
    };
  }, [focused, open, sections]);

  let railColor = "before:bg-border/80 dark:before:bg-zinc-700";
  if (running) {
    railColor = "before:bg-sky-500/70";
  } else if (loopTripped) {
    railColor = "before:bg-amber-500/70";
  } else if (result?.is_error) {
    railColor = "before:bg-red-500/60";
  }

  return (
    <section
      ref={targetRef}
      aria-label={focused ? t("tool.pinned", { seq: use?.seq ?? "" }) : undefined}
      className={cn(
        "relative my-0.5 pl-3 text-xs before:absolute before:top-0 before:bottom-0 before:left-1 before:w-0.5 before:rounded-full",
        railColor,
        focused && "my-1.5 rounded-xl border-2 border-primary bg-primary/5 p-2.5 before:hidden",
      )}
    >
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "group flex w-full items-center gap-2 rounded-lg border px-2.5 py-1.5 text-left transition-all",
          loopTripped && "border-amber-500/40 bg-amber-500/8 hover:bg-amber-500/15",
          !loopTripped &&
            (open
              ? "border-primary/30 bg-accent/65 text-foreground shadow-2xs dark:border-zinc-700 dark:bg-zinc-800/90"
              : "border-border/70 bg-card/90 hover:border-primary/25 hover:bg-accent/40 dark:border-zinc-800/90 dark:bg-zinc-900/60 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/70"),
        )}
      >
        <span className="shrink-0 text-muted-foreground transition-transform">
          {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </span>
        {seqNum != null && seqNum >= 0 && (
          <span className="hidden shrink-0 rounded bg-muted/80 px-1 py-0.5 font-mono text-[10px] text-muted-foreground tabular-nums sm:inline dark:bg-zinc-800">
            #{seqNum}
          </span>
        )}
        <span
          className={cn(
            "inline-flex shrink-0 items-center gap-1 rounded border px-1.5 py-0.5 font-mono font-semibold text-[10px] tracking-tight",
            cat.pillClass,
          )}
        >
          <ToolIcon className="size-3" />
          {cat.tag}
        </span>
        {showWorker && <span className={chip(group.worker)}>{group.worker}</span>}
        <span
          className="shrink-0 font-mono font-semibold text-[11.5px] text-foreground"
          title={toolName !== cat.displayName ? toolName : undefined}
        >
          {cat.displayName}
        </span>
        {cmd && (
          <span
            className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground group-hover:text-foreground/85"
            title={cmd}
          >
            {cmd}
          </span>
        )}
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {stepTs && (
            <span className="hidden md:inline">
              <ActivityTime ts={stepTs} />
            </span>
          )}
          <span
            className={cn(
              "inline-flex shrink-0 items-center gap-1 rounded-md border px-1.5 py-0.5 font-medium font-mono text-[10px]",
              statusPillClass,
            )}
          >
            {running && <Loader2Icon className="size-2.5 animate-spin" />}
            {statusText}
          </span>
        </div>
      </button>

      {open && (
        <div className="mt-1.5 mb-2 ml-1 overflow-hidden rounded-xl border border-border/85 bg-card shadow-xs dark:border-zinc-800 dark:bg-zinc-900/85">
          {!sections ? (
            <div className="flex items-center gap-2 p-3 font-mono text-[11px] text-muted-foreground">
              <Loader2Icon className="size-3.5 animate-spin" />
              {t("loading")}
            </div>
          ) : (
            <div className="divide-y divide-border/75 dark:divide-zinc-800">
              {sections.map((sec) => {
                const formatted = prettyJson ? tryPrettyJson(sec.content) : null;
                const displayText = formatted ?? sec.content;
                const lines = countLines(displayText);
                const chars = displayText.length;
                return (
                  <div key={sec.label} className="flex flex-col">
                    <div className="flex flex-wrap items-center justify-between gap-2 bg-muted/50 px-3 py-1.5 text-[10px] dark:bg-zinc-800/60">
                      <div className="flex items-center gap-2">
                        <span
                          className={cn(
                            "font-semibold uppercase tracking-wide",
                            sec.isError ? "text-red-600 dark:text-red-400" : "text-foreground/80",
                          )}
                        >
                          {sec.label}
                        </span>
                        <span className="rounded bg-background/80 px-1.5 py-0.5 font-mono text-[9.5px] text-muted-foreground tabular-nums dark:bg-zinc-900/80">
                          {t("tool.metaStats", { lines, chars })}
                        </span>
                      </div>
                      <div className="flex items-center gap-1">
                        {tryPrettyJson(sec.content) !== null && (
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation();
                              setPrettyJson((p) => !p);
                            }}
                            className={cn(
                              "inline-flex size-5 items-center justify-center rounded transition-colors hover:bg-accent hover:text-foreground",
                              prettyJson ? "text-primary" : "text-muted-foreground",
                            )}
                            title={t("tool.formatJson")}
                            aria-label={t("tool.formatJson")}
                          >
                            <CodeIcon className="size-3" />
                          </button>
                        )}
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation();
                            setWrapLines((w) => !w);
                          }}
                          className={cn(
                            "inline-flex size-5 items-center justify-center rounded transition-colors hover:bg-accent hover:text-foreground",
                            wrapLines ? "text-primary" : "text-muted-foreground",
                          )}
                          title={t("tool.wrapLines")}
                          aria-label={t("tool.wrapLines")}
                        >
                          <WrapTextIcon className="size-3" />
                        </button>
                        <CopyTextButton text={displayText} />
                      </div>
                    </div>
                    <pre
                      className={cn(
                        "max-h-80 overflow-auto p-3 font-mono text-[11px] leading-relaxed",
                        wrapLines ? "whitespace-pre-wrap break-all" : "whitespace-pre",
                        sec.isCmd
                          ? "bg-sky-500/[0.03] text-sky-800 dark:bg-zinc-950/50 dark:text-sky-300"
                          : sec.isError
                            ? "bg-red-500/[0.03] text-red-700 dark:bg-red-950/20 dark:text-red-300"
                            : "text-foreground/90 dark:bg-zinc-950/30",
                      )}
                    >
                      {displayText}
                    </pre>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

// MessageBlock renders a coalesced agent message / reasoning trace (merged streaming
// text/thinking/result fragments into one block) with distinct reasoning styling.
function MessageBlock({
  group,
  getDetail,
  showWorker,
}: {
  group: Extract<Group, { type: "msg" }>;
  getDetail: (seq: number) => Promise<string>;
  showWorker?: boolean;
}) {
  const t = useTranslations("transcript");
  const [open, setOpen] = React.useState(false);
  const [stepDetails, setStepDetails] = React.useState<{ kind: string; text: string }[] | null>(null);
  const loadedKey = React.useRef<string | null>(null);
  const speak = group.steps.filter((s) => s.kind !== "thinking");
  const hasThinking = group.steps.some((s) => s.kind === "thinking");
  const onlyThinking = speak.length === 0 && hasThinking;
  const isError = group.steps.some((s) => s.is_error);
  const body = (speak.length ? speak : group.steps).map((s) => s.summary).join("  ") || "…";
  let Icon = MessageSquare;
  if (isError) {
    Icon = Flag;
  } else if (onlyThinking || hasThinking) {
    Icon = BrainCircuitIcon;
  }

  let tone = "text-foreground";
  if (isError) {
    tone = "text-red-600 dark:text-red-400";
  } else if (onlyThinking) {
    tone = "text-muted-foreground italic";
  }

  const detailKey = group.steps.map((s) => s.seq).join(",");
  React.useEffect(() => {
    if (!open || loadedKey.current === detailKey) return;
    let live = true;
    void Promise.all(
      group.steps.map((s) =>
        getDetail(s.seq)
          .then((d) => d || s.summary)
          .catch(() => s.summary),
      ),
    ).then((parts) => {
      if (!live) return;
      setStepDetails(
        group.steps.map((s, i) => ({
          kind: t(`kind.${kindKey(s.kind)}`),
          text: parts[i],
        })),
      );
      loadedKey.current = detailKey;
    });
    return () => {
      live = false;
    };
  }, [open, detailKey, group.steps, getDetail, t]);

  return (
    <div className="relative pl-3 text-xs before:absolute before:top-0 before:bottom-0 before:left-1 before:w-px before:bg-border/70">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left transition-colors",
          open ? "bg-secondary/60" : "hover:bg-muted/50",
        )}
      >
        <span className="shrink-0 text-muted-foreground">
          {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </span>
        <Icon
          className={cn(
            "size-3.5 shrink-0",
            isError && "text-red-500",
            !isError && (hasThinking ? "text-violet-500 dark:text-violet-400" : "text-primary/80"),
          )}
        />
        {showWorker && <span className={chip(group.worker)}>{group.worker}</span>}
        {hasThinking && (
          <span className="shrink-0 rounded border border-violet-500/30 bg-violet-500/10 px-1.5 py-0.5 font-medium text-[10px] text-violet-600 dark:text-violet-300">
            {t("kind.thinking")}
          </span>
        )}
        <span className={cn("min-w-0 flex-1 truncate", tone)}>{body}</span>
      </button>
      {open && (
        <div className="mt-1.5 mb-2 ml-2 overflow-hidden rounded-lg border border-border/80 bg-secondary/30 shadow-2xs">
          {!stepDetails ? (
            <div className="p-3 font-mono text-[11px] text-muted-foreground">{t("loading")}</div>
          ) : (
            <div className="divide-y divide-border/70">
              {stepDetails.map((item, idx) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: coalesced steps are static per detailKey
                <div key={idx} className="flex flex-col">
                  <div className="flex items-center justify-between bg-muted/40 px-3 py-1 text-[10px]">
                    <span className="font-semibold text-muted-foreground uppercase">{item.kind}</span>
                    <CopyTextButton text={item.text} />
                  </div>
                  <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words p-2.5 font-mono text-[11px] text-foreground/90 leading-relaxed">
                    {item.text}
                  </pre>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// fmtBytes renders a human file size for attachment chips.
function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

type MsgAttachment = { name: string; path: string; size: number };

// parseUserBody splits a user turn's body into text + attachments.
export function parseUserBody(body: string): {
  text: string;
  attachments: MsgAttachment[];
} {
  if (body.startsWith("{")) {
    try {
      const p = JSON.parse(body);
      if (p && Array.isArray(p.attachments)) {
        return {
          text: typeof p.text === "string" ? p.text : "",
          attachments: p.attachments,
        };
      }
    } catch {
      /* plain text that merely starts with "{" */
    }
  }
  return { text: body, attachments: [] };
}

export interface TranscriptRollback {
  disabledReason?: string;
  onRollback: (step: Activity) => void;
}

function UserRow({
  step,
  intent,
  getDetail,
  rollback,
}: {
  step: Activity;
  intent?: boolean;
  getDetail: (seq: number) => Promise<string>;
  rollback?: TranscriptRollback;
}) {
  const tr = useTranslations("checkpoints");
  const Icon = intent ? CrosshairIcon : UserIcon;
  const [ref, inView] = useInView();
  const inline = step.detail && step.detail.length > 0 ? step.detail : null;
  const [full, setFull] = React.useState<string | null>(inline);
  React.useEffect(() => {
    if (inline) return;
    if (!inView) return;
    let live = true;
    getDetail(step.seq)
      .then((d) => {
        if (live) setFull(d || step.summary);
      })
      .catch(() => {
        if (live) setFull(step.summary);
      });
    return () => {
      live = false;
    };
  }, [inView, step.seq, getDetail, step.summary, inline]);
  const { text, attachments } = parseUserBody(full ?? step.summary);
  return (
    <div ref={ref} className="group mt-4 mb-2.5 flex min-w-0 justify-end gap-2.5">
      <div className="flex min-w-0 max-w-[85%] flex-col items-end gap-1.5">
        {attachments.length > 0 && (
          <div className="flex min-w-0 max-w-full flex-wrap justify-end gap-1.5">
            {attachments.map((a) => (
              <div
                key={a.path}
                className="flex min-w-0 max-w-full items-center gap-1.5 rounded-lg border bg-card px-2.5 py-1 text-xs shadow-2xs"
                title={a.path}
              >
                <PaperclipIcon className="size-3 shrink-0 text-primary" />
                <span className="max-w-[180px] truncate font-medium">{a.name}</span>
                <span className="shrink-0 text-muted-foreground">{fmtBytes(a.size)}</span>
              </div>
            ))}
          </div>
        )}
        {text && (
          <div className="min-w-0 max-w-full whitespace-pre-wrap rounded-2xl rounded-tr-sm bg-primary px-3.5 py-2 text-primary-foreground text-sm leading-relaxed shadow-xs [overflow-wrap:anywhere]">
            {text}
          </div>
        )}
        <div className="flex items-center gap-1.5">
          <ActivityTime ts={step.ts} />
          {rollback && !intent && (
            <span title={rollback.disabledReason ?? tr("msgRollback.tip")}>
              <button
                type="button"
                onClick={() => rollback.onRollback(step)}
                disabled={!!rollback.disabledReason}
                aria-label={tr("msgRollback.tip")}
                className="flex size-5 items-center justify-center rounded text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 disabled:pointer-events-none group-hover:opacity-100 group-hover:disabled:opacity-40"
              >
                <Undo2Icon className="size-3.5" />
              </button>
            </span>
          )}
        </div>
      </div>
      <div className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full border border-primary/25 bg-primary/12">
        <Icon className="size-3.5 text-primary" />
      </div>
    </div>
  );
}

// AnswerBlock renders the agent's FINAL answer (kind="result") in full — never
// collapsed. Styled as an elevated tactical conclusion card with a primary accent bar.
function AnswerBlock({ step, getDetail }: { step: Activity; getDetail: (seq: number) => Promise<string> }) {
  const [ref, inView] = useInView();
  const [full, setFull] = React.useState<string | null>(null);
  React.useEffect(() => {
    if (!inView || step.is_error) return;
    let live = true;
    getDetail(step.seq)
      .then((d) => {
        if (live) setFull(d || step.summary);
      })
      .catch(() => {
        if (live) setFull(step.summary);
      });
    return () => {
      live = false;
    };
  }, [inView, step.seq, step.is_error, getDetail, step.summary]);
  const contentText = step.is_error ? step.summary.split("\n")[0] : (full ?? step.summary);
  return (
    <div ref={ref} className="mt-2 mb-3 flex min-w-0 flex-col gap-1">
      <div
        className={cn(
          "min-w-0 flex-1 break-words rounded-xl border px-4 py-3 shadow-2xs",
          step.is_error
            ? "border-red-500/35 bg-red-500/8 text-red-600 text-sm dark:text-red-400"
            : "border-border/80 border-l-2 border-l-primary bg-muted/45",
        )}
      >
        {step.is_error ? <span>{contentText}</span> : <Markdown text={contentText} />}
      </div>
      <div className="flex items-center justify-between px-1">
        <ActivityTime ts={step.ts} />
        {!step.is_error && contentText && <CopyTextButton text={contentText} />}
      </div>
    </div>
  );
}

// ExecView renders an agent execution replay (planner / worker / main agent) in
// the compact, grouped, expand-to-detail format with an interactive tool telemetry bar.
function ExecView({
  activity,
  taskId,
  chat,
  fetchDetail,
  focusedSeq,
  rollback,
}: {
  activity: Activity[];
  taskId?: string;
  chat?: boolean;
  fetchDetail?: (seq: number) => Promise<string>;
  focusedSeq?: number;
  rollback?: TranscriptRollback;
}) {
  const t = useTranslations("transcript");
  const [filterMode, setFilterMode] = React.useState<"all" | "tools" | "errors">("all");
  const [expandSignal, setExpandSignal] = React.useState<{ version: number; open: boolean }>({
    version: 0,
    open: false,
  });
  const showWorker = new Set(activity.map((a) => a.worker)).size > 1;
  const getDetail = React.useCallback(
    (seq: number) => (fetchDetail ? fetchDetail(seq) : api.activityDetail(seq, taskId).then((r) => r.detail ?? "")),
    [fetchDetail, taskId],
  );
  const groups = React.useMemo(() => groupSteps(activity, !!chat), [activity, chat]);
  const toolStats = React.useMemo(() => {
    let total = 0;
    let ok = 0;
    let running = 0;
    let errors = 0;
    for (const g of groups) {
      if (g.type !== "tool") continue;
      total++;
      if (!g.result) {
        running++;
      } else if (g.result.is_error || g.result.summary?.includes("专家战术熔断提醒")) {
        errors++;
      } else {
        ok++;
      }
    }
    return { total, ok, running, errors };
  }, [groups]);

  const visibleGroups = React.useMemo(() => {
    if (filterMode === "all") return groups;
    if (filterMode === "tools") {
      return groups.filter((g) => g.type === "tool" || g.type === "intercept" || (g.type === "user" && g.intent));
    }
    return groups.filter((g) => {
      if (g.type === "tool") {
        return Boolean(g.result?.is_error || g.result?.summary?.includes("专家战术熔断提醒"));
      }
      if (g.type === "intercept") return true;
      if (g.type === "answer") return Boolean(g.step.is_error);
      if (g.type === "msg") return g.steps.some((s) => s.is_error);
      return false;
    });
  }, [groups, filterMode]);

  return (
    <div className="flex flex-col gap-1">
      {toolStats.total > 0 && (
        <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2 rounded-xl border border-border/80 bg-muted/40 px-3 py-1.5 text-[11px] dark:border-zinc-800 dark:bg-zinc-900/80">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="inline-flex items-center gap-1 font-medium text-foreground">
              <Wrench className="size-3 text-primary" />
              {t("tool.telemetryCalls", { total: toolStats.total })}
            </span>
            <span className="rounded bg-emerald-500/12 px-1.5 py-0.5 font-mono text-[10px] text-emerald-700 tabular-nums dark:text-emerald-300">
              {t("tool.telemetryOk", { count: toolStats.ok })}
            </span>
            {toolStats.running > 0 && (
              <span className="inline-flex items-center gap-1 rounded bg-sky-500/15 px-1.5 py-0.5 font-mono text-[10px] text-sky-700 tabular-nums dark:text-sky-300">
                <Loader2Icon className="size-2.5 animate-spin" />
                {t("tool.telemetryRunning", { count: toolStats.running })}
              </span>
            )}
            {toolStats.errors > 0 && (
              <span className="rounded bg-red-500/12 px-1.5 py-0.5 font-mono text-[10px] text-red-600 tabular-nums dark:text-red-300">
                {t("tool.telemetryErrors", { count: toolStats.errors })}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-1">
            <button
              type="button"
              onClick={() => setFilterMode("all")}
              className={cn(
                "rounded-md border px-2 py-0.5 text-[10px] transition-colors",
                filterMode === "all"
                  ? "border-primary/40 bg-primary/12 font-semibold text-primary dark:border-zinc-600 dark:bg-zinc-800 dark:text-zinc-100"
                  : "border-transparent text-muted-foreground hover:bg-accent",
              )}
            >
              {t("tool.allSteps")}
            </button>
            <button
              type="button"
              onClick={() => setFilterMode("tools")}
              className={cn(
                "rounded-md border px-2 py-0.5 text-[10px] transition-colors",
                filterMode === "tools"
                  ? "border-primary/40 bg-primary/12 font-semibold text-primary dark:border-zinc-600 dark:bg-zinc-800 dark:text-zinc-100"
                  : "border-transparent text-muted-foreground hover:bg-accent",
              )}
            >
              {t("tool.toolsOnly", { count: toolStats.total })}
            </button>
            {toolStats.errors > 0 && (
              <button
                type="button"
                onClick={() => setFilterMode("errors")}
                className={cn(
                  "rounded-md border px-2 py-0.5 text-[10px] transition-colors",
                  filterMode === "errors"
                    ? "border-red-500/40 bg-red-500/15 font-semibold text-red-600 dark:text-red-300"
                    : "border-transparent text-muted-foreground hover:bg-accent",
                )}
              >
                {t("tool.errorsOnly", { count: toolStats.errors })}
              </button>
            )}
            <span className="mx-0.5 h-3.5 w-px bg-border dark:bg-zinc-700" />
            <button
              type="button"
              onClick={() => setExpandSignal((s) => ({ version: s.version + 1, open: !s.open }))}
              className="inline-flex items-center gap-1 rounded-md border border-border/70 bg-background/80 px-2 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground dark:border-zinc-700 dark:bg-zinc-800/80"
              title={expandSignal.open ? t("tool.collapseAll") : t("tool.expandAll")}
            >
              {expandSignal.open ? (
                <>
                  <ChevronsDownUpIcon className="size-3" />
                  {t("tool.collapseAll")}
                </>
              ) : (
                <>
                  <ChevronsUpDownIcon className="size-3" />
                  {t("tool.expandAll")}
                </>
              )}
            </button>
          </div>
        </div>
      )}
      {visibleGroups.map((g) => {
        if (g.type === "round") {
          return (
            <div
              key={`r${g.key}`}
              className="my-3 flex items-center gap-2.5 font-semibold text-[10px] text-muted-foreground uppercase tracking-wider"
            >
              <span className="h-px flex-1 bg-border/80" />
              <span className="inline-flex items-center gap-1 rounded-full border bg-muted/60 px-2.5 py-0.5">
                <ZapIcon className="size-2.5 text-primary" />
                {g.label || t("newRound")}
              </span>
              <span className="h-px flex-1 bg-border/80" />
            </div>
          );
        }
        if (g.type === "user") {
          return (
            <UserRow key={`u${g.key}`} step={g.step} intent={g.intent} getDetail={getDetail} rollback={rollback} />
          );
        }
        if (g.type === "answer") {
          return <AnswerBlock key={`a${g.key}`} step={g.step} getDetail={getDetail} />;
        }
        if (g.type === "tool") {
          return (
            <ToolBlock
              key={`t${g.key}`}
              group={g}
              getDetail={getDetail}
              showWorker={showWorker}
              focused={focusedSeq != null && g.use?.seq === focusedSeq}
              expandSignal={expandSignal}
            />
          );
        }
        if (g.type === "intercept") {
          return <InterceptCard key={`ic${g.key}`} step={g.step} getDetail={getDetail} />;
        }
        return <MessageBlock key={`m${g.key}`} group={g} getDetail={getDetail} showWorker={showWorker} />;
      })}
    </div>
  );
}

// Transcript renders a session's activity as a compact grouped execution replay.
export function Transcript({
  activity,
  live,
  taskId,
  chat,
  fetchDetail,
  focusedSeq,
  rollback,
}: {
  activity: Activity[];
  live?: boolean;
  taskId?: string;
  chat?: boolean;
  fetchDetail?: (seq: number) => Promise<string>;
  focusedSeq?: number;
  rollback?: TranscriptRollback;
}) {
  const t = useTranslations("transcript");
  const transcriptRef = React.useRef<HTMLDivElement>(null);
  const [focusPadding, setFocusPadding] = React.useState(0);
  React.useLayoutEffect(() => {
    if (focusedSeq == null) {
      setFocusPadding(0);
      return;
    }
    const viewport = transcriptRef.current?.closest('[data-slot="scroll-area-viewport"]');
    if (!viewport) return;
    const measure = () => setFocusPadding(viewport.clientHeight / 2);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(viewport);
    return () => observer.disconnect();
  }, [focusedSeq]);
  return (
    <div
      ref={transcriptRef}
      className="flex flex-col gap-1"
      style={focusPadding ? { paddingBlock: focusPadding } : undefined}
    >
      <ExecView
        activity={activity}
        taskId={taskId}
        chat={chat}
        fetchDetail={fetchDetail}
        focusedSeq={focusedSeq}
        rollback={rollback}
      />
      {live && (
        <div className="flex items-center gap-2 pt-1.5 pl-3 text-muted-foreground text-xs">
          <span className="flex gap-1">
            <span className="size-1.5 animate-bounce rounded-full bg-primary [animation-delay:-0.3s]" />
            <span className="size-1.5 animate-bounce rounded-full bg-primary [animation-delay:-0.15s]" />
            <span className="size-1.5 animate-bounce rounded-full bg-primary" />
          </span>
          {t("streaming")}
        </div>
      )}
    </div>
  );
}
