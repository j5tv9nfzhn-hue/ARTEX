"use client";

import { useTranslations } from "next-intl";

import { LiveDot } from "@/components/live-dot";
import type { AgentPhase } from "@/lib/agent-phase";
import { cn } from "@/lib/utils";

// AgentPhaseBadge 是任务页与对话页共用的状态标签：文案只来自统一状态机
// （deriveAgentPhase），不再各页各写一套。之前两边一个写死“实时”、一个写死
// “SSE 实时”，状态变化靠各自的块去碰，才会出现推理/正文来回跳（Memo 01、02）。
export function AgentPhaseBadge({ phase, className }: { phase: AgentPhase; className?: string }) {
  const t = useTranslations("transcript");
  const text: Record<AgentPhase, string> = {
    idle: t("streaming"),
    thinking: t("phase.thinking"),
    writing: t("phase.writing"),
    tool: t("phase.tool"),
    waiting: t("phase.waiting"),
    error: t("phase.error"),
  };
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 font-medium text-[10px] text-blue-600 dark:text-blue-400",
        className,
      )}
    >
      <LiveDot className="size-1" />
      {text[phase]}
    </span>
  );
}
