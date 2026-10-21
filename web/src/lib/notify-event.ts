// 站内通知的「事件形状 + 展示派生」层。
//
// 这些函数原先内嵌在 components/ui-notifier.tsx 里，只有那一个组件用。改造后
// 卡片宿主（ui-notifier）与「通知推送」页的站内通知列表都要用同一套口径（类型签、
// 时间分组、筛选），所以抽出来各取所需，避免两边各写一份、慢慢跑偏。
//
// 本模块不引用 React，也不 import 任何 @/ 别名——它只做数据换算。

import { triggerOf } from "./notify-prefs";

export interface NotifyEvent {
  id: number;
  ts: number;
  kind: string;
  level: "success" | "info" | "warn" | "error";
  severity?: string;
  task_id?: string;
  task_title?: string;
  title: string;
  body?: string;
  href?: string;
}

/** 屏幕上同时最多几张卡片。 */
export const MAX_STACK = 4;
/** 会话内保留多少条历史（不落库，刷新即清空）。 */
export const MAX_KEPT = 40;

export type FilterKey = "all" | "urgent" | "warn" | "info" | "finding";
export const FILTERS: readonly FilterKey[] = ["all", "urgent", "warn", "info", "finding"];

/**
 * 是否属于「值得打扰用户」的事件。判定实现收在 notify-prefs 的 triggerOf 里，
 * 这里只做导出，保证设置面板与弹窗对「哪些事件会弹」绝不会有两套口径。
 */
export function isNotifiable(ev: NotifyEvent): boolean {
  return triggerOf(ev) !== null;
}

export function matchesFilter(ev: NotifyEvent, f: FilterKey): boolean {
  switch (f) {
    case "urgent":
      return ev.level === "error" || ev.severity === "critical" || ev.severity === "high";
    case "warn":
      return ev.level === "warn";
    case "info":
      return ev.level === "info" || ev.level === "success";
    case "finding":
      return ev.kind === "finding";
    default:
      return true;
  }
}

export type DayBucket = "today" | "yesterday" | "earlier";
export const BUCKET_ORDER: readonly DayBucket[] = ["today", "yesterday", "earlier"];

const MINUTE = 60000;
const HOUR = 3600000;
const DAY = 86400000;

export function dayBucket(ts: number, now: number): DayBucket {
  const n = new Date(now);
  const startOfToday = new Date(n.getFullYear(), n.getMonth(), n.getDate()).getTime();
  if (ts >= startOfToday) return "today";
  if (ts >= startOfToday - DAY) return "yesterday";
  return "earlier";
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** 24 小时制 HH:MM。 */
export function clockTime(ts: number): string {
  const d = new Date(ts);
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

export type RelUnit = "justNow" | "minutes" | "hours" | "days";

/** 「多久之前」只算数字与单位，文案交给 i18n（relJustNow / relMinutes 等）。 */
export function relBucket(ts: number, now: number): { unit: RelUnit; count: number } {
  const diff = Math.max(0, now - ts);
  if (diff < MINUTE) return { unit: "justNow", count: 0 };
  if (diff < HOUR) return { unit: "minutes", count: Math.floor(diff / MINUTE) };
  if (diff < DAY) return { unit: "hours", count: Math.floor(diff / HOUR) };
  return { unit: "days", count: Math.floor(diff / DAY) };
}

export type KindTextKey = "kindFinding" | "kindTask" | "kindSubagent" | "kindChat" | "kindApproval" | "kindOther";

/** 事件类型签 → i18n key（notifyUi 命名空间下）。 */
export function kindTextKey(kind: string): KindTextKey {
  if (kind === "finding") return "kindFinding";
  if (kind === "approval_pending") return "kindApproval";
  if (kind.startsWith("task_")) return "kindTask";
  if (kind.startsWith("subagent_")) return "kindSubagent";
  if (kind.startsWith("chat_")) return "kindChat";
  return "kindOther";
}
