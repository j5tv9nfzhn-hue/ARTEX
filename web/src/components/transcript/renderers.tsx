"use client";

import * as React from "react";

import { CheckIcon, WrapTextIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { CopyTextButton } from "@/components/transcript/copy-text-button";
import { DiffStat, DiffView, PatchFileView } from "@/components/transcript/diff-view";
import { buildDiff, type DiffModel, parsePatchDocument } from "@/lib/hunks";
import type { ToolStatus } from "@/lib/tool-status";
import { cn } from "@/lib/utils";

export interface ToolSectionModel {
  label: string;
  content: string;
  isCmd?: boolean;
  isError?: boolean;
}

export interface ToolBodyProps {
  tool: string;
  sections: ToolSectionModel[];
  status: ToolStatus;
  wrapLines: boolean;
  onToggleWrap: () => void;
}

// 详情区的一块（调用入参 / 执行输出）：标题栏 + 换行开关 + 复制 + 正文。
// 所有 renderer 共用它，保证「换行、复制」这类通用能力不会因为换了渲染器而消失。
function SectionBlock({
  section,
  wrapLines,
  onToggleWrap,
}: {
  section: ToolSectionModel;
  wrapLines: boolean;
  onToggleWrap: () => void;
}) {
  const t = useTranslations("transcript");
  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between gap-2 bg-muted/40 px-3 py-1 text-[10px]">
        <span
          className={cn(
            "font-semibold uppercase tracking-wide",
            section.isError ? "text-red-500" : "text-muted-foreground",
          )}
        >
          {section.label}
        </span>
        <div className="flex items-center gap-1">
          <button
            aria-label={t("tool.wrapLines")}
            className={cn(
              "inline-flex size-5 items-center justify-center rounded text-[10px] transition-colors hover:bg-accent hover:text-foreground",
              wrapLines ? "text-primary" : "text-muted-foreground",
            )}
            onClick={(event) => {
              event.stopPropagation();
              onToggleWrap();
            }}
            title={t("tool.wrapLines")}
            type="button"
          >
            <WrapTextIcon className="size-3" />
          </button>
          <CopyTextButton text={section.content} />
        </div>
      </div>
      <pre
        className={cn(
          "max-h-72 overflow-auto p-2.5 font-mono text-[11px] leading-relaxed",
          wrapLines ? "whitespace-pre-wrap break-all" : "whitespace-pre",
          section.isCmd ? "text-sky-700 dark:text-sky-300" : "text-foreground/90",
        )}
      >
        {section.content || "（空）"}
      </pre>
    </div>
  );
}

function safeJson(text: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
    return null;
  } catch {
    return null;
  }
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function firstString(input: Record<string, unknown>, keys: string[]): string {
  for (const key of keys) {
    const value = input[key];
    if (typeof value === "string" && value.length > 0) return value;
  }
  return "";
}

// ---------- exec：Bash 命令块（3 行限高，可展开） ----------
function CommandBody({ sections, wrapLines, onToggleWrap }: ToolBodyProps) {
  const t = useTranslations("transcript");
  const command = sections[0]?.content ?? "";
  const output = sections[1];
  const [expanded, setExpanded] = React.useState(false);
  const long = command.split("\n").length > 3;
  return (
    <div className="divide-y divide-border/70">
      <div className="flex flex-col">
        <div className="flex items-center justify-between gap-2 bg-muted/40 px-3 py-1 text-[10px]">
          <span className="font-semibold text-muted-foreground uppercase tracking-wide">{sections[0]?.label ?? ""}</span>
          <div className="flex items-center gap-1">
            {long ? (
              <button
                className="rounded px-1 text-[10px] text-muted-foreground hover:text-foreground"
                onClick={(event) => {
                  event.stopPropagation();
                  setExpanded((value) => !value);
                }}
                type="button"
              >
                {expanded ? t("tool.collapseCommand") : t("tool.expandCommand")}
              </button>
            ) : null}
            <CopyTextButton text={command} />
          </div>
        </div>
        <div
          className={cn(
            "overflow-auto p-2.5 font-mono text-[11px] leading-relaxed",
            wrapLines ? "whitespace-pre-wrap break-all" : "whitespace-pre",
            expanded ? "max-h-72" : "max-h-16",
          )}
        >
          <span className="select-none pr-1 text-muted-foreground/70">$</span>
          <span className="text-sky-700 dark:text-sky-300">{command || "（空）"}</span>
        </div>
      </div>
      {output ? <SectionBlock onToggleWrap={onToggleWrap} section={output} wrapLines={wrapLines} /> : null}
    </div>
  );
}

// ---------- 读文件 / 搜索：把 JSON 入参折成「目标 + 条件」一行，正文仍是原文 ----------
function InputBody({ tool, sections, wrapLines, onToggleWrap }: ToolBodyProps) {
  const input = safeJson(sections[0]?.content ?? "");
  const path = input ? firstString(input, ["file_path", "path", "pattern"]) : "";
  const detail = input
    ? [
        firstString(input, ["pattern"]) && tool !== "Read" ? `pattern=${firstString(input, ["pattern"])}` : "",
        input.offset ? `offset=${input.offset}` : "",
        input.limit ? `limit=${input.limit}` : "",
        firstString(input, ["glob"]) ? `glob=${firstString(input, ["glob"])}` : "",
        firstString(input, ["output_mode"]) ? `mode=${firstString(input, ["output_mode"])}` : "",
      ]
        .filter(Boolean)
        .join("  ")
    : "";
  return (
    <div className="divide-y divide-border/70">
      {input ? (
        <div className="flex flex-wrap items-center gap-2 px-3 py-1.5 font-mono text-[11px]">
          {path ? <span className="min-w-0 break-all text-foreground/90">{path}</span> : null}
          {detail ? <span className="text-muted-foreground">{detail}</span> : null}
          <span className="ml-auto flex items-center gap-1">
            <CopyTextButton text={JSON.stringify(input, null, 2)} />
          </span>
        </div>
      ) : (
        <SectionBlock onToggleWrap={onToggleWrap} section={sections[0]} wrapLines={wrapLines} />
      )}
      {sections[1] ? <SectionBlock onToggleWrap={onToggleWrap} section={sections[1]} wrapLines={wrapLines} /> : null}
    </div>
  );
}

// ---------- HTTP：方法 + URL 一行，响应头/体走通用分段 ----------
function HttpBody({ sections, wrapLines, onToggleWrap }: ToolBodyProps) {
  const input = safeJson(sections[0]?.content ?? "");
  const method = input ? str(input.method).toUpperCase() || "GET" : "GET";
  const url = input ? firstString(input, ["url", "target"]) : "";
  return (
    <div className="divide-y divide-border/70">
      {input && url ? (
        <div className="flex items-center gap-2 px-3 py-1.5">
          <span className="rounded bg-sky-500/15 px-1.5 py-0.5 font-mono font-semibold text-[10px] text-sky-600 dark:text-sky-400">
            {method}
          </span>
          <span className="min-w-0 flex-1 break-all font-mono text-[11px] text-foreground/90">{url}</span>
          <CopyTextButton text={url} />
        </div>
      ) : (
        sections[0] && <SectionBlock onToggleWrap={onToggleWrap} section={sections[0]} wrapLines={wrapLines} />
      )}
      {sections[1] ? <SectionBlock onToggleWrap={onToggleWrap} section={sections[1]} wrapLines={wrapLines} /> : null}
    </div>
  );
}

// ---------- 计划：TodoWrite 的清单 ----------
function TodoBody({ sections, wrapLines, onToggleWrap }: ToolBodyProps) {
  const input = safeJson(sections[0]?.content ?? "");
  const raw = input && Array.isArray(input.todos) ? input.todos : null;
  if (!raw) {
    return <GenericBody onToggleWrap={onToggleWrap} sections={sections} status="completed" tool="TodoWrite" wrapLines={wrapLines} />;
  }
  return (
    <div className="divide-y divide-border/70">
      <div className="flex flex-col gap-1 px-3 py-2">
        {raw.map((item, index) => {
          const todo = (item ?? {}) as Record<string, unknown>;
          const text = str(todo.content) || str(todo.text) || str(todo.subject);
          const done = str(todo.status) === "completed" || str(todo.status) === "done";
          return (
            // biome-ignore lint/suspicious/noArrayIndexKey: 计划项按行序展示，内容本身可能重复
            <div className={cn("flex items-start gap-2 text-[11px]", done && "text-muted-foreground")} key={index}>
              <span
                className={cn(
                  "mt-0.5 flex size-3 shrink-0 items-center justify-center rounded-[3px] border",
                  done ? "border-transparent bg-emerald-500/20 text-emerald-600 dark:text-emerald-400" : "border-border",
                )}
              >
                {done ? <CheckIcon className="size-2" /> : null}
              </span>
              <span className={cn("min-w-0 break-words", done && "line-through")}>{text}</span>
            </div>
          );
        })}
      </div>
      {sections[1] ? <SectionBlock onToggleWrap={onToggleWrap} section={sections[1]} wrapLines={wrapLines} /> : null}
    </div>
  );
}

// ---------- 改动类工具：红绿差异 ----------
interface DiffJob {
  path: string;
  oldText: string;
  newText: string;
}

function diffJobs(tool: string, raw: string): DiffJob[] | null {
  const input = safeJson(raw);
  if (!input) return null;
  if (tool === "Write") {
    const path = firstString(input, ["file_path", "path"]);
    return path ? [{ path, oldText: "", newText: str(input.content) }] : null;
  }
  if (tool === "Edit") {
    const path = firstString(input, ["file_path", "path"]);
    return path ? [{ path, oldText: str(input.old_string), newText: str(input.new_string) }] : null;
  }
  if (tool === "MultiEdit") {
    const path = firstString(input, ["file_path", "path"]);
    const edits = Array.isArray(input.edits) ? input.edits : [];
    const jobs = edits.map((edit) => {
      const row = (edit ?? {}) as Record<string, unknown>;
      return { path, oldText: str(row.old_string), newText: str(row.new_string) };
    });
    return jobs.length > 0 ? jobs : null;
  }
  if (tool === "apply_patch") {
    const patches = Array.isArray(input.patches) ? input.patches : [];
    const jobs: DiffJob[] = [];
    for (const patch of patches) {
      const row = (patch ?? {}) as Record<string, unknown>;
      const path = firstString(row, ["file_path", "path"]);
      const edits = Array.isArray(row.edits) ? row.edits : [];
      for (const edit of edits) {
        const item = (edit ?? {}) as Record<string, unknown>;
        jobs.push({ path, oldText: str(item.old_string), newText: str(item.new_string) });
      }
    }
    return jobs.length > 0 ? jobs : null;
  }
  return null;
}

function DiffJobView({ job }: { job: DiffJob }) {
  const model: DiffModel = React.useMemo(() => buildDiff(job.oldText, job.newText), [job.oldText, job.newText]);
  return (
    <div>
      <div className="flex items-center gap-2 bg-muted/30 px-2 py-0.5 font-mono text-[10px] text-muted-foreground">
        <span className="min-w-0 truncate">{job.path}</span>
        <DiffStat added={model.added} className="ml-auto" removed={model.removed} />
      </div>
      <DiffView model={model} />
    </div>
  );
}

function DiffBody({ tool, sections, wrapLines, onToggleWrap }: ToolBodyProps) {
  const raw = sections[0]?.content ?? "";
  const jobs = React.useMemo(() => diffJobs(tool, raw), [tool, raw]);
  const patchFiles = React.useMemo(() => (jobs ? [] : parsePatchDocument(raw)), [jobs, raw]);
  if (!jobs && patchFiles.length === 0) {
    return <GenericBody onToggleWrap={onToggleWrap} sections={sections} status="completed" tool={tool} wrapLines={wrapLines} />;
  }
  return (
    <div className="divide-y divide-border/70">
      <div className="max-h-96 overflow-auto">
        {jobs ? jobs.map((job, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 同一次调用里的多处编辑按顺序展示
          <DiffJobView job={job} key={index} />
        )) : patchFiles.map((file) => <PatchFileView file={file} key={file.path} />)}
      </div>
      {sections[1] ? <SectionBlock onToggleWrap={onToggleWrap} section={sections[1]} wrapLines={wrapLines} /> : null}
    </div>
  );
}

// ---------- 兜底：不认识的工具照旧走「入参 / 输出」两段 ----------
function GenericBody({ sections, wrapLines, onToggleWrap }: ToolBodyProps) {
  return (
    <div className="divide-y divide-border/70">
      {sections.map((section) => (
        <SectionBlock
          key={section.label}
          onToggleWrap={onToggleWrap}
          section={section}
          wrapLines={wrapLines}
        />
      ))}
    </div>
  );
}

const EDIT_TOOLS = new Set(["Edit", "MultiEdit", "Write", "apply_patch", "NotebookEdit"]);
const READ_TOOLS = new Set(["Read", "Grep", "Glob", "LS"]);
const HTTP_TOOLS = new Set(["http_request", "http_fuzz", "http_sequence"]);

// ToolBody 按工具名分发。新增工具只要在下面加一行，没有登记的工具自动走兜底渲染。
export function ToolBody(props: ToolBodyProps) {
  if (EDIT_TOOLS.has(props.tool)) return <DiffBody {...props} />;
  if (props.tool === "Bash") return <CommandBody {...props} />;
  if (HTTP_TOOLS.has(props.tool)) return <HttpBody {...props} />;
  if (props.tool === "TodoWrite") return <TodoBody {...props} />;
  if (READ_TOOLS.has(props.tool)) return <InputBody {...props} />;
  return <GenericBody {...props} />;
}
