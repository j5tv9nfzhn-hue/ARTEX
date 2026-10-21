"use client";

import * as React from "react";

import { Markdown } from "@/components/markdown";

// 流式正文的 markdown 渲染。要同时解决两件事：
//
//   1) **语法只写到一半**。模型一次只吐几个字，任何时刻的文本都可能停在
//      ``` 未闭合、`**` 只有单数个、`[文字](` 没关括号的地方。直接交给
//      react-markdown，轻则整段降级成普通段落，重则把后文全吞进代码块里。
//      所以渲染前先把「看起来还没写完」的部分补齐；等真正的闭合符到达时，
//      补出来的形状自然被替换掉，已渲染的内容不会跳变。
//
//   2) **帧率**。增量是 40ms 一帧（约 25fps），而一次 markdown 解析是整棵树
//      重建。这里用 120ms 的节流窗口（约 8fps）渲染：文字仍在滚，重排次数降到 1/3。
const RENDER_EVERY_MS = 120;

const FENCE_RE = /^ {0,3}(`{3,}|~{3,})/;

function count(hay: string, needle: string): number {
  let n = 0;
  let i = hay.indexOf(needle);
  while (i !== -1) {
    n++;
    i = hay.indexOf(needle, i + needle.length);
  }
  return n;
}

/** 去掉围栏代码块，只留正文——行内语法统计不能被代码里的括号/星号带偏。 */
function stripFences(src: string): string {
  let out = "";
  let open: string | null = null;
  for (const line of src.split("\n")) {
    const m = FENCE_RE.exec(line);
    if (m) {
      const f = m[1];
      if (!open) {
        open = f;
        continue;
      }
      if (f[0] === open[0] && f.length >= open.length) {
        open = null;
        continue;
      }
    }
    if (!open) out += line + "\n";
  }
  return out;
}

/** 把「写到一半」的 markdown 补成一份可以安全渲染的完整文档。 */
export function repairStreamMarkdown(src: string): string {
  if (!src) return "";
  let out = src;

  // 末尾正好是半截围栏（"``" / "~~"）时先削掉：留着它会被当成正文，
  // 而补出来的闭合围栏反而落进了代码块里。
  const partialFence = /(^|\n) {0,3}(`{1,2}|~{1,2})$/.exec(out);
  if (partialFence) out = out.slice(0, out.length - partialFence[2].length);

  // 未闭合的围栏代码块：补一行同字符、不短于开头的围栏。
  let open: string | null = null;
  for (const line of out.split("\n")) {
    const m = FENCE_RE.exec(line);
    if (!m) continue;
    const f = m[1];
    if (!open) open = f;
    else if (f[0] === open[0] && f.length >= open.length) open = null;
  }
  if (open) return out + "\n" + open; // 代码块内部就是源码，不再做行内补全

  // 行内语法：补闭合符而不是整段回退——已经渲染出来的部分不会闪。
  const prose = stripFences(out);
  if (count(prose, "`") % 2 === 1) out += "`";
  if (count(prose, "**") % 2 === 1) out += "**";
  const brackets = count(prose, "[") - count(prose, "]");
  if (brackets > 0) out += "]".repeat(brackets);
  const parens = count(prose, "](") - count(prose, ")");
  if (parens > 0) out += ")".repeat(parens);
  return out;
}

/**
 * 把高频文本压到 ms 一档：首次立即生效（leading），之后每 ms 最多一次，
 * 且在窗口结束时取当时最新的值（trailing）——不会因为一直在变而永远不渲染。
 */
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
    if (timer.current !== null) return; // 已有定时器在等，到点会取最新值
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
 * 运行中的正文：markdown 实时渲染 + 行尾光标。
 *
 * 落库之后同一条消息会换成 Transcript 里的 AnswerBlock（同样是 Markdown），
 * 所以两者的排版是一致的——切换时不会出现「流式时是素文本、结束后突然变成
 * 标题/列表」的跳变。
 */
export function LiveStreamMarkdown({ text, className }: { text: string; className?: string }) {
  const shown = useThrottledText(text, RENDER_EVERY_MS);
  const repaired = React.useMemo(() => repairStreamMarkdown(shown), [shown]);
  return (
    <div className={className}>
      <Markdown text={repaired} />
      <span
        aria-hidden
        className="mt-0.5 inline-block h-3 w-1 animate-pulse rounded-xs bg-primary align-middle"
      />
    </div>
  );
}
