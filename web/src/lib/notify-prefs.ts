// 站内通知「显示偏好」的唯一事实源。
//
// 这个模块刻意保持**零依赖**：不引用 React、不碰 localStorage、不 import 任何 @/
// 别名路径。原因有两个——它要能被 `node --experimental-strip-types --test` 直接
// 单元测试（见 notify-prefs.test.mjs），以及它要在服务端渲染阶段被 import（此时
// 没有 window）。读写浏览器存储的胶水代码在 hooks/use-notify-prefs.ts。
//
// 为什么要有这个文件：同一组口径同时被两处消费——常挂的 <UINotifier>（弹卡片、
// 响提示音）与侧边栏「通知推送」页的设置表单。以前 TTL、哪些事件该弹、声音开关
// 散落在组件里的常量和一个 localStorage 键上，用户改不了；现在集中到一处，
// 表单改完 <UINotifier> 立刻跟着变。

/** 提示音策略：全部事件响、只在严重事件响、不响。 */
export type NotifySoundMode = "off" | "urgent" | "all";

/**
 * 卡片落点。默认 top-right 是这次整改的关键：老的常驻铃铛固定在 right-4 bottom-4，
 * 会压住页面右下角的「创建」类按钮。
 */
export type NotifyDock = "top-right" | "top-center" | "bottom-right";

/** 单条卡片的存活时长：按事件级别自动，或统一固定秒数。 */
export type NotifyTtlMode = "auto" | "fixed";

/** 会触发卡片/提示音的事件类别。过程态（任务开始、子代理结算、AI 回复结束）不在此列。 */
export type NotifyTriggerKey = "finding" | "approval_pending" | "error" | "task_done" | "task_timeout";

export const NOTIFY_TRIGGERS: readonly NotifyTriggerKey[] = [
  "finding",
  "approval_pending",
  "error",
  "task_done",
  "task_timeout",
];

export interface NotifyPrefs {
  /** 收到事件后延迟多少秒才弹卡片/响铃；0 = 立即。 */
  delaySec: number;
  dock: NotifyDock;
  sound: NotifySoundMode;
  triggers: Record<NotifyTriggerKey, boolean>;
  ttlMode: NotifyTtlMode;
  /** ttlMode === "fixed" 时生效的秒数。 */
  ttlSec: number;
}

export const DELAY_MAX_SEC = 120;
export const TTL_MIN_SEC = 3;
export const TTL_MAX_SEC = 120;

// 默认值 = 改造前的实际行为（立即弹、按级别 TTL、有声），只是落点从右下角
// 挪到了右上角，不再压住页面按钮。
export const DEFAULT_NOTIFY_PREFS: NotifyPrefs = {
  delaySec: 0,
  dock: "top-right",
  sound: "all",
  triggers: {
    finding: true,
    approval_pending: true,
    error: true,
    task_done: true,
    task_timeout: true,
  },
  ttlMode: "auto",
  ttlSec: 8,
};

function toInt(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) return Math.trunc(value);
  if (typeof value === "string" && value.trim() !== "") {
    const n = Number(value);
    if (Number.isFinite(n)) return Math.trunc(n);
  }
  return null;
}

/** 延迟秒数收敛到 0..DELAY_MAX_SEC；非法值回落到默认值（不是 0，避免静默改变语义）。 */
export function clampDelaySec(value: unknown): number {
  const n = toInt(value);
  if (n === null) return DEFAULT_NOTIFY_PREFS.delaySec;
  return Math.min(DELAY_MAX_SEC, Math.max(0, n));
}

/** 单条显示秒数收敛到 TTL_MIN_SEC..TTL_MAX_SEC；非法值回落到默认值。 */
export function clampTtlSec(value: unknown): number {
  const n = toInt(value);
  if (n === null) return DEFAULT_NOTIFY_PREFS.ttlSec;
  return Math.min(TTL_MAX_SEC, Math.max(TTL_MIN_SEC, n));
}

function pickEnum<T extends string>(value: unknown, allowed: readonly T[], fallback: T): T {
  return typeof value === "string" && (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

const SOUND_MODES: readonly NotifySoundMode[] = ["off", "urgent", "all"];
const DOCKS: readonly NotifyDock[] = ["top-right", "top-center", "bottom-right"];
const TTL_MODES: readonly NotifyTtlMode[] = ["auto", "fixed"];

/**
 * 从 localStorage 的原始字符串解析偏好。
 *
 * 容错原则：任何一处坏掉都只回落那一个字段，不整条丢弃——用户手改过存储、
 * 或将来新增了字段，都不该让设置面板变成默认值。
 */
export function parseNotifyPrefs(raw: string | null | undefined): NotifyPrefs {
  const base = DEFAULT_NOTIFY_PREFS;
  if (typeof raw !== "string" || raw.trim() === "") {
    return { ...base, triggers: { ...base.triggers } };
  }
  let obj: unknown;
  try {
    obj = JSON.parse(raw);
  } catch {
    return { ...base, triggers: { ...base.triggers } };
  }
  if (typeof obj !== "object" || obj === null) {
    return { ...base, triggers: { ...base.triggers } };
  }
  const src = obj as Record<string, unknown>;
  const rawTriggers = (typeof src.triggers === "object" && src.triggers !== null ? src.triggers : {}) as Record<
    string,
    unknown
  >;
  const triggers = { ...base.triggers };
  for (const key of NOTIFY_TRIGGERS) {
    const v = rawTriggers[key];
    if (typeof v === "boolean") triggers[key] = v;
    else if (v === "1" || v === 1) triggers[key] = true;
    else if (v === "0" || v === 0) triggers[key] = false;
  }
  return {
    delaySec: clampDelaySec(src.delaySec ?? base.delaySec),
    dock: pickEnum(src.dock, DOCKS, base.dock),
    sound: pickEnum(src.sound, SOUND_MODES, base.sound),
    triggers,
    ttlMode: pickEnum(src.ttlMode, TTL_MODES, base.ttlMode),
    ttlSec: clampTtlSec(src.ttlSec ?? base.ttlSec),
  };
}

/** 序列化前先规范化，保证存进 localStorage 的一定是合法形状。 */
export function serializeNotifyPrefs(prefs: NotifyPrefs): string {
  return JSON.stringify(parseNotifyPrefs(JSON.stringify(prefs)));
}

/** 只描述判断所需的最小事件形状，避免与 NotifyEvent 互相 import。 */
export interface NotifyEventLike {
  readonly kind: string;
  readonly level: string;
  readonly severity?: string;
}

/**
 * 事件 → 触发键；不属于「需要人处理」的过程态返回 null。
 *
 * 判断顺序有据：finding / approval_pending 是明确的任务产出与阻塞点；task_done
 * 与 task_timeout 是终态；error 兜底（子代理步数耗尽、AI 回复失败这类 level 才是
 * 唯一线索）。任务开始、子代理结算、AI 回复结束都属于过程态，只进列表不打扰。
 */
export function triggerOf(ev: NotifyEventLike): NotifyTriggerKey | null {
  if (ev.kind === "finding") return "finding";
  if (ev.kind === "approval_pending") return "approval_pending";
  if (ev.kind === "task_done") return "task_done";
  if (ev.kind === "task_timeout") return "task_timeout";
  if (ev.level === "error") return "error";
  return null;
}

/** 「必须打断」：error 级，或高危/严重漏洞。声音的「仅严重」档与红色色条共用此口径。 */
export function isUrgent(ev: NotifyEventLike): boolean {
  return ev.level === "error" || ev.severity === "critical" || ev.severity === "high";
}

/** 该事件是否应该弹卡片：本身可触发，且用户没有在设置里关掉这一类。 */
export function shouldPopup(prefs: NotifyPrefs, ev: NotifyEventLike): boolean {
  const key = triggerOf(ev);
  if (key === null) return false;
  return prefs.triggers[key] !== false;
}

// 所有卡片都必须到点自动收起：早期版本把 error 的 TTL 设成 0（"严重事件不能被
// 悄悄吞掉"），结果子代理步数耗尽这类红色卡片会永久钉在屏幕上，连开三条就挡住
// 半个屏幕。严重事件的价值在于"及时看见"，不是常驻——列表里仍然留着，随时可回看。
const TTL_INFO = 5000;
const TTL_WARN = 8000;
const TTL_TASK = 8000; // 任务完成：够看清标题，又不至于长期占屏
const TTL_ERROR = 10000;

/** 按级别推导的默认存活时长（毫秒）。 */
export function baseTtlMs(ev: NotifyEventLike): number {
  if (ev.level === "error") return TTL_ERROR;
  if (ev.kind === "finding" && (ev.severity === "critical" || ev.severity === "high")) return TTL_ERROR;
  if (ev.kind === "task_done") return TTL_TASK;
  if (ev.level === "warn") return TTL_WARN;
  return TTL_INFO;
}

/** 最终存活时长：用户选了固定值就用固定值，否则按级别。 */
export function resolveTtlMs(prefs: NotifyPrefs, ev: NotifyEventLike): number {
  if (prefs.ttlMode === "fixed") return clampTtlSec(prefs.ttlSec) * 1000;
  return baseTtlMs(ev);
}

export type SoundKind = "error" | "warn" | "success";

/**
 * 该事件要不要响、响哪一种。返回 null 表示静默。
 *
 * 三档声音：error 下行双音（出事，要立刻看）、task_timeout 单音（停了但不是崩溃）、
 * 其余上行双音（漏洞落库 / 任务完成）。
 */
export function soundFor(prefs: NotifyPrefs, ev: NotifyEventLike): SoundKind | null {
  if (prefs.sound === "off") return null;
  if (triggerOf(ev) === null) return null;
  if (prefs.sound === "urgent" && !isUrgent(ev)) return null;
  if (isUrgent(ev)) return "error";
  if (ev.kind === "task_timeout") return "warn";
  return "success";
}
