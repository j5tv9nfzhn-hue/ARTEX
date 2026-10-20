"use client";

// 左栏：工作区 + 新建 + 按「要不要你动手」分组的会话列表。
//
// 排序不是时间线而是状态机（Needs input 置顶），这是 Claude Code 侧栏的核心
// 结论：用户第一眼要回答的是「哪个会话在等我」，不是「最新说了什么」。

import * as React from "react";

import { FolderOpenIcon, PlusIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { Agent, Conversation } from "@/lib/types";
import { cn } from "@/lib/utils";

type Group = { key: string; label: string; rows: Conversation[] };

function relTime(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "";
  const d = (Date.now() - t) / 1000;
  if (d < 60) return "刚刚";
  if (d < 3600) return `${Math.floor(d / 60)} 分前`;
  if (d < 86400) return `${Math.floor(d / 3600)} 小时前`;
  return `${Math.floor(d / 86400)} 天前`;
}

export interface ConversationListProps {
  conversations: Conversation[];
  agents: Map<string, Agent>;
  activeId: number | null;
  hasPending: boolean;
  workspace: string;
  onSelect: (id: number) => void;
  onCreate: () => void;
}

export function ConversationList({
  conversations,
  agents,
  activeId,
  hasPending,
  workspace,
  onSelect,
  onCreate,
}: ConversationListProps) {
  const groups = React.useMemo<Group[]>(() => {
    const running = conversations.filter((c) => c.running);
    const idle = conversations.filter((c) => !c.running);
    return [
      { key: "needs", label: "需要你", rows: activeId && hasPending ? running.filter((c) => c.id === activeId) : [] },
      { key: "run", label: "进行中", rows: running.filter((c) => !(activeId && hasPending && c.id === activeId)) },
      { key: "idle", label: "空闲", rows: idle },
    ].filter((g) => g.rows.length > 0);
  }, [conversations, activeId, hasPending]);

  return (
    <aside className="flex min-h-0 w-[266px] shrink-0 flex-col border-r bg-sidebar">
      <div className="flex items-center gap-2 px-3.5 pt-3.5 pb-2">
        <span className="flex size-6 items-center justify-center rounded-md bg-primary font-bold text-[12px] text-primary-foreground">
          A
        </span>
        <b className="text-[12.5px]">ARTEX Harness</b>
        <span className="ml-auto font-mono text-[10.5px] text-muted-foreground">WORK</span>
      </div>

      <div className="mx-2.5 mb-2 flex items-center gap-2 rounded-md border bg-background px-2.5 py-2">
        <FolderOpenIcon className="size-3.5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="truncate font-medium text-[12px]">工作区</div>
          <div className="truncate font-mono text-[10.5px] text-muted-foreground">{workspace || "工作区根目录"}</div>
        </div>
      </div>

      <Button
        variant="outline"
        size="sm"
        onClick={onCreate}
        className="mx-2.5 mb-2 justify-start gap-1.5 border-dashed"
      >
        <PlusIcon className="size-3.5" /> 新建会话
      </Button>

      <div className="harness-scroll min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {groups.map((g) => (
          <div key={g.key} className="mb-1.5">
            <div className="flex items-center gap-1.5 px-1.5 py-1 font-semibold text-[10.5px] text-muted-foreground uppercase tracking-wide">
              <span
                className={cn(
                  "size-1 rounded-full",
                  g.key === "needs" && "bg-[var(--hx-warn)]",
                  g.key === "run" && "bg-[var(--hx-ok)]",
                )}
              />
              {g.label}
              <span className="font-mono opacity-70">{g.rows.length}</span>
            </div>
            {g.rows.map((c) => {
              const agent = agents.get(c.agent_key);
              const on = c.id === activeId;
              return (
                <button
                  key={c.id}
                  type="button"
                  onClick={() => onSelect(c.id)}
                  className={cn(
                    "mb-0.5 block w-full rounded-md border px-2 py-1.5 text-left transition-colors",
                    on
                      ? "border-border bg-background shadow-[inset_2px_0_0_var(--primary)]"
                      : "border-transparent hover:bg-accent/60",
                  )}
                >
                  <span className="block truncate font-medium text-[12.5px]">{c.title || "未命名会话"}</span>
                  <span className="mt-0.5 flex items-center gap-1.5 text-[10.5px] text-muted-foreground">
                    <span className="rounded-sm bg-muted px-1 font-mono">{agent?.name ?? c.agent_key}</span>
                    {on && hasPending ? (
                      <span className="inline-flex items-center gap-1 text-[var(--hx-warn)]">
                        <span className="size-1 rounded-full bg-[var(--hx-warn)]" /> 待审批
                      </span>
                    ) : (
                      <span>{relTime(c.updated_at)}</span>
                    )}
                  </span>
                </button>
              );
            })}
          </div>
        ))}
        {!conversations.length ? (
          <p className="px-2 py-6 text-center text-[12px] text-muted-foreground">还没有会话，点上面的按钮开始。</p>
        ) : null}
      </div>

      <div className="flex items-center gap-2 border-t px-3 py-2 text-[11px] text-muted-foreground">
        <a href="/chat" className="hover:text-foreground">
          返回控制台
        </a>
        <span className="ml-auto font-mono">v1</span>
      </div>
    </aside>
  );
}
