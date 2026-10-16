"use client";

import * as React from "react";

import { cn } from "@/lib/utils";

// ── 动效助手(主题重塑第 8 项)────────────────────────────────────────────────
// 实现刻意保持极简:只包一层带 .animate-fade-up 的 div,延迟用 style.animation-delay
// (毫秒)表达,零依赖、零全局上下文。动画本体、令牌与 reduced-motion 降级都在
// globals.css(.animate-fade-up / .animate-scale-in / .animate-pulse-soft / .animate-glow-once)。
//
// 使用边界(避免动效悄悄改变布局):
//  · 包一层 div 会让"包装 div"替代原元素成为父级(grid / flex / divide-y)的直接子项。
//    对靠拉伸对齐的卡片网格、或被 divide-y 分隔的行,适合用本组件(布局类名透传给
//    包装层即可);对 <tr> 等表格结构则不能插 div —— 给原元素直接加
//    className="animate-fade-up" + 内联 animationDelay 更稳(见任务列表的接线注释)。
//  · 分页/筛选导致的批量重渲染不要重复挂动画:用 useStaggerOnce 控制"仅首次进场"。

export interface MotionProps {
  /** 进场延迟(毫秒)。列表项由调用方按 index * step 计算;组件不做递增推算。 */
  delay?: number;
  className?: string;
  children: React.ReactNode;
}

/** 单元素进场:延迟由调用方给出(省略 = 立即进场)。 */
export function FadeIn({ delay = 0, className, children }: MotionProps) {
  return (
    <div
      className={cn("animate-fade-up", className)}
      style={delay > 0 ? { animationDelay: `${delay}ms` } : undefined}
    >
      {children}
    </div>
  );
}

// 列表项进场的延迟封顶:排到 ~0.45s 之后,观感就从"依次落座"退化成"等待",
// 与"克制"的基调相悖;超出部分与最后一项同时进场。
const STAGGER_MAX_DELAY_MS = 450;

/** 列表项进场:按调用方给的 delay 排队并自动封顶,配合 useStaggerOnce 使用。 */
export function Stagger({ delay = 0, className, children }: MotionProps) {
  return (
    <div
      className={cn("animate-fade-up", className)}
      style={{ animationDelay: `${Math.min(Math.max(delay, 0), STAGGER_MAX_DELAY_MS)}ms` }}
    >
      {children}
    </div>
  );
}

/**
 * useStaggerOnce:列表进场只播一次的窗口。
 * 为什么需要:分页/筛选会把整批行换成新节点,若每批都挂动画就会反复闪动。
 * 语义:ready 首次变为 true 时开窗(1.2s ≈ 450ms 最大延迟 + 320ms 动画 + 余量),
 * 到点关窗后不再返回 true —— 之后再挂载的新节点(翻页、改筛选)保持静态。
 * 因此调用方把 ready 传"首批数据已就绪"(如列表 loaded/非空),行本身仍按原样渲染。
 */
export function useStaggerOnce(ready: boolean, windowMs = 1200): boolean {
  const [open, setOpen] = React.useState(false);
  const fired = React.useRef(false);
  React.useEffect(() => {
    if (!ready || fired.current) return;
    fired.current = true;
    setOpen(true);
    const timer = window.setTimeout(() => setOpen(false), windowMs);
    return () => window.clearTimeout(timer);
  }, [ready, windowMs]);
  return open;
}
