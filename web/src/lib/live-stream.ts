"use client";

import * as React from "react";

import type { LiveFrame, LiveStream } from "./live-stream-store";

/**
 * 订阅实时流的最小 hooks。
 *
 * 关键点（与 useSyncExternalStore 的契约一一对应）：
 *   - `subscribe` 直接取实例上的方法：同一个 store 实例永远是同一个函数引用，
 *     不会因为组件重渲染而反复退订/重订（这是最常见的隐性抖动来源）；
 *   - `getSnapshot` 由 store 缓存：内容没变就返回同一个对象，React 用 Object.is
 *     比较后直接跳过渲染；
 *   - 两个 hook 都提供 getServerSnapshot：静态导出（NEXT_EXPORT）时服务端没有
 *     实时流，返回 null / undefined，水合前后一致，不会触发 mismatch 重渲染。
 */

/** 订阅整帧（实时块本体用）。文本每帧都在变，所以只有这一层会跟着 40ms 抖动。 */
export function useLiveFrame(stream: LiveStream): LiveFrame | null {
  return React.useSyncExternalStore(stream.subscribe, stream.getSnapshot, stream.getServerSnapshot);
}

/**
 * 只订阅「帧的类型」（状态徽标用）。
 *
 * 返回的是字符串而不是对象：同一段推理从 30 字涨到 3000 字，kind 一直是
 * `thinking`，Object.is 相等 → 徽标一次都不重渲染。这正是把「状态」与「内容」
 * 拆成两条订阅通道的意义——状态机互斥只跟 kind 有关，跟 token 无关。
 */
export function useLiveKind(stream: LiveStream): string | undefined {
  return React.useSyncExternalStore(
    stream.subscribe,
    () => stream.getSnapshot()?.kind,
    () => undefined,
  );
}
