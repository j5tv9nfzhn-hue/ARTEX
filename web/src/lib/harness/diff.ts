// unified diff 解析：渲染层只要「一行一条」的结构，不依赖 diff 库。
//
// 刻意不做行号对齐逻辑：harness 的窄栏里不显示旧行号（见设计文档 §06），
// 只保留 hunk 头、增删标记与文本。

export interface DiffRow {
  kind: "hunk" | "add" | "del" | "ctx";
  /** 新文件侧行号；hunk 行为 0。 */
  line: number;
  text: string;
}

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

export function parseUnifiedDiff(patch: string | undefined): DiffRow[] {
  if (!patch) return [];
  const rows: DiffRow[] = [];
  let newNo = 0;

  for (const raw of patch.split("\n")) {
    const line = raw.replace(/\r$/, "");
    const hunk = HUNK.exec(line);
    if (hunk) {
      newNo = Number(hunk[2]);
      rows.push({ kind: "hunk", line: 0, text: line });
      continue;
    }
    // 文件头（diff --git / --- / +++ / index）不进正文，避免窄栏噪音。
    if (/^(diff --git|index |--- |\+\+\+ )/.test(line)) continue;

    if (line.startsWith("+")) {
      rows.push({ kind: "add", line: newNo++, text: line.slice(1) });
    } else if (line.startsWith("-")) {
      rows.push({ kind: "del", line: newNo, text: line.slice(1) });
    } else {
      rows.push({ kind: "ctx", line: newNo++, text: line.replace(/^ /, "") });
    }
  }
  return rows;
}

/** 统计增删行数，用于徽标。 */
export function diffStat(rows: DiffRow[]): { adds: number; dels: number } {
  let adds = 0;
  let dels = 0;
  for (const r of rows) {
    if (r.kind === "add") adds += 1;
    else if (r.kind === "del") dels += 1;
  }
  return { adds, dels };
}

/** 从 diff 头推断文件路径（diff --git a/x b/x 或 +++ b/x）。 */
export function patchPath(patch: string | undefined): string | undefined {
  if (!patch) return undefined;
  const plus = /^\+\+\+ b\/(.+)$/m.exec(patch);
  if (plus) return plus[1].trim();
  const git = /^diff --git a\/(.+?) b\//m.exec(patch);
  return git ? git[1].trim() : undefined;
}
