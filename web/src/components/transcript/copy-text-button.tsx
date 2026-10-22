"use client";

import * as React from "react";

import { CheckIcon, CopyIcon } from "lucide-react";

// CopyTextButton 是行内复制按钮：点一下写剪贴板，1.5s 内显示对勾。
// 从 transcript.tsx 里提出来，是因为工具行渲染器（renderers.tsx）也要用；
// 由 transcript 导出会让 renderers ← transcript ← renderers 形成循环引用。
export function CopyTextButton({ text }: { text: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <button
      aria-label="Copy"
      className="inline-flex size-5 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      onClick={(event) => {
        event.stopPropagation();
        if (!text) return;
        void navigator.clipboard?.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
      type="button"
    >
      {copied ? <CheckIcon className="size-3 text-emerald-500" /> : <CopyIcon className="size-3" />}
    </button>
  );
}
