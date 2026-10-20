"use client";

// 一行式工具行（24px）。
//
// 规格来自 dsh 的 ToolRow：图标 16 + 工具名 13/24 + 2×2 分隔点 + 摘要截断；
// hover 时左侧图标交叉淡出成 chevron，点击才展开输出。密度优先：默认不占
// 竖向空间，几十次工具调用也能一屏看完。

import * as React from "react";

import { ChevronRightIcon } from "lucide-react";

import type { ToolItem } from "@/lib/harness/types";
import { cn } from "@/lib/utils";

import { DiffBlock } from "./diff-block";

function toolGlyph(name: string): string {
  const t = name.toLowerCase();
  if (/(shell|bash|exec|powershell|cmd|terminal)/.test(t)) return ">_";
  if (/(write|edit|patch|replace|apply|mkdir)/.test(t)) return "±";
  if (/(http|fetch|request|search|browse|web)/.test(t)) return "⇅";
  if (/(read|list|glob|grep|cat)/.test(t)) return "◎";
  return "•";
}

export function ToolRow({ item }: { item: ToolItem }) {
  const [open, setOpen] = React.useState(false);
  const hasBody = Boolean(item.output) || Boolean(item.patch);

  return (
    <div className="group/tool">
      <button
        type="button"
        onClick={() => hasBody && setOpen((v) => !v)}
        aria-expanded={open}
        className={cn(
          "flex h-6 w-full min-w-0 items-center rounded-sm px-1.5 text-left transition-colors",
          "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
          !hasBody && "cursor-default",
        )}
      >
        <span className="relative flex size-4 shrink-0 items-center justify-center font-mono text-[11px]">
          <span className="transition-opacity duration-100 group-hover/tool:opacity-0">{toolGlyph(item.tool)}</span>
          <ChevronRightIcon className="absolute size-3.5 opacity-0 transition-opacity duration-100 group-hover/tool:opacity-100" />
        </span>
        <span className="ml-1.5 shrink-0 text-[13px] leading-6">{item.tool}</span>
        <span className="mx-2 size-0.5 shrink-0 rounded-full bg-muted-foreground/60" />
        <span className="min-w-0 flex-1 truncate text-[13px] leading-6">{item.args || item.output.slice(0, 120)}</span>
        {item.isError ? (
          <span className="ml-2 shrink-0 font-mono text-[11px] text-[var(--hx-error)]">失败</span>
        ) : item.done ? (
          <span className="ml-2 shrink-0 font-mono text-[11px] text-[var(--hx-ok)]">完成</span>
        ) : (
          <span className="ml-2 shrink-0 font-mono text-[11px] text-[var(--hx-warn)]">运行中</span>
        )}
      </button>

      {open && hasBody ? (
        <div className="mt-1 mb-2 pl-[22px]">
          {item.patch ? (
            // 输出里能认出 unified diff 就直接按 diff 渲染，别把补丁当纯文本糊在屏幕上。
            <DiffBlock patch={item.patch} compact />
          ) : (
            <pre className="harness-code max-h-80 overflow-auto rounded-lg border bg-[var(--hx-code-bg)] p-3 text-[11.5px] leading-[18px]">
              {item.output}
            </pre>
          )}
        </div>
      ) : null}
    </div>
  );
}
