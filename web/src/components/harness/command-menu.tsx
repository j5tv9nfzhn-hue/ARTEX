// 命令菜单：输入框的 `/` 浮层与 ⌘K 面板共用同一套键盘交互。
//
// 交互对齐 Codex / Claude Code：↑↓ 选中、Enter 执行、Esc 关闭、鼠标悬停跟随。

import * as React from "react";

import { CornerDownLeftIcon, SearchIcon } from "lucide-react";

import type { HarnessCommand } from "@/lib/harness/commands";
import { fuzzyScore } from "@/lib/harness/commands";
import type { Conversation } from "@/lib/types";
import { cn } from "@/lib/utils";

export interface CommandMenuProps {
  commands: HarnessCommand[];
  active: number;
  onActiveChange: (index: number) => void;
  onPick: (cmd: HarnessCommand) => void;
}

/** 输入框上方的斜杠命令浮层。 */
export function CommandMenu({ commands, active, onActiveChange, onPick }: CommandMenuProps) {
  if (!commands.length) return null;
  return (
    <div className="absolute bottom-full left-0 z-30 mb-2 w-[min(520px,calc(100%-16px))] overflow-hidden rounded-lg border bg-popover shadow-lg">
      <div className="flex items-center gap-1.5 border-b px-2.5 py-1.5 text-[10.5px] text-muted-foreground">
        <span className="font-mono">/</span>
        <span>命令 · ↑↓ 选择 · Enter 执行 · Esc 关闭</span>
      </div>
      <div className="harness-scroll max-h-[260px] overflow-y-auto py-1">
        {commands.map((cmd, i) => (
          <button
            key={cmd.id}
            type="button"
            onMouseEnter={() => onActiveChange(i)}
            onMouseDown={(e) => {
              e.preventDefault(); // 不让输入框先失焦
              onPick(cmd);
            }}
            className={cn(
              "flex w-full items-center gap-2 px-2.5 py-1.5 text-left",
              i === active ? "bg-accent" : "hover:bg-accent/60",
            )}
          >
            <span className="shrink-0 font-mono text-[11.5px] text-foreground">/{cmd.name}</span>
            <span className="shrink-0 text-[11.5px]">{cmd.label}</span>
            <span className="min-w-0 flex-1 truncate text-[10.5px] text-muted-foreground">{cmd.hint}</span>
            {i === active ? <CornerDownLeftIcon className="size-3 shrink-0 text-muted-foreground" /> : null}
          </button>
        ))}
      </div>
    </div>
  );
}

export interface CommandPaletteProps {
  open: boolean;
  onClose: () => void;
  commands: HarnessCommand[];
  conversations: Conversation[];
  activeId: number | null;
  onPickCommand: (cmd: HarnessCommand) => void;
  onPickConversation: (id: number) => void;
}

type Row = { key: string; kind: "cmd"; cmd: HarnessCommand } | { key: string; kind: "conv"; conv: Conversation };

/**
 * CommandPalette：⌘K 打开。会话检索和命令检索在同一个输入里，
 * 命令永远排在会话之前 —— 键盘用户按 ⌘K 十次，九次是想执行动作。
 */
export function CommandPalette({
  open,
  onClose,
  commands,
  conversations,
  activeId,
  onPickCommand,
  onPickConversation,
}: CommandPaletteProps) {
  const [query, setQuery] = React.useState("");
  const [active, setActive] = React.useState(0);
  const inputRef = React.useRef<HTMLInputElement | null>(null);

  React.useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    // 等一帧再聚焦：避免和上一层的 keydown 抢焦点。
    const raf = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(raf);
  }, [open]);

  const rows = React.useMemo<Row[]>(() => {
    const q = query.trim();
    const cmdRows: Row[] = commands
      .map((cmd) => ({
        cmd,
        score: Math.max(fuzzyScore(cmd.name, q), fuzzyScore(cmd.label, q), fuzzyScore(cmd.keys ?? "", q)),
      }))
      .filter((r) => r.score >= 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, q ? 6 : 12)
      .map((r) => ({ key: `c-${r.cmd.id}`, kind: "cmd" as const, cmd: r.cmd }));
    const convRows: Row[] = conversations
      .map((conv) => ({
        conv,
        score: Math.max(fuzzyScore(conv.title || `会话 #${conv.id}`, q), fuzzyScore(conv.agent_key, q)),
      }))
      .filter((r) => r.score >= 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, 8)
      .map((r) => ({ key: `v-${r.conv.id}`, kind: "conv" as const, conv: r.conv }));
    return [...cmdRows, ...convRows];
  }, [commands, conversations, query]);

  const clamped = Math.min(active, Math.max(rows.length - 1, 0));

  if (!open) return null;

  const pick = (row: Row) => {
    if (row.kind === "cmd") onPickCommand(row.cmd);
    else onPickConversation(row.conv.id);
    onClose();
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center px-4 pt-[12vh]">
      {/* 遮罩本身是按钮：键盘可达、无障碍语义正确，也不依赖「点空白处」这种隐式交互。 */}
      <button
        type="button"
        aria-label="关闭命令面板"
        onMouseDown={onClose}
        className="absolute inset-0 cursor-default bg-black/40"
      />
      <div className="relative w-[min(640px,100%)] overflow-hidden rounded-xl border bg-popover shadow-2xl">
        <div className="flex items-center gap-2 border-b px-3 py-2">
          <SearchIcon className="size-4 shrink-0 text-muted-foreground" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActive(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                onClose();
              } else if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((i) => Math.min(i + 1, rows.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((i) => Math.max(i - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                const row = rows[clamped];
                if (row) pick(row);
              }
            }}
            placeholder="搜索会话、或输入命令（new / model / stop …）"
            className="h-8 min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-muted-foreground"
          />
          <span className="shrink-0 rounded border px-1 font-mono text-[10px] text-muted-foreground">esc</span>
        </div>
        <div className="harness-scroll max-h-[52vh] overflow-y-auto py-1">
          {rows.length ? (
            rows.map((row, i) =>
              row.kind === "cmd" ? (
                <button
                  key={row.key}
                  type="button"
                  onMouseEnter={() => setActive(i)}
                  onClick={() => pick(row)}
                  className={cn(
                    "flex w-full items-center gap-2 px-3 py-1.5 text-left",
                    i === clamped ? "bg-accent" : "hover:bg-accent/60",
                  )}
                >
                  <span className="w-8 shrink-0 font-mono text-[11px] text-muted-foreground">/{row.cmd.name}</span>
                  <span className="shrink-0 text-[12px]">{row.cmd.label}</span>
                  <span className="min-w-0 flex-1 truncate text-[10.5px] text-muted-foreground">{row.cmd.hint}</span>
                  <span className="shrink-0 rounded-sm bg-muted px-1 text-[9.5px] text-muted-foreground">
                    {row.cmd.group}
                  </span>
                </button>
              ) : (
                <button
                  key={row.key}
                  type="button"
                  onMouseEnter={() => setActive(i)}
                  onClick={() => pick(row)}
                  className={cn(
                    "flex w-full items-center gap-2 px-3 py-1.5 text-left",
                    i === clamped ? "bg-accent" : "hover:bg-accent/60",
                  )}
                >
                  <span className="w-8 shrink-0 text-center text-[10px] text-muted-foreground">
                    {row.conv.running ? "●" : "·"}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-[12px]">
                    {row.conv.title || `会话 #${row.conv.id}`}
                  </span>
                  <span className="shrink-0 font-mono text-[9.5px] text-muted-foreground">{row.conv.agent_key}</span>
                  {row.conv.id === activeId ? (
                    <span className="shrink-0 rounded-sm bg-muted px-1 text-[9.5px] text-muted-foreground">当前</span>
                  ) : null}
                </button>
              ),
            )
          ) : (
            <p className="px-3 py-6 text-center text-[11.5px] text-muted-foreground">没有匹配的会话或命令。</p>
          )}
        </div>
      </div>
    </div>
  );
}
