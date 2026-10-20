"use client";

// 时间线：Item 类型 → 渲染器。
//
// 与 Codex 的 Item 生命周期对齐：后端落库什么就渲染什么，运行中的骨架由
// Composer 上方的「运行中」条表达，不做假的打字动画。

import * as React from "react";

import { ChevronRightIcon } from "lucide-react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

import type { HarnessComment, HarnessItem } from "@/lib/harness/types";
import type { InterceptPending } from "@/lib/types";
import { cn } from "@/lib/utils";

import { ApprovalCard } from "./approval-card";
import { DiffBlock } from "./diff-block";
import { ToolRow } from "./tool-row";

const MD = [
  "text-[14px] leading-[22px] text-foreground",
  "[&_p]:my-2.5 [&_p:first-child]:mt-0 [&_p:last-child]:mb-0",
  "[&_ul]:my-2.5 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:my-2.5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-1",
  "[&_h1]:mt-5 [&_h1]:mb-2 [&_h1]:text-[16px] [&_h1]:font-semibold",
  "[&_h2]:mt-5 [&_h2]:mb-2 [&_h2]:text-[15px] [&_h2]:font-semibold",
  "[&_h3]:mt-4 [&_h3]:mb-1.5 [&_h3]:text-[14px] [&_h3]:font-semibold",
  "[&_a]:text-primary [&_a]:underline [&_a]:underline-offset-2",
  "[&_code]:rounded-sm [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[12.5px]",
  "[&_pre]:my-3 [&_pre]:overflow-auto [&_pre]:rounded-lg [&_pre]:border [&_pre]:bg-[var(--hx-code-bg)] [&_pre]:p-3",
  "[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-[11.5px] [&_pre_code]:leading-[18px]",
  "[&_blockquote]:my-2.5 [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground",
  "[&_table]:my-3 [&_table]:w-full [&_table]:border-collapse [&_th]:border [&_th]:px-2 [&_th]:py-1 [&_td]:border [&_td]:px-2 [&_td]:py-1",
].join(" ");

function Reasoning({ text }: { text: string }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="group flex h-6 items-center gap-1.5 text-[13px] text-muted-foreground transition-colors hover:text-foreground"
      >
        <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        <span>思考</span>
        <span className="text-[11px] opacity-70">{open ? "收起" : "展开"}</span>
      </button>
      {open ? (
        <p className="mt-1 whitespace-pre-wrap pl-[22px] text-[13px] text-muted-foreground leading-5">{text}</p>
      ) : null}
    </div>
  );
}

export interface TimelineProps {
  items: HarnessItem[];
  pending: InterceptPending[];
  onDecide: (d: "allowed" | "denied") => Promise<void>;
  comments: HarnessComment[];
  onComment: (path: string, line: number, text: string) => void;
  deciding?: boolean;
}

export function Timeline({ items, pending, onDecide, comments, onComment, deciding }: TimelineProps) {
  return (
    <div className="mx-auto flex w-full max-w-[820px] flex-col gap-2.5 px-6 py-5">
      {items.map((item) => {
        switch (item.type) {
          case "user":
            return (
              <div key={item.id} className="flex justify-end">
                <div className="max-w-[76%] whitespace-pre-wrap rounded-xl rounded-br-sm border bg-card px-3.5 py-2 text-[14px] leading-[22px]">
                  {item.text}
                </div>
              </div>
            );
          case "agent":
            return (
              <div key={item.id} className={MD}>
                <Markdown remarkPlugins={[remarkGfm]}>{item.text}</Markdown>
              </div>
            );
          case "reasoning":
            return <Reasoning key={item.id} text={item.text} />;
          case "tool":
            return <ToolRow key={item.id} item={item} />;
          case "diff":
            return (
              <div key={item.id} className="my-1">
                <div className="mb-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                  <span className="font-mono">{item.path}</span>
                </div>
                <DiffBlock patch={item.patch} commentPath={item.path} comments={comments} onComment={onComment} />
              </div>
            );
          case "result":
            return (
              <div
                key={item.id}
                className="flex items-center gap-3 border-t border-dashed pt-2 font-mono text-[11px] text-muted-foreground"
              >
                <span
                  className={cn("rounded-sm px-1.5", item.isError ? "bg-destructive/10 text-destructive" : "bg-muted")}
                >
                  turn {item.isError ? "中断" : "完成"}
                </span>
                {item.tokens ? <span>{item.tokens.toLocaleString("zh-CN")} tokens</span> : null}
              </div>
            );
          default:
            return (
              <div key={item.id} className="flex items-center gap-2 font-mono text-[11px] text-muted-foreground/80">
                <span className="size-1 rounded-full bg-muted-foreground/50" />
                {item.text}
              </div>
            );
        }
      })}

      {pending.length ? (
        <div className="mt-1">
          <ApprovalCard rows={pending} onDecide={onDecide} busy={deciding} />
        </div>
      ) : null}

      {!items.length ? (
        <div className="flex flex-col items-center gap-2 py-24 text-center text-muted-foreground">
          <span className="text-2xl opacity-50">◇</span>
          <p className="text-[13px]">开始一个任务。agent 会读写工作区文件、运行命令，越界前会先问你。</p>
        </div>
      ) : null}
    </div>
  );
}
