// 漏洞严重度(severity)的唯一事实源:
// - 阶梯:critical(深洋红 rose)→ high(亮红 red)→ medium(琥珀 amber)→ low(冷蓝 sky)→ info(中灰 zinc)。
//   旧版在多处把 critical 写成 rose-500、high 写成 red-400,在深色底上 red-400 反而比 rose-500 更亮,
//   形成"高危比严重更刺眼"的倒挂;这里让 critical 固定比 high 高半档饱和、并带轻微描边强化;
// - 亮/暗双模:文字用 text-*-700 dark:text-*-300(或 600/400),避免 text-*-400 在亮色白底上发虚;
// - Tailwind v4 按字面扫描类名,所有完整类名必须静态出现在本文件内,禁止模板字符串拼接色名。

export type Severity = "critical" | "high" | "medium" | "low" | "info";

export const SEVERITY_ORDER: readonly Severity[] = ["critical", "high", "medium", "low", "info"] as const;

interface SeveritySpec {
  // Badge / 胶囊(border + 半透明底 + 前景字,适配 variant="outline")
  badge: string;
  // 纯文字强调(仪表盘内联数字、小标题等)
  text: string;
  // 实心色块(分布条、小圆点、左边框色条)
  bar: string;
  dot: string;
  // Canvas / G6 图谱等非 DOM 场景用的十六进制主色
  hex: string;
}

const SEVERITY_MAP: Record<Severity, SeveritySpec> = {
  critical: {
    badge: "bg-rose-500/15 text-rose-700 border-rose-500/45 font-medium dark:bg-rose-500/18 dark:text-rose-300 dark:border-rose-500/45",
    text: "text-rose-600 dark:text-rose-400",
    bar: "bg-rose-500",
    dot: "bg-rose-500",
    hex: "#f43f5e",
  },
  high: {
    badge: "bg-red-500/12 text-red-700 border-red-500/35 font-medium dark:bg-red-500/15 dark:text-red-300 dark:border-red-500/40",
    text: "text-red-600 dark:text-red-400",
    bar: "bg-red-500",
    dot: "bg-red-500",
    hex: "#ef4444",
  },
  medium: {
    badge: "bg-amber-500/12 text-amber-700 border-amber-500/35 font-medium dark:bg-amber-500/15 dark:text-amber-300 dark:border-amber-500/40",
    text: "text-amber-600 dark:text-amber-400",
    bar: "bg-amber-500",
    dot: "bg-amber-500",
    hex: "#f59e0b",
  },
  low: {
    badge: "bg-sky-500/12 text-sky-700 border-sky-500/35 font-medium dark:bg-sky-500/15 dark:text-sky-300 dark:border-sky-500/40",
    text: "text-sky-600 dark:text-sky-400",
    bar: "bg-sky-500",
    dot: "bg-sky-500",
    hex: "#0ea5e9",
  },
  info: {
    badge: "bg-zinc-500/12 text-zinc-700 border-zinc-500/30 font-medium dark:bg-zinc-500/15 dark:text-zinc-300 dark:border-zinc-500/35",
    text: "text-zinc-600 dark:text-zinc-400",
    bar: "bg-zinc-400",
    dot: "bg-zinc-400",
    hex: "#71717a",
  },
};

const FALLBACK: SeveritySpec = SEVERITY_MAP.info;

export function normalizeSeverity(s?: string | null): Severity {
  const k = (s ?? "").toLowerCase();
  if (k === "critical" || k === "high" || k === "medium" || k === "low" || k === "info") return k;
  return "info";
}

export function severitySpec(s?: string | null): SeveritySpec {
  return SEVERITY_MAP[normalizeSeverity(s)] ?? FALLBACK;
}

export function severityBadgeClass(s?: string | null): string {
  return severitySpec(s).badge;
}

export function severityTextClass(s?: string | null): string {
  return severitySpec(s).text;
}

export function severityBarClass(s?: string | null): string {
  return severitySpec(s).bar;
}

export function severityDotClass(s?: string | null): string {
  return severitySpec(s).dot;
}

export function severityHex(s?: string | null): string {
  return severitySpec(s).hex;
}
