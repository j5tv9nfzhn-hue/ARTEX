import * as React from "react";

import { loadCache, saveCache } from "@/api/cache";

export type PollState<T> = {
  data: T | null;
  error: string | null;
  loading: boolean;
  updatedAt: number;
  /** true = 当前展示的是本地缓存（连不上服务器时），界面要明确标注。 */
  stale: boolean;
  refresh: () => Promise<void>;
};

/**
 * 手机端刻意用轮询而不是长连接：前台 8–10s 拉一次，退到后台就停。
 * 传了 cacheKey 时，请求失败会回落到上一次成功的载荷，并标记 stale。
 */
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs = 8000, cacheKey?: string): PollState<T> {
  const [data, setData] = React.useState<T | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [updatedAt, setUpdatedAt] = React.useState(0);
  const [stale, setStale] = React.useState(false);

  const fetcherRef = React.useRef(fetcher);
  fetcherRef.current = fetcher;

  const refresh = React.useCallback(async () => {
    try {
      const next = await fetcherRef.current();
      setData(next);
      setError(null);
      setStale(false);
      setUpdatedAt(Date.now());
      if (cacheKey) void saveCache(cacheKey, next);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败");
      if (cacheKey) {
        const cached = await loadCache<T>(cacheKey);
        if (cached) {
          setData(cached.value);
          setUpdatedAt(cached.at);
          setStale(true);
        }
      }
    } finally {
      setLoading(false);
    }
  }, [cacheKey]);

  React.useEffect(() => {
    let alive = true;
    const tick = () => {
      if (alive) void refresh();
    };
    tick();
    const timer = setInterval(tick, intervalMs);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [intervalMs, refresh]);

  return { data, error, loading, updatedAt, stale, refresh };
}
