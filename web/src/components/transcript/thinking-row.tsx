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
 *
 * 2026-10-24 抽搐修复（推理流是 SSE 里最密的一条：一行一行往外吐）：
 *
 *   1. 预览不再对**整段**推理做空白折叠。推理动辄上万字，每 40ms 一帧，等于每秒
 *      对整串做 25 次全量扫描 + 字符串分配；主线程被占住，后面的高度变化只能排队，
 *      观感就是越写越抽。现在只扫尾部固定窗口，成本与推理长度无关。
 *   2. 预览文字用「定高 + 绝对定位靠右」渲染：容器高度恒定，文字再涨也不会改变
 *      布局，只重绘这一小块。之前是 flex 里的一行 truncate，每帧的文字变化都会参与
 *      外层 flex 的重新排版。
 *   3. 预览显示的是**最新**的尾巴。之前用 truncate，省略号在右边，用户看到的永远是
 *      那 200 字窗口的**开头**，最新一行被挤在省略号后面——视觉上文字一直在左移。
 *   4. 展开态的 <pre> 自己钉在底部（内层滚动），外层容器高度不变，于是外层滚动条
 *      在推理流式期间完全不动，视觉焦点自然锁在最新一行。
 *
 * 注意：推理从来不走 Markdown（这里是 <pre> 纯文本），所以「Markdown 解析卡住」
 * 不是推理抽搐的原因；真正在做全量 Markdown 的是正文块（见 answer-text.tsx）。
 */

/** 折叠态：只在这段尾部窗口里做空白折叠。 */
const SCAN_WINDOW = 1200;
/** 折叠态预览字符数。 */
const PREVIEW_CHARS = 180;
/** 展开态最多渲染的字数：再长的 pre 只是排版负担，没有阅读价值。 */
const MAX_BODY_CHARS = 20000;
/** 展开态内层滚动区离底多少像素内仍算贴底。 */
const PRE_SLACK_PX = 24;

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

  const preview = React.useMemo(() => {
    const win = text.length > SCAN_WINDOW ? text.slice(-SCAN_WINDOW) : text;
    const flat = win.replace(/\s+/g, " ").trim();
    return flat.length > PREVIEW_CHARS ? "\u2026" + flat.slice(-PREVIEW_CHARS) : flat;
  }, [text]);

  const body = React.useMemo(() => {
    const src = full ?? detail ?? text;
    return src.length > MAX_BODY_CHARS ? "\u2026" + src.slice(-MAX_BODY_CHARS) : src;
  }, [full, detail, text]);

  // 展开态：推理还在涨，就把内层 <pre> 的滚动钉在底部。用户自己往上翻就停手。
  const preRef = React.useRef<HTMLPreElement>(null);
  const preStuck = React.useRef(true);
  React.useEffect(() => {
    if (!open || !live) return;
    const el = preRef.current;
    if (!el || !preStuck.current) return;
    const target = el.scrollHeight - el.clientHeight;
    if (target > 0 && Math.abs(el.scrollTop - target) >= 1) el.scrollTop = target;
  }, [open, live, body]);

  function onPreScroll() {
    const el = preRef.current;
    if (!el) return;
    preStuck.current = el.scrollTop + el.clientHeight >= el.scrollHeight - PRE_SLACK_PX;
  }

  function toggle() {
    const next = !open;
    setOpen(next);
    preStuck.current = true;
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
    <div className={cn("pl-3 [contain:content]", className)}>
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        className="flex w-full min-w-0 items-center gap-1.5 text-left text-muted-foreground text-xs hover:text-foreground"
      >
        <span className={cn("size-1.5 shrink-0 rounded-full bg-violet-500", live && "animate-pulse")} />
        <span className="shrink-0 font-medium">{label}</span>
        {!open && (
          // 定高 + 绝对定位：文字每帧变化只重绘这一块，不参与外层 flex 重排。
          // 右侧对齐 + 左侧渐隐，保证「最新的一行」始终可见。
          <span
            className="relative h-4 min-w-0 flex-1 overflow-hidden opacity-70"
            style={{ maskImage: "linear-gradient(to right, transparent, #000 1.5rem)" }}
          >
            <span className="absolute right-0 bottom-0 whitespace-nowrap">{preview}</span>
          </span>
        )}
        <span className="shrink-0 opacity-60">
          {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
        </span>
      </button>
      {open && (
        <pre
          ref={preRef}
          onScroll={onPreScroll}
          className="mt-1 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 text-ui-xs leading-relaxed [contain:content] [overflow-anchor:none]"
        >
          {loading && !full ? "\u2026" : body}
        </pre>
      )}
    </div>
  );
}
