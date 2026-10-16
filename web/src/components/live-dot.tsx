import { cn } from "@/lib/utils";

// LiveDot:全站"实时语义"的统一呼吸点(流量录制中 / 会话运行 / SSE 在线 / 轮次生成中)。
// 视觉与动画走 globals.css 的 pulse-soft(柔和低幅透明度呼吸);
// prefers-reduced-motion 下由 globals.css 统一退化为静态点 —— 不在每个调用点各写开关。
// 尺寸/颜色用 className 覆盖即可(如 size-1 / bg-emerald-500,tailwind-merge 负责合并)。
// 装饰性元素:语义由旁边的文案承担,故对辅助技术隐藏。
export function LiveDot({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("inline-block size-1.5 shrink-0 animate-pulse-soft rounded-full bg-blue-500", className)}
    />
  );
}
