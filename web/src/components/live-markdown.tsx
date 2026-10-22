// 流式正文的 markdown 渲染。要同时解决两件事：
//
//   1) **语法只写到一半**。模型一次只吐几个字，任何时刻的文本都可能停在
//      ``` 未闭合、`**` 只有单数个、`[文字](` 没关括号的地方。直接交给
//      react-markdown，轻则整段降级成普通段落，重则把后文全吞进代码块里。
//      所以渲染前先把「看起来还没写完」的部分补齐；等真正的闭合符到达时，
//      补出来的形状自然被替换掉，已渲染的内容不会跳变。
//
//   帧率节流不在这里：同一份正文渲染（components/transcript/answer-text.tsx）
//   自己做 120ms 节流，这个文件只负责把“写了一半”的语法补齐。
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
