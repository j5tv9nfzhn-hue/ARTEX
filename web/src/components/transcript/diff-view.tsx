"use client";

import * as React from "react";

import { ChevronDown, ChevronRight } from "lucide-react";
import { useTranslations } from "next-intl";

import type { DiffLine, DiffModel, PatchFileDiff } from "@/lib/hunks";
import { cn } from "@/lib/utils";

// 变更行的配色只做底色 + 标记两件事：行号、标记列宽度固定，正文按需换行。
// 颜色用 emerald/red 的低透明度底，暗色主题下不刺眼，也不与状态圈的红绿撞色。
const LINE_TONE: Record<DiffLine["kind"], string> = {
  add: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  del: "bg-red-500/10 text-red-700 dark:text-red-300",
  ctx: "text-muted-foreground",
  skip: "",
};

const MARKER: Record<DiffLine["kind"], string> = { add: "+", del: "-", ctx: " ", skip: "" };

function Row({ line }: { line: DiffLine }) {
  return (
    <div className={cn("flex items-start font-mono text-[11px] leading-relaxed", LINE_TONE[line.kind])}>
      <span className="w-8 shrink-0 select-none pr-2 text-right text-muted-foreground/60 tabular-nums">
        {line.oldNo ?? ""}
      </span>
      <span className="w-8 shrink-0 select-none pr-2 text-right text-muted-foreground/60 tabular-nums">
        {line.newNo ?? ""}
      </span>
      <span className="w-3 shrink-0 select-none text-center opacity-70">{MARKER[line.kind]}</span>
      <span className="min-w-0 flex-1 whitespace-pre-wrap break-all pr-2">{line.text || " "}</span>
    </div>
  );
}

// 折叠段不写死「不可展开」：点一下把隐藏行插回来，用户能看清上下文再判断改动对不对。
function SkipRow({ line }: { line: DiffLine }) {
  const t = useTranslations("transcript");
  const [open, setOpen] = React.useState(false);
  const hidden = line.hidden ?? [];
  if (open) {
    return (
      <div>
        {hidden.map((item) => (
          <Row key={`${item.oldNo}:${item.newNo}`} line={item} />
        ))}
        <button
          className="flex w-full items-center justify-center gap-1 bg-muted/30 py-0.5 text-[10px] text-muted-foreground hover:text-foreground"
          onClick={() => setOpen(false)}
          type="button"
        >
          <ChevronRight className="size-3" />
          {t("diff.collapse")}
        </button>
      </div>
    );
  }
  return (
    <button
      className="flex w-full items-center justify-center gap-1 bg-muted/30 py-0.5 text-[10px] text-muted-foreground hover:text-foreground"
      onClick={() => setOpen(true)}
      type="button"
    >
      <ChevronDown className="size-3" />
      {t("diff.expandHidden", { count: hidden.length })}
    </button>
  );
}

// DiffView 渲染一处改动的红绿差异。hunk 头只在多 hunk 时出现，单 hunk 不占一行。
export function DiffView({ model, className }: { model: DiffModel; className?: string }) {
  if (model.hunks.length === 0) return null;
  return (
    <div className={cn("divide-y divide-border/50", className)}>
      {model.hunks.map((hunk) => (
        <div key={hunk.key}>
          {model.hunks.length > 1 ? (
            <div className="bg-muted/30 px-2 py-0.5 font-mono text-[10px] text-muted-foreground">{hunk.header}</div>
          ) : null}
          {hunk.lines.map((line, index) =>
            line.kind === "skip" ? (
              // biome-ignore lint/suspicious/noArrayIndexKey: hunk 内的行顺序固定，key 只用于本地折叠状态
              <SkipRow key={`skip-${index}`} line={line} />
            ) : (
              // biome-ignore lint/suspicious/noArrayIndexKey: 同上
              <Row key={`${line.oldNo}:${line.newNo}:${index}`} line={line} />
            ),
          )}
        </div>
      ))}
    </div>
  );
}

// PatchFileView 是 apply_patch 文档的展示形态：补丁本来就只有 +/-/空格，直接画，不再算一次 diff。
export function PatchFileView({ file }: { file: PatchFileDiff }) {
  return (
    <div>
      <div className="flex items-center gap-2 bg-muted/30 px-2 py-0.5 font-mono text-[10px] text-muted-foreground">
        <span className="min-w-0 truncate">{file.path}</span>
        <DiffStat added={file.added} removed={file.removed} className="ml-auto" />
      </div>
      {file.lines.map((line, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 补丁行顺序固定
        <div className="flex items-start font-mono text-[11px] leading-relaxed" key={index}>
          <span className={cn("min-w-0 flex-1 whitespace-pre-wrap break-all px-2", LINE_TONE[line.kind])}>
            {line.kind === "ctx" ? null : MARKER[line.kind]}
            {line.text || " "}
          </span>
        </div>
      ))}
    </div>
  );
}

// DiffStat 同时给摘要行和补丁头用：+N −N，两个数各自着色。
export function DiffStat({ added, removed, className }: { added: number; removed: number; className?: string }) {
  return (
    <span className={cn("shrink-0 font-mono text-[10px] tabular-nums", className)}>
      <span className="text-emerald-600 dark:text-emerald-400">+{added}</span>{" "}
      <span className="text-red-600 dark:text-red-400">-{removed}</span>
    </span>
  );
}
