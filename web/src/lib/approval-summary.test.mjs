import { parseApprovalSummary } from "./approval-summary.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("中文审批摘要保留工具名与 pending_id", () => {
  assert.deepEqual(parseApprovalSummary("工具 Bash 审批请求 (#42)"), { pendingId: 42, toolName: "Bash" });
  assert.deepEqual(parseApprovalSummary("工具 scope_check 审批请求 (#7)"), { pendingId: 7, toolName: "scope_check" });
});

test("缺失或无效标记不能生成审批 ID", () => {
  for (const summary of ["工具 Bash 审批请求", "工具 Bash 审批请求 (#invalid)"]) {
    assert.deepEqual(parseApprovalSummary(summary), { pendingId: null, toolName: "Bash" });
  }
});

test("未知摘要保留原文，不伪造工具名", () => {
  assert.deepEqual(parseApprovalSummary("自定义摘要 (#9)"), { pendingId: 9, toolName: "自定义摘要 (#9)" });
  assert.deepEqual(parseApprovalSummary(""), { pendingId: null, toolName: "" });
});
