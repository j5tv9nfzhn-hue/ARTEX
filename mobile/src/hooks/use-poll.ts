import * as React from "react";

export type PollState<T> = {
  data: T | null;
  error: string | null;
  loading: boolean;
  updatedAt: number;
  refresh: () => Promise<void>;
};

/**
 * 手机端刻意用轮询而不是长连接：前台 5–10s 拉一次，退到后台就停。
 * 这样不会像 WSS 那样在 iOS 后台被挂起后假装还活着。
 */
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs = 8000): PollState<T> {
  const [data, setData] = React.useState<T | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [updatedAt, setUpdatedAt] = React.useState(0);

  const fetcherRef = React.useRef(fetcher);
  fetcherRef.current = fetcher;

  const refresh = React.useCallback(async () => {
    try {
      const next = await fetcherRef.current();
      setData(next);
      setError(null);
      setUpdatedAt(Date.now());
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败");
    } finally {
      setLoading(false);
    }
  }, []);

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

  return { data, error, loading, updatedAt, refresh };
}
