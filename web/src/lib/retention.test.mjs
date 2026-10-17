// Future authorized test source only. This phase does not execute tests.

import {
  draftRetentionPolicy,
  leaseRetentionPreview,
  parseRetentionAudit,
  parseRetentionEnvelope,
  parseRetentionExecution,
  parseRetentionPreview,
  parseRetentionSave,
  RETENTION_CONFIRMATION,
  RETENTION_TABLES,
  readRetentionDraft,
  retentionExecutionBlock,
  retentionRemaining,
  sameRetentionPolicy,
} from "./retention.ts";
import assert from "node:assert/strict";
import test from "node:test";

const policy = { enabled: true, usage_days: 90, log_days: 30, notification_days: 90, max_rows_per_table: 100 };
const wall = Date.parse("2026-10-17T12:00:00Z");
const counts = RETENTION_TABLES.map((table) => ({
  table,
  selected: 2,
  more_candidates: false,
  deleted: 0,
  skipped: 0,
}));
function preview(overrides = {}) {
  return {
    confirmation_token: "a".repeat(64),
    policy: { ...policy },
    counts: counts.map((row) => ({ ...row })),
    automatic_cleanup: false,
    expires_at: "2026-10-17T12:15:00Z",
    ...overrides,
  };
}
function lease() {
  return leaseRetentionPreview(parseRetentionPreview(preview()), 100, wall, 200, 900).lease;
}

test("策略表单不将空值、指数、小数或负数转成合法天数", () => {
  for (const value of ["", " ", "1e2", "30.0", "-1", "+30", "29", "3651"]) {
    const result = readRetentionDraft({ ...draftRetentionPolicy(policy), usage_days: value });
    assert.equal(result.policy, null);
    assert.equal(result.errors.usage_days, true);
  }
  for (const value of ["0", "30", "3650"])
    assert.ok(readRetentionDraft({ ...draftRetentionPolicy(policy), usage_days: value }).policy);
  assert.equal(readRetentionDraft({ ...draftRetentionPolicy(policy), log_days: "6" }).policy, null);
  for (const value of ["0", "501"])
    assert.equal(readRetentionDraft({ ...draftRetentionPolicy(policy), max_rows_per_table: value }).policy, null);
});

test("读取和保存策略必须明确关闭自动清理且具有真实契约字段", () => {
  const envelope = { policy, automatic_cleanup: false, preview_ttl_seconds: 900, scope: "fixture" };
  assert.ok(sameRetentionPolicy(parseRetentionEnvelope(envelope).policy, policy));
  assert.throws(() => parseRetentionEnvelope({ ...envelope, automatic_cleanup: true }));
  assert.throws(() => parseRetentionEnvelope({ ...envelope, preview_ttl_seconds: 901 }));
  assert.throws(() => parseRetentionEnvelope({ ...envelope, policy: { ...policy, delete_tasks: true } }));
  assert.throws(() => parseRetentionEnvelope(null));
  assert.throws(() => parseRetentionSave({ policy, automatic_cleanup: false }));
  assert.deepEqual(parseRetentionSave({ policy, automatic_cleanup: false, old_previews_invalidated: true }), policy);
});

test("预览只接受四张唯一白名单表和有效计数，不伪造零候选", () => {
  assert.equal(parseRetentionPreview(preview()).counts.length, 4);
  for (const value of [
    preview({ counts: [] }),
    preview({ counts: [counts[0], counts[0], counts[2], counts[3]] }),
    preview({ counts: [{ ...counts[0], table: "tasks" }, ...counts.slice(1)] }),
    preview({ counts: [{ ...counts[0], deleted: 1 }, ...counts.slice(1)] }),
    preview({ counts: [{ ...counts[0], more_candidates: true }, ...counts.slice(1)] }),
    preview({ confirmation_token: "invalid" }),
    preview({ expires_at: "not-a-date" }),
    preview({ automatic_cleanup: true }),
  ])
    assert.throws(() => parseRetentionPreview(value));
});

test("永久保留的表不允许返回待删除行", () => {
  assert.throws(() => parseRetentionPreview(preview({ policy: { ...policy, usage_days: 0 } })));
  const allZero = preview({
    policy: { ...policy, usage_days: 0, log_days: 0, notification_days: 0 },
    counts: counts.map((row) => ({ ...row, selected: 0 })),
  });
  const current = parseRetentionPreview(allZero);
  const result = leaseRetentionPreview(current, 100, wall, 200, 900);
  assert.equal(retentionExecutionBlock(result.lease, current.policy, false, wall, 200), "empty");
});

test("租约不含令牌，等待时间和传输安全余量扣减有效期", () => {
  const data = leaseRetentionPreview(parseRetentionPreview(preview()), 100, wall, 300, 900);
  assert.equal(data.token, "a".repeat(64));
  assert.equal(Object.hasOwn(data.lease, "confirmation_token"), false);
  assert.equal(JSON.stringify(data.lease).includes(data.token), false);
  assert.ok(retentionRemaining(data.lease, wall, 300) < 900000);
  assert.equal(retentionRemaining(data.lease, wall + 901000, 300), 0);
  assert.equal(retentionRemaining(data.lease, wall - 3600000, 901000), 0);
  assert.equal(retentionRemaining(data.lease, Number.NaN, 300), 0);
});

test("执行前独立检查未保存、关闭、策略变化、过期和缺失预览", () => {
  const current = lease();
  assert.equal(retentionExecutionBlock(current, policy, false, wall, 200), null);
  assert.equal(retentionExecutionBlock(null, policy, false, wall, 200), "noPreview");
  assert.equal(retentionExecutionBlock(current, policy, true, wall, 200), "dirty");
  assert.equal(retentionExecutionBlock(current, { ...policy, enabled: false }, false, wall, 200), "disabled");
  assert.equal(retentionExecutionBlock(current, { ...policy, log_days: 60 }, false, wall, 200), "changed");
  assert.equal(retentionExecutionBlock(current, policy, false, wall + 901000, 200), "expired");
  assert.equal(RETENTION_CONFIRMATION, "DELETE_PREVIEWED_ROWS");
});

test("成功响应必须提交且逐表数量守恒，不以预览冒充删除结果", () => {
  const snapshot = lease();
  const result = { committed: true, counts: counts.map((row) => ({ ...row, deleted: 1, skipped: 1 })) };
  assert.equal(parseRetentionExecution(result, snapshot).committed, true);
  assert.throws(() => parseRetentionExecution({ ...result, committed: false }, snapshot));
  assert.throws(() => parseRetentionExecution({ ...result, counts: [] }, snapshot));
  assert.throws(() =>
    parseRetentionExecution({ ...result, counts: result.counts.map((row) => ({ ...row, deleted: 2 })) }, snapshot),
  );
  assert.throws(() =>
    parseRetentionExecution({ ...result, counts: result.counts.map((row) => ({ ...row, selected: 3 })) }, snapshot),
  );
});

test("审计缺失、重复 ID 或超限不当作空历史", () => {
  const entry = {
    id: 1,
    at: "2026-10-17T12:00:00Z",
    action: "execute_committed",
    plan_hash: "b".repeat(64),
    actor: "authenticated_operator",
    detail: counts,
  };
  assert.equal(parseRetentionAudit({ entries: [entry], limit: 100 }).length, 1);
  assert.deepEqual(parseRetentionAudit({ entries: [], limit: 100 }), []);
  assert.throws(() => parseRetentionAudit({ limit: 100 }));
  assert.throws(() => parseRetentionAudit({ entries: [entry, entry], limit: 100 }));
  assert.throws(() =>
    parseRetentionAudit({ entries: Array.from({ length: 101 }, (_, id) => ({ ...entry, id: id + 1 })), limit: 100 }),
  );
});
