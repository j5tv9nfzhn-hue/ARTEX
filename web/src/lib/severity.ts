// 全站「严重度」色彩契约的唯一事实源（Tactical Security Telemetry 语义状态色）。
//
// 为什么单独成文件：severity 需要的不只是 tone——正文色、药丸（背景 tint + 边框 +
// 文字）、圆点、条形填充、画布 hex（亮/暗两套）都要从这里取；lib/status.ts 是通用的
// 「状态 → tone」注册表，severity 域直接引用本模块的完整 class（见 statusMeta），
// 不再经 tone 间接推导，避免各页面各写一套色。
//
// 所有 class 都是完整静态字符串（不做模板拼接），保证 Tailwind 能扫描到；
// 浅色底用深字、暗色底用浅字，两套组合的文字对比度都在 4.5:1 以上。

import type { Severity } from "@/lib/types";

export type { Severity };

// 展示顺序：由重到轻（findings 筛选 chip、统计卡、编辑下拉统一用它）。
export const SEVERITIES: readonly Severity[] = ["critical", "high", "medium", "low"] as const;

// 聚合/排序权重（越大越重），与展示顺序一致。
export const SEVERITY_RANK: Record<Severity, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
};

export interface SeverityMeta {
  /** 正文/数字的强调色（亮色深字 + dark: 浅字） */
  text: string;
  /** 药丸：背景 tint + 边框 + 文字（StatusBadge 等） */
  badge: string;
  /** 自带容器样式的柔色 chip：仅背景 tint + 文字，无边框（播报板类型签等） */
  softChip: string;
  /** 圆点：独立色点（StatusBadge dot / 时间线） */
  dot: string;
  /** 进度条 / 条形填充 */
  bar: string;
  /** 画布（SVG / 力导向图等）用十六进制色，亮/暗两套 */
  hex: { light: string; dark: string };
}

export const SEVERITY_META: Record<Severity, SeverityMeta> = {
  critical: {
    text: "text-rose-600 dark:text-rose-400",
    badge:
      "bg-rose-500/15 text-rose-700 border-rose-500/45 font-medium dark:bg-rose-500/18 dark:text-rose-300 dark:border-rose-500/45",
    softChip: "bg-rose-500/15 text-rose-700 dark:bg-rose-500/20 dark:text-rose-300",
    dot: "bg-rose-500 dark:bg-rose-400 shadow-[0_0_8px_rgba(244,63,94,0.65)]",
    bar: "bg-rose-500 dark:bg-rose-400",
    hex: { light: "#E11D48", dark: "#FB7185" },
  },
  high: {
    text: "text-red-600 dark:text-red-400",
    badge:
      "bg-red-500/12 text-red-700 border-red-500/35 font-medium dark:bg-red-500/15 dark:text-red-300 dark:border-red-500/40",
    softChip: "bg-red-500/12 text-red-700 dark:bg-red-500/18 dark:text-red-300",
    dot: "bg-red-500 dark:bg-red-400",
    bar: "bg-red-500 dark:bg-red-400",
    hex: { light: "#DC2626", dark: "#F87171" },
  },
  medium: {
    text: "text-amber-600 dark:text-amber-400",
    badge:
      "bg-amber-500/12 text-amber-700 border-amber-500/35 font-medium dark:bg-amber-500/15 dark:text-amber-300 dark:border-amber-500/40",
    softChip: "bg-amber-500/12 text-amber-700 dark:bg-amber-500/18 dark:text-amber-300",
    dot: "bg-amber-500 dark:bg-amber-400",
    bar: "bg-amber-500 dark:bg-amber-400",
    hex: { light: "#D97706", dark: "#FBBF24" },
  },
  low: {
    text: "text-sky-600 dark:text-sky-400",
    badge:
      "bg-sky-500/12 text-sky-700 border-sky-500/35 font-medium dark:bg-sky-500/15 dark:text-sky-300 dark:border-sky-500/40",
    softChip: "bg-sky-500/12 text-sky-700 dark:bg-sky-500/18 dark:text-sky-300",
    dot: "bg-sky-500 dark:bg-sky-400",
    bar: "bg-sky-500 dark:bg-sky-400",
    hex: { light: "#0284C7", dark: "#38BDF8" },
  },
};

// 筛选 chip（ToggleGroupItem）的选中态：同一套色调，带 data-[state=on] 前缀。
// 逐条写出完整 class 而不是拼字符串前缀，保证 Tailwind 静态扫描。
const FILTER_CHIP_ON: Record<Severity, string> = {
  critical:
    "data-[state=on]:border-rose-500/45 data-[state=on]:bg-rose-500/15 data-[state=on]:text-rose-700 dark:data-[state=on]:border-rose-400/45 dark:data-[state=on]:bg-rose-500/20 dark:data-[state=on]:text-rose-300",
  high: "data-[state=on]:border-red-500/40 data-[state=on]:bg-red-500/12 data-[state=on]:text-red-700 dark:data-[state=on]:border-red-400/40 dark:data-[state=on]:bg-red-500/18 dark:data-[state=on]:text-red-300",
  medium:
    "data-[state=on]:border-amber-500/40 data-[state=on]:bg-amber-500/12 data-[state=on]:text-amber-700 dark:data-[state=on]:border-amber-400/40 dark:data-[state=on]:bg-amber-500/18 dark:data-[state=on]:text-amber-300",
  low: "data-[state=on]:border-sky-500/40 data-[state=on]:bg-sky-500/12 data-[state=on]:text-sky-700 dark:data-[state=on]:border-sky-400/40 dark:data-[state=on]:bg-sky-500/18 dark:data-[state=on]:text-sky-300",
};

// isSeverity 用于运行时字符串（后端返回的 severity 是 string）判定。
export function isSeverity(value: string): value is Severity {
  return (SEVERITIES as readonly string[]).includes(value);
}

export function severityMeta(value: Severity | string): SeverityMeta {
  const key = (value ?? "").toLowerCase();
  return isSeverity(key) ? SEVERITY_META[key] : SEVERITY_META.low;
}

export function severityTextClass(value: Severity | string): string {
  return severityMeta(value).text;
}

export function severityBadgeClass(value: Severity | string): string {
  return severityMeta(value).badge;
}

export function severitySoftChipClass(value: Severity | string): string {
  return severityMeta(value).softChip;
}

export function severityDotClass(value: Severity | string): string {
  return severityMeta(value).dot;
}

export function severityBarClass(value: Severity | string): string {
  return severityMeta(value).bar;
}

/** 画布（SVG / Canvas）用 hex；mode 缺省 light。 */
export function severityHex(value: Severity | string, mode: "light" | "dark" = "light"): string {
  return severityMeta(value).hex[mode];
}

/** 筛选 chip 选中态 class（配 ToggleGroupItem 使用）。 */
export function severityFilterChipClass(value: Severity): string {
  return FILTER_CHIP_ON[value];
}
