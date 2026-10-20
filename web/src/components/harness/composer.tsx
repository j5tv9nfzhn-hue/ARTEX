"use client";

// 输入区：草稿 + 待发送的逐行评论 chip + 权限档位。
//
// Enter 发送、Shift+Enter 换行、⌘/Ctrl+Enter 在等待审批时直接允许。
// 「计划/编辑/自动」目前是本地的行为提示，真正的策略门控仍在后端 intercept 层；
// 这里不伪造权限，只在 UI 上标注当前意图。

import * as React from "react";

import { ArrowUpIcon, SquareIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { HarnessComment, PermissionMode } from "@/lib/harness/types";
import { cn } from "@/lib/utils";

const PERM: Array<{ key: PermissionMode; label: string; hint: string }> = [
  { key: "plan", label: "计划", hint: "只读 + 计划，任何写入都会先问你" },
  { key: "edit", label: "编辑", hint: "工作区内可写；越界与提权操作需审批" },
  { key: "auto", label: "自动", hint: "自动执行，仅高危操作暂停" },
];

export interface ComposerProps {
  onSend: (text: string, files?: File[]) => Promise<void>;
  onStop: () => Promise<void>;
  running: boolean;
  sending: boolean;
  blocked: boolean;
  perm: PermissionMode;
  onPermChange: (p: PermissionMode) => void;
  comments: HarnessComment[];
  onRemoveComment: (index: number) => void;
  /** 本会话累计 token（来自 result 活动）。 */
  tokens?: number;
}

export function Composer({
  onSend,
  onStop,
  running,
  sending,
  blocked,
  perm,
  onPermChange,
  comments,
  onRemoveComment,
  tokens,
}: ComposerProps) {
  const [text, setText] = React.useState("");
  // 待发送的图片：粘贴进来先挂在这里（候选），发送时随消息一起上传。
  const [files, setFiles] = React.useState<File[]>([]);
  // 发送失败的原因就地显示，不依赖父级 ErrorBar（它在页面顶部，容易被忽略）。
  const [localError, setLocalError] = React.useState<string | null>(null);
  // 同一 tick 内连按两次 Enter 时 sending 还没变成 true，用它兜住重复提交。
  const busyRef = React.useRef(false);
  const ref = React.useRef<HTMLTextAreaElement | null>(null);

  // 粘贴图片：截图/复制的图直接进候选，不必先存文件再选。
  // 只接管 file+image/* 的粘贴，纯文本一行都不碰。
  const onPaste = (e: React.ClipboardEvent<HTMLDivElement>) => {
    const items = e.clipboardData?.items;
    if (!items) return;
    const picked: File[] = [];
    for (const item of Array.from(items)) {
      if (item.kind !== "file") continue;
      const f = item.getAsFile();
      if (!f || !f.type.startsWith("image/")) continue;
      const ext = (f.type.split("/")[1] || "png") === "jpeg" ? "jpg" : f.type.split("/")[1] || "png";
      const stamp = new Date().toISOString().replace(/[-:T]/g, "").slice(0, 14);
      const name = `paste-${stamp}-${Math.random().toString(36).slice(2, 6)}.${ext}`;
      try {
        picked.push(new File([f], name, { type: f.type || "image/png" }));
      } catch {
        picked.push(f);
      }
    }
    if (!picked.length) return;
    e.preventDefault();
    setFiles((prev) => [...prev, ...picked]);
  };

  React.useEffect(() => {
    const el = ref.current;
    if (!el) return;
    // text 只作为「内容变化」的触发器，高度由 scrollHeight 反推。
    void text;
    el.style.height = "auto";
    el.style.height = `${Math.min(200, el.scrollHeight)}px`;
  }, [text]);

  const submit = async () => {
    const value = text.trim();
    const pending = files;
    // 允许「只贴图不写字」：图片本身就是内容。
    if ((!value && pending.length === 0) || sending || blocked || busyRef.current) return;
    busyRef.current = true;
    setLocalError(null);
    try {
      await onSend(value, pending.length ? pending : undefined);
      // 确认送达后才清空。此前是「先清空再发」：一旦下游静默短路，用户看到的就是
      // 消息凭空消失，而且连一个字都拿不回来。
      setText("");
      setFiles([]);
    } catch (e) {
      setText(value);
      setFiles(pending);
      setLocalError(e instanceof Error ? e.message : String(e));
    } finally {
      busyRef.current = false;
    }
  };

  const hint = PERM.find((p) => p.key === perm)?.hint ?? "";

  return (
    // shrink-0：父级是 flex 列，输入区不能被压——否则底部提示行会被 root 的 overflow-hidden 裁掉。
    <div className="mx-auto w-full max-w-[820px] shrink-0 px-6 pb-3">
      {comments.length ? (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {comments.map((c, i) => (
            <span
              key={`${c.path}-${c.line}-${i}`}
              className="inline-flex max-w-full items-center gap-1.5 rounded-sm border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-[10.5px]"
            >
              <b className="font-mono text-[10.5px]">
                {c.path.split("/").pop()}:{c.line}
              </b>
              <span className="truncate text-muted-foreground">{c.text}</span>
              <button
                type="button"
                onClick={() => onRemoveComment(i)}
                className="text-muted-foreground hover:text-foreground"
              >
                ✕
              </button>
            </span>
          ))}
        </div>
      ) : null}

      {files.length ? (
        <div className="mb-1.5 flex flex-wrap gap-1.5">
          {files.map((f, i) => (
            <span
              key={`${f.name}-${i}`}
              className="inline-flex max-w-full items-center gap-1.5 rounded-md border bg-muted/60 px-1.5 py-0.5 text-[10.5px]"
              title={f.name}
            >
              <span className="truncate font-mono">{f.name}</span>
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                onClick={() => setFiles((prev) => prev.filter((_, j) => j !== i))}
              >
                ✕
              </button>
            </span>
          ))}
        </div>
      ) : null}

      <div
        onPaste={onPaste}
        className={cn(
          "rounded-xl border bg-card transition-colors focus-within:border-primary",
          blocked && "border-[var(--hx-warn)]",
        )}
      >
        <textarea
          ref={ref}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              void submit();
            }
          }}
          rows={1}
          disabled={blocked}
          placeholder={
            blocked ? "等待审批，先在时间线里允许或拒绝…" : "描述任务，Enter 发送，Shift+Enter 换行；可直接粘贴图片"
          }
          className="block max-h-[200px] min-h-[46px] w-full resize-none bg-transparent px-3.5 pt-3 text-[13px] leading-6 outline-none placeholder:text-muted-foreground/70 disabled:cursor-not-allowed"
        />
        <div className="flex items-center gap-1 px-2 pb-2">
          <div className="flex items-center gap-0.5 rounded-md bg-muted p-0.5">
            {PERM.map((p) => (
              <button
                key={p.key}
                type="button"
                onClick={() => onPermChange(p.key)}
                className={cn(
                  "h-6 rounded-sm px-2 text-[11px] transition-colors",
                  perm === p.key
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {p.label}
              </button>
            ))}
          </div>

          <span className="ml-auto font-mono text-[10.5px] text-muted-foreground">
            {tokens ? `Σ ${tokens.toLocaleString("zh-CN")} tokens` : null}
          </span>

          {running ? (
            <Button size="sm" variant="destructive" onClick={() => void onStop()} className="h-7 gap-1">
              <SquareIcon className="size-3" /> 停止
            </Button>
          ) : (
            <Button
              size="sm"
              onClick={() => void submit()}
              disabled={sending || blocked || (!text.trim() && files.length === 0)}
              className="h-7 gap-1"
            >
              <ArrowUpIcon className="size-3" /> 发送
            </Button>
          )}
        </div>
      </div>

      {localError ? <p className="mt-1.5 text-[10.5px] text-destructive">发送失败：{localError}</p> : null}
      <p className="mt-1.5 text-[10.5px] text-muted-foreground">
        {blocked ? "等待审批：时间线里的审批卡处理完才能继续。" : hint}
      </p>
    </div>
  );
}
