// Retention's destructive workflow fails closed on unknown/malformed contracts.
// Pure helpers: no fetch, browser storage, timers or database side effects.
export const RETENTION_CONFIRMATION = "DELETE_PREVIEWED_ROWS";
export const RETENTION_TABLES = ["llm_usage", "server_logs", "notification_deliveries", "notification_events"] as const;
export type RetentionTable = (typeof RETENTION_TABLES)[number];
export interface RetentionPolicy {
  enabled: boolean;
  usage_days: number;
  log_days: number;
  notification_days: number;
  max_rows_per_table: number;
}
export interface RetentionPolicyEnvelope {
  policy: RetentionPolicy;
  automatic_cleanup: false;
  preview_ttl_seconds: number;
  scope: string;
}
export interface RetentionCount {
  table: RetentionTable;
  selected: number;
  more_candidates: boolean;
  deleted: number;
  skipped: number;
}
export interface RetentionPreview {
  confirmation_token: string;
  expires_at: string;
  policy: RetentionPolicy;
  counts: RetentionCount[];
  automatic_cleanup: false;
}
// Never put the token into a URL, DOM attribute, storage, error or audit display.
export type RetentionSnapshot = Omit<RetentionPreview, "confirmation_token">;
export interface RetentionLease extends RetentionSnapshot {
  wallDeadline: number;
  monotonicDeadline: number;
}
export interface RetentionExecution {
  committed: true;
  counts: RetentionCount[];
}
export interface RetentionAuditEntry {
  id: number;
  at: string;
  action: string;
  plan_hash: string;
  actor: string;
  detail: unknown;
}
export interface RetentionDraft {
  enabled: boolean;
  usage_days: string;
  log_days: string;
  notification_days: string;
  max_rows_per_table: string;
}
export type RetentionField = Exclude<keyof RetentionDraft, "enabled">;
export class RetentionContractError extends Error {
  constructor() {
    super("retention_contract_mismatch");
    this.name = "RetentionContractError";
  }
}
function ensure(value: unknown): asserts value {
  if (!value) throw new RetentionContractError();
}
function record(value: unknown): Record<string, unknown> {
  ensure(value !== null && typeof value === "object" && !Array.isArray(value));
  return value as Record<string, unknown>;
}
function keys(value: Record<string, unknown>, expected: string[]) {
  ensure(Object.keys(value).length === expected.length && expected.every((key) => Object.hasOwn(value, key)));
}
function int(value: unknown, min: number, max: number): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= min && value <= max;
}
function text(value: unknown, max: number): value is string {
  return typeof value === "string" && value.length <= max;
}
function time(value: unknown): value is string {
  return (
    typeof value === "string" &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
    Number.isFinite(Date.parse(value))
  );
}
export function parseRetentionPolicy(value: unknown): RetentionPolicy {
  const v = record(value);
  keys(v, ["enabled", "usage_days", "log_days", "notification_days", "max_rows_per_table"]);
  ensure(typeof v.enabled === "boolean");
  for (const [field, min] of [
    ["usage_days", 30],
    ["log_days", 7],
    ["notification_days", 30],
  ] as const) {
    ensure(v[field] === 0 || int(v[field], min, 3650));
  }
  ensure(int(v.max_rows_per_table, 1, 500));
  return {
    enabled: v.enabled,
    usage_days: v.usage_days as number,
    log_days: v.log_days as number,
    notification_days: v.notification_days as number,
    max_rows_per_table: v.max_rows_per_table,
  };
}
export function parseRetentionEnvelope(value: unknown): RetentionPolicyEnvelope {
  const v = record(value);
  keys(v, ["policy", "automatic_cleanup", "preview_ttl_seconds", "scope"]);
  ensure(v.automatic_cleanup === false && int(v.preview_ttl_seconds, 1, 900) && text(v.scope, 4096));
  return {
    policy: parseRetentionPolicy(v.policy),
    automatic_cleanup: false,
    preview_ttl_seconds: v.preview_ttl_seconds,
    scope: v.scope,
  };
}
export function parseRetentionSave(value: unknown): RetentionPolicy {
  const v = record(value);
  keys(v, ["policy", "automatic_cleanup", "old_previews_invalidated"]);
  ensure(v.automatic_cleanup === false && v.old_previews_invalidated === true);
  return parseRetentionPolicy(v.policy);
}
function counts(value: unknown, maximum: number): RetentionCount[] {
  ensure(Array.isArray(value) && value.length <= 4);
  const seen = new Set<string>();
  return value.map((raw) => {
    const v = record(raw);
    keys(v, ["table", "selected", "more_candidates", "deleted", "skipped"]);
    ensure(RETENTION_TABLES.some((table) => table === v.table) && !seen.has(String(v.table)));
    ensure(int(v.selected, 0, maximum) && int(v.deleted, 0, maximum) && int(v.skipped, 0, maximum));
    ensure(typeof v.more_candidates === "boolean");
    seen.add(String(v.table));
    return {
      table: v.table as RetentionTable,
      selected: v.selected,
      more_candidates: v.more_candidates,
      deleted: v.deleted,
      skipped: v.skipped,
    };
  });
}
export function retentionDays(policy: RetentionPolicy, table: RetentionTable): number {
  if (table === "llm_usage") return policy.usage_days;
  if (table === "server_logs") return policy.log_days;
  return policy.notification_days;
}
export function parseRetentionPreview(value: unknown): RetentionPreview {
  const v = record(value),
    policy = parseRetentionPolicy(v.policy);
  keys(v, ["confirmation_token", "expires_at", "policy", "counts", "automatic_cleanup"]);
  ensure(v.automatic_cleanup === false && time(v.expires_at));
  ensure(typeof v.confirmation_token === "string" && /^[0-9a-f]{64}$/.test(v.confirmation_token));
  const rows = counts(v.counts, policy.max_rows_per_table);
  ensure(rows.length === 4);
  for (const row of rows) {
    ensure(row.deleted === 0 && row.skipped === 0);
    ensure(!row.more_candidates || row.selected === policy.max_rows_per_table);
    ensure(retentionDays(policy, row.table) !== 0 || (row.selected === 0 && !row.more_candidates));
  }
  return {
    policy,
    counts: rows,
    confirmation_token: v.confirmation_token,
    expires_at: v.expires_at,
    automatic_cleanup: false,
  };
}
export function parseRetentionExecution(value: unknown, snapshot: RetentionSnapshot): RetentionExecution {
  const v = record(value);
  keys(v, ["committed", "counts"]);
  ensure(v.committed === true);
  const rows = counts(v.counts, snapshot.policy.max_rows_per_table);
  const expected = snapshot.counts.filter((row) => retentionDays(snapshot.policy, row.table) > 0);
  ensure(rows.length === expected.length);
  for (const row of rows) {
    const before = expected.find((item) => item.table === row.table);
    ensure(
      before && row.selected === before.selected && row.deleted + row.skipped === row.selected && !row.more_candidates,
    );
  }
  return { committed: true, counts: rows };
}
export function parseRetentionAudit(value: unknown): RetentionAuditEntry[] {
  const v = record(value);
  keys(v, ["entries", "limit"]);
  ensure(v.limit === 100 && Array.isArray(v.entries) && v.entries.length <= 100);
  const ids = new Set<number>();
  return v.entries.map((raw) => {
    const e = record(raw);
    keys(e, ["id", "at", "action", "plan_hash", "actor", "detail"]);
    ensure(int(e.id, 1, Number.MAX_SAFE_INTEGER) && !ids.has(e.id));
    ids.add(e.id);
    ensure(time(e.at) && text(e.action, 100) && text(e.actor, 100));
    ensure(typeof e.plan_hash === "string" && (e.plan_hash === "" || /^[0-9a-f]{64}$/.test(e.plan_hash)));
    // Bounded but intentionally not rendered as raw JSON: future fields may be sensitive.
    const detail = JSON.stringify(e.detail);
    ensure(typeof detail === "string" && detail.length <= 16384);
    return { id: e.id, at: e.at, action: e.action, actor: e.actor, plan_hash: e.plan_hash, detail: e.detail };
  });
}
export function draftRetentionPolicy(policy: RetentionPolicy): RetentionDraft {
  return {
    enabled: policy.enabled,
    usage_days: String(policy.usage_days),
    log_days: String(policy.log_days),
    notification_days: String(policy.notification_days),
    max_rows_per_table: String(policy.max_rows_per_table),
  };
}
export function readRetentionDraft(draft: RetentionDraft): {
  policy: RetentionPolicy | null;
  errors: Partial<Record<RetentionField, true>>;
} {
  const errors: Partial<Record<RetentionField, true>> = {};
  const fields = ["usage_days", "log_days", "notification_days", "max_rows_per_table"] as const;
  const numbers: Record<RetentionField, number> = {
    usage_days: 0,
    log_days: 0,
    notification_days: 0,
    max_rows_per_table: 0,
  };
  for (const field of fields) {
    const raw = draft[field];
    if (!/^\d{1,4}$/.test(raw)) {
      errors[field] = true;
      continue;
    }
    const n = Number(raw);
    numbers[field] = n;
    const min = field === "log_days" ? 7 : 30;
    if (field === "max_rows_per_table" ? !int(n, 1, 500) : n !== 0 && !int(n, min, 3650)) errors[field] = true;
  }
  return { errors, policy: Object.keys(errors).length ? null : { enabled: draft.enabled, ...numbers } };
}
export function sameRetentionPolicy(a: RetentionPolicy, b: RetentionPolicy): boolean {
  return (
    a.enabled === b.enabled &&
    a.usage_days === b.usage_days &&
    a.log_days === b.log_days &&
    a.notification_days === b.notification_days &&
    a.max_rows_per_table === b.max_rows_per_table
  );
}
export function leaseRetentionPreview(
  preview: RetentionPreview,
  startMonotonic: number,
  wallNow: number,
  monotonicNow: number,
  ttlSeconds: number,
): { token: string; lease: RetentionLease } {
  ensure([startMonotonic, wallNow, monotonicNow].every(Number.isFinite) && int(ttlSeconds, 1, 900));
  // Both clocks are checked. Local clock rollback or tab suspension never buys
  // a new lease. Request latency and a small transport margin consume the TTL.
  const duration = Math.max(
    0,
    Math.min(Date.parse(preview.expires_at) - wallNow, ttlSeconds * 1000 - Math.max(0, monotonicNow - startMonotonic)) -
      1500,
  );
  const { confirmation_token, ...snapshot } = preview;
  return {
    token: confirmation_token,
    lease: { ...snapshot, wallDeadline: wallNow + duration, monotonicDeadline: monotonicNow + duration },
  };
}
export function retentionRemaining(lease: RetentionLease, wallNow: number, monotonicNow: number): number {
  if (![lease.wallDeadline, lease.monotonicDeadline, wallNow, monotonicNow].every(Number.isFinite)) return 0;
  return Math.max(0, Math.min(lease.wallDeadline - wallNow, lease.monotonicDeadline - monotonicNow));
}
export type RetentionBlock = "noPreview" | "dirty" | "disabled" | "changed" | "expired" | "empty";
export function retentionExecutionBlock(
  lease: RetentionLease | null,
  policy: RetentionPolicy | null,
  dirty: boolean,
  wallNow: number,
  monotonicNow: number,
): RetentionBlock | null {
  if (!lease || !policy) return "noPreview";
  if (dirty) return "dirty";
  if (!policy.enabled || !lease.policy.enabled) return "disabled";
  if (!sameRetentionPolicy(policy, lease.policy)) return "changed";
  if (retentionRemaining(lease, wallNow, monotonicNow) <= 0) return "expired";
  if (lease.counts.every((row) => row.selected === 0)) return "empty";
  return null;
}
