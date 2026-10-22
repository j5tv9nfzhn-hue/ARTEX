"use client";

import * as React from "react";

import { DiffStat } from "@/components/transcript/diff-view";
import { StatusRing } from "@/components/transcript/status-ring";
import { countLines, parsePartialJson, pickField } from "@/lib/partial-json";

const EDIT_TOOLS = new Set(["Edit", "MultiEdit", "Write", "apply_patch", "NotebookEdit"]);
const PATH_FIELDS = ["file_path", "path", "file", "target_file"];
const CONTENT_FIELDS = ["content", "file_text", "fileText", "new_string", "new_str"];

// liveTarget 决定这一行右侧显示什么：一行字，取「这个工具在做什么」最直接的那个字段。
// 参数还在流的时候字段可能是空的（key 到了、值刚开始），因此逐级回退到原文尾部。
function liveTarget(tool: string, fields: Record<string, string>, raw: string): string {
  if (tool === "Bash" || tool === "shell_open") return pickField(fields, ["command"]);
  if (tool === "Grep" || tool === "Glob") return pickField(fields, ["pattern", "glob", "query"]);
  if (tool.startsWith("http_")) {
    const method = pickField(fields, ["method"]).toUpperCase() || "GET";
    const url = pickField(fields, ["url", "target", "host", "path"]);
    return url ? `${method} ${url}` : "";
  }
  if (EDIT_TOOLS.has(tool) || tool === "Read") return pickField(fields, PATH_FIELDS);
  const first = Object.values(fields).find((value) => value !== "");
  if (first) return first;
  // 一个字段都还没成形：把原始 JSON 的尾巴露出来，证明它确实在流。
  return raw.slice(-80);
}

// LiveToolRow 是「参数生成中」的预览行：正式的 tool_use 活动行要等模型把参数发完
// 才落库，在那之前这一行负责让人看见「它要干什么」以及「它还在动」。
//
// 计数只做行数（写入 N 行 / 改 +A −D），不在这里算 diff：参数还是半成品，逐帧跑
// jsdiff 既慢又会在末尾给出会变的噪声数字；定稿后的真实红绿差异仍由 DiffBody 画。
export function LiveToolRow({ tool, args }: { tool: string; args: string }) {
  const fields = React.useMemo(() => parsePartialJson(args), [args]);
  const target = React.useMemo(() => liveTarget(tool, fields, args), [tool, fields, args]);

  let added = 0;
  let removed = 0;
  if (tool === "Write") {
    added = countLines(pickField(fields, CONTENT_FIELDS));
  } else if (tool === "Edit" || tool === "NotebookEdit") {
    added = countLines(pickField(fields, ["new_string", "new_str", "new_source"]));
    removed = countLines(pickField(fields, ["old_string", "old_str", "old_source"]));
  }

  return (
    <div className="flex items-center gap-2 py-0.5 pr-2 pl-3 text-ui-xs">
      <StatusRing status="running" />
      <span className="shrink-0 font-medium text-muted-foreground text-shimmer">{tool}</span>
      {target ? (
        <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">{target}</span>
      ) : (
        <span className="min-w-0 flex-1 truncate text-muted-foreground/60">…</span>
      )}
      {added > 0 || removed > 0 ? <DiffStat added={added} removed={removed} /> : null}
    </div>
  );
}
