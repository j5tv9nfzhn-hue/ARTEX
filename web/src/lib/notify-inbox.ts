// 站内通知的会话内收件箱。
//
// 为什么要有独立的状态模块：通知原来只活在 <UINotifier> 的 useState 里，面板一关
// 就没有任何入口能看到「刚才那条是什么」，侧边栏也无从知道有没有未读。改造后
// 事件写进这个模块级单例，卡片宿主、侧边栏角标、「通知推送」页的列表读的是同一份
// 数据，不会出现「弹窗里有、列表里没有」。
//
// 生命周期与后端一致：**不落库**，刷新即清空（server/ui_notify.go 对新连接不补
// 历史），所以这里也不做 localStorage 持久化。

import { MAX_KEPT, type NotifyEvent } from "./notify-event";

export interface InboxState {
  /** 新的在前。 */
  readonly events: readonly NotifyEvent[];
  /** 已读水位线：ts 大于它的可提醒事件算未读。 */
  readonly lastSeenTs: number;
}

const listeners = new Set<() => void>();
const seen = new Set<number>();

let events: readonly NotifyEvent[] = [];
let lastSeenTs = 0;
let state: InboxState = { events, lastSeenTs };

// 服务端渲染阶段的稳定快照：必须与客户端首帧一致（都是空），否则 hydration 报错。
const SERVER_STATE: InboxState = { events: [], lastSeenTs: 0 };

function emit(next: InboxState) {
  state = next;
  for (const listener of listeners) listener();
}

let toastHandler: ((ev: NotifyEvent) => void) | null = null;

/**
 * 卡片宿主注册「该如何弹」（延迟、声音、存活时长都在宿主里算）。
 * 返回注销函数。宿主永远是 (main) 布局里的 <UINotifier>，一挂到底。
 */
export function setToastHandler(fn: (ev: NotifyEvent) => void): () => void {
  toastHandler = fn;
  return () => {
    if (toastHandler === fn) toastHandler = null;
  };
}

export const notifyInbox = {
  subscribe(listener: () => void): () => void {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },
  getSnapshot(): InboxState {
    return state;
  },
  getServerSnapshot(): InboxState {
    return SERVER_STATE;
  },
  has(id: number): boolean {
    return seen.has(id);
  },
  /** 追加一条；重复 id 返回 false（SSE 重连补洞会重发，靠这个去重）。 */
  add(ev: NotifyEvent): boolean {
    if (seen.has(ev.id)) return false;
    seen.add(ev.id);
    events = [ev, ...events].slice(0, MAX_KEPT);
    emit({ events, lastSeenTs });
    return true;
  },
  /** 删掉一条：列表与（若还在屏上）卡片一起消失，同时允许它再次被推送。 */
  remove(id: number) {
    seen.delete(id);
    events = events.filter((e) => e.id !== id);
    emit({ events, lastSeenTs });
  },
  clear() {
    seen.clear();
    events = [];
    lastSeenTs = Date.now();
    emit({ events, lastSeenTs });
  },
  /**
   * 把已读水位线推到 `watermark`（默认此刻）或最新一条，取较大者。
   * 列表页挂载与收到新事件时各调一次：传「最新一条的时间戳」即可，
   * 传默认为的是把「此刻之前都算看过」这件事记下来。
   */
  markAllRead(watermark: number = Date.now()) {
    const latest = events.length > 0 ? events[0].ts : 0;
    const next = Math.max(watermark, latest);
    if (next <= lastSeenTs) return;
    lastSeenTs = next;
    emit({ events, lastSeenTs });
  },
};

/**
 * 事件进入收件箱的唯一入口：SSE 与设置页的「发送测试通知」都走这里，
 * 这样预览出来的卡片和真实事件走的是同一套延迟/声音/时长规则。
 */
export function pushNotifyEvent(ev: NotifyEvent): boolean {
  if (!notifyInbox.add(ev)) return false;
  toastHandler?.(ev);
  return true;
}
