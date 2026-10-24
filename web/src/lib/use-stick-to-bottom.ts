"use client";

import * as React from "react";

/**
 * 「贴底」滚动的唯一实现（scroll anchoring）。
 *
 * 为什么要有这个文件（2026-10-24 推理流抽搐排查）：
 *
 * 同一个页面里原来有**三个**独立的写入者同时改 scrollTop：
 *   1. 滚动事件里维护 atBottom；
 *   2. `activity` / `messages` 变化时的 useLayoutEffect；
 *   3. 实时流的 onLiveGrow 回调 + 一个额外的 ResizeObserver。
 * 而浏览器自己的 scroll anchoring（`overflow-anchor: auto`，默认开启）在内容变高时
 * 也会调整 scrollTop。于是同一帧里有两套系统抢同一个滚动位置：浏览器先把锚点钉住
 * （视觉上「往上推」），我们再把它拽到底，下一帧再来一次——这就是流式期间整块区域
 * 上下抽搐的根因，不是 CSS 过渡能做到的事。
 *
 * 现在的约定：
 *   - 只有一个写入口 `write()`，同一帧最多执行一次（rAF 合并）；
 *   - 已经在底部（差值 < 1px）就直接跳过，不产生无意义的重排；
 *   - 用户上滑离开底部后立即停手，绝不把视图拽回去；
 *   - 配合 Transcript 根节点的 `overflow-anchor: none`，让浏览器不再插手。
 *
 * 注意：这里**不**用 `scrollIntoView`。它会连带滚动所有祖先容器、可能抢走焦点，
 * 并且在每帧调用时开销远大于直接写一个容器的 scrollTop——我们要钉的只有这一个视口。
 */

/** 离底多少像素内仍算「贴底」。 */
const SLACK_PX = 60;
/** 距顶部多少像素内触发「加载更早」。 */
const NEAR_TOP_PX = 80;

export type StickToBottomOptions = {
  /** 冻结：审批聚焦等场景不要再自动滚动。 */
  paused?: boolean;
  /** 离底多少像素内仍算贴底。 */
  slack?: number;
  /** 滚到接近顶部时的回调（上滑加载更早的记录）。 */
  onNearTop?: () => void;
  /** 复用外部已有的 atBottom ref（页面里还有别的逻辑要看它）。 */
  atBottomRef?: { current: boolean };
  /**
   * 内容容器会被卸载再挂回时（切会话、从空态切到有内容），把这个值传进来：
   * 它一变就重新挂接 ResizeObserver。少了这个，观察器会留在已经卸载的节点上，
   * 之后所有高度变化都无人处理，贴底就静默失效了。
   */
  refreshKey?: unknown;
};

export type StickToBottom = {
  /** 页面的滚动视口（懒取；未挂载时返回 null）。 */
  viewport: () => HTMLElement | null;
  /** 内容变高 / 新行落库：合并到一帧，最多写一次。 */
  pin: () => void;
  /** 跳到最新并重新贴底（切会话、打开历史）。 */
  reset: () => void;
  /** 是否仍贴底。 */
  atBottom: { current: boolean };
};

export function useStickToBottom(
  contentRef: { current: HTMLElement | null },
  options: StickToBottomOptions = {},
): StickToBottom {
  const { paused = false, slack = SLACK_PX, onNearTop, atBottomRef, refreshKey } = options;

  const inner = React.useRef(true);
  const atBottom = atBottomRef ?? inner;

  // 回调用 ref 持有：它们的身份变化不该导致滚动监听重新订阅（重新订阅会丢状态）。
  const pausedRef = React.useRef(paused);
  pausedRef.current = paused;
  const nearTopRef = React.useRef(onNearTop);
  nearTopRef.current = onNearTop;

  const viewport = React.useCallback(
    () => (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ?? null,
    [contentRef],
  );

  const write = React.useCallback(() => {
    if (pausedRef.current || !atBottom.current) return;
    const vp = viewport();
    if (!vp) return;
    const target = vp.scrollHeight - vp.clientHeight;
    if (target <= 0) return;
    // 已经在底部：不写。多余的 scrollTop 赋值会让浏览器做一次无谓的重排/重绘。
    if (Math.abs(vp.scrollTop - target) < 1) return;
    vp.scrollTop = target;
  }, [viewport]);

  const frame = React.useRef<number | null>(null);
  const pin = React.useCallback(() => {
    if (frame.current !== null) return; // 本帧已经排过一次，合并掉
    frame.current = requestAnimationFrame(() => {
      frame.current = null;
      write();
    });
  }, [write]);

  // 内容长高（实时流每一帧、详情懒加载展开、图片落位）→ 直接写。
  // ResizeObserver 回调发生在 layout 之后、paint 之前，所以这里同步写不会出现
  // 「先画出一帧旧位置、下一帧再纠正」的抖；而它只会因尺寸变化触发，不会自激。
  //
  // 容器不是一定在这一刻就存在（选中会话后内容区才渲染），所以挂不上就按帧重试，
  // 最多约 2 秒；refreshKey 变化时重新走一遍整个过程。
  React.useEffect(() => {
    let ro: ResizeObserver | null = null;
    const attach = (): boolean => {
      const el = contentRef.current;
      if (!el) return false;
      if (typeof ResizeObserver === "undefined") return true; // 没有 RO 时靠 pin() 兜底
      if (ro) return true;
      ro = new ResizeObserver(() => write());
      ro.observe(el);
      return true;
    };
    let frame = 0;
    if (!attach()) {
      let tries = 0;
      const retry = () => {
        if (attach() || ++tries > 120) return;
        frame = requestAnimationFrame(retry);
      };
      frame = requestAnimationFrame(retry);
    }
    return () => {
      if (frame) cancelAnimationFrame(frame);
      ro?.disconnect();
      ro = null;
    };
  }, [contentRef, write, refreshKey]);

  // 用户滚动 → 维护贴底状态；接近顶部 → 拉更早的一页。
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      atBottom.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - slack;
      if (vp.scrollTop <= NEAR_TOP_PX) nearTopRef.current?.();
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, slack, atBottom]);

  React.useEffect(
    () => () => {
      if (frame.current !== null) cancelAnimationFrame(frame.current);
    },
    [],
  );

  const reset = React.useCallback(() => {
    atBottom.current = true;
    write();
  }, [atBottom, write]);

  return { viewport, pin, reset, atBottom };
}
