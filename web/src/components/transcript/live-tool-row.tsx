"use client";

import { ChevronDown, ChevronRight } from "lucide-react";
import * as React from "react";

import { DiffStat } from "@/components/transcript/diff-view";
import { StatusRing } from "@/components/transcript/status-ring";
import { useShellCapability } from "@/hooks/use-shell-capability";
import { execToolLabel } from "@/lib/shell-label";
import { countLines, type PartialEntry, parsePartialJson, pickField, scanToolInput } from "@/lib/partial-json";

const EDIT_TOOLS = new Set(["Edit", "MultiEdit", "Write", "apply_patch", "NotebookEdit"]);
const PATH_FIELDS = ["file_path", "path", "file", "target_file"];
const CONTENT_FIELDS = ["content", "file_text", "fileText"];
const NEW_FIELDS = ["new_string", "new_str", "new_source"];
const OLD_FIELDS = ["old_string", "old_str", "old_source"];
// 逐项预览里不铺原文的字段：它们动辄上千字符，行内只报「N 行」。
const BULK_FIELDS = new Set([...CONTENT_FIELDS, ...NEW_FIELDS, ...OLD_FIELDS, "patch", "body", "data"]);
const ITEM_LIMIT = 4;

interface LiveTarget {
  // 摘要行占用的字段名（逐项列表里不再重复它）
  key: string;
  text: string;
}

// liveTarget 取「这个工具在做什么」最直接的那个字段，作为摘要行右侧的一行字。
function liveTarget(tool: string, fields: Record<string, string>): LiveTarget {
  const pick = (names: string[]): LiveTarget => {
    for (const name of names) {
      const value = fields[name];
      if (typeof value === "string" && value !== "") return { key: name, text: value };
    }
    return { key: names[0] ?? "", text: "" };
  };
  if (tool === "Bash" || tool === "shell_open" || tool === "shell_send") return pick(["command", "input"]);
  if (tool === "Grep" || tool === "Glob") return pick(["pattern", "glob", "query"]);
  if (tool.startsWith("http_")) {
    const method = pickField(fields, ["method"]).toUpperCase() || "GET";
    const url = pickField(fields, ["url", "target", "host", "path"]);
    return { key: url ? "url" : "target", text: url ? `${method} ${url}` : "" };
  }
  if (EDIT_TOOLS.has(tool) || tool === "Read") return pick(PATH_FIELDS);
  return pick(Object.keys(fields));
}

// liveItems 把已经到手的参数排成「逐项」：键名 + 值，值还在流的那一项带光标。
// 同一个键出现多次时后面覆盖前面（数组里多个对象只会显示最后一项，预览够用）。
function liveItems(entries: PartialEntry[], skipKey: string): PartialEntry[] {
  const latest = new Map<string, PartialEntry>();
  for (const entry of entries) latest.set(entry.key, entry);
  const out: PartialEntry[] = [];
  for (const [key, entry] of latest) {
    if (key === skipKey) continue;
    if (key.includes("[") && !key.includes(".")) continue; // 纯标量数组元素：摘要行里没用
    out.push(entry);
  }
  return out.slice(0, ITEM_LIMIT);
}

// LiveToolRow 是「参数生成中」的预览行：正式的 tool_use 活动行要等模型把参数发完
// 才落库，在那之前这一行负责让人看见「它要干什么」以及「它还在往外吐每一项」。
//
// 计数只做行数（写入 N 行 / 改 +A −D），不在这里算 diff：参数还是半成品，逐帧跑
// jsdiff 既慢又会在末尾给出会变的噪声数字；定稿后的真实红绿差异仍由 DiffBody 画。
// 工具调用默认折叠（Memo 02 ②）：平时只占一行，左侧图标就是实时圆圈，
// 参数流式增量只在展开后才逐行出现。
export function LiveToolRow({ tool, args }: { tool: string; args: string }) {
  const [open, setOpen] = React.useState(false);
  // 同 tool-summary-row：显示名走运行时探测，逻辑仍用原始 tool 名。
  const shell = useShellCapability();
  const fields = React.useMemo(() => parsePartialJson(args), [args]);
  const entries = React.useMemo(() => scanToolInput(args).entries, [args]);
  const target = React.useMemo(() => liveTarget(tool, fields), [tool, fields]);
  const items = React.useMemo(() => liveItems(entries, target.key), [entries, target.key]);

  let added = 0;
  let removed = 0;
  if (tool === "Write") {
    added = countLines(pickField(fields, CONTENT_FIELDS));
  } else if (tool === "Edit" || tool === "NotebookEdit") {
    added = countLines(pickField(fields, NEW_FIELDS));
    removed = countLines(pickField(fields, OLD_FIELDS));
  }

  return (
    <div className="flex flex-col gap-1 py-0.5 pr-2 pl-3">
      <button
        aria-expanded={open}
        className="flex w-full items-center gap-2 text-left text-ui-xs"
        onClick={() => setOpen((v) => !v)}
        type="button"
      >
        <StatusRing status="running" />
        <span className="shrink-0 text-muted-foreground">
          {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
        </span>
        <span className="shrink-0 font-medium text-muted-foreground text-shimmer">
          {execToolLabel(tool, shell)}
        </span>
        {target.text ? (
          <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">{target.text}</span>
        ) : (
          <span className="flex-1 text-muted-foreground/60">…</span>
        )}
        {added > 0 || removed > 0 ? <DiffStat added={added} removed={removed} /> : null}
      </button>
      {open && items.length > 0 ? (
        <ul className="ml-5 flex flex-col gap-0.5 border-border/60 border-l pl-3 font-mono text-ui-2xs">
          {items.map((item) => {
            const leaf = item.key.slice(item.key.lastIndexOf(".") + 1);
            const bulk = BULK_FIELDS.has(leaf);
            return (
              <li key={item.key} className="flex min-w-0 items-baseline gap-2">
                <span className="shrink-0 text-muted-foreground/60">{item.key}</span>
                <span className="min-w-0 truncate text-muted-foreground">
                  {bulk ? `${countLines(item.value)} 行` : item.value}
                  {item.partial ? <span className="animate-pulse text-foreground">▍</span> : null}
                </span>
              </li>
            );
          })}
        </ul>
      ) : null}
    </div>
  );
}
