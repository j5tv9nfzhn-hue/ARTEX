"use client";

import { ChevronDown, ChevronRight } from "lucide-react";
import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * 推理（thinking）的唯一渲染实现 —— 实时流与历史回放共用。
 *
 * 改造前：实时走 LiveThinking（脉冲点 + 尾部预览），历史走 MessageBlock 的紫色
 * chip + BrainCircuitIcon，一条推理在流式结束的瞬间会跳成另一种样子，就是
 * Memo 01 里“又退回老样式”的来源。现在只有这一个组件：默认折叠、只显示尾部
 * 预览、点开看全文；实时时圆点脉冲，结束后静止。
 */
export function ThinkingRow({
  text,
  label,
  live = false,
  detail,
  fetchDetail,
  className,
}: {
  /** 预览来源（实时帧全文 / 历史 summary） */
  text: string;
  label: string;
  /** true = 正在流式接收 */
  live?: boolean;
  /** 已拿到的全文；缺省时点开才调用 fetchDetail */
  detail?: string;
  fetchDetail?: () => Promise<string>;
  className?: string;
}) {
  const [open, setOpen] = React.useState(false);
  const [full, setFull] = React.useState<string | null>(detail ?? null);
  const [loading, setLoading] = React.useState(false);

  const flat = text.replace(/\s+/g, " ").trim();
  const tail = flat.length > 200 ? "\u2026" + flat.slice(-200) : flat;
  const body = full ?? detail ?? text;

  function toggle() {
    const next = !open;
    setOpen(next);
    if (next && full === null && fetchDetail && !loading) {
      setLoading(true);
      fetchDetail()
        .then((d) => {
          if (d) setFull(d);
        })
        .catch(() => {})
        .finally(() => setLoading(false));
    }
  }

  return (
    <div className={cn("pl-3", className)}>
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        className="flex w-full min-w-0 items-center gap-1.5 text-left text-muted-foreground text-xs hover:text-foreground"
      >
        <span
          className={cn("size-1.5 shrink-0 rounded-full bg-violet-500", live && "animate-pulse")}
        />
        <span className="shrink-0 font-medium">{label}</span>
        {!open && <span className="min-w-0 flex-1 truncate opacity-70">{tail}</span>}
        <span className="shrink-0 opacity-60">
          {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
        </span>
      </button>
      {open && (
        <pre className="mt-1 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 text-ui-xs leading-relaxed">
          {loading && !full ? "\u2026" : body}
        </pre>
      )}
    </div>
  );
}
