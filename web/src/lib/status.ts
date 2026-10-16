// Centralised status → color semantics, reused across the whole app.
// Spec §8.3: 意图 / 覆盖 / 任务 / 严重度 each have a consistent color set.
// User-facing labels live in the "status" message namespace (ko/zh); the maps
// here only carry the color tone, keyed by the same status value.

import { severityBadgeClass, severityDotClass } from "@/lib/severity";

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  tone: Tone;
  // severity 域用完整 class 覆盖 tone 推导(唯一事实源见 lib/severity.ts);
  // 其余域不设置,继续走 toneClasses/toneDot。
  badgeClass?: string;
  dotClass?: string;
}

const intent: Record<string, StatusMeta> = {
  open: { tone: "slate" },
  running: { tone: "blue" },
  paused: { tone: "amber" },
  done: { tone: "green" },
  // blocked = 模型/API/网络故障重试用尽，这条意图基本没真正探成（非目标拦截）。
  blocked: { tone: "red" },
  // exhausted = 达到步数/时间预算被中途掐断、只写回部分结果（非方向已探尽）。
  exhausted: { tone: "violet" },
  // stopped = 历史软删除状态（保留,历史数据）。
  stopped: { tone: "slate" },
  // deleted = 用户假删除了该意图（保留节点与血缘，删除原因见 delete_reason 字段）。
  deleted: { tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { tone: "slate" },
  queued: { tone: "amber" },
  running: { tone: "blue" },
  paused: { tone: "amber" },
  done: { tone: "green" },
  failed: { tone: "red" },
  timeout: { tone: "amber" },
};

// severity:色板与药丸样式来自 lib/severity.ts(全站唯一事实源,修正旧版
// critical=rose / high=red 的亮度倒挂);tone 仅为兜底参考值,StatusBadge
// 渲染实际使用 badgeClass/dotClass。
const severity: Record<string, StatusMeta> = {
  critical: {
    tone: "red",
    badgeClass: severityBadgeClass("critical"),
    dotClass: severityDotClass("critical"),
  },
  high: {
    tone: "amber",
    badgeClass: severityBadgeClass("high"),
    dotClass: severityDotClass("high"),
  },
  medium: {
    tone: "amber",
    badgeClass: severityBadgeClass("medium"),
    dotClass: severityDotClass("medium"),
  },
  low: {
    tone: "slate",
    badgeClass: severityBadgeClass("low"),
    dotClass: severityDotClass("low"),
  },
};

const finding: Record<string, StatusMeta> = {
  pending: { tone: "amber" },
  in_progress: { tone: "blue" },
  confirmed: { tone: "red" },
  resolved: { tone: "green" },
  fixed: { tone: "green" },
  false_positive: { tone: "slate" },
  ignored: { tone: "neutral" },
  duplicate: { tone: "neutral" },
  risk_accepted: { tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { tone: "blue" },
  paused: { tone: "amber" },
  stalled: { tone: "red" },
  idle: { tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { tone: "blue" },
  met: { tone: "green" },
  abandoned: { tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { tone: "green" },
  block: { tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { tone: "slate" },
  confirmed: { tone: "green" },
  tombstoned: { tone: "neutral" },
};

// 推送投递状态。sending 用 blue 而不是 amber：它不是「有问题」，
// 而是「已被领取、正在发」，与 pending 的等待语义要能区分开。
const delivery: Record<string, StatusMeta> = {
  pending: { tone: "amber" },
  sending: { tone: "blue" },
  sent: { tone: "green" },
  failed: { tone: "red" },
  skipped: { tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { tone: "neutral" };
}
