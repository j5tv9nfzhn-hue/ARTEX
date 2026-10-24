"use client";

import * as React from "react";

import { repairStreamMarkdown } from "@/components/live-markdown";
import { Markdown } from "@/components/markdown";
import { cn } from "@/lib/utils";

// 流式正文每 40ms 来一帧，而一次 markdown 重建很贵。渲染节流到 ~8fps，
// 既能看出“打字机”效果，又不会把主线程占满。
const RENDER_EVERY_MS = 120;

// ---- 流式正文的分块增量渲染 -------------------------------------------------
//
// 之前每 120ms 把「累计全文」整段丢给 react-markdown：正文跑到几千字，就是每秒 8
// 次全量解析 + 全量 DOM diff，长任务把主线程占住，高度变化只能排队处理——视觉上
// 正文在抽。现在按「最近一个空行」把文本切成两段：
//   - 稳定前缀（写完的段落）→ 交给 Markdown，并用 memo 锁住：前缀字符串没变就不重新
//     解析，所以每帧成本从 O(全文) 降到 O(新增的一行)；
//   - 未完成的尾巴 → 直接当纯文本渲染，不做任何 Markdown 解析。
// 切点若落在未闭合的代码块里就往前退，保证前缀本身是自洽的。

/** 前缀里是否有未闭合的 ``` 代码块。 */
function fenceOpen(s: string): boolean {
  let n = 0;
  let i = s.indexOf("```");
  while (i !== -1) {
    n++;
    i = s.indexOf("```", i + 3);
  }
  return n % 2 === 1;
}

/**
 * splitStableMarkdown 把流式文本切成「可以定稿的前缀」+「还没写完的尾巴」。
 *
 * 只认空行作切点：段内换行可能是列表/引用/表格的一部分，切在段中会让前缀解析出
 * 半截结构，视觉上比不切更抖。找不到合适的切点（比如整段都在一个代码块里）就整段
 * 当尾巴，先按纯文本显示，等代码块闭合再升级成 Markdown。
 */
export function splitStableMarkdown(text: string): { stable: string; tail: string } {
  let cut = text.lastIndexOf("\n\n");
  // 退让次数设上限：极端输入（超长代码块里全是空行）不至于退化成 O(n^2)。
  for (let guard = 0; cut > 0 && guard < 16; guard++) {
    const head = text.slice(0, cut);
    if (!fenceOpen(head)) return { stable: head, tail: text.slice(cut + 2) };
    cut = text.lastIndexOf("\n\n", cut - 1);
  }
  return { stable: "", tail: text };
}

/** 前缀字符串没变时直接跳过解析——这正是把「每帧全量解析」变没的关键。 */
const MemoMarkdown = React.memo(Markdown);

/** 把频繁变化的文本压到 ms 一次：领先写一次，之后每 ms 至少一次，取最新值。 */
function useThrottledText(text: string, ms: number): string {
  const [shown, setShown] = React.useState(text);
  const latest = React.useRef(text);
  const lastAt = React.useRef(0);
  const timer = React.useRef<number | null>(null);
  const raf = React.useRef<number | null>(null);
  latest.current = text;

  // 提交动作统一落到「下一帧」：定时器只说明窗口到了，真正挂载发生在
  // requestAnimationFrame 里，与实时流的批量刷新同一个节拍。否则会出现
  // 「半帧 setState → 同一帧内又被新 token 顶掉 → 一帧两次重排」，视觉上就是正文在闪。
  const commit = React.useCallback(() => {
    if (raf.current !== null) return;
    raf.current = window.requestAnimationFrame(() => {
      raf.current = null;
      lastAt.current = Date.now();
      setShown(latest.current);
    });
  }, []);

  React.useEffect(() => {
    if (shown === text) return;
    const wait = Math.max(0, ms - (Date.now() - lastAt.current));
    if (wait === 0) {
      commit();
      return;
    }
    if (timer.current !== null) return; // 已有定时器在等，到点取最新值
    timer.current = window.setTimeout(() => {
      timer.current = null;
      commit();
    }, wait);
  }, [text, shown, ms, commit]);

  React.useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
      if (raf.current !== null) window.cancelAnimationFrame(raf.current);
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
  const split = React.useMemo(
    () => (streaming ? splitStableMarkdown(shown) : { stable: shown, tail: "" }),
    [streaming, shown],
  );
  // 修复只在「前缀真的变了」（= 又定稿一段）时跑一次，而不是每帧都扫一遍全文。
  const stable = React.useMemo(
    () => (streaming && split.stable ? repairStreamMarkdown(split.stable) : split.stable),
    [streaming, split.stable],
  );
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
      {isError ? (
        <span>
          {split.stable}
          {split.tail}
        </span>
      ) : (
        <>
          {stable ? <MemoMarkdown text={stable} /> : null}
          {split.tail ? <div className="whitespace-pre-wrap break-words text-sm leading-relaxed">{split.tail}</div> : null}
        </>
      )}
      {streaming && (
        <span
          aria-hidden
          className="ml-0.5 inline-block h-3 w-1 animate-pulse rounded-xs bg-primary align-middle"
        />
      )}
    </div>
  );
}
