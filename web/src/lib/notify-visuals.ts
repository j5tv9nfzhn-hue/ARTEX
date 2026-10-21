// 站内通知的视觉口径：图标、色条、类型签配色。
//
// 与 notify-event.ts（纯数据）分开，是因为这里要 import lucide 图标与全站
// severity 色板——把它塞进纯模块会让那份文件无法被 node 直接测。

import { AlertTriangle, CheckCircle2, Info, XCircle } from "lucide-react";

import type { NotifyEvent } from "./notify-event";
import { isSeverity, severityMeta } from "./severity";

export function levelIcon(level: NotifyEvent["level"]) {
  switch (level) {
    case "success":
      return CheckCircle2;
    case "error":
      return XCircle;
    case "warn":
      return AlertTriangle;
    default:
      return Info;
  }
}

/** 卡片左侧色条：漏洞事件优先按 severity 上色（与全站色彩契约一致），其余按 level。 */
export function accentClass(ev: NotifyEvent): string {
  if (ev.kind === "finding" && ev.severity && isSeverity(ev.severity)) {
    return severityMeta(ev.severity).bar;
  }
  switch (ev.level) {
    case "success":
      return "bg-emerald-500 dark:bg-emerald-400";
    case "error":
      return "bg-red-600 dark:bg-red-400";
    case "warn":
      return "bg-amber-500 dark:bg-amber-400";
    default:
      return "bg-sky-500 dark:bg-sky-400";
  }
}

export function iconTone(ev: NotifyEvent): string {
  if (ev.kind === "finding" && ev.severity && isSeverity(ev.severity)) {
    return severityMeta(ev.severity).text;
  }
  switch (ev.level) {
    case "success":
      return "text-emerald-600 dark:text-emerald-400";
    case "error":
      return "text-red-600 dark:text-red-400";
    case "warn":
      return "text-amber-600 dark:text-amber-400";
    default:
      return "text-muted-foreground";
  }
}

/** 类型签配色。前缀与 server/ui_notify.go 的 Kind 约定一一对应。 */
export function kindTone(kind: string): string {
  if (kind === "finding") {
    return "bg-red-600/10 text-red-700 dark:bg-red-400/10 dark:text-red-400";
  }
  if (kind === "approval_pending") {
    return "bg-orange-600/10 text-orange-700 dark:bg-orange-400/10 dark:text-orange-400";
  }
  if (kind.startsWith("task_")) {
    return "bg-sky-600/10 text-sky-700 dark:bg-sky-400/10 dark:text-sky-400";
  }
  if (kind.startsWith("subagent_")) {
    return "bg-violet-600/10 text-violet-700 dark:bg-violet-400/10 dark:text-violet-400";
  }
  if (kind.startsWith("chat_")) {
    return "bg-teal-600/10 text-teal-700 dark:bg-teal-400/10 dark:text-teal-400";
  }
  return "bg-muted text-muted-foreground";
}
