"use client";

import * as React from "react";

import {
  CheckIcon,
  ChevronDown,
  ChevronRight,
  CopyIcon,
  CrosshairIcon,
  Flag,
  MessageSquare,
  PaperclipIcon,
  ShieldAlertIcon,
  Undo2Icon,
  UserIcon,
  WrapTextIcon,
  XIcon,
  ZapIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { ApprovalDetail } from "@/components/approval-records";
import { LiveStreamMarkdown } from "@/components/live-markdown";
import { Markdown } from "@/components/markdown";
import { CopyTextButton } from "@/components/transcript/copy-text-button";
import { LiveToolRow } from "@/components/transcript/live-tool-row";
import { ToolBody, type ToolSectionModel } from "@/components/transcript/renderers";
import { StatusRing } from "@/components/transcript/status-ring";
import { ThinkingRow } from "@/components/transcript/thinking-row";
import { toolKindFor } from "@/components/transcript/tool-kind";
import { ToolSummaryRow } from "@/components/transcript/tool-summary-row";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { api } from "@/lib/api";
import { parseApprovalSummary } from "@/lib/approval-summary";
import { resolveToolStatus, TOOL_STATUS_LABEL_KEY, type ToolStatus } from "@/lib/tool-status";
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

// 工具分类已移到 components/transcript/tool-kind.ts：分类只决定行首图标与类别文案，
// 不再携带彩色胶囊样式——行的层级交给状态圈 + 文案表达，颜色只留给失败/拦截。
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
      className="text-ui-2xs text-muted-foreground tabular-nums"
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
    if (tool === "Skill" && typeof o.skill === "string") {
      return o.skill;
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
          <span className={cn("shrink-0 rounded-md px-2 py-0.5 font-medium text-ui-xs", decidedBadgeClass)}>
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

// ToolBlock renders ONE tool call using 3-tier progressive disclosure:
// Tier 1 (collapsed): Timeline left rail + semantic category pill + tool name + primary target + status badge.
// Tier 2 (expanded): Split Command/Input & Output/Response inspector with copy and line-wrap controls.
function ToolBlock({
  group,
  getDetail,
  showWorker,
  focused = false,
}: {
  group: Extract<Group, { type: "tool" }>;
  getDetail: (seq: number) => Promise<string>;
  showWorker?: boolean;
  focused?: boolean;
}) {
  const t = useTranslations("transcript");
  const [open, setOpen] = React.useState(focused);
  const [wrapLines, setWrapLines] = React.useState(true);
  const targetRef = React.useRef<HTMLElement>(null);
  const [sections, setSections] = React.useState<ToolSectionModel[] | null>(null);
  const loadedKey = React.useRef<string | null>(null);
  const { use, result } = group;
  const toolName = use?.tool || result?.tool || t("tool.name");
  // 六态统一由 lib/tool-status.ts 判定：显式 metadata 优先，老数据退回文案标记。
  const kind = toolKindFor(toolName);
  const status: ToolStatus = resolveToolStatus({ result });

  const rawCmd = use?.summary.startsWith(toolName)
    ? use.summary.slice(toolName.length).trimStart()
    : (use?.summary ?? "");
  const cmd = toolInputText(toolName, rawCmd);
  const loopTripped = Boolean(
    result?.summary?.includes("专家战术熔断提醒") || sections?.some((s) => s.content.includes("专家战术熔断提醒")),
  );
  const statusLabel = loopTripped ? "⚡ 3-Strike" : t(TOOL_STATUS_LABEL_KEY[status]);

  // 运行中的工具行默认展开：ZCode 的做法是「干活的时候看得见」，参数一落地就先把
  // 差异/命令画出来（写文件的 +N、改文件的红绿），而不是等执行完才一次性出现。
  // 结束（完成/失败/拦截/停止）后回落到折叠态，可以在不展开的情况下扫完整条时间线。
  const expanded = open || status === "running";

  const detailKey = `${use?.seq ?? ""}:${result?.seq ?? ""}`;
  React.useEffect(() => {
    if (!expanded || loadedKey.current === detailKey) return;
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
  }, [expanded, detailKey, use, result, getDetail, toolName, t]);

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

  return (
    <section
      ref={targetRef}
      aria-label={focused ? t("tool.pinned", { seq: use?.seq ?? "" }) : undefined}
      className={cn(
        "relative pl-3 text-xs before:absolute before:top-0 before:bottom-0 before:left-1 before:w-px",
        loopTripped ? "before:bg-amber-500/50" : "before:bg-border/70",
        focused && "my-1 rounded-xl border-2 border-primary bg-primary/5 p-2.5 before:hidden",
      )}
    >
      <ToolSummaryRow
        alert={loopTripped}
        endedAt={result?.ts}
        kind={kind}
        onToggle={() => setOpen((o) => !o)}
        open={expanded}
        startedAt={use?.ts}
        status={status}
        statusLabel={statusLabel}
        target={cmd}
        tool={toolName}
        worker={showWorker ? group.worker : undefined}
        workerChipClass={showWorker ? chip(group.worker) : undefined}
      />

      {expanded && (
        <div className="mt-1.5 mb-2 ml-2 overflow-hidden rounded-lg border border-border/80 bg-secondary/30 shadow-2xs">
          {!sections ? (
            <div className="p-3 font-mono text-ui-xs text-muted-foreground">{t("loading")}</div>
          ) : (
            <ToolBody
              onToggleWrap={() => setWrapLines((w) => !w)}
              sections={sections}
              status={status}
              tool={toolName}
              wrapLines={wrapLines}
            />
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
  const thinkingSteps = group.steps.filter((s) => s.kind === "thinking");
  const hasThinking = thinkingSteps.length > 0;
  const onlyThinking = speak.length === 0 && hasThinking;
  // 历史推理与实时推理共用 ThinkingRow：折叠时用摘要预览，展开时再取全文。
  const thinkingText = thinkingSteps.map((s) => s.summary).filter(Boolean).join("\n\n");
  const loadThinking = React.useCallback(
    () =>
      Promise.all(
        thinkingSteps.map((s) => getDetail(s.seq).then((d) => d || s.summary).catch(() => s.summary)),
      ).then((parts) => parts.join("\n\n")),
    [thinkingSteps, getDetail],
  );
  const isError = group.steps.some((s) => s.is_error);
  const body = (speak.length ? speak : group.steps).map((s) => s.summary).join("  ") || "…";
  let Icon = MessageSquare;
  if (isError) {
    Icon = Flag;
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

  if (onlyThinking && thinkingText) {
    return (
      <ThinkingRow
        className="-ml-3"
        fetchDetail={loadThinking}
        label={t("kind.thinking")}
        text={thinkingText}
      />
    );
  }

  return (
    <div className="relative pl-3 text-xs before:absolute before:top-0 before:bottom-0 before:left-1 before:w-px before:bg-border/70">
      {hasThinking && thinkingText && (
        <ThinkingRow
          className="-ml-3 mb-1"
          fetchDetail={loadThinking}
          label={t("kind.thinking")}
          text={thinkingText}
        />
      )}
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
            !isError && "text-primary/80",
          )}
        />
        {showWorker && <span className={chip(group.worker)}>{group.worker}</span>}
        <span className={cn("min-w-0 flex-1 truncate", tone)}>{body}</span>
      </button>
      {open && (
        <div className="mt-1.5 mb-2 ml-2 overflow-hidden rounded-lg border border-border/80 bg-secondary/30 shadow-2xs">
          {!stepDetails ? (
            <div className="p-3 font-mono text-ui-xs text-muted-foreground">{t("loading")}</div>
          ) : (
            <div className="divide-y divide-border/70">
              {stepDetails.map((item, idx) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: coalesced steps are static per detailKey
                <div key={idx} className="flex flex-col">
                  <div className="flex items-center justify-between bg-muted/40 px-3 py-1 text-ui-2xs">
                    <span className="font-semibold text-muted-foreground uppercase">{item.kind}</span>
                    <CopyTextButton text={item.text} />
                  </div>
                  <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words p-2.5 font-mono text-ui-xs text-foreground/90 leading-relaxed">
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
// the compact, grouped, expand-to-detail format.
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
  const showWorker = new Set(activity.map((a) => a.worker)).size > 1;
  const getDetail = React.useCallback(
    (seq: number) => (fetchDetail ? fetchDetail(seq) : api.activityDetail(seq, taskId).then((r) => r.detail ?? "")),
    [fetchDetail, taskId],
  );
  return (
    <div className="flex flex-col gap-0.5">
      {groupSteps(activity, !!chat).map((g) => {
        if (g.type === "round") {
          return (
            <div
              key={`r${g.key}`}
              className="my-3 flex items-center gap-2.5 font-semibold text-ui-2xs text-muted-foreground uppercase tracking-wider"
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
  liveDelta,
  taskId,
  chat,
  fetchDetail,
  focusedSeq,
  rollback,
}: {
  activity: Activity[];
  live?: boolean;
  /** 尚未落库的实时增量（累计全文）：thinking / text / tool_use。 */
  liveDelta?: { kind: string; text?: string; tool?: string };
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
      {liveDelta?.kind === "thinking" && !!liveDelta.text && (
        <ThinkingRow text={liveDelta.text} label={t("kind.thinking")} live />
      )}
      {liveDelta?.kind === "text" && !!liveDelta.text && (
        <LiveStreamMarkdown text={liveDelta.text} className="min-w-0 break-words pl-3" />
      )}
      {liveDelta?.kind === "tool_use" && !!liveDelta.tool && (
        <LiveToolRow args={liveDelta.text ?? ""} tool={liveDelta.tool} />
      )}
      {(liveDelta?.kind === "tool_start" || liveDelta?.kind === "tool_input") && !!liveDelta.tool && (
        <LiveToolRow args={liveDelta.text ?? ""} tool={liveDelta.tool} />
      )}
      {liveDelta?.kind === "progress" && !!liveDelta.text && (
        <div className="flex items-start gap-1.5 pl-3 text-ui-xs text-muted-foreground">
          <span className="mt-0.5 shrink-0">
            <StatusRing status="running" />
          </span>
          {liveDelta.tool ? (
            <span className="shrink-0 font-mono opacity-80">{liveDelta.tool}</span>
          ) : null}
          <span className="min-w-0 truncate">{liveDelta.text}</span>
        </div>
      )}
      {live && !liveDelta && (
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
