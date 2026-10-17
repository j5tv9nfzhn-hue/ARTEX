import { ApiError, http } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import {
  parseRetentionAudit,
  parseRetentionEnvelope,
  parseRetentionExecution,
  parseRetentionPreview,
  parseRetentionSave,
  RETENTION_CONFIRMATION,
  type RetentionPolicy,
  type RetentionSnapshot,
} from "@/lib/retention";

export type RetentionFailure = "mock" | "http" | "network" | "timeout" | "protocol";
export class RetentionRequestError extends Error {
  readonly kind: RetentionFailure;
  readonly status?: number;
  constructor(kind: RetentionFailure, status?: number) {
    // Do not retain response/request bodies or a token in an error message.
    super(`retention_${kind}`);
    this.name = "RetentionRequestError";
    this.kind = kind;
    this.status = status;
  }
}

async function request<T>(
  path: string,
  method: "GET" | "PUT" | "POST",
  body: object | undefined,
  signal: AbortSignal,
  decode: (raw: unknown) => T,
): Promise<T> {
  // A maintenance page must never pass through permissive demo/mock responses.
  if (MOCK) throw new RetentionRequestError("mock");
  let raw: unknown;
  try {
    raw = await http<unknown>(`/retention/${path}`, {
      method,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
      cache: "no-store",
      credentials: "same-origin",
    });
  } catch (error) {
    if (signal.aborted) throw new RetentionRequestError("timeout");
    if (error instanceof ApiError) throw new RetentionRequestError("http", error.status);
    if (error instanceof SyntaxError) throw new RetentionRequestError("protocol");
    // The shared client handles 401 navigation. Do not print its arbitrary error.
    throw new RetentionRequestError("network");
  }
  try {
    return decode(raw);
  } catch {
    throw new RetentionRequestError("protocol");
  }
}

// No retries, keepalive requests, mount-triggered mutation, or GET-based execute.
export const retentionApi = {
  policy: (signal: AbortSignal) => request("policy", "GET", undefined, signal, parseRetentionEnvelope),
  audit: (signal: AbortSignal) => request("audit", "GET", undefined, signal, parseRetentionAudit),
  save: (policy: RetentionPolicy, signal: AbortSignal) => request("policy", "PUT", policy, signal, parseRetentionSave),
  preview: (signal: AbortSignal) => request("preview", "POST", {}, signal, parseRetentionPreview),
  execute: (token: string, snapshot: RetentionSnapshot, signal: AbortSignal) =>
    request("execute", "POST", { confirmation_token: token, confirmation: RETENTION_CONFIRMATION }, signal, (raw) =>
      parseRetentionExecution(raw, snapshot),
    ),
};
