// 半成品 JSON 的宽容读取：模型还在生成工具参数时，界面想先显示已经到手的字段。
//
// 为什么不用 JSON.parse + 补括号：工具入参经常是上千字符的多行内容（Write 的
// content、Edit 的 new_string），在任意位置补引号/括号都会解析失败或解析出错误
// 的值；而这里要的是「已经收到的每一项」，逐字符扫一遍就够。
//
// 扫描规则：
//   - 字符串值（含尚未闭合的最后一个）整段返回，未闭合的标记 partial；
//   - 数字 / true / false / null 也逐项返回，数字在缓冲末尾时同样算 partial；
//   - 嵌套对象与数组继续往里扫，键名带路径（identities[0].host），
//     所以「逐项」看到的是参数在 JSON 里的真实位置，而不是各级都叫 name；
//   - 同一个键出现多次（数组里的多个对象）时后者覆盖前者，预览够用。

export type PartialJsonFields = Record<string, string>;

export interface PartialEntry {
  key: string;
  value: string;
  // 值还在流（字符串没闭合 / 数字被缓冲截断）：界面可以据此画输入光标。
  partial: boolean;
}

export interface PartialScan {
  entries: PartialEntry[];
  // false = 还有没闭合的括号或没写完的键，说明这不是一份完整的 JSON。
  done: boolean;
}

interface ReadResult {
  value: string;
  next: number;
  closed: boolean;
}

interface Frame {
  kind: "obj" | "arr";
  index: number;
  pushed: boolean;
}

const SPACE = new Set([" ", "\n", "\r", "\t"]);
const LITERAL_STOP = new Set([",", "}", "]", " ", "\n", "\r", "\t"]);

function decodeEscape(ch: string): string {
  switch (ch) {
    case "n":
      return "\n";
    case "r":
      return "\r";
    case "t":
      return "\t";
    case "b":
      return "\b";
    case "f":
      return "\f";
    case '"':
      return '"';
    case "\\":
      return "\\";
    case "/":
      return "/";
    default:
      return "";
  }
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
      if (esc === "u") {
        const hex = text.slice(i + 2, i + 6);
        if (hex.length < 4) return { value: out, next: text.length, closed: false };
        const code = Number.parseInt(hex, 16);
        if (Number.isFinite(code)) out += String.fromCharCode(code);
        i += 6;
        continue;
      }
      out += decodeEscape(esc);
      i += 2;
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
  while (i < text.length && SPACE.has(text[i])) i += 1;
  return i;
}

// scanToolInput 扫一遍缓冲，返回「已经到手的每一项」。
export function scanToolInput(text: string): PartialScan {
  const entries: PartialEntry[] = [];
  const segments: string[] = [];
  const frames: Frame[] = [];
  let pendingKey: string | null = null;
  let i = 0;

  const label = (): string => {
    let out = "";
    for (const seg of segments) {
      if (seg.startsWith("[")) out += seg;
      else out += (out ? "." : "") + seg;
    }
    return out;
  };

  while (i < text.length) {
    const ch = text[i];
    if (SPACE.has(ch) || ch === "," || ch === ":") {
      i += 1;
      continue;
    }
    const frame = frames[frames.length - 1];
    if (ch === "{") {
      if (pendingKey !== null) {
        segments.push(pendingKey);
        pendingKey = null;
        frames.push({ kind: "obj", index: -1, pushed: true });
      } else if (frame && frame.kind === "arr") {
        segments.push(`[${frame.index}]`);
        frame.index += 1;
        frames.push({ kind: "obj", index: -1, pushed: true });
      } else {
        frames.push({ kind: "obj", index: -1, pushed: false });
      }
      i += 1;
      continue;
    }
    if (ch === "[") {
      let pushed = false;
      if (pendingKey !== null) {
        segments.push(pendingKey);
        pendingKey = null;
        pushed = true;
      }
      frames.push({ kind: "arr", index: 0, pushed });
      i += 1;
      continue;
    }
    if (ch === "}" || ch === "]") {
      const closing = frames.pop();
      if (closing && closing.pushed) segments.pop();
      i += 1;
      continue;
    }
    if (ch === '"') {
      const s = readString(text, i + 1);
      const j = skipSpace(text, s.next);
      if (text[j] === ":") {
        pendingKey = s.value;
        i = j + 1;
        continue;
      }
      // 这是一个值：对象里用刚读到的键，数组里用下标。
      const inArray = pendingKey === null && frame?.kind === "arr";
      if (pendingKey !== null) segments.push(pendingKey);
      const key = inArray ? `${label()}[${frame.index}]` : label();
      entries.push({ key, value: s.value, partial: !s.closed });
      if (pendingKey !== null) segments.pop();
      if (frame && frame.kind === "arr") frame.index += 1;
      pendingKey = null;
      if (!s.closed) return { entries, done: false };
      i = s.next;
      continue;
    }
    // 数字 / true / false / null
    let j = i;
    while (j < text.length && !LITERAL_STOP.has(text[j])) j += 1;
    const raw = text.slice(i, j);
    if (raw !== "") {
      const inArray = pendingKey === null && frame?.kind === "arr";
      if (pendingKey !== null) segments.push(pendingKey);
      const key = inArray ? `${label()}[${frame.index}]` : label();
      entries.push({ key, value: raw, partial: j >= text.length });
      if (pendingKey !== null) segments.pop();
      if (frame && frame.kind === "arr") frame.index += 1;
      pendingKey = null;
    }
    i = j;
  }
  return { entries, done: pendingKey === null && frames.length === 0 };
}

// parsePartialJson 返回叶子字段（路径最后一段）→ 文本，供 pickField 这类按名取值用。
export function parsePartialJson(text: string): PartialJsonFields {
  const fields: PartialJsonFields = {};
  for (const entry of scanToolInput(text).entries) {
    const cut = entry.key.lastIndexOf(".");
    fields[cut < 0 ? entry.key : entry.key.slice(cut + 1)] = entry.value;
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
