// 全站「严重度」色彩契约的唯一事实源(语义状态色)。
//
// 为什么单独成文件:severity 需要的不只是 tone——正文色、药丸(背景 tint + 边框 +
// 文字)、圆点、条形填充、画布 hex(亮/暗两套)都要从这里取;lib/status.ts 是通用的
// 「状态 → tone」注册表,severity 域直接引用本模块的完整 class(见 statusMeta),
// 不再经 tone 间接推导,避免各页面各写一套色。
//
// 色序遵循安全领域惯例(修正旧版 critical=rose / high=red 的亮度倒挂):
//   critical 红 → high 橙 → medium 琥珀 → low 灰蓝。
// 与图表系列色(chart token,var(--chart-*))无关:severity = 语义状态,chart = 数据系列。
// 当前数据模型的 Severity 只有 4 级、没有 info;若未来新增 info 级,按契约用青色
// (亮 #0891B2 / 暗 #22D3EE)。
//
// 所有 class 都是完整静态字符串(不做模板拼接),保证 Tailwind 能扫描到;
// 浅色底用深字、暗色底用浅字,两套组合的文字对比度都在 4.5:1 以上。

import type { Severity } from "@/lib/types";

export type { Severity };

// 展示顺序:由重到轻(findings 筛选 chip、统计卡、编辑下拉统一用它)。
export const SEVERITIES: readonly Severity[] = ["critical", "high", "medium", "low"] as const;

// 聚合/排序权重(越大越重),与展示顺序一致。
export const SEVERITY_RANK: Record<Severity, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
};

export interface SeverityMeta {
  /** 正文/数字的强调色(亮色深字 + dark: 浅字) */
  text: string;
  /** 药丸:背景 tint + 边框 + 文字(StatusBadge 等) */
  badge: string;
  /** 自带容器样式的柔色 chip:仅背景 tint + 文字,无边框(播报板类型签等) */
  softChip: string;
  /** 圆点:独立色点(StatusBadge dot / 时间线) */
  dot: string;
  /** 进度条 / 条形填充 */
  bar: string;
  /** 画布(SVG / 力导向图等)用十六进制色,亮/暗两套 */
  hex: { light: string; dark: string };
}

export const SEVERITY_META: Record<Severity, SeverityMeta> = {
  critical: {
    text: "text-red-600 dark:text-red-400",
    badge:
      "bg-red-600/10 text-red-700 border-red-600/20 dark:bg-red-400/10 dark:text-red-400 dark:border-red-400/20",
    softChip: "bg-red-600/10 text-red-700 dark:bg-red-400/10 dark:text-red-400",
    dot: "bg-red-600 dark:bg-red-400",
    bar: "bg-red-600 dark:bg-red-400",
    hex: { light: "#DC2626", dark: "#F87171" },
  },
  high: {
    text: "text-orange-700 dark:text-orange-400",
    badge:
      "bg-orange-500/10 text-orange-700 border-orange-500/20 dark:bg-orange-400/10 dark:text-orange-400 dark:border-orange-400/20",
    softChip: "bg-orange-500/10 text-orange-700 dark:bg-orange-400/10 dark:text-orange-400",
    dot: "bg-orange-500 dark:bg-orange-400",
    bar: "bg-orange-500 dark:bg-orange-400",
    hex: { light: "#F97316", dark: "#FB923C" },
  },
  medium: {
    text: "text-amber-700 dark:text-amber-400",
    badge:
      "bg-amber-500/10 text-amber-700 border-amber-500/20 dark:bg-amber-400/10 dark:text-amber-400 dark:border-amber-400/20",
    softChip: "bg-amber-500/10 text-amber-700 dark:bg-amber-400/10 dark:text-amber-400",
    dot: "bg-amber-500 dark:bg-amber-400",
    bar: "bg-amber-500 dark:bg-amber-400",
    hex: { light: "#F59E0B", dark: "#FBBF24" },
  },
  low: {
    text: "text-slate-600 dark:text-slate-400",
    badge:
      "bg-slate-500/10 text-slate-600 border-slate-500/20 dark:bg-slate-400/10 dark:text-slate-400 dark:border-slate-400/20",
    softChip: "bg-slate-500/10 text-slate-600 dark:bg-slate-400/10 dark:text-slate-400",
    dot: "bg-slate-500 dark:bg-slate-400",
    bar: "bg-slate-500 dark:bg-slate-400",
    hex: { light: "#64748B", dark: "#94A3B8" },
  },
};

// 筛选 chip(ToggleGroupItem)的选中态:同一套色调,带 data-[state=on] 前缀。
// 逐条写出完整 class 而不是拼字符串前缀,保证 Tailwind 静态扫描。
const FILTER_CHIP_ON: Record<Severity, string> = {
  critical:
    "data-[state=on]:border-red-600/40 data-[state=on]:bg-red-600/10 data-[state=on]:text-red-700 dark:data-[state=on]:border-red-400/40 dark:data-[state=on]:bg-red-400/10 dark:data-[state=on]:text-red-400",
  high:
    "data-[state=on]:border-orange-500/40 data-[state=on]:bg-orange-500/10 data-[state=on]:text-orange-700 dark:data-[state=on]:border-orange-400/40 dark:data-[state=on]:bg-orange-400/10 dark:data-[state=on]:text-orange-400",
  medium:
    "data-[state=on]:border-amber-500/40 data-[state=on]:bg-amber-500/10 data-[state=on]:text-amber-700 dark:data-[state=on]:border-amber-400/40 dark:data-[state=on]:bg-amber-400/10 dark:data-[state=on]:text-amber-400",
  low:
    "data-[state=on]:border-slate-500/40 data-[state=on]:bg-slate-500/10 data-[state=on]:text-slate-600 dark:data-[state=on]:border-slate-400/40 dark:data-[state=on]:bg-slate-400/10 dark:data-[state=on]:text-slate-400",
};

// isSeverity 用于运行时字符串(后端返回的 severity 是 string)判定。
export function isSeverity(value: string): value is Severity {
  return (SEVERITIES as readonly string[]).includes(value);
}

export function severityMeta(value: Severity): SeverityMeta {
  return SEVERITY_META[value];
}

export function severityTextClass(value: Severity): string {
  return SEVERITY_META[value].text;
}

export function severityBadgeClass(value: Severity): string {
  return SEVERITY_META[value].badge;
}

export function severitySoftChipClass(value: Severity): string {
  return SEVERITY_META[value].softChip;
}

export function severityDotClass(value: Severity): string {
  return SEVERITY_META[value].dot;
}

export function severityBarClass(value: Severity): string {
  return SEVERITY_META[value].bar;
}

/** 画布(SVG / Canvas)用 hex;mode 缺省 light。 */
export function severityHex(value: Severity, mode: "light" | "dark" = "light"): string {
  return SEVERITY_META[value].hex[mode];
}

/** 筛选 chip 选中态 class(配 ToggleGroupItem 使用)。 */
export function severityFilterChipClass(value: Severity): string {
  return FILTER_CHIP_ON[value];
}
