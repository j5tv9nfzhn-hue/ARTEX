"use client";

import * as React from "react";

type PollingOptions = {
  /** 轮询间隔（毫秒）。<= 0 表示不启用。 */
  intervalMs: number;
  /** false 时完全停止轮询（例如尚未拿到 taskId）。默认 true。 */
  enabled?: boolean;
  /** 页面从后台切回前台时是否立即补一次刷新。默认 true。 */
  refreshOnVisible?: boolean;
  /** 连续失败后的退避上限（毫秒）。默认 30000。 */
  maxBackoffMs?: number;
};

/**
 * usePolling 是控制台所有定时刷新的唯一入口，替代散落各处的 setInterval。
 *
 * 它在不牺牲实时性的前提下消除三类无效工作：
 *
 *  1. 页面不可见时暂停。用户切到别的标签页/最小化时，定时器不再打后端；
 *     切回来立刻补一次，屏幕上的数据仍然是当下的。
 *  2. 在途去重。上一次请求没回来就跳过本轮，慢响应不会堆成请求风暴。
 *  3. 失败退避。连续失败时按 2 的幂退避（上限 maxBackoffMs），成功后立刻
 *     复位到正常节奏。后端短暂故障时，几十个页面不会继续按原频率重试。
 *
 * 与 setInterval 的关键差异：下一次调度是在上一次**完成之后**再排的，
 * 因此慢请求不会造成回调堆积（setInterval 在回调是 async 时会出现）。
 *
 * callback 允许返回 Promise；它抛错或 reject 都计为一次失败。用 ref 持有，
 * 调用方不必为了稳定的函数标识而 useCallback。
 */
export function usePolling(callback: () => void | Promise<void>, options: PollingOptions) {
  const { intervalMs, enabled = true, refreshOnVisible = true, maxBackoffMs = 30_000 } = options;

  const cbRef = React.useRef(callback);
  React.useEffect(() => {
    cbRef.current = callback;
  }, [callback]);

  React.useEffect(() => {
    if (!enabled || intervalMs <= 0) return;
    if (typeof window === "undefined") return;

    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let failures = 0;
    // running 挡住"上一轮还没结束就开下一轮"。timer 与 running 一起保证
    // 任一时刻最多一个在途调用。
    let running = false;

    const clear = () => {
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
    };

    const schedule = (delay: number) => {
      clear();
      if (disposed) return;
      timer = setTimeout(tick, delay);
    };

    const nextDelay = () => {
      if (failures === 0) return intervalMs;
      const backoff = intervalMs * 2 ** Math.min(failures, 5);
      return Math.min(backoff, Math.max(intervalMs, maxBackoffMs));
    };

    const tick = async () => {
      timer = null;
      if (disposed) return;
      // 后台标签页里跳过本轮并直接排下一轮：不做请求，但不改变节奏。
      if (document.hidden) {
        schedule(intervalMs);
        return;
      }
      if (running) {
        schedule(intervalMs);
        return;
      }
      running = true;
      try {
        await cbRef.current();
        failures = 0;
      } catch {
        failures += 1;
      } finally {
        running = false;
      }
      if (!disposed) schedule(nextDelay());
    };

    // 首次立即执行，保持与原 setInterval(load, N) + void load() 相同的首屏行为。
    void tick();

    const onVisible = () => {
      if (document.hidden) return;
      if (!refreshOnVisible) return;
      // 切回前台：立刻补一次，并把退避复位（用户回来了，说明值得重试）。
      failures = 0;
      if (!running) void tick();
      else schedule(intervalMs);
    };
    document.addEventListener("visibilitychange", onVisible);

    return () => {
      disposed = true;
      clear();
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [enabled, intervalMs, refreshOnVisible, maxBackoffMs]);
}
