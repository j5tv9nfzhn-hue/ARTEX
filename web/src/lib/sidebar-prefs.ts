"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// 侧边栏个性化（批次 7 / #177–182）是浏览器本地偏好，只存 localStorage，不与账户同步。
// 与 chat-send-mode.ts 相同的 useSyncExternalStore 模式；区别是快照为对象，
// 必须缓存解析结果：每次 getSnapshot 返回新对象会让 React 判定外部状态一直变化。
export interface SidebarPrefs {
  // 置顶（收藏）条目 id，顺序即收藏区展示顺序。
  pinned: string[];
  // 隐藏条目 id：从侧边栏（含收藏区）整体过滤。
  hidden: string[];
  // 已折叠的分组 id（0 为收藏分组）。
  collapsedGroups: number[];
}

export const SIDEBAR_PREFS_KEY = "artex_sidebar_prefs";

// 默认置顶四个核心入口（#180 一键可达）：未设置过偏好的浏览器首屏即生效；
// 用户可在「自定义侧边栏」中取消置顶或复位。
export const DEFAULT_SIDEBAR_PREFS: SidebarPrefs = {
  pinned: ["tasks", "chat", "findings", "dashboard"],
  hidden: [],
  collapsedGroups: [],
};

function sanitizeIds(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const v of value) {
    if (typeof v === "string" && v !== "" && !seen.has(v)) {
      seen.add(v);
      out.push(v);
    }
  }
  return out;
}

function sanitizeGroupIds(value: unknown): number[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<number>();
  const out: number[] = [];
  for (const v of value) {
    if (typeof v === "number" && Number.isFinite(v) && !seen.has(v)) {
      seen.add(v);
      out.push(v);
    }
  }
  return out;
}

function parsePrefs(raw: string | null): SidebarPrefs {
  if (!raw) return DEFAULT_SIDEBAR_PREFS;
  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    return DEFAULT_SIDEBAR_PREFS;
  }
  if (typeof data !== "object" || data === null) return DEFAULT_SIDEBAR_PREFS;
  const record = data as Partial<SidebarPrefs>;
  const pinned = sanitizeIds(record.pinned);
  // 不变量：同一 id 至多出现在 pinned / hidden 之一（置顶优先，损坏的数据在此收敛）。
  const hidden = sanitizeIds(record.hidden).filter((id) => !pinned.includes(id));
  return { pinned, hidden, collapsedGroups: sanitizeGroupIds(record.collapsedGroups) };
}

// storage 事件只通知其他标签页；本页修改后需 emit 通知订阅组件，
// 避免必须刷新才生效（同 chat-send-mode.ts）。
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// 缓存原始字符串与解析结果：仅在 localStorage 内容真正变化时返回新对象，
// 保持 useSyncExternalStore 的快照稳定（Object.is 比较）。
let cachedRaw: string | null = null;
let cached: SidebarPrefs = DEFAULT_SIDEBAR_PREFS;

function getSnapshot(): SidebarPrefs {
  const raw = getLocalStorageValue(SIDEBAR_PREFS_KEY);
  if (raw === cachedRaw) return cached;
  cachedRaw = raw;
  cached = parsePrefs(raw);
  return cached;
}

// 服务器无 localStorage，先使用默认值，水合后由 getSnapshot 校正。
function getServerSnapshot(): SidebarPrefs {
  return DEFAULT_SIDEBAR_PREFS;
}

export function useSidebarPrefs(): SidebarPrefs {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

function writePrefs(next: SidebarPrefs) {
  setLocalStorageValue(
    SIDEBAR_PREFS_KEY,
    JSON.stringify({ pinned: next.pinned, hidden: next.hidden, collapsedGroups: next.collapsedGroups }),
  );
  for (const listener of listeners) listener();
}

// 置顶与隐藏互斥：置顶一个隐藏中的条目会同时取消隐藏，反向同理。
export function toggleSidebarItemPinned(id: string, pinned: boolean) {
  const cur = getSnapshot();
  if (pinned) {
    if (cur.pinned.includes(id)) return;
    writePrefs({
      pinned: [...cur.pinned, id],
      hidden: cur.hidden.filter((x) => x !== id),
      collapsedGroups: cur.collapsedGroups,
    });
  } else {
    if (!cur.pinned.includes(id)) return;
    writePrefs({ ...cur, pinned: cur.pinned.filter((x) => x !== id) });
  }
}

export function toggleSidebarItemHidden(id: string, hidden: boolean) {
  const cur = getSnapshot();
  if (hidden) {
    if (cur.hidden.includes(id)) return;
    writePrefs({
      pinned: cur.pinned.filter((x) => x !== id),
      hidden: [...cur.hidden, id],
      collapsedGroups: cur.collapsedGroups,
    });
  } else {
    if (!cur.hidden.includes(id)) return;
    writePrefs({ ...cur, hidden: cur.hidden.filter((x) => x !== id) });
  }
}

export function toggleSidebarGroupCollapsed(id: number, collapsed: boolean) {
  const cur = getSnapshot();
  const has = cur.collapsedGroups.includes(id);
  if (has === collapsed) return;
  writePrefs({
    ...cur,
    collapsedGroups: collapsed ? [...cur.collapsedGroups, id] : cur.collapsedGroups.filter((x) => x !== id),
  });
}

export function resetSidebarPrefs() {
  writePrefs(DEFAULT_SIDEBAR_PREFS);
}
