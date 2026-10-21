"use client";

import { useSyncExternalStore } from "react";

import { type InboxState, notifyInbox } from "@/lib/notify-inbox";
import { triggerOf } from "@/lib/notify-prefs";

export function useNotifyInbox(): InboxState {
  return useSyncExternalStore(notifyInbox.subscribe, notifyInbox.getSnapshot, notifyInbox.getServerSnapshot);
}

/**
 * 侧边栏角标用的未读数。
 *
 * 只数「会提醒」的事件：任务开始这类过程态如果也算未读，用户会被一个永远清不掉
 * 的数字绑架——他根本不知道该点哪一条。
 */
export function useNotifyUnread(): number {
  const state = useNotifyInbox();
  let unread = 0;
  for (const ev of state.events) {
    if (ev.ts > state.lastSeenTs && triggerOf(ev) !== null) unread += 1;
  }
  return unread;
}
