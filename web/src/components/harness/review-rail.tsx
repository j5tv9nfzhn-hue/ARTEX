"use client";

// 右栏：变更 / 计划 / 文件。
//
// 变更来自时间线里携带 unified diff 的工具结果（后端目前没有独立的
// /conversations/{id}/changes；补上后这里换成一次拉取即可，其余不动）。
// 计划由 intent 活动折叠（见 lib/harness/types.ts#buildPlan）。

import * as React from "react";

import { FileTextIcon, GitCompareIcon, ListChecksIcon } from "lucide-react";

import { diffStat, parseUnifiedDiff } from "@/lib/harness/diff";
import type { PlanStep } from "@/lib/harness/types";
import type { HarnessChange } from "@/lib/harness/use-harness";
import type { WorkspaceEntry } from "@/lib/types";
import { cn } from "@/lib/utils";

import { DiffBlock } from "./diff-block";

export type RailTab = "changes" | "plan" | "files";

function fmtSize(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export interface ReviewRailProps {
  changes: HarnessChange[];
  plan: PlanStep[];
  entries: WorkspaceEntry[];
  onLoadWorkspace: (path: string) => Promise<void>;
}

export function ReviewRail({ changes, plan, entries, onLoadWorkspace }: ReviewRailProps) {
  const [tab, setTab] = React.useState<RailTab>("changes");
  const [open, setOpen] = React.useState<Record<string, boolean>>({});

  const done = plan.filter((p) => p.status === "done").length;

  return (
    <aside className="flex min-h-0 w-[372px] shrink-0 flex-col border-l bg-card">
      <div className="flex gap-0.5 px-2.5 pt-2.5">
        {(
          [
            { key: "changes", label: "变更", icon: GitCompareIcon, badge: changes.length },
            { key: "plan", label: "计划", icon: ListChecksIcon, badge: plan.length },
            { key: "files", label: "文件", icon: FileTextIcon, badge: 0 },
          ] as const
        ).map((t) => (
          <button
            key={t.key}
            type="button"
            onClick={() => {
              setTab(t.key);
              if (t.key === "files" && !entries.length) void onLoadWorkspace("");
            }}
            className={cn(
              "flex h-7 flex-1 items-center justify-center gap-1 rounded-md text-[11.5px] transition-colors",
              tab === t.key ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
            {t.badge ? <span className="rounded-sm bg-muted px-1 font-mono text-[9.5px]">{t.badge}</span> : null}
          </button>
        ))}
      </div>

      <div className="harness-scroll min-h-0 flex-1 overflow-y-auto p-2.5">
        {tab === "changes" ? (
          changes.length ? (
            changes.map((c) => {
              const rows = parseUnifiedDiff(c.patch);
              const stat = diffStat(rows);
              const isOpen = open[c.path] ?? changes.length === 1;
              return (
                <div key={c.path} className="mb-1.5 overflow-hidden rounded-md border bg-background">
                  <button
                    type="button"
                    onClick={() => setOpen((v) => ({ ...v, [c.path]: !isOpen }))}
                    className="flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-accent/50"
                  >
                    <span className="rounded-sm bg-[var(--hx-warn-soft)] px-1 font-mono text-[9.5px] text-[var(--hx-warn)]">
                      M
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-[11px]" dir="rtl">
                      {c.path}
                    </span>
                    <span className="shrink-0 font-mono text-[10px]">
                      <span className="text-[var(--hx-ok)]">+{stat.adds}</span>{" "}
                      <span className="text-[var(--hx-error)]">-{stat.dels}</span>
                    </span>
                  </button>
                  {isOpen ? (
                    <div className="border-t">
                      <DiffBlock patch={c.patch} compact />
                    </div>
                  ) : null}
                </div>
              );
            })
          ) : (
            <p className="px-2 py-10 text-center text-[11.5px] text-muted-foreground leading-5">
              agent 写入文件后，带 diff 的工具结果会出现在这里。
              <br />
              行内评论可在时间线里直接加。
            </p>
          )
        ) : null}

        {tab === "plan" ? (
          plan.length ? (
            <div className="px-1">
              <div className="mb-2 flex items-center gap-2">
                <span className="h-[3px] flex-1 overflow-hidden rounded-full bg-muted">
                  <span
                    className="block h-full rounded-full bg-primary"
                    style={{ width: `${Math.round((done / plan.length) * 100)}%` }}
                  />
                </span>
                <span className="font-mono text-[10px] text-muted-foreground">
                  {done}/{plan.length}
                </span>
              </div>
              {plan.map((p) => (
                <div key={p.id} className="flex items-start gap-2 rounded-md px-1.5 py-1">
                  <span
                    className={cn(
                      "mt-0.5 flex size-3.5 shrink-0 items-center justify-center rounded-sm border text-[9px]",
                      p.status === "done" && "border-[var(--hx-ok)] bg-[var(--hx-ok-soft)] text-[var(--hx-ok)]",
                      p.status === "active" && "border-[var(--hx-warn)]",
                    )}
                  >
                    {p.status === "done" ? "✓" : null}
                  </span>
                  <span
                    className={cn(
                      "text-[11.5px] leading-5",
                      p.status === "done" && "text-muted-foreground line-through",
                      p.status === "active" && "text-foreground",
                    )}
                  >
                    {p.title}
                  </span>
                </div>
              ))}
            </div>
          ) : (
            <p className="px-2 py-10 text-center text-[11.5px] text-muted-foreground leading-5">
              当前会话还没有计划。
              <br />
              agent 产出 intent 后会在这里跟踪。
            </p>
          )
        ) : null}

        {tab === "files" ? (
          entries.length ? (
            <div className="font-mono text-[11px]">
              {entries.map((e) => (
                <div key={e.path} className="flex items-center gap-1.5 rounded-sm px-1.5 py-0.5 hover:bg-accent/50">
                  <span className="w-3 shrink-0 text-center text-muted-foreground">{e.dir ? "▸" : "·"}</span>
                  <span className={cn("min-w-0 flex-1 truncate", e.dir && "text-foreground")}>{e.name}</span>
                  {!e.dir && e.size ? (
                    <span className="shrink-0 text-[10px] text-muted-foreground">{fmtSize(e.size)}</span>
                  ) : null}
                </div>
              ))}
            </div>
          ) : (
            <p className="px-2 py-10 text-center text-[11.5px] text-muted-foreground">工作区为空或暂不可读。</p>
          )
        ) : null}
      </div>
    </aside>
  );
}
