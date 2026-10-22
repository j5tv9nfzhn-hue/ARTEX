"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { cn } from "@/lib/utils";

const RADIUS = 10;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

function formatTokens(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0";
  if (value < 1000) return String(Math.round(value));
  const k = value / 1000;
  return `${k >= 10 ? Math.round(k) : Math.round(k * 10) / 10}k`;
}

// ContextUsageRing 是输入条上的上下文容量指示：一个环 + 点击展开的小面板。
// 数据只有「本轮已用 / 模型窗口」两个数，来自 usage 活动的 metadata（后端在
// capture 时按模型窗口写入），前端不做任何估算；拿不到窗口就整块不渲染。
export function ContextUsageRing({
  used,
  size,
  className,
}: {
  used: number;
  size: number;
  className?: string;
}) {
  const t = useTranslations("transcript");
  const [open, setOpen] = React.useState(false);
  const wrapRef = React.useRef<HTMLDivElement>(null);
  const ratio = size > 0 ? Math.min(Math.max(used / size, 0), 1) : 0;
  const percent = ratio * 100;
  const tone =
    ratio >= 0.95 ? "text-red-500" : ratio >= 0.8 ? "text-amber-500" : "text-muted-foreground";

  React.useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (!wrapRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return (
    <div className={cn("relative inline-flex", className)} ref={wrapRef}>
      <button
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t("context.ariaLabel", { percent: percent.toFixed(1) })}
        className={cn(
          "inline-flex size-6 items-center justify-center rounded transition-colors hover:bg-accent",
          tone,
        )}
        onClick={() => setOpen((value) => !value)}
        type="button"
      >
        <svg aria-hidden="true" className="size-3.5" viewBox="0 0 24 24">
          <circle
            cx="12"
            cy="12"
            fill="none"
            opacity={0.25}
            r={RADIUS}
            stroke="currentColor"
            strokeWidth="4"
          />
          <circle
            cx="12"
            cy="12"
            fill="none"
            opacity={0.9}
            r={RADIUS}
            stroke="currentColor"
            strokeDasharray={`${CIRCUMFERENCE} ${CIRCUMFERENCE}`}
            strokeDashoffset={CIRCUMFERENCE * (1 - ratio)}
            strokeLinecap="round"
            strokeWidth="4"
            style={{ transform: "rotate(-90deg)", transformOrigin: "center" }}
          />
        </svg>
      </button>
      {open && (
        <div
          aria-label={t("context.title")}
          className="absolute right-0 bottom-full z-50 mb-2 w-64 rounded-xl border bg-popover p-3 text-popover-foreground shadow-md"
          role="dialog"
        >
          <div className="mb-2 flex items-baseline gap-2">
            <span className="font-medium text-xs">{t("context.title")}</span>
            <span className="ml-auto font-mono text-ui-2xs text-muted-foreground tabular-nums">
              {formatTokens(used)} / {formatTokens(size)} ({percent.toFixed(1)}%)
            </span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-muted">
            <div
              className={cn(
                "h-full rounded-full transition-[width] duration-200 motion-reduce:transition-none",
                ratio >= 0.95 ? "bg-red-500" : ratio >= 0.8 ? "bg-amber-500" : "bg-primary",
              )}
              style={{ width: `${Math.max(percent, 1)}%` }}
            />
          </div>
          <p className="mt-2 text-ui-2xs text-muted-foreground leading-relaxed">{t("context.hint")}</p>
        </div>
      )}
    </div>
  );
}
