import { structuredPatch } from "diff";

// 行级差异统一走 jsdiff 的 structuredPatch，不自己写 LCS：
// 上下文合并、hunk 切分、"\\ No newline" 这些边界都由成熟库处理，
// 我们只把它整理成渲染需要的形状。
export interface DiffLine {
  kind: "add" | "del" | "ctx" | "skip";
  text: string;
  oldNo: number | null;
  newNo: number | null;
  // 只在 kind === "skip" 时存在：被折叠掉的未变更行，展开时原样插回来。
  hidden?: DiffLine[];
}

export interface DiffHunk {
  key: string;
  header: string;
  lines: DiffLine[];
}

export interface DiffModel {
  hunks: DiffHunk[];
  added: number;
  removed: number;
}

const NO_NEWLINE = "\\ No newline at end of file";

// buildDiff 生成「带折叠」的差异模型：变更行前后各留 context 行，中间连续未变更的部分
// 折成一条 skip；展开时再插回来，所以不需要第二次 diff。
export function buildDiff(oldText: string, newText: string, context = 3): DiffModel {
  const patch = structuredPatch("a", "b", oldText, newText, "", "", { context: Number.MAX_SAFE_INTEGER });
  const hunks: DiffHunk[] = [];
  let added = 0;
  let removed = 0;

  patch.hunks.forEach((hunk, index) => {
    const lines: DiffLine[] = [];
    let oldNo = hunk.oldStart;
    let newNo = hunk.newStart;
    for (const raw of hunk.lines) {
      if (raw === NO_NEWLINE) continue;
      const marker = raw.slice(0, 1);
      const text = raw.slice(1);
      if (marker === "+") {
        lines.push({ kind: "add", text, oldNo: null, newNo });
        newNo += 1;
        added += 1;
      } else if (marker === "-") {
        lines.push({ kind: "del", text, oldNo, newNo: null });
        oldNo += 1;
        removed += 1;
      } else {
        lines.push({ kind: "ctx", text, oldNo, newNo });
        oldNo += 1;
        newNo += 1;
      }
    }
    hunks.push({
      key: `${index}:${hunk.oldStart}:${hunk.newStart}`,
      header: hunkHeader(hunk.oldStart, hunk.oldLines, hunk.newStart, hunk.newLines),
      lines: foldContext(lines, context),
    });
  });

  return { hunks, added, removed };
}

function hunkHeader(oldStart: number, oldLines: number, newStart: number, newLines: number): string {
  return `@@ -${oldStart},${oldLines} +${newStart},${newLines} @@`;
}

// foldContext 只折叠「离任何变更都超过 context 行」的连续未变更行；
// 距离计算是双向的（前 context 行、后 context 行都要留），所以先算每行到最近变更的距离。
function foldContext(lines: DiffLine[], context: number): DiffLine[] {
  const keep = lines.map((line) => line.kind !== "ctx");
  const distance = new Array<number>(lines.length).fill(Number.POSITIVE_INFINITY);
  let last = Number.NEGATIVE_INFINITY;
  for (let i = 0; i < lines.length; i += 1) {
    if (!keep[i]) continue;
    distance[i] = 0;
    last = i;
    continue;
  }
  for (let i = 0; i < lines.length; i += 1) {
    if (lines[i].kind === "ctx" && i - last <= context) distance[i] = i - last;
  }
  for (let i = lines.length - 1, next = Number.POSITIVE_INFINITY; i >= 0; i -= 1) {
    if (lines[i].kind !== "ctx") {
      next = i;
      continue;
    }
    if (next - i <= context) distance[i] = Math.min(distance[i], next - i);
  }

  const out: DiffLine[] = [];
  let buffer: DiffLine[] = [];
  const flush = () => {
    if (buffer.length === 0) return;
    out.push({ kind: "skip", text: "", oldNo: buffer[0].oldNo, newNo: buffer[0].newNo, hidden: buffer });
    buffer = [];
  };
  lines.forEach((line, index) => {
    if (distance[index] <= context) {
      flush();
      out.push(line);
      return;
    }
    buffer.push(line);
  });
  flush();
  return out;
}

// apply_patch 的补丁文档本身就是 +/-/空格 三态，直接解析成差异行，不需要再算一次 diff。
export interface PatchFileDiff {
  path: string;
  lines: DiffLine[];
  added: number;
  removed: number;
}

export function parsePatchDocument(text: string): PatchFileDiff[] {
  const files: PatchFileDiff[] = [];
  // 用「当前文件下标」而不是一个可为 null 的变量：push 在闭包里改状态，
  // TS 的控制流分析看不到闭包里的赋值，会把 current 一路窄化成 never。
  let currentIndex = -1;
  const push = (path: string) => {
    files.push({ path, lines: [], added: 0, removed: 0 });
    currentIndex = files.length - 1;
  };
  for (const raw of text.split("\n")) {
    const line = raw.replace(/\r$/, "");
    if (line.startsWith("*** ")) {
      if (line === "*** Begin Patch" || line === "*** End Patch") continue;
      const rest = line.replace(/^\*\*\* \w+ File: /, "");
      if (rest !== line) push(rest.trim());
      continue;
    }
    if (currentIndex < 0) continue;
    const current = files[currentIndex];
    if (line.startsWith("@@")) {
      current.lines.push({ kind: "ctx", text: line, oldNo: null, newNo: null });
      continue;
    }
    if (line.startsWith("+")) {
      current.added += 1;
      current.lines.push({ kind: "add", text: line.slice(1), oldNo: null, newNo: null });
      continue;
    }
    if (line.startsWith("-")) {
      current.removed += 1;
      current.lines.push({ kind: "del", text: line.slice(1), oldNo: null, newNo: null });
      continue;
    }
    current.lines.push({ kind: "ctx", text: line.replace(/^ /, ""), oldNo: null, newNo: null });
  }
  return files.filter((file) => file.lines.length > 0);
}
