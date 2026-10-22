import * as SecureStore from "expo-secure-store";

const KEY_BASE_URL = "artex.base.url";
const KEY_TOKEN = "artex.token";

export type Session = { baseUrl: string; token: string };

let cached: Session = { baseUrl: "", token: "" };

export function getSession(): Session {
  return cached;
}

/** 应用启动时调用一次：把凭据从安全存储装进内存。 */
export async function loadSession(): Promise<Session> {
  const [baseUrl, token] = await Promise.all([
    SecureStore.getItemAsync(KEY_BASE_URL).catch(() => null),
    SecureStore.getItemAsync(KEY_TOKEN).catch(() => null),
  ]);
  cached = { baseUrl: baseUrl ?? "", token: token ?? "" };
  return cached;
}

export async function saveSession(next: Partial<Session>): Promise<void> {
  cached = { ...cached, ...next };
  if (next.baseUrl !== undefined) {
    await SecureStore.setItemAsync(KEY_BASE_URL, next.baseUrl).catch(() => {});
  }
  if (next.token !== undefined) {
    await SecureStore.setItemAsync(KEY_TOKEN, next.token).catch(() => {});
  }
}

export async function clearSession(): Promise<void> {
  cached = { ...cached, token: "" };
  await SecureStore.deleteItemAsync(KEY_TOKEN).catch(() => {});
}

/** 用户可能只填了 IP 或 IP:端口，这里统一补全协议与去尾斜杠。 */
export function normalizeBaseUrl(input: string): string {
  const trimmed = input.trim().replace(/\/+$/, "");
  if (!trimmed) return "";
  if (/^https?:\/\//i.test(trimmed)) return trimmed;
  return `http://${trimmed}`;
}
