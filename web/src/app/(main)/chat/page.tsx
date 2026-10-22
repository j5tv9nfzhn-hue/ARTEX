"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpIcon,
  Bot,
  ChevronDownIcon,
  ChevronRightIcon,
  ListChecksIcon,
  Loader2Icon,
  MoreHorizontalIcon,
  PaperclipIcon,
  PencilIcon,
  PinIcon,
  PinOffIcon,
  PlusIcon,
  Square,
  Trash2Icon,
  XIcon,
  ZapIcon,
} from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { toast } from "sonner";

import { ApprovalExecutionFocus, useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { CheckpointTimelineButton } from "@/components/checkpoint-timeline";
import { EmptyState } from "@/components/empty-state";
import { LiveDot } from "@/components/live-dot";
import { MentionTextarea } from "@/components/mention-textarea";
import { MessageRollbackDialog } from "@/components/message-rollback-dialog";
import { SideQuestionButton, SideQuestionWorkspace } from "@/components/side-question-workspace";
import { TodoPopover } from "@/components/todo-popover";
import { parseUserBody, Transcript } from "@/components/transcript";
import { ContextUsageRing } from "@/components/transcript/context-usage";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { mergeActivities } from "@/lib/activity-merge";
import { api } from "@/lib/api";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { isBtwCommand } from "@/lib/side-questions";
import type { Activity, Agent, ChatAttachment, Conversation, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

// fmtBytes renders a human file size for attachment chips (mirrors transcript.tsx).
function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
function _fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

// HISTORY_PAGE is how many steps one history page loads: the latest page on open,
// then one more page each time the user scrolls to the top. Kept modest so a long
// thread stays snappy (only ~a page of rows is in the DOM until you scroll up).
const HISTORY_PAGE = 200;
const CONVERSATION_LIST_PAGE = 100;

// Which agent groups the user has collapsed in the left rail. Persisted so the
// rail looks the same after a reload; unknown keys are harmless (a deleted agent
// simply never renders a group again).
const COLLAPSED_AGENTS_KEY = "artex.chat.collapsed-agents";

function conversationIsPinned(conversation: Conversation): boolean {
  return conversation.pinned ?? Boolean(conversation.pinned_at);
}

// AgentGroup is one collapsible section of the left rail: all unpinned
// conversations of a single agent, newest activity first.
interface AgentGroup {
  key: string;
  name: string;
  conversations: Conversation[];
  runningCount: number;
}

// groupByAgent buckets conversations by agent, preserving the incoming order
// both inside a group and across groups. The server already sorts by updated_at
// DESC, so first-appearance order == most-recently-active group first.
function groupByAgent(conversations: Conversation[], agentByKey: Map<string, Agent>): AgentGroup[] {
  const groups = new Map<string, AgentGroup>();
  for (const conversation of conversations) {
    let group = groups.get(conversation.agent_key);
    if (!group) {
      group = {
        key: conversation.agent_key,
        name: agentByKey.get(conversation.agent_key)?.name || conversation.agent_key,
        conversations: [],
        runningCount: 0,
      };
      groups.set(conversation.agent_key, group);
    }
    group.conversations.push(conversation);
    if (conversation.running) group.runningCount++;
  }
  return [...groups.values()];
}

// LiveBadge 复用任务主 Agent 控制台的“实时”动态标签，在轮次流式生成时显示。
function LiveBadge() {
  const t = useTranslations("chat");
  return (
    <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 font-medium text-[10px] text-blue-600 dark:text-blue-400">
      <LiveDot className="size-1" />
      {t("live")}
    </span>
  );
}

// pastedImageFile 把剪贴板里的图片重新命名成一个稳定、可辨识的文件。
// 浏览器给的名字通常是 image.png：同一轮连贴两张会依赖后端 -1/-2 去重，
// 时间线里也看不出是哪次粘贴留下的。扩展名必须与真实 MIME 一致——
// 后端按扩展名判输入模态（db/attachmentModality），错了会被闸门拦下。
function pastedImageFile(f: File): File {
  const fromType = (f.type || "").split("/")[1] || "png";
  const ext = fromType === "jpeg" ? "jpg" : fromType;
  const stamp = new Date().toISOString().replace(/[-:T]/g, "").slice(0, 14);
  const rand = Math.random().toString(36).slice(2, 6);
  try {
    return new File([f], `paste-${stamp}-${rand}.${ext}`, { type: f.type || "image/png" });
  } catch {
    // 极老浏览器不支持 File 构造器：退回原始 File，名字退化成 image.png 也能上传。
    return f;
  }
}

// Composer is the shared Two-Deck Composer Dock (Deck 1: 100%-width adaptive textarea;
// Deck 2: Agent selector pill, attachment button, inline LLM profile selector, Todo popover, and Send/Stop action).
function Composer({
  value,
  onChange,
  onSend,
  disabled,
  placeholder,
  leftSlot,
  bottomSlot,
  running,
  onStop,
  stopDisabled,
  attachments,
  onPickFiles,
  onRemoveAttachment,
  uploading,
  allowBtw,
  contextUsage,
}: {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  disabled: boolean;
  placeholder: string;
  leftSlot?: React.ReactNode;
  bottomSlot?: React.ReactNode;
  running?: boolean;
  onStop?: () => void;
  stopDisabled?: boolean;
  // 文件上传方式一：提供 onPickFiles 才显示附件按钮和附件 chip 预览。
  attachments?: ChatAttachment[];
  onPickFiles?: (files: File[]) => void;
  onRemoveAttachment?: (path: string) => void;
  uploading?: boolean;
  allowBtw?: boolean;
  // 上下文环：窗口与已用都取后端数据，缺失时不渲染。
  contextUsage?: { used: number; size: number } | null;
}) {
  const t = useTranslations("chat");
  const fileInputRef = React.useRef<HTMLInputElement>(null);
  const atts = attachments ?? [];
  // 粘贴图片：截图/网页复制的图片直接进候选附件，不必先存成文件再点回形针。
  // 只接管「file 且是 image/*」的粘贴；纯文本粘贴一行都不碰（不 preventDefault）。
  function onPaste(e: React.ClipboardEvent<HTMLDivElement>) {
    if (!onPickFiles || uploading) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (const item of Array.from(items)) {
      if (item.kind !== "file") continue;
      const f = item.getAsFile();
      if (f && f.type.startsWith("image/")) files.push(pastedImageFile(f));
    }
    if (files.length === 0) return;
    e.preventDefault();
    onPickFiles(files);
  }
  // 发送按键由系统设置 localStorage 决定，默认 Enter 发送。
  const sendMode = useChatSendMode();
  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (!shouldSubmitOnKey(e, sendMode)) return;
    e.preventDefault();
    onSend();
  }
  return (
    <div className="px-3 pt-1.5 pb-3 md:px-6">
      <div className="mx-auto flex w-full max-w-4xl flex-col rounded-2xl border border-border/85 bg-background/75 p-2.5 shadow-xs transition-all focus-within:border-primary/50 focus-within:ring-2 focus-within:ring-primary/20">
        {atts.length > 0 && (
          <div className="mb-2 flex flex-wrap gap-1.5 px-1">
            {atts.map((a) => (
              <div
                key={a.path}
                className="flex items-center gap-1.5 rounded-lg border bg-muted/60 px-2.5 py-1 text-xs"
                title={a.path}
              >
                <PaperclipIcon className="size-3 shrink-0 text-primary" />
                <span className="max-w-[180px] truncate font-medium">{a.name}</span>
                <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
                {onRemoveAttachment && (
                  <button
                    type="button"
                    className="ml-0.5 text-muted-foreground hover:text-foreground"
                    onClick={() => onRemoveAttachment(a.path)}
                    title={t("composer.removeAttachment")}
                  >
                    <XIcon className="size-3" />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
        {/* Deck 1: Full-width adaptive textarea so placeholder is never truncated */}
        {/* onPaste 挂在容器上：MentionTextarea 不必改 API，事件会从 textarea 冒泡上来。 */}
        <div className="flex min-w-0 flex-1" onPaste={onPaste}>
          <MentionTextarea
            className="max-h-40 min-h-11 w-full min-w-0 flex-1 resize-none border-0 bg-transparent px-1.5 py-1 text-sm shadow-none focus-visible:ring-0 dark:bg-transparent"
            rows={1}
            placeholder={placeholder}
            value={value}
            disabled={disabled && !(running && allowBtw)}
            onValueChange={onChange}
            onKeyDown={onKeyDown}
          />
        </div>
        {/* Deck 2: Integrated control bar (Agent pill, paperclip, LLM profile popover, Todo, Send/Stop) */}
        <div className="mt-1.5 flex flex-wrap items-center justify-between gap-2 border-border/50 border-t pt-2">
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
            {leftSlot ? <div className="shrink-0">{leftSlot}</div> : null}
            {onPickFiles && (
              <>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={(e) => {
                    const picked = Array.from(e.target.files ?? []);
                    e.target.value = "";
                    if (picked.length > 0) onPickFiles(picked);
                  }}
                />
                <Button
                  size="icon-sm"
                  variant="ghost"
                  className="shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={disabled || uploading}
                  title={`${t("composer.uploadFile")} · ${t("composer.pasteHint")}`}
                >
                  {uploading ? <Loader2Icon className="size-4 animate-spin" /> : <PaperclipIcon className="size-4" />}
                </Button>
              </>
            )}
            {bottomSlot}
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {contextUsage && <ContextUsageRing size={contextUsage.size} used={contextUsage.used} />}
            {running && allowBtw && isBtwCommand(value) && (
              <Button
                size="icon-sm"
                className="rounded-xl"
                onClick={onSend}
                aria-label={t("composer.sendSideQuestion")}
                title={t("composer.sendSideQuestion")}
              >
                <ArrowUpIcon />
              </Button>
            )}
            {running ? (
              <Button
                size="icon-sm"
                variant="destructive"
                className="rounded-xl"
                onClick={onStop}
                disabled={stopDisabled}
                title={t("composer.stopRun")}
              >
                <Square className="size-3.5 fill-current" />
              </Button>
            ) : (
              <Button
                size="icon-sm"
                className="rounded-xl"
                onClick={onSend}
                disabled={disabled || (!value.trim() && atts.length === 0)}
                title={t("composer.sendMessage")}
                aria-label={t("composer.sendMessage")}
              >
                <ArrowUpIcon />
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

// LLMProfileRow renders as a sleek inline capsule inside the Composer toolbar deck.
function LLMProfileRow({
  profiles,
  selected,
  onChange,
  disabled,
  rightSlot,
}: {
  profiles: LLMProfile[];
  selected: number | null;
  onChange: (id: number | null) => void;
  disabled?: boolean;
  rightSlot?: React.ReactNode;
}) {
  const t = useTranslations("chat");
  const [open, setOpen] = React.useState(false);
  const activeDefault = profiles.find((p) => p.is_default);
  const current = selected != null ? profiles.find((p) => Number(p.id) === selected) : null;
  const defaultLabel = activeDefault
    ? t("llmProfile.defaultNamed", { name: activeDefault.name })
    : t("llmProfile.default");
  const label = current ? current.name : defaultLabel;

  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            disabled={disabled}
            title={`${label} · ${t("llmProfile.change")}`}
            className="inline-flex h-7 min-w-0 max-w-56 items-center gap-1.5 rounded-lg border border-border/70 bg-muted/50 px-2.5 text-muted-foreground text-xs transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
          >
            <ZapIcon className="size-3 shrink-0 text-primary" />
            <span className="truncate">{label}</span>
            <ChevronDownIcon className="size-3 shrink-0 opacity-70" />
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-64 p-1">
          <p className="px-2 py-1 font-medium text-[11px] text-muted-foreground">{t("llmProfile.select")}</p>
          {/* default option */}
          <button
            type="button"
            onClick={() => {
              onChange(null);
              setOpen(false);
            }}
            className={cn(
              "flex w-full flex-col rounded px-2 py-1.5 text-left hover:bg-accent",
              selected == null && "bg-accent",
            )}
          >
            <span className="text-sm">{defaultLabel}</span>
            {activeDefault && (
              <span className="text-[11px] text-muted-foreground">
                {activeDefault.format} · {activeDefault.model}
              </span>
            )}
          </button>
          {profiles.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => {
                onChange(Number(p.id));
                setOpen(false);
              }}
              className={cn(
                "flex w-full flex-col rounded px-2 py-1.5 text-left hover:bg-accent",
                selected === Number(p.id) && "bg-accent",
              )}
            >
              <span className="text-sm">{p.name}</span>
              <span className="text-[11px] text-muted-foreground">
                {p.format} · {p.model}
              </span>
            </button>
          ))}
        </PopoverContent>
      </Popover>
      {rightSlot}
    </div>
  );
}

// DraftChat is the default right-pane view: a fresh chat (agent picker in the
// header, centered empty state, composer) with NO conversation created yet. The
// conversation is created lazily on the first send (ChatGPT-style), then the
// parent switches to the real ChatView.
function DraftChat({
  agents,
  profiles,
  onStarted,
}: {
  agents: Agent[];
  profiles: LLMProfile[];
  onStarted: (c: Conversation, pending?: { input?: string; attachments?: ChatAttachment[] }) => void;
}) {
  const t = useTranslations("chat");
  const [agentKey, setAgentKey] = React.useState("");
  const [llmProfileId, setLlmProfileId] = React.useState<number | null>(null);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [uploading, setUploading] = React.useState(false);

  // default the agent to Auto once agents load.
  React.useEffect(() => {
    if (!agentKey && agents.some((a) => a.key === "auto")) setAgentKey("auto");
  }, [agentKey, agents]);

  const agent = agents.find((a) => a.key === agentKey);

  async function send() {
    const msg = input.trim();
    if (!msg || !agentKey || sending) return;
    setSending(true);
    try {
      const c = await api.createConversation(agentKey, "", llmProfileId);
      await api.sendConversationMessage(c.id, msg);
      onStarted(c);
    } catch (e) {
      toast.error(t("toast.sendFailed", { msg: (e as Error).message }));
      setSending(false);
    }
  }

  // Attachments need a conversation to own the uploads dir (sessions/conv-<id>/),
  // and a draft has none yet — so picking a file CREATES the conversation, uploads
  // into it, then hands off to ChatView carrying the typed text + attachments (the
  // user sends from there). Mirrors the task main-agent console's upload, adapted to
  // the ChatGPT-style lazy-create flow.
  async function pickFiles(files: File[]) {
    if (files.length === 0 || !agentKey || uploading || sending) return;
    setUploading(true);
    try {
      const c = await api.createConversation(agentKey, "", llmProfileId);
      const r = await api.chatUpload("session", `conv-${c.id}`, files);
      onStarted(c, { input, attachments: r.attachments });
    } catch (e) {
      toast.error(t("toast.uploadFailed", { msg: (e as Error).message }));
      setUploading(false);
    }
  }

  const agentPicker = (
    <Select value={agentKey} onValueChange={setAgentKey}>
      <SelectTrigger
        size="sm"
        className="h-7 w-auto min-w-28 gap-1.5 rounded-lg border-border/70 bg-muted/50 px-2.5 text-xs"
      >
        <SelectValue placeholder={t("agentPicker.placeholder")} />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {agents.map((a) => (
            <SelectItem key={a.key} value={a.key}>
              <span className="flex items-center gap-2">
                <Bot className="size-3.5" />
                {a.name}
                {!a.builtin && (
                  <Badge variant="outline" className="px-1 py-0 text-[9px]">
                    {t("custom")}
                  </Badge>
                )}
              </span>
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );

  return (
    <>
      {/* 空态 / 落地页:插画 + 引导语 + 可点击建议 chips(点一下把句子填入输入框),让空态自己"教书" */}
      <EmptyState
        illustration="chat"
        className="min-h-0 flex-1"
        title={t("emptyStart", { agent: agent?.name ?? "Agent" })}
        description={agent?.description || t("emptyStartHint")}
        suggestions={[
          { label: t("suggestions.recon"), onClick: () => setInput(t("suggestions.recon")) },
          { label: t("suggestions.traffic"), onClick: () => setInput(t("suggestions.traffic")) },
          { label: t("suggestions.findings"), onClick: () => setInput(t("suggestions.findings")) },
        ]}
        actions={
          profiles.length === 0 ? (
            <Button asChild size="sm" variant="outline">
              <Link href="/system/llm">{t("llmProfile.configure")}</Link>
            </Button>
          ) : undefined
        }
      />

      <Composer
        value={input}
        onChange={setInput}
        onSend={send}
        disabled={sending || uploading || !agentKey}
        placeholder={t("composer.placeholder")}
        leftSlot={agentPicker}
        bottomSlot={
          <LLMProfileRow
            profiles={profiles}
            selected={llmProfileId}
            onChange={setLlmProfileId}
            disabled={sending || uploading}
          />
        }
        onPickFiles={pickFiles}
        uploading={uploading}
      />
    </>
  );
}

// ChatView is the right pane for one conversation — mirrors the task detail's
// main-agent console: header with agent + live badge + token/duration meta, a
// scroll-stick transcript (chat mode), and the composer.
function ChatView({
  conv,
  agents,
  profiles,
  initial,
  onTitleMaybeChanged,
  onConvUpdated,
}: {
  conv: Conversation;
  agents: Agent[];
  profiles: LLMProfile[];
  // pending text + already-uploaded attachments handed off from a draft that
  // created this conversation via the paperclip (consumed once, on mount).
  initial?: { input?: string; attachments?: ChatAttachment[] };
  onTitleMaybeChanged: () => void;
  onConvUpdated: () => void;
}) {
  const t = useTranslations("chat");
  const tc = useTranslations("checkpoints");
  const approvalFocus = useApprovalFocus({ conversationId: conv.id });
  const [messages, setMessages] = React.useState<Activity[]>([]);
  const [running, setRunning] = React.useState(false);
  const [input, setInput] = React.useState(initial?.input ?? "");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  // 文件上传方式一：已上传文件位于 sessions/conv-<id>/uploads，随下一条消息发送。
  const [attachments, setAttachments] = React.useState<ChatAttachment[]>(initial?.attachments ?? []);
  const [uploading, setUploading] = React.useState(false);
  const cursorRef = React.useRef(0); // newest loaded id — incremental-tail anchor
  const earliestRef = React.useRef(0); // earliest loaded id — reverse-pagination anchor
  const hasMoreRef = React.useRef(false); // older history remains above the loaded window
  const loadingMoreRef = React.useRef(false); // guard: one scroll-up load at a time
  const [historyLoaded, setHistoryLoaded] = React.useState(false);
  const [hasMore, setHasMore] = React.useState(false); // drives the "load earlier" hint
  // 检查点回滚后强制重载时间线（隐藏的步骤从列表消失）。
  const [reloadToken, setReloadToken] = React.useState(0);
  // 消息级回滚（气泡箭头）：待确认的用户消息 + 提交中标记；确认后消息原文回填输入框。
  const [rollbackTarget, setRollbackTarget] = React.useState<Activity | null>(null);
  const [rollbackBusy, setRollbackBusy] = React.useState(false);
  const agent = agents.find((a) => a.key === conv.agent_key);
  const currentProfileId = conv.llm_profile_id ?? null;
  const side = useSideQuestions(`/api/conversations/${conv.id}`);

  async function changeProfile(id: number | null) {
    try {
      await api.updateConversationProfile(conv.id, id);
      onConvUpdated();
    } catch (e) {
      toast.error(t("toast.switchLlmFailed", { msg: (e as Error).message }));
    }
  }

  // conversation-scoped detail fetcher for the reused Transcript renderer.
  const fetchDetail = React.useCallback((seq: number) => api.conversationMsgDetail(conv.id, seq), [conv.id]);

  // seq of the most-recent TodoWrite tool call (for the Todo popover); null if none.
  const latestTodoSeq = React.useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const a = messages[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [messages]);

  // reset + load whenever the selected conversation changes. Load only the LATEST
  // page on open — a long thread's final answer sits at the very end, so the newest
  // page shows it immediately (issue #3: opening/refresh used to load the oldest
  // page, so the completed result was missing until another message advanced the
  // cursor). Older history streams in on scroll-up (loadEarlier below).
  // reloadToken 不在回调内引用:它是触发式依赖——检查点回滚后 bump(onReverted)
  // 强制重载时间线,因此不能按 Biome 建议移除,显式抑制该诊断。
  // biome-ignore lint/correctness/useExhaustiveDependencies: 触发式依赖:回滚后强制重载时间线
  React.useEffect(() => {
    cursorRef.current = 0;
    earliestRef.current = 0;
    hasMoreRef.current = false;
    setHasMore(false);
    setMessages([]);
    setHistoryLoaded(false);
    setRunning(false);
    let live = true;
    api
      .conversationHistory(conv.id, 0, HISTORY_PAGE)
      .then((r) => {
        if (!live) return;
        setMessages((current) => mergeActivities(r.items, current));
        cursorRef.current = Math.max(cursorRef.current, r.cursor);
        earliestRef.current = r.items.length ? r.items[0].seq : 0;
        hasMoreRef.current = r.hasMore;
        setHasMore(r.hasMore);
        setRunning((current) => current || r.running);
        setHistoryLoaded(true);
      })
      .catch(() => {
        // A source link can retry history through its locator; ordinary chats
        // retain the existing usable empty state on a transient failure.
        if (live) setHistoryLoaded(true);
      });
    return () => {
      live = false;
    };
  }, [conv.id, reloadToken]);

  // A trigger or another tab may start a turn while this conversation is open.
  // A false list snapshot must not stop the tail before its final messages load.
  React.useEffect(() => {
    if (conv.running) setRunning(true);
  }, [conv.running]);

  // poll while a turn is running: pull new steps after the cursor.
  React.useEffect(() => {
    if (!running) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      let keepPolling = true;
      try {
        const r = await api.conversationMessages(conv.id, cursorRef.current);
        if (!live) return;
        if (r.items.length) {
          setMessages((prev) => mergeActivities(prev, r.items));
        }
        cursorRef.current = Math.max(cursorRef.current, r.cursor);
        keepPolling = r.running;
        setRunning(r.running);
        if (!r.running) onTitleMaybeChanged(); // first-turn auto-title landed
      } catch {
        /* transient — keep polling */
      } finally {
        // Slow responses must not overlap another poll with the same cursor.
        if (live && keepPolling) timer = setTimeout(() => void tick(), 1000);
      }
    };
    void tick();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [running, conv.id, onTitleMaybeChanged]);

  const loadFocusPage = React.useCallback(
    (before: number) => api.conversationHistory(conv.id, before, HISTORY_PAGE),
    [conv.id],
  );
  const mergeFocusPage = React.useCallback((page: { items: Activity[]; hasMore: boolean }) => {
    setMessages((prev) => mergeActivities(page.items, prev));
    earliestRef.current = page.items[0]?.seq ?? earliestRef.current;
    hasMoreRef.current = page.hasMore;
    setHasMore(page.hasMore);
  }, []);
  const focusHistory = useApprovalHistory(
    approvalFocus.state?.source,
    historyLoaded,
    messages,
    loadFocusPage,
    mergeFocusPage,
  );

  // ---- transcript auto-scroll (open → bottom; stick to bottom unless scrolled up) ----
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const atBottomRef = React.useRef(true);
  const viewport = React.useCallback(
    () => (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ?? null,
    [],
  );
  // Scroll-up loads one older page and prepends it, preserving the visual position
  // so the view doesn't jump (record height/offset before, restore the delta after).
  const loadEarlier = React.useCallback(async () => {
    if (loadingMoreRef.current || !hasMoreRef.current) return;
    const vp = viewport();
    if (!vp) return;
    loadingMoreRef.current = true;
    const prevH = vp.scrollHeight;
    const prevTop = vp.scrollTop;
    try {
      const r = await api.conversationHistory(conv.id, earliestRef.current, HISTORY_PAGE);
      if (r.items.length) {
        setMessages((prev) => mergeActivities(r.items, prev));
        earliestRef.current = r.items[0].seq;
      }
      hasMoreRef.current = r.hasMore;
      setHasMore(r.hasMore);
      requestAnimationFrame(() => {
        const v = viewport();
        if (v) v.scrollTop = prevTop + (v.scrollHeight - prevH);
      });
    } catch {
      /* transient — a later scroll retries */
    } finally {
      loadingMoreRef.current = false;
    }
  }, [conv.id, viewport]);
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      if (approvalFocus.state && !focusHistory.ready) return;
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
      if (vp.scrollTop <= 80) void loadEarlier(); // near top → pull an older page
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, loadEarlier, approvalFocus.state, focusHistory.ready]);
  // open/switch a conversation → jump to the latest (bottom)
  // biome-ignore lint/correctness/useExhaustiveDependencies: changing conversations intentionally retriggers the scroll reset.
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [conv.id, viewport]);
  // new activity → stick to bottom only if the user is already pinned there
  // biome-ignore lint/correctness/useExhaustiveDependencies: message and running changes intentionally retrigger bottom anchoring.
  React.useLayoutEffect(() => {
    if (approvalFocus.state || !atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [messages, running, viewport, approvalFocus.state]);

  // Per-conversation token total, live — same accounting as the main-agent
  // console: completed runs' `result` sum + the in-progress run's latest `usage`.
  const tokenTotal = React.useMemo(() => {
    let i = 0,
      o = 0,
      cr = 0;
    let li = 0,
      lo = 0,
      lcr = 0;
    let turns = 0; // Agent 迭代次数等于模型调用次数，每条 usage 活动计一次。
    for (const a of messages) {
      if (a.kind === "result") {
        i += a.input_tokens ?? 0;
        o += a.output_tokens ?? 0;
        cr += a.cache_read_tokens ?? 0;
        li = lo = lcr = 0;
      } else if (a.kind === "usage") {
        turns += 1;
        li = a.input_tokens ?? 0;
        lo = a.output_tokens ?? 0;
        lcr = a.cache_read_tokens ?? 0;
      }
    }
    const I = i + li,
      O = o + lo,
      CR = cr + lcr;
    return { i: I, o: O, cr: CR, turns, any: I + O + CR > 0 };
  }, [messages]);

  // 上下文窗口：优先用 capture 写入 usage 活动 metadata 的模型窗口；
  // 老会话（usage 行无 metadata）回退到本会话当前 LLM profile 的
  // context_window_k（后端 agent/provider.go CompactionWindow 同口径：K → token，
  // 缺省 200K，夹在 32K–1M）。都是后端配置值，前端不做任何估算。
  const configuredWindow = React.useMemo(() => {
    const profile =
      currentProfileId != null
        ? profiles.find((item) => Number(item.id) === currentProfileId)
        : profiles.find((item) => item.is_default);
    const k = profile?.context_window_k ?? 0;
    if (k <= 0) return 0;
    return Math.min(Math.max(k, 32), 1000) * 1000;
  }, [profiles, currentProfileId]);

  // 已用取最新一条 usage 活动：metadata.context_used 优先，否则用该行自带的
  // input + cache 写入（与 capture 统计口径一致）。没有 usage 行就不渲染环。
  const contextUsage = React.useMemo(() => {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const item = messages[index];
      if (item.kind !== "usage") continue;
      const size = item.metadata?.context_window ?? configuredWindow;
      if (!size) return null;
      const used =
        item.metadata?.context_used ??
        (item.input_tokens ?? 0) + (item.cache_read_tokens ?? 0) + (item.cache_write_tokens ?? 0);
      return { size, used };
    }
    return null;
  }, [messages, configuredWindow]);

  // 会话预算：上限/模式来自服务端（默认值即可用、无需用户配置）；已用量用本页已算好的
  // tokenTotal 做实时值——轮内的 usage 行还没落成 result 行，服务端汇总会滞后一轮。
  const [budget, setBudget] = React.useState<{ limit: number; mode: string } | null>(null);
  React.useEffect(() => {
    // 运行中不刷新：此时服务端的 used 还落后一轮，读到的上限也不会变；
    // 等本轮结束（running→false）再取一次即可。running 是这条 effect 的触发器。
    if (running) return;
    let alive = true;
    api
      .conversationBudget(conv.id)
      .then((b) => {
        if (alive) setBudget({ limit: b.limit, mode: b.mode });
      })
      .catch(() => {
        if (alive) setBudget(null);
      });
    return () => {
      alive = false;
    };
  }, [conv.id, running]);

  // pickFiles uploads into this conversation's session dir (sessions/conv-<id>/
  // uploads/) and queues the returned metadata to send with the next message.
  async function pickFiles(files: File[]) {
    if (files.length === 0) return;
    setUploading(true);
    try {
      const r = await api.chatUpload("session", `conv-${conv.id}`, files);
      setAttachments((prev) => [...prev, ...r.attachments]);
    } catch (e) {
      toast.error(t("toast.uploadFailed", { msg: (e as Error).message }));
    } finally {
      setUploading(false);
    }
  }

  async function send() {
    const msg = input.trim();
    const atts = attachments;
    if (side.handleCommand(msg, () => setInput(""))) return;
    if ((!msg && atts.length === 0) || sending || running) return;
    setSending(true);
    setInput("");
    setAttachments([]);
    try {
      await api.sendConversationMessage(conv.id, msg, atts.length ? atts : undefined);
      // The live loop pulls the persisted human turn immediately. Sharing that
      // fetch avoids racing a separate post-send request against the poller.
      setRunning(true);
    } catch (e) {
      toast.error(t("toast.sendFailed", { msg: (e as Error).message }));
      setInput(msg); // restore so the user doesn't lose their text
      setAttachments(atts); // and their attachments
    } finally {
      setSending(false);
    }
  }

  // stop aborts the in-flight run for this conversation. running flips to false on
  // the next 1s poll once the backend unwinds the agent; the trigger queue is not
  // affected — the agent's next queued fire still starts.
  async function stop() {
    if (stopping) return;
    setStopping(true);
    try {
      await api.stopConversation(conv.id);
    } catch (e) {
      toast.error(t("toast.stopFailed", { msg: (e as Error).message }));
    } finally {
      setStopping(false);
    }
  }

  // confirmRollback 执行消息级回滚（气泡箭头）：后端隐藏该消息及之后的步骤，这里
  // 把消息原文连同附件回填输入框，并 bump reloadToken 强制重载时间线（被回滚的
  // 内容从列表消失）。
  async function confirmRollback() {
    const step = rollbackTarget;
    if (!step || rollbackBusy) return;
    setRollbackBusy(true);
    try {
      const body = parseUserBody((await api.conversationMsgDetail(conv.id, step.seq)) || step.summary);
      const result = await api.rollbackConversationMessage(conv.id, step.seq);
      setInput(body.text);
      setAttachments(body.attachments.map((a) => ({ name: a.name, path: a.path, size: a.size })));
      setRollbackTarget(null);
      setReloadToken((n) => n + 1);
      toast.success(tc("msgRollback.doneConversation", { hidden: result.hidden }));
    } catch (e) {
      toast.error(tc("msgRollback.failed", { msg: (e as Error).message }));
    } finally {
      setRollbackBusy(false);
    }
  }

  return (
    <SideQuestionWorkspace side={side} label={agent?.name ?? conv.agent_key} composerLayout="inline">
      {/* header: unified agent workbench bar + live + token meta */}
      <div className="flex min-w-0 flex-wrap items-center gap-2 border-b bg-background/35 px-4 py-2.5 backdrop-blur-xs">
        <div className="flex size-6 shrink-0 items-center justify-center rounded-md bg-primary/12 text-primary">
          <Bot className="size-3.5" />
        </div>
        <span className="min-w-0 max-w-48 truncate font-semibold text-sm">{agent?.name ?? conv.agent_key}</span>
        <span className="hidden shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground sm:inline">
          {conv.agent_key}
        </span>
        {agent && !agent.builtin && (
          <Badge variant="outline" className="shrink-0 px-1.5 py-0 text-[10px]">
            {t("custom")}
          </Badge>
        )}
        {agent?.description && (
          <span className="min-w-0 truncate text-muted-foreground text-xs">{agent.description}</span>
        )}
        {running && <LiveBadge />}
        <SideQuestionButton side={side} />
        <CheckpointTimelineButton
          scope="conversation"
          id={String(conv.id)}
          onReverted={() => setReloadToken((token) => token + 1)}
        />
        <div className="ml-auto flex min-w-0 max-w-full items-center justify-end gap-x-2 gap-y-1 text-muted-foreground text-xs max-sm:w-full max-sm:flex-wrap">
          {tokenTotal.turns > 0 && (
            <span
              title={t("turnsTooltip")}
              className="rounded-md border bg-muted/60 px-2 py-0.5 text-[11px] tabular-nums"
            >
              {t("turnsCount", { turns: tokenTotal.turns })}
            </span>
          )}
          {tokenTotal.any && (
            <span
              title={t("tokensTooltip")}
              className="min-w-0 truncate rounded-md border bg-muted/60 px-2 py-0.5 text-[11px] tabular-nums"
            >
              {t("tokensLine", {
                input: fmtTokens(tokenTotal.i),
                cache: fmtTokens(tokenTotal.cr),
                output: fmtTokens(tokenTotal.o),
              })}
            </span>
          )}
          {budget &&
            budget.limit > 0 &&
            (() => {
              const used = tokenTotal.i + tokenTotal.o;
              // mode!=enforce 时只做统计展示：不能让「预算 400K」看起来像已经拦了。
              const enforced = budget.mode === "enforce";
              const tier = !enforced
                ? "ok"
                : used >= budget.limit
                  ? "exhausted"
                  : used * 100 >= budget.limit * 85
                    ? "tight"
                    : used * 100 >= budget.limit * 70
                      ? "watch"
                      : "ok";
              const cls =
                tier === "exhausted"
                  ? "border-red-500/60 text-red-600 dark:text-red-400"
                  : tier === "tight"
                    ? "border-amber-500/60 text-amber-600 dark:text-amber-400"
                    : "text-muted-foreground";
              return (
                <span
                  title={
                    enforced
                      ? `本条会话累计 token 预算：已用 ${fmtTokens(used)} / 上限 ${fmtTokens(budget.limit)}。达到上限后本轮自动收尾（可在系统配置调整 default_conversation_token_budget）。`
                      : `仅统计，不拦截：已用 ${fmtTokens(used)}（参考上限 ${fmtTokens(budget.limit)}）。在系统配置填写 default_conversation_token_budget 后才启用收尾。`
                  }
                  className={`rounded-md border bg-muted/60 px-2 py-0.5 text-[11px] tabular-nums ${cls}`}
                >
                  {tier === "exhausted" ? "预算用尽" : enforced ? "预算" : "用量"} {fmtTokens(used)}/
                  {fmtTokens(budget.limit)}
                </span>
              );
            })()}
        </div>
      </div>

      <ApprovalExecutionFocus focus={approvalFocus} history={focusHistory} />

      {/* messages: centered comfortable reading column */}
      <ScrollArea type="auto" className="[&_[data-slot=scroll-area-viewport]>div]:block! min-h-0 min-w-0 flex-1">
        <div className="mx-auto min-w-0 max-w-4xl px-4 py-4 md:px-6" ref={contentRef}>
          {messages.length === 0 && !running ? (
            <EmptyState illustration="chat" title={t("emptyStart", { agent: agent?.name ?? conv.agent_key })} />
          ) : (
            <>
              {hasMore && (
                <div className="pb-2 text-center text-[11px] text-muted-foreground/70">{t("loadEarlierHint")}</div>
              )}
              <Transcript
                activity={messages}
                live={running}
                chat
                fetchDetail={fetchDetail}
                focusedSeq={focusHistory.ready ? approvalFocus.state?.source?.seq : undefined}
                rollback={{
                  disabledReason: running ? tc("msgRollback.busyTurn") : undefined,
                  onRollback: setRollbackTarget,
                }}
              />
            </>
          )}
        </div>
      </ScrollArea>

      <Composer
        value={input}
        onChange={setInput}
        onSend={send}
        disabled={running || sending}
        allowBtw
        placeholder={running ? t("composer.placeholderRunning") : t("composer.placeholder")}
        running={running}
        onStop={stop}
        stopDisabled={stopping}
        attachments={attachments}
        onPickFiles={pickFiles}
        onRemoveAttachment={(path) => setAttachments((p) => p.filter((x) => x.path !== path))}
        uploading={uploading}
        contextUsage={contextUsage}
        bottomSlot={
          <LLMProfileRow
            profiles={profiles}
            selected={currentProfileId}
            onChange={changeProfile}
            disabled={running || sending}
            rightSlot={<TodoPopover seq={latestTodoSeq} fetchDetail={fetchDetail} />}
          />
        }
      />
      <MessageRollbackDialog
        open={rollbackTarget !== null}
        busy={rollbackBusy}
        onOpenChange={(open) => {
          if (!open) setRollbackTarget(null);
        }}
        onConfirm={() => void confirmRollback()}
      />
    </SideQuestionWorkspace>
  );
}

// ConversationItem is one row in the left rail: title, agent subtitle, inline
// rename, pin marker, and a compact action menu. Rows nested under an agent
// group drop the agent subtitle (showAgent=false) — the header already says it.
const ConversationItem = React.memo(function ConversationItem({
  conv,
  agent,
  showAgent = true,
  active,
  renaming,
  renameText,
  onSelect,
  onStartRename,
  onRenameText,
  onCommitRename,
  onCancelRename,
  onTogglePinned,
  onDelete,
  selectionMode,
  selectedForDelete,
  onSelectedForDeleteChange,
}: {
  conv: Conversation;
  agent?: Agent;
  showAgent?: boolean;
  active: boolean;
  renaming: boolean;
  renameText: string;
  onSelect: (id: number) => void;
  onStartRename: (conversation: Conversation) => void;
  onRenameText: (v: string) => void;
  onCommitRename: (id: number, title: string) => void;
  onCancelRename: () => void;
  onTogglePinned: (conversation: Conversation) => void;
  onDelete: (id: number) => void;
  selectionMode: boolean;
  selectedForDelete: boolean;
  onSelectedForDeleteChange: (id: number, checked: boolean) => void;
}) {
  const t = useTranslations("chat");
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const renameInputRef = React.useRef<HTMLInputElement>(null);
  const cancelRenameRef = React.useRef(false);
  const pinned = conversationIsPinned(conv);

  React.useEffect(() => {
    if (!renaming) return;
    cancelRenameRef.current = false;
    renameInputRef.current?.focus();
    renameInputRef.current?.select();
  }, [renaming]);

  return (
    <div
      className={cn(
        "group flex min-w-0 items-center gap-1 rounded-lg border-l-2 pr-1 transition-colors",
        active
          ? "border-l-primary bg-accent font-medium text-accent-foreground shadow-2xs"
          : "border-l-transparent hover:bg-accent/45",
      )}
    >
      {selectionMode && (
        <Checkbox
          checked={selectedForDelete}
          onCheckedChange={(checked) => onSelectedForDeleteChange(conv.id, checked === true)}
          aria-label={t("item.selectAria", { title: conv.title || t("untitledConversation") })}
          className="ml-1 shrink-0"
        />
      )}
      {renaming ? (
        <input
          ref={renameInputRef}
          value={renameText}
          onChange={(e) => onRenameText(e.target.value)}
          onBlur={() => {
            if (!cancelRenameRef.current) onCommitRename(conv.id, renameText);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommitRename(conv.id, renameText);
            if (e.key === "Escape") {
              e.preventDefault();
              cancelRenameRef.current = true;
              onCancelRename();
            }
          }}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-2 py-1 text-sm"
        />
      ) : (
        <button
          type="button"
          onClick={() => onSelect(conv.id)}
          onDoubleClick={() => onStartRename(conv)}
          title={t("item.renameHint")}
          className="min-w-0 flex-1 rounded-md px-2 py-1.5 text-left"
        >
          <div className="flex min-w-0 items-center gap-1.5">
            {pinned && <PinIcon className="size-3 shrink-0 text-primary" aria-label={t("item.pinnedAria")} />}
            <div className="truncate text-sm">{conv.title || t("untitledConversation")}</div>
            {conv.running ? (
              <Badge variant="secondary" className="shrink-0 gap-1" title={t("item.runningTooltip")}>
                <Spinner className="size-3" aria-hidden="true" />
                {t("running")}
              </Badge>
            ) : null}
          </div>
          <div className="flex min-w-0 items-center gap-1 text-[11px] text-muted-foreground">
            {showAgent && (
              <>
                <Bot className="size-3 shrink-0" />
                <span className="min-w-0 truncate">{agent?.name ?? conv.agent_key}</span>
                <span className="shrink-0">·</span>
              </>
            )}
            <span className="shrink-0">
              {new Date(conv.created_at).toLocaleDateString("zh-CN", {
                month: "numeric",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </span>
            <span className="shrink-0 opacity-60">#{conv.id}</span>
          </div>
        </button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className="shrink-0 text-muted-foreground"
            aria-label={t("item.manageAria", { title: conv.title || t("untitledConversation") })}
          >
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuItem onSelect={() => onStartRename(conv)}>
              <PencilIcon />
              {t("item.rename")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onTogglePinned(conv)}>
              {pinned ? <PinOffIcon /> : <PinIcon />}
              {pinned ? t("item.unpin") : t("item.pin")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem variant="destructive" onSelect={() => setDeleteOpen(true)}>
              <Trash2Icon />
              {t("item.delete")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("item.deleteTitle", { title: conv.title || t("untitledConversation") })}
            </AlertDialogTitle>
            <AlertDialogDescription>{t("item.deleteDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("item.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => onDelete(conv.id)}>{t("item.delete")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
});

// AgentGroupHeader is the sticky, clickable divider above one agent's rows:
// collapse chevron, agent name, a running marker, and the row count.
function AgentGroupHeader({
  group,
  collapsed,
  hasActive,
  onToggle,
}: {
  group: AgentGroup;
  collapsed: boolean;
  hasActive: boolean;
  onToggle: (key: string) => void;
}) {
  const t = useTranslations("chat");
  return (
    <button
      type="button"
      onClick={() => onToggle(group.key)}
      aria-expanded={!collapsed}
      title={collapsed ? t("group.expand", { name: group.name }) : t("group.collapse", { name: group.name })}
      className={cn(
        "sticky top-0 z-10 flex min-w-0 items-center gap-1.5 rounded-md bg-card px-1.5 py-1 text-left font-medium text-[11px] transition-colors hover:bg-accent/50",
        collapsed && hasActive ? "text-foreground" : "text-muted-foreground",
      )}
    >
      <ChevronRightIcon className={cn("size-3 shrink-0 transition-transform", !collapsed && "rotate-90")} />
      <Bot className="size-3 shrink-0" />
      <span className="min-w-0 flex-1 truncate">{group.name}</span>
      {collapsed && hasActive && (
        <span className="size-1.5 shrink-0 rounded-full bg-primary" title={t("group.activeInside")} />
      )}
      {group.runningCount > 0 && (
        <Spinner className="size-3 shrink-0" aria-label={t("group.runningAria", { count: group.runningCount })} />
      )}
      <span className="shrink-0 tabular-nums opacity-60">{group.conversations.length}</span>
    </button>
  );
}

export default function ChatPage() {
  const locale = useLocale();
  const t = useTranslations("chat");
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [convs, setConvs] = React.useState<Conversation[]>([]);
  const [agentFilter, setAgentFilter] = React.useState<string | null>(null);
  const [selectedId, setSelectedId] = React.useState<number | null>(null);
  const [sourceRequested, setSourceRequested] = React.useState(false);
  const [convsLoaded, setConvsLoaded] = React.useState(false);
  const selectConversation = React.useCallback(
    (id: number | null) => {
      if (id !== selectedId) {
        const url = new URL(window.location.href);
        url.searchParams.delete("approval");
        setSourceRequested(false);
        window.history.replaceState(null, "", url);
      }
      setSelectedId(id);
    },
    [selectedId],
  );

  const [renamingId, setRenamingId] = React.useState<number | null>(null);
  const [renameText, setRenameText] = React.useState("");
  const [selectedConversationIds, setSelectedConversationIds] = React.useState<Set<number>>(() => new Set());
  // selectionMode 控制多选 UI，默认关闭以保持列表简洁。
  // “多选”按钮开启，“完成”关闭并清除选择。
  const [selectionMode, setSelectionMode] = React.useState(false);
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false);
  const [bulkDeleting, setBulkDeleting] = React.useState(false);
  const [visibleConversationCount, setVisibleConversationCount] = React.useState(CONVERSATION_LIST_PAGE);
  // Collapsed agent groups. Hydrated from localStorage after mount (not a lazy
  // useState init) so the server and client render the same first pass.
  const [collapsedAgents, setCollapsedAgents] = React.useState<Set<string>>(() => new Set());
  // Pending text + uploaded attachments handed off from a draft that created a
  // conversation via the paperclip, keyed by the new conversation id (consumed once
  // by ChatView on mount; ids never repeat, so leftover entries are harmless).
  const [pendingByConv, setPendingByConv] = React.useState<
    Record<number, { input?: string; attachments?: ChatAttachment[] }>
  >({});

  const conversationListSeq = React.useRef(0);
  const reloadConvs = React.useCallback(async () => {
    const seq = ++conversationListSeq.current;
    try {
      const items = await api.conversations();
      if (seq === conversationListSeq.current) {
        setConvs(items);
        setConvsLoaded(true);
      }
    } catch {
      // Preserve the selected transcript and list on a transient poll failure.
    }
  }, []);
  React.useEffect(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => {
        /* Agent metadata is optional for rendering an existing conversation. */
      });
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => {
        /* The conversation remains usable without profile labels. */
      });
  }, []);

  // One list poll supplies runtime state for all sidebar rows, including the
  // unselected ones. Await completion so slow requests do not overlap.
  React.useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      await reloadConvs();
      if (!disposed) timer = setTimeout(() => void poll(), 2000);
    }
    void poll();
    return () => {
      disposed = true;
      conversationListSeq.current++;
      clearTimeout(timer);
    };
  }, [reloadConvs]);

  React.useEffect(() => {
    setSelectedConversationIds((current) => {
      if (current.size === 0) return current;
      const live = new Set(convs.map((conversation) => conversation.id));
      const next = new Set([...current].filter((id) => live.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [convs]);

  React.useEffect(() => {
    const raw = getLocalStorageValue(COLLAPSED_AGENTS_KEY);
    if (!raw) return;
    try {
      const keys = JSON.parse(raw);
      if (Array.isArray(keys))
        setCollapsedAgents(new Set(keys.filter((key): key is string => typeof key === "string")));
    } catch {
      // Corrupted entry → start with everything expanded.
    }
  }, []);

  const toggleAgentCollapsed = React.useCallback((key: string) => {
    setCollapsedAgents((current) => {
      const next = new Set(current);
      if (!next.delete(key)) next.add(key);
      setLocalStorageValue(COLLAPSED_AGENTS_KEY, JSON.stringify([...next]));
      return next;
    });
  }, []);

  // Restore the open conversation from the URL (?c=<id>) on mount, so a refresh
  // returns to the same thread instead of the empty draft view. Runs after
  // hydration (not a lazy useState init) to avoid a server/client mismatch.
  React.useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    setSourceRequested(params.has("approval"));
    const c = params.get("c");
    const id = c ? Number(c) : NaN;
    if (Number.isFinite(id)) setSelectedId(id);
  }, []);
  // Mirror the current selection into the URL (replaceState → no history spam). A
  // selectedId with no matching conversation (e.g. a stale ?c=, or a just-created
  // one before reloadConvs lands) simply renders the draft view — harmless — so we
  // deliberately do NOT auto-clear it here (that raced new-conversation creation).
  React.useEffect(() => {
    const url = new URL(window.location.href);
    if (selectedId != null) url.searchParams.set("c", String(selectedId));
    else url.searchParams.delete("c");
    window.history.replaceState(null, "", url);
  }, [selectedId]);

  const selected = React.useMemo(() => convs.find((c) => c.id === selectedId) ?? null, [convs, selectedId]);
  const agentByKey = React.useMemo(() => new Map(agents.map((agent) => [agent.key, agent])), [agents]);
  const filteredConversations = React.useMemo(
    () => (agentFilter === null ? convs : convs.filter((conversation) => conversation.agent_key === agentFilter)),
    [convs, agentFilter],
  );
  const visibleConversations = React.useMemo(
    () => filteredConversations.slice(0, visibleConversationCount),
    [filteredConversations, visibleConversationCount],
  );
  // Pinned rows stay a flat block above the groups (server order = pinned_at DESC);
  // everything else is bucketed per agent, most-recently-active agent first.
  const pinnedConversations = React.useMemo(
    () => visibleConversations.filter(conversationIsPinned),
    [visibleConversations],
  );
  const agentGroups = React.useMemo(
    () =>
      groupByAgent(
        visibleConversations.filter((c) => !conversationIsPinned(c)),
        agentByKey,
      ),
    [visibleConversations, agentByKey],
  );
  // 聊天 Agent 包含自定义 Agent 与 role=assistant 的对话型内置 Agent，如 Auto、渗透测试。
  // goals、planner、mainagent、worker 是任务专用编排角色，不显示于聊天页。
  const chatAgents = React.useMemo(() => agents.filter((a) => !a.builtin || a.role === "assistant"), [agents]);
  const agentFilterOptions = React.useMemo(() => {
    const counts = new Map<string, number>();
    for (const conversation of convs) {
      counts.set(conversation.agent_key, (counts.get(conversation.agent_key) ?? 0) + 1);
    }
    // Include historical sources even if their Agent has since been disabled or deleted.
    const keys = new Set([...chatAgents.map((agent) => agent.key), ...counts.keys()]);
    if (agentFilter !== null) keys.add(agentFilter);
    return [...keys]
      .map((key) => ({ key, name: agentByKey.get(key)?.name || key, count: counts.get(key) ?? 0 }))
      .sort((a, b) => a.name.localeCompare(b.name, locale));
  }, [convs, chatAgents, agentByKey, agentFilter, locale]);
  const conversationCountLabel =
    agentFilter === null
      ? t("totalCount", { count: convs.length })
      : t("filteredCount", { shown: filteredConversations.length, total: convs.length });

  function changeAgentFilter(key: string | null) {
    setAgentFilter(key);
    setVisibleConversationCount(CONVERSATION_LIST_PAGE);
    setSelectedConversationIds(new Set());
    setBulkDeleteOpen(false);
    setRenamingId(null);
  }

  const selectedConversationCount = selectedConversationIds.size;
  const allConversationsSelected =
    filteredConversations.length > 0 && selectedConversationCount === filteredConversations.length;
  const someConversationsSelected = selectedConversationCount > 0 && !allConversationsSelected;
  let conversationHeaderChecked: boolean | "indeterminate" = false;
  if (allConversationsSelected) conversationHeaderChecked = true;
  else if (someConversationsSelected) conversationHeaderChecked = "indeterminate";

  const toggleConversationSelected = React.useCallback((id: number, checked: boolean) => {
    setSelectedConversationIds((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  function toggleAllConversations(checked: boolean) {
    setSelectedConversationIds(
      checked ? new Set(filteredConversations.map((conversation) => conversation.id)) : new Set(),
    );
  }

  function exitSelectionMode() {
    setSelectionMode(false);
    setSelectedConversationIds(new Set());
  }

  const del = React.useCallback(
    async (id: number) => {
      try {
        await api.deleteConversation(id);
        setSelectedId((current) => (current === id ? null : current));
        setSelectedConversationIds((current) => {
          if (!current.has(id)) return current;
          const next = new Set(current);
          next.delete(id);
          return next;
        });
        void reloadConvs();
      } catch (e) {
        toast.error(t("toast.deleteFailed", { msg: (e as Error).message }));
      }
    },
    [reloadConvs, t],
  );

  async function deleteSelectedConversations() {
    const ids = [...selectedConversationIds];
    if (ids.length === 0 || bulkDeleting) return;
    setBulkDeleting(true);
    const deleted = new Set<number>();
    const failed: { id: number; error: string }[] = [];
    try {
      for (let offset = 0; offset < ids.length; offset += 100) {
        const result = await api.deleteConversations(ids.slice(offset, offset + 100));
        for (const item of result.items) {
          if (item.ok) deleted.add(item.id);
          else failed.push({ id: item.id, error: item.error ?? t("conversationNotFound") });
        }
      }
      if (deleted.has(selectedId ?? -1)) selectConversation(null);
      setSelectedConversationIds((current) => {
        const next = new Set(current);
        for (const id of deleted) next.delete(id);
        return next;
      });
      if (deleted.size > 0) toast.success(t("bulkDeleted", { count: deleted.size }));
      if (failed.length > 0) {
        const details = failed
          .slice(0, 3)
          .map((item) => t("bulkDeleteFailDetail", { id: item.id, error: item.error }))
          .join(", ");
        toast.error(t("bulkDeleteFailed", { count: failed.length, details }) + (failed.length > 3 ? t("andMore") : ""));
      }
      setBulkDeleteOpen(false);
      // Fully successful → return to the clean list; keep selection mode on if
      // some failed so the user can retry the remaining ones.
      if (failed.length === 0) setSelectionMode(false);
      void reloadConvs();
    } catch (error) {
      toast.error(t("bulkDeleteError", { msg: (error as Error).message }));
      void reloadConvs();
    } finally {
      setBulkDeleting(false);
    }
  }

  const togglePinned = React.useCallback(
    async (conversation: Conversation) => {
      const pinned = conversationIsPinned(conversation);
      try {
        await api.pinConversation(conversation.id, !pinned);
        void reloadConvs();
      } catch (e) {
        const msg = (e as Error).message;
        toast.error(pinned ? t("toast.unpinFailed", { msg }) : t("toast.pinFailed", { msg }));
      }
    },
    [reloadConvs, t],
  );

  const startRename = React.useCallback((c: Conversation) => {
    setRenamingId(c.id);
    setRenameText(c.title || "");
  }, []);
  const commitRename = React.useCallback(
    async (id: number, value: string) => {
      const title = value.trim();
      setRenamingId(null);
      if (!title) return;
      try {
        await api.renameConversation(id, title);
        void reloadConvs();
      } catch (e) {
        toast.error(t("toast.renameFailed", { msg: (e as Error).message }));
      }
    },
    [reloadConvs, t],
  );
  const cancelRename = React.useCallback(() => setRenamingId(null), []);

  return (
    <div
      data-content-padding="false"
      className="flex h-[calc(100svh-3rem)] min-w-0 flex-col overflow-hidden p-2 sm:p-3 md:h-[calc(100svh-3.5rem)] md:p-4"
    >
      <div className="grid min-h-0 min-w-0 flex-1 grid-cols-1 grid-rows-[minmax(10rem,14rem)_minmax(0,1fr)] overflow-hidden rounded-xl border border-border/80 bg-card shadow-xs md:grid-cols-[17.5rem_minmax(0,1fr)] md:grid-rows-[minmax(0,1fr)]">
        {/* left: conversation fleet rail */}
        <div className="flex flex-col overflow-hidden border-border/75 bg-sidebar/40 max-md:border-b md:border-r">
          <div className="flex flex-col gap-2 border-b p-2.5">
            <Button size="sm" className="w-full" onClick={() => selectConversation(null)}>
              <PlusIcon /> {t("newConversation")}
            </Button>
            <Select
              value={agentFilter === null ? "all" : `agent:${agentFilter}`}
              onValueChange={(value) => changeAgentFilter(value === "all" ? null : value.slice(6))}
              disabled={bulkDeleting}
            >
              <SelectTrigger size="sm" className="w-full min-w-0" aria-label={t("filterByAgentAria")}>
                <Bot />
                <SelectValue placeholder={t("allAgents")} />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">{t("allAgents")}</SelectItem>
                  {agentFilterOptions.map((agent) => (
                    <SelectItem key={agent.key} value={`agent:${agent.key}`}>
                      {t("agentOption", { name: agent.name, count: agent.count })}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {convs.length > 0 &&
              (selectionMode ? (
                <div className="flex items-center gap-2 px-1">
                  <Checkbox
                    checked={conversationHeaderChecked}
                    onCheckedChange={(checked) => toggleAllConversations(checked === true)}
                    aria-label={t("selectAllAria")}
                    disabled={filteredConversations.length === 0 || bulkDeleting}
                  />
                  <span className="min-w-0 flex-1 text-muted-foreground text-xs tabular-nums">
                    {selectedConversationCount > 0
                      ? t("selectedCount", { count: selectedConversationCount })
                      : conversationCountLabel}
                  </span>
                  {selectedConversationCount > 0 && (
                    <Button
                      size="sm"
                      variant="destructive"
                      disabled={bulkDeleting}
                      onClick={() => setBulkDeleteOpen(true)}
                    >
                      <Trash2Icon data-icon="inline-start" />
                      {t("item.delete")}
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={exitSelectionMode}>
                    {t("done")}
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-2 px-1">
                  <span className="min-w-0 flex-1 text-muted-foreground text-xs tabular-nums">
                    {conversationCountLabel}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-muted-foreground"
                    onClick={() => setSelectionMode(true)}
                    disabled={filteredConversations.length === 0}
                  >
                    <ListChecksIcon data-icon="inline-start" />
                    {t("multiSelect")}
                  </Button>
                </div>
              ))}
          </div>
          <ScrollArea
            key={agentFilter === null ? "all" : `agent:${agentFilter}`}
            type="auto"
            className="[&_[data-slot=scroll-area-viewport]>div]:block! min-h-0 min-w-0 flex-1"
          >
            <div className="flex min-w-0 flex-col gap-0.5 p-2">
              {filteredConversations.length === 0 && (
                <EmptyState
                  compact
                  illustration="chat"
                  className="py-6"
                  title={agentFilter === null ? t("emptyAll") : t("emptyForAgent")}
                />
              )}
              {pinnedConversations.map((c) => (
                <ConversationItem
                  key={c.id}
                  conv={c}
                  agent={agentByKey.get(c.agent_key)}
                  active={selectedId === c.id}
                  renaming={renamingId === c.id}
                  renameText={renamingId === c.id ? renameText : ""}
                  onSelect={selectConversation}
                  onStartRename={startRename}
                  onRenameText={setRenameText}
                  onCommitRename={commitRename}
                  onCancelRename={cancelRename}
                  onTogglePinned={togglePinned}
                  onDelete={del}
                  selectionMode={selectionMode}
                  selectedForDelete={selectedConversationIds.has(c.id)}
                  onSelectedForDeleteChange={toggleConversationSelected}
                />
              ))}
              {agentGroups.map((group) => {
                const collapsed = collapsedAgents.has(group.key);
                return (
                  <React.Fragment key={group.key}>
                    <AgentGroupHeader
                      group={group}
                      collapsed={collapsed}
                      hasActive={group.conversations.some((c) => c.id === selectedId)}
                      onToggle={toggleAgentCollapsed}
                    />
                    {!collapsed &&
                      group.conversations.map((c) => (
                        <ConversationItem
                          key={c.id}
                          conv={c}
                          agent={agentByKey.get(c.agent_key)}
                          showAgent={false}
                          active={selectedId === c.id}
                          renaming={renamingId === c.id}
                          renameText={renamingId === c.id ? renameText : ""}
                          onSelect={selectConversation}
                          onStartRename={startRename}
                          onRenameText={setRenameText}
                          onCommitRename={commitRename}
                          onCancelRename={cancelRename}
                          onTogglePinned={togglePinned}
                          onDelete={del}
                          selectionMode={selectionMode}
                          selectedForDelete={selectedConversationIds.has(c.id)}
                          onSelectedForDeleteChange={toggleConversationSelected}
                        />
                      ))}
                  </React.Fragment>
                );
              })}
              {visibleConversationCount < filteredConversations.length && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="mt-1 w-full"
                  onClick={() => setVisibleConversationCount((count) => count + CONVERSATION_LIST_PAGE)}
                >
                  {t("loadMore")}
                </Button>
              )}
            </div>
          </ScrollArea>
        </div>

        {/* right: chat view */}
        <div className="flex min-w-0 flex-col overflow-hidden bg-card">
          {selected ? (
            <ChatView
              key={selected.id}
              conv={selected}
              agents={agents}
              profiles={profiles}
              initial={pendingByConv[selected.id]}
              onTitleMaybeChanged={reloadConvs}
              onConvUpdated={reloadConvs}
            />
          ) : sourceRequested ? (
            <div role="status" className="p-6 text-muted-foreground text-sm">
              {convsLoaded ? t("deletedConversation") : t("loadingConversation")}
            </div>
          ) : (
            <DraftChat
              agents={chatAgents}
              profiles={profiles}
              onStarted={(c, pending) => {
                if (agentFilter !== null && agentFilter !== c.agent_key) changeAgentFilter(null);
                // Insert the new conversation immediately so `selected` resolves to
                // it on this render (switching to ChatView right away, before the
                // async reloadConvs lands); reloadConvs then reconciles titles etc.
                if (pending) setPendingByConv((p) => ({ ...p, [c.id]: pending }));
                setConvs((prev) => {
                  if (prev.some((item) => item.id === c.id)) return prev;
                  const firstUnpinned = prev.findIndex((item) => !conversationIsPinned(item));
                  const insertAt = firstUnpinned < 0 ? prev.length : firstUnpinned;
                  return [...prev.slice(0, insertAt), c, ...prev.slice(insertAt)];
                });
                setSelectedId(c.id);
                void reloadConvs();
              }}
            />
          )}
        </div>
      </div>
      <AlertDialog open={bulkDeleteOpen} onOpenChange={setBulkDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("bulkDeleteTitle", { count: selectedConversationCount })}</AlertDialogTitle>
            <AlertDialogDescription>{t("bulkDeleteDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={bulkDeleting}>{t("item.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={bulkDeleting || selectedConversationCount === 0}
              onClick={(event) => {
                event.preventDefault();
                void deleteSelectedConversations();
              }}
            >
              {bulkDeleting && <Loader2Icon data-icon="inline-start" className="animate-spin" />}
              {bulkDeleting ? t("deleting") : t("confirmDelete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
