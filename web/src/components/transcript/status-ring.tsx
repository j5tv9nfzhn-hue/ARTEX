"use client";

import type { ToolStatus } from "@/lib/tool-status";
import { cn } from "@/lib/utils";

const RADIUS = 6;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

const TONE: Record<ToolStatus, string> = {
  running: "text-sky-500",
  completed: "text-emerald-600 dark:text-emerald-400",
  failed: "text-red-500 dark:text-red-400",
  denied: "text-amber-500 dark:text-amber-400",
  stopped: "text-muted-foreground",
};

// arcRatio 决定环上留多少弧：
//   运行中 —— 没有确定进度时是 1/4 弧靠转动表达「在跑」，有进度就直接画进度；
//   拦截/停止 —— 半环（有结论，但不算完成）；
//   完成/失败 —— 整环，颜色承担区别。
function arcRatio(status: ToolStatus, progress: number | null): number {
  if (status === "running") {
    return progress === null ? 0.25 : Math.min(Math.max(progress / 100, 0), 1);
  }
  if (status === "denied" || status === "stopped") return 0.5;
  return 1;
}

// StatusRing 是工具行/思考行的行首指示器。旋转只作用在 svg 上（整环一起转，底环是同心圆，
// 视觉上等价于只转弧），走的是合成层，不触发布局；reduced-motion 下停转、只留静态形态。
export function StatusRing({
  status,
  progress = null,
  className,
}: {
  status: ToolStatus;
  progress?: number | null;
  className?: string;
}) {
  const dash = CIRCUMFERENCE * arcRatio(status, progress);
  const spinning = status === "running" && progress === null;
  return (
    <svg
      aria-hidden="true"
      className={cn(
        "size-3.5 shrink-0",
        TONE[status],
        spinning && "animate-spin motion-reduce:animate-none",
        className,
      )}
      data-status={status}
      viewBox="0 0 16 16"
    >
      <circle className="opacity-25" cx="8" cy="8" fill="none" r={RADIUS} stroke="currentColor" strokeWidth="1.5" />
      <circle
        className="transition-[stroke-dasharray] duration-200 motion-reduce:transition-none"
        cx="8"
        cy="8"
        fill="none"
        r={RADIUS}
        stroke="currentColor"
        strokeDasharray={`${dash} ${CIRCUMFERENCE - dash}`}
        strokeLinecap="round"
        strokeWidth="1.5"
        style={{ transform: "rotate(-90deg)", transformOrigin: "center" }}
      />
    </svg>
  );
}
