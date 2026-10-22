// 半成品 JSON 的宽容读取：模型还在生成工具参数时，界面想先显示已经到手的字段。
//
// 为什么不用 JSON.parse + 补括号：工具入参经常是上千字符的多行内容（Write 的
// content、Edit 的 new_string），在任意位置补引号/括号都会解析失败或解析出错误
// 的值；而这里只需要「已经收到的字符串字段」，逐字符扫一遍就够了。
//
// 只认 "key": "value" 这一种形态：
//   - 字符串值（含未闭合的最后一个）被记录；
//   - 数字/布尔/null/对象/数组本身不记录，但会继续往里扫，所以嵌套对象里的
//     字段同样能拿到（数组里多个对象时后者覆盖前者，用于预览足够）。

export type PartialJsonFields = Record<string, string>;

function decodeEscape(ch: string): string {
  switch (ch) {
    case "n":
      return "\n";
    case "r":
      return "\r";
    case "t":
      return "\t";
    case '"':
      return '"';
    case "\\":
      return "\\";
    case "/":
      return "/";
    default:
      // \uXXXX 之类的转义不还原：预览里出现原文比出现乱码好。
      return "";
  }
}

interface ReadResult {
  value: string;
  next: number;
  closed: boolean;
}

// readString 从 start（第一个正文字符）读到闭合引号；读到末尾说明这个值还在流。
function readString(text: string, start: number): ReadResult {
  let out = "";
  let i = start;
  while (i < text.length) {
    const ch = text[i];
    if (ch === "\\") {
      const esc = text[i + 1];
      if (esc === undefined) return { value: out, next: i, closed: false };
      out += decodeEscape(esc);
      i += esc === "u" ? 6 : 2;
      continue;
    }
    if (ch === '"') return { value: out, next: i + 1, closed: true };
    out += ch;
    i += 1;
  }
  return { value: out, next: text.length, closed: false };
}

function skipSpace(text: string, from: number): number {
  let i = from;
  while (i < text.length && (text[i] === " " || text[i] === "\n" || text[i] === "\r" || text[i] === "\t")) i += 1;
  return i;
}

// parsePartialJson 返回「已收到的字符串字段」。同一个 key 出现多次时以后者为准。
export function parsePartialJson(text: string): PartialJsonFields {
  const fields: PartialJsonFields = {};
  let i = 0;
  while (i < text.length) {
    const quote = text.indexOf('"', i);
    if (quote < 0) break;
    const key = readString(text, quote + 1);
    if (!key.closed) break; // key 本身都没收完，后面的不可信
    let j = skipSpace(text, key.next);
    if (text[j] !== ":") {
      i = key.next; // 这是一个值而不是 key（例如数组里的字符串）
      continue;
    }
    j = skipSpace(text, j + 1);
    if (text[j] === '"') {
      const value = readString(text, j + 1);
      fields[key.value] = value.value;
      if (!value.closed) break; // 当前值还在流，后面的字段还没开始
      i = value.next;
      continue;
    }
    i = j; // 非字符串值：跳过它本身，继续往对象/数组里面扫
  }
  return fields;
}

// pickField 按优先级取第一个非空字段：工具在不同版本里字段名不完全一致。
export function pickField(fields: PartialJsonFields, names: string[]): string {
  for (const name of names) {
    const value = fields[name];
    if (typeof value === "string" && value !== "") return value;
  }
  return "";
}

// countLines 数行数（末尾换行不算一行）。用于「正在写入 N 行」这类实时计数。
export function countLines(text: string): number {
  if (!text) return 0;
  const lines = text.split("\n");
  return text.endsWith("\n") ? lines.length - 1 : lines.length;
}
