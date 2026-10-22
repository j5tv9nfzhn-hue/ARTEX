import { getSession } from "./session";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: unknown;
  timeoutMs?: number;
  signal?: AbortSignal;
};

/** 单点出口：所有请求都带 Bearer token，超时默认 12s，401 交给调用方处理。 */
export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { baseUrl, token } = getSession();
  if (!baseUrl) {
    throw new ApiError(0, "尚未配置服务器地址");
  }

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), options.timeoutMs ?? 12000);
  if (options.signal) {
    options.signal.addEventListener("abort", () => controller.abort(), { once: true });
  }

  try {
    const response = await fetch(`${baseUrl}${path}`, {
      method: options.method ?? "GET",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: controller.signal,
    });

    const text = await response.text();
    const payload = text ? (JSON.parse(text) as unknown) : null;

    if (!response.ok) {
      const message =
        payload && typeof payload === "object" && "error" in payload
          ? String((payload as { error: unknown }).error)
          : `HTTP ${response.status}`;
      throw new ApiError(response.status, message);
    }
    return payload as T;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    if (error instanceof Error && error.name === "AbortError") {
      throw new ApiError(0, "请求超时");
    }
    throw new ApiError(0, error instanceof Error ? error.message : "网络错误");
  } finally {
    clearTimeout(timeout);
  }
}
