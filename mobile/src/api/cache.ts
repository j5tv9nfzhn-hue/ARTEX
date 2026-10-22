import * as SecureStore from "expo-secure-store";

/**
 * 离线兜底：把最后一次成功的载荷按 key 存下来。
 * 手机上「看到 3 分钟前的数据 + 时间戳」远好过「白屏 + 一个转圈」。
 * 只存聚合结果，不存任何凭据。
 */
type Envelope<T> = { at: number; value: T };

const PREFIX = "artex.cache.";

export async function saveCache<T>(key: string, value: T): Promise<void> {
  const payload: Envelope<T> = { at: Date.now(), value };
  await SecureStore.setItemAsync(PREFIX + key, JSON.stringify(payload)).catch(() => {});
}

export async function loadCache<T>(key: string): Promise<Envelope<T> | null> {
  const raw = await SecureStore.getItemAsync(PREFIX + key).catch(() => null);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Envelope<T>;
    if (!parsed || typeof parsed.at !== "number") return null;
    return parsed;
  } catch {
    return null;
  }
}
