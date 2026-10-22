import { normalizeBaseUrl } from "./session";

// 手机绑定（扫码配对）的客户端实现。
//
// 二维码里是电脑发的一次性配对码：
//
//	ARTEXPAIR1|<host:port>|<code>
//
// 这里故意不复用 client.ts 的 apiFetch：那一刻本地还没有 baseUrl，
// 而 baseUrl 正是要从这张码里读出来的。

export const PAIR_PREFIX = "ARTEXPAIR1";

export type PairTarget = { host: string; code: string };

export type PairClaim = { token: string; base_url: string; version?: string };

/** 解析扫码结果。不是 ARTEX 的码就返回 null，让界面提示「这不是配对码」。 */
export function parsePairPayload(raw: string): PairTarget | null {
  const text = (raw ?? "").trim();
  const parts = text.split("|");
  if (parts.length !== 3) return null;
  if (parts[0].trim().toUpperCase() !== PAIR_PREFIX) return null;
  const host = parts[1].trim();
  const code = parts[2].trim().toUpperCase();
  if (!host || !code) return null;
  return { host, code };
}

/** 用配对码换 token；成功后把这个 baseUrl 一起存进会话。 */
export async function claimPair(host: string, code: string): Promise<PairClaim> {
  const baseUrl = normalizeBaseUrl(host);
  if (!baseUrl) throw new Error("配对码里的地址为空");
  const res = await fetch(`${baseUrl}/api/auth/pair-claim`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  const text = await res.text();
  const payload = text ? (JSON.parse(text) as unknown) : null;
  if (!res.ok) {
    const message =
      payload && typeof payload === "object" && "error" in payload
        ? String((payload as { error: unknown }).error)
        : `HTTP ${res.status}`;
    throw new Error(message);
  }
  const claim = payload as PairClaim;
  return { token: claim.token, base_url: claim.base_url || baseUrl, version: claim.version };
}
