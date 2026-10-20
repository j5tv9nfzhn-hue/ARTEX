"use client";

// 审批卡：阻塞对象，出现在时间线里，不是独立页。
//
// 数据直接来自 /api/intercept/pending（按 conversation_id 过滤），决策写回
// /api/intercept/pending/{id}/decide —— 审批策略始终在后端，前端只表达意图。
// 无循环动画：长时间等待时发光会变成噪音。

import * as React from "react";

import { ShieldAlertIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { InterceptPending } from "@/lib/types";

export interface ApprovalCardProps {
  rows: InterceptPending[];
  onDecide: (decision: "allowed" | "denied") => Promise<void>;
  busy?: boolean;
}

function cmdOf(row: InterceptPending): string {
  const input = row.tool_input ?? {};
  const pick = ["command", "cmd", "path", "url", "pattern", "query"];
  for (const k of pick) {
    const v = input[k];
    if (typeof v === "string" && v.trim()) return v;
  }
  try {
    return JSON.stringify(input).slice(0, 400);
  } catch {
    return "";
  }
}

export function ApprovalCard({ rows, onDecide, busy }: ApprovalCardProps) {
  const [pending, setPending] = React.useState(false);
  const head = rows[0];
  if (!head) return null;
  // 两个独立布尔，任一为真即禁用；不能换成 ??（false 会短路）。
  const disabled = Boolean(busy) || Boolean(pending);

  const run = async (decision: "allowed" | "denied") => {
    setPending(true);
    try {
      await onDecide(decision);
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="rounded-[20px] border border-[var(--hx-warn)] bg-card shadow-lg">
      <div className="flex items-center gap-2 rounded-t-[20px] bg-[var(--hx-warn-soft)] px-4 py-2.5 text-[13px] text-[var(--hx-warn)]">
        <ShieldAlertIcon className="size-4 shrink-0" />
        <span className="font-medium">需要批准：{head.tool_name}</span>
        <span className="ml-auto rounded-sm border border-[var(--hx-warn)] px-1.5 font-mono text-[10px]">HIGH</span>
      </div>

      <div className="flex flex-col gap-1.5 px-4 pt-3">
        <div className="break-all rounded-md border bg-[var(--hx-code-bg)] px-3 py-2 font-mono text-[13px] leading-5">
          {cmdOf(head)}
        </div>
        {head.reason ? <p className="text-[13px] text-muted-foreground leading-5">{head.reason}</p> : null}
        {rows.length > 1 ? (
          <p className="text-[11px] text-muted-foreground">还有 {rows.length - 1} 条待审批，处理完这条后继续。</p>
        ) : null}
      </div>

      <div className="flex items-center justify-end gap-2 px-4 py-3.5">
        <span className="mr-auto text-[11px] text-muted-foreground">批准会写入审计日志，可在控制台撤销</span>
        <Button variant="destructive" size="sm" disabled={disabled} onClick={() => void run("denied")}>
          拒绝
        </Button>
        <Button size="sm" disabled={disabled} onClick={() => void run("allowed")}>
          允许一次
        </Button>
      </div>
    </div>
  );
}
