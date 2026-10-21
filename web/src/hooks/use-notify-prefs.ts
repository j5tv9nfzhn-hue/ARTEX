"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { DEFAULT_NOTIFY_PREFS, type NotifyPrefs, parseNotifyPrefs, serializeNotifyPrefs } from "@/lib/notify-prefs";

// 通知显示偏好的读写胶水。
//
// 用 useSyncExternalStore 而不是 useState + Context：设置表单在 /system/notify，
// 卡片宿主挂在 (main)/layout 里，两者不在同一条 React 树上；模块级单例 + 订阅是
// 最省事的同步方式，而且不会因为 Context 缺失而静默失效。
//
// 关键细节：getSnapshot 必须返回**同一个对象**直到真正变更，否则 useSyncExternalStore
// 会判定「每次都变了」而无限重渲染——所以下面缓存在 current 里，只在 emit 时换引用。

export const NOTIFY_PREFS_KEY = "artex_ui_notify_prefs";
/** 老版本只存了一个布尔开关（"0" = 静音）。首次加载且没有新键时用它做一次性迁移。 */
const LEGACY_SOUND_KEY = "artex_ui_notify_sound";

let current: NotifyPrefs = DEFAULT_NOTIFY_PREFS;
let loaded = false;
const listeners = new Set<() => void>();

function persist(prefs: NotifyPrefs) {
  setLocalStorageValue(NOTIFY_PREFS_KEY, serializeNotifyPrefs(prefs));
}

function ensureLoaded(): NotifyPrefs {
  if (loaded) return current;
  loaded = true;
  const raw = getLocalStorageValue(NOTIFY_PREFS_KEY);
  const parsed = parseNotifyPrefs(raw);
  if (raw === null && getLocalStorageValue(LEGACY_SOUND_KEY) === "0") {
    // 老用户关过提示音：迁移成 sound="off"，别让升级后突然开始响。
    current = { ...parsed, sound: "off" };
    persist(current);
  } else {
    current = parsed;
  }
  return current;
}

function emit(next: NotifyPrefs) {
  current = next;
  persist(next);
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function getSnapshot(): NotifyPrefs {
  return ensureLoaded();
}

function getServerSnapshot(): NotifyPrefs {
  return DEFAULT_NOTIFY_PREFS;
}

/** 非组件上下文（SSE 回调里）读取当前偏好；不要用它渲染。 */
export function getNotifyPrefs(): NotifyPrefs {
  return ensureLoaded();
}

/** 局部更新。传入的字段会先被 parseNotifyPrefs 规范化。 */
export function setNotifyPrefs(patch: Partial<NotifyPrefs>) {
  const base = ensureLoaded();
  emit(parseNotifyPrefs(JSON.stringify({ ...base, ...patch })));
}

export function resetNotifyPrefs() {
  emit({ ...DEFAULT_NOTIFY_PREFS, triggers: { ...DEFAULT_NOTIFY_PREFS.triggers } });
}

export function useNotifyPrefs(): NotifyPrefs {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
