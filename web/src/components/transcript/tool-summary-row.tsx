"use client";

import * as React from "react";

import { ChevronDown, ChevronRight } from "lucide-react";
import { useTranslations } from "next-intl";

import { StatusRing } from "@/components/transcript/status-ring";
import type { ToolKindMeta } from "@/components/transcript/tool-kind";
import { formatElapsed, type ToolStatus } from "@/lib/tool-status";
import { cn } from "@/lib/utils";

const STATUS_TEXT_TONE: Record<ToolStatus, string> = {
  running: "text-sky-600 dark:text-sky-400",
  completed: "text-muted-foreground",
  failed: "text-red-600 dark:text-red-400",
  denied: "text-amber-600 dark:text-amber-400",
  stopped: "text-muted-foreground",
};

// useElapsed 只在行还在跑的时候走表；有结束时间就直接算差值，不再重渲染。
// 时间戳来自 activity 的 ts，因此历史会话打开时也能显示每行真实的耗时。
function useElapsed(startedAt?: string, endedAt?: string): string {
  const active = !endedAt;
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  const start = startedAt ? Date.parse(startedAt) : Number.NaN;
  if (Number.isNaN(start)) return "";
  const end = endedAt ? Date.parse(endedAt) : now;
  if (Number.isNaN(end)) return "";
  return formatElapsed(end - start);
}

// ToolSummaryRow 是工具调用行的第一层（折叠态）：状态圈 + 类别 + 工具名 + 目标 + 耗时 + 状态文案。
// 展开/收起的箭头只在 hover 或已展开时出现，让静止的行保持安静 —— 层级由状态圈和文案承担，
// 不靠常驻的装饰性控件。
export function ToolSummaryRow({
  kind,
  tool,
  target,
  worker,
  workerChipClass,
  status,
  statusLabel,
  startedAt,
  endedAt,
  open,
  onToggle,
  alert = false,
}: {
  kind: ToolKindMeta;
  tool: string;
  target?: string;
  worker?: string;
  workerChipClass?: string;
  status: ToolStatus;
  statusLabel: string;
  startedAt?: string;
  endedAt?: string;
  open: boolean;
  onToggle: () => void;
  alert?: boolean;
}) {
  const t = useTranslations("transcript");
  const elapsed = useElapsed(startedAt, endedAt);
  const running = status === "running";
  return (
    <button
      aria-expanded={open}
      className={cn(
        "group flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left transition-colors",
        alert && "border border-amber-500/35 bg-amber-500/8 hover:bg-amber-500/15",
        !alert && (open ? "bg-secondary/60 text-foreground" : "hover:bg-muted/50"),
      )}
      onClick={onToggle}
      type="button"
    >
      <StatusRing status={status} />
      <span className={cn("shrink-0 font-medium text-[10px] text-muted-foreground", running && "text-shimmer")}>
        {t(kind.labelKey)}
      </span>
      {worker ? <span className={workerChipClass}>{worker}</span> : null}
      <span className="shrink-0 font-mono font-medium text-[11.5px] text-foreground">{tool}</span>
      {target ? (
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground">{target}</span>
      ) : (
        <span className="flex-1" />
      )}
      {elapsed ? (
        <span className="shrink-0 font-mono text-[10px] text-muted-foreground tabular-nums" suppressHydrationWarning>
          {elapsed}
        </span>
      ) : null}
      <span
        className={cn("shrink-0 font-medium text-[10px]", STATUS_TEXT_TONE[status], running && "text-shimmer")}
        suppressHydrationWarning
      >
        {statusLabel}
      </span>
      <span
        className={cn(
          "shrink-0 text-muted-foreground transition-opacity",
          !open && "opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100",
        )}
      >
        {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
      </span>
    </button>
  );
}
