"use client";

import * as React from "react";

import { repairStreamMarkdown } from "@/components/live-markdown";
import { Markdown } from "@/components/markdown";
import { cn } from "@/lib/utils";

// 流式正文每 40ms 来一帧，而一次 markdown 重建很贵。渲染节流到 ~8fps，
// 既能看出“打字机”效果，又不会把主线程占满。
const RENDER_EVERY_MS = 120;

/** 把频繁变化的文本压到 ms 一次：领先写一次，之后每 ms 至少一次，取最新值。 */
function useThrottledText(text: string, ms: number): string {
  const [shown, setShown] = React.useState(text);
  const latest = React.useRef(text);
  const lastAt = React.useRef(0);
  const timer = React.useRef<number | null>(null);
  latest.current = text;

  React.useEffect(() => {
    if (shown === text) return;
    const wait = Math.max(0, ms - (Date.now() - lastAt.current));
    if (wait === 0) {
      lastAt.current = Date.now();
      setShown(text);
      return;
    }
    if (timer.current !== null) return; // 已有定时器在等，到点取最新值
    timer.current = window.setTimeout(() => {
      timer.current = null;
      lastAt.current = Date.now();
      setShown(latest.current);
    }, wait);
  }, [text, shown, ms]);

  React.useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
    },
    [],
  );

  return shown;
}

/**
 * AnswerText 是“正文”的唯一渲染实现：历史回放（kind=result/text）与实时流共用
 * 同一个外壳、同一个 Markdown、同一套卡片样式。
 *
 * 之前实时走 LiveStreamMarkdown、历史走 AnswerBlock，两边外壳不同，于是一段正文在
 * 流式结束那一瞬会“换皮”，也就是 Memo 01 里推理/正文互相抢的余留。
 * 现在只差一个 streaming 标志：流式时先把“写了一半”的语法补齐再渲染，尾部加光标；
 * 结束后光标消失，其余一模一样。
 */
export function AnswerText({
  text,
  streaming = false,
  isError = false,
  className,
}: {
  text: string;
  /** true = 正在接收实时帧 */
  streaming?: boolean;
  isError?: boolean;
  className?: string;
}) {
  const shown = useThrottledText(text, streaming ? RENDER_EVERY_MS : 0);
  const body = streaming ? repairStreamMarkdown(shown) : shown;
  return (
    <div
      className={cn(
        "min-w-0 flex-1 break-words rounded-xl border px-4 py-3 shadow-2xs",
        isError
          ? "border-red-500/35 bg-red-500/8 text-red-600 text-sm dark:text-red-400"
          : "border-border/80 border-l-2 border-l-primary bg-muted/45",
        className,
      )}
    >
      {isError ? <span>{body}</span> : <Markdown text={body} />}
      {streaming && (
        <span
          aria-hidden
          className="ml-0.5 inline-block h-3 w-1 animate-pulse rounded-xs bg-primary align-middle"
        />
      )}
    </div>
  );
}
