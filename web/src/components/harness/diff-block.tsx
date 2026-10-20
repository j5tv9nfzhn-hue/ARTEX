"use client";

// unified diff 渲染。
//
// 不用独立行号列：harness 右栏只有 360px 左右，行号会吃掉 40px 且窄屏对齐困难。
// 做法是「- / + 前缀字符 + 3px 内嵌条 + 10% 底色」，默认 white-space: pre 保列。

import * as React from "react";

import { MessageSquarePlusIcon } from "lucide-react";

import { parseUnifiedDiff } from "@/lib/harness/diff";
import type { HarnessComment } from "@/lib/harness/types";
import { cn } from "@/lib/utils";

export interface DiffBlockProps {
  patch: string;
  /** 打开行内评论时传；不传则只读。 */
  commentPath?: string;
  comments?: HarnessComment[];
  onComment?: (path: string, line: number, text: string) => void;
  /** 右栏里更紧凑：隐藏行号、字号小一号。 */
  compact?: boolean;
}

export function DiffBlock({ patch, commentPath, comments = [], onComment, compact = false }: DiffBlockProps) {
  const rows = React.useMemo(() => parseUnifiedDiff(patch), [patch]);
  const [draft, setDraft] = React.useState<{ line: number; text: string } | null>(null);
  const inputRef = React.useRef<HTMLInputElement | null>(null);

  // 点开某一行时把焦点交给输入框；不用 autoFocus（a11y/noAutofocus）。
  React.useEffect(() => {
    if (draft) inputRef.current?.focus();
  }, [draft]);

  if (!rows.length) return null;

  const submit = () => {
    if (!draft || !commentPath || !onComment) return;
    const text = draft.text.trim();
    if (!text) {
      setDraft(null);
      return;
    }
    onComment(commentPath, draft.line, text);
    setDraft(null);
  };

  return (
    <div className="harness-diff overflow-hidden rounded-lg border bg-[var(--hx-code-bg)]">
      <div
        className={cn(
          "overflow-x-auto py-1.5 font-mono",
          compact ? "text-[10.5px] leading-[17px]" : "text-[11.5px] leading-[19px]",
        )}
      >
        {rows.map((row, i) => {
          const mine = comments.filter((c) => c.line === row.line);
          return (
            <React.Fragment key={`${row.kind}-${i}-${row.line}`}>
              {row.kind === "hunk" ? (
                <div className="px-3 text-muted-foreground/70">{row.text}</div>
              ) : (
                <div
                  className={cn(
                    "group/diff relative flex min-h-[1lh] whitespace-pre px-3",
                    row.kind === "add" && "bg-[var(--hx-add-bg)] shadow-[inset_3px_0_0_var(--hx-add-bar)]",
                    row.kind === "del" && "bg-[var(--hx-del-bg)] shadow-[inset_3px_0_0_var(--hx-del-bar)]",
                    row.kind === "add" && "text-[var(--hx-ok)]",
                    row.kind === "del" && "text-[var(--hx-error)]",
                    row.kind === "ctx" && "text-muted-foreground",
                  )}
                >
                  {!compact ? (
                    <span className="mr-2 w-8 shrink-0 select-none text-right text-[10px] text-muted-foreground/60 tabular-nums">
                      {row.line || ""}
                    </span>
                  ) : null}
                  <span className="shrink-0 select-none opacity-70">
                    {row.kind === "add" ? "+ " : row.kind === "del" ? "- " : "  "}
                  </span>
                  <span className="min-w-0">{row.text}</span>
                  {onComment && row.kind !== "del" ? (
                    <button
                      type="button"
                      title="评论这一行"
                      onClick={() => setDraft({ line: row.line, text: "" })}
                      className="absolute top-0 right-1.5 hidden size-4 items-center justify-center rounded-sm bg-primary text-primary-foreground group-hover/diff:flex"
                    >
                      <MessageSquarePlusIcon className="size-2.5" />
                    </button>
                  ) : null}
                </div>
              )}
              {draft && draft.line === row.line ? (
                <div className="flex items-start gap-1.5 border-primary border-l-2 bg-primary/10 px-2 py-1">
                  <input
                    ref={inputRef}
                    value={draft.text}
                    onChange={(e) => setDraft({ line: row.line, text: e.target.value })}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") submit();
                      if (e.key === "Escape") setDraft(null);
                    }}
                    placeholder={`评论第 ${row.line} 行，回车加入待发送`}
                    className="min-w-0 flex-1 bg-transparent text-[11px] outline-none placeholder:text-muted-foreground/70"
                  />
                  <button type="button" onClick={submit} className="text-[11px] text-primary">
                    加入
                  </button>
                </div>
              ) : null}
              {mine.map((c, ci) => (
                <div
                  key={ci}
                  className="border-primary border-l-2 bg-primary/10 px-2 py-1 text-[11px] text-foreground/90"
                >
                  {c.text}
                </div>
              ))}
            </React.Fragment>
          );
        })}
      </div>
    </div>
  );
}
