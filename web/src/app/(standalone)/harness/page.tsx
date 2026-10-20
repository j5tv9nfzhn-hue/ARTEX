"use client";

// Harness 工作台：三栏（会话 / 时间线 / 变更）。
//
// 独立前端：路由挂在 (standalone) 组下，不套控制台外壳（侧栏 / 顶栏 / 通知气泡）。
// 侧栏「功能」组用 newTab 在新标签打开；直接访问 URL 也是同一个整页界面。
// 深链：/harness?conv=104 —— 刷新或转链接后仍落在同一会话。
//
// 布局取满视口、内部三栏各自滚动：时间线是唯一的滚动主轴，左右栏不跟着跑。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon, LinkIcon, Loader2Icon, TerminalIcon, Undo2Icon } from "lucide-react";

import { Composer } from "@/components/harness/composer";
import { ConversationList } from "@/components/harness/conversation-list";
import { ReviewRail } from "@/components/harness/review-rail";
import { Timeline } from "@/components/harness/timeline";
import { Button } from "@/components/ui/button";
import { useHarness } from "@/lib/harness/use-harness";
import { cn } from "@/lib/utils";

import "@/styles/harness.css";

// 单独抽出来给 message 一个显式类型：直接写 h.error ? … : null 会被
// biome 的 noMisusedPromises 误判（hook 返回值推断不稳）。
function ErrorBar({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <div className="shrink-0 border-b bg-destructive/10 px-3 py-1.5 text-[11.5px] text-destructive">{message}</div>
  );
}

function Workspace() {
  const params = useSearchParams();
  const conv = Number(params.get("conv")) || undefined;
  const h = useHarness(conv);
  const scrollRef = React.useRef<HTMLDivElement | null>(null);
  const [deciding, setDeciding] = React.useState(false);
  const [copied, setCopied] = React.useState(false);

  // 时间线贴着底部：新项到达时只在用户本来就靠底时自动跟随，否则不抢滚动。
  const _lastKey = h.items.length ? h.items[h.items.length - 1].id : "";
  React.useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 160;
    if (nearBottom) el.scrollTop = el.scrollHeight;
  }, []);

  // 切换会话时把深链同步到地址栏（replace 不新增历史）。
  React.useEffect(() => {
    if (!h.activeId) return;
    // 不带尾斜杠：dev 下 /harness/ 会 308 回 /harness，静态导出由 webuiHandler 兜底。
    const url = `/harness?conv=${h.activeId}`;
    if (window.location.pathname.replace(/\/+$/, "") + window.location.search !== url) {
      window.history.replaceState(null, "", url);
    }
  }, [h.activeId]);

  const decide = async (d: "allowed" | "denied") => {
    setDeciding(true);
    try {
      await h.decide(d);
    } finally {
      setDeciding(false);
    }
  };

  const lastUserSeq = React.useMemo(() => {
    for (let i = h.items.length - 1; i >= 0; i -= 1) {
      const it = h.items[i];
      if (it.type === "user" && it.seq > 0) return it.seq;
    }
    return 0;
  }, [h.items]);

  const copyLink = async () => {
    if (!h.activeId) return;
    try {
      await navigator.clipboard.writeText(`${window.location.origin}/harness?conv=${h.activeId}`);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    } catch {
      // 非安全上下文下 clipboard 不可用，静默失败即可。
    }
  };

  if (h.loading) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        <Loader2Icon className="mr-2 size-4 animate-spin" />
        <span className="text-[13px]">正在打开工作台…</span>
      </div>
    );
  }

  const title = h.activeConversation?.title || "新会话";
  const agent = h.activeConversation ? h.agentByKey.get(h.activeConversation.agent_key) : undefined;

  return (
    // 独立页没有控制台头部与内容区 padding，直接锁满视口。
    // 不锁高的话整页会跟着时间线长高，三栏各自的滚动轴就失效了。
    <div className="harness-root flex h-dvh min-h-0 overflow-hidden bg-background">
      <div className="hidden min-h-0 md:flex">
        <ConversationList
          conversations={h.conversations}
          agents={h.agentByKey}
          activeId={h.activeId}
          hasPending={h.pending.length > 0}
          workspace={h.workspace}
          onSelect={(id) => void h.openConversation(id)}
          onCreate={() => void h.newConversation()}
        />
      </div>

      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-11 shrink-0 items-center gap-2 border-b px-3">
          <TerminalIcon className="hidden size-3.5 shrink-0 text-muted-foreground md:block" />
          {/* 窄屏没有左栏（会话列表 md 以上才显示），用原生下拉补一个会话切换入口。 */}
          <select
            aria-label="切换会话"
            className="h-7 max-w-[52%] min-w-0 rounded-sm border bg-transparent px-1 text-[12px] md:hidden"
            onChange={(e) => {
              const v = e.target.value;
              if (v === "new") void h.newConversation();
              else if (v) void h.openConversation(Number(v));
            }}
            value={h.activeId ?? ""}
          >
            {h.conversations.map((c) => (
              <option key={c.id} value={c.id}>
                {c.running ? "● " : ""}
                {c.title || `会话 #${c.id}`}
              </option>
            ))}
            <option value="new">＋ 新建会话</option>
          </select>
          <span className="hidden max-w-[42%] truncate font-medium text-[13px] md:inline">{title}</span>
          {agent ? (
            <span className="hidden shrink-0 rounded-sm border px-1.5 font-mono text-[10px] text-muted-foreground sm:inline">
              {agent.key}
            </span>
          ) : null}
          <span
            className={cn(
              "ml-1 flex shrink-0 items-center gap-1.5 rounded-sm px-1.5 py-0.5 text-[10.5px]",
              h.pending.length
                ? "bg-[var(--hx-warn-soft)] text-[var(--hx-warn)]"
                : h.running
                  ? "bg-[var(--hx-ok-soft)] text-[var(--hx-ok)]"
                  : "bg-muted text-muted-foreground",
            )}
          >
            <span
              className={cn(
                "size-1.5 rounded-full",
                h.pending.length
                  ? "bg-[var(--hx-warn)]"
                  : h.running
                    ? "animate-pulse bg-[var(--hx-ok)]"
                    : "bg-muted-foreground",
              )}
            />
            {h.pending.length ? "待审批" : h.running ? "运行中" : "空闲"}
          </span>
          {/* 传输指示：SSE 时不打接口也能看到新步骤，退回轮询时这里会显示「轮询」。 */}
          <span
            className="ml-1 flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground"
            title={h.transport === "sse" ? "SSE 实时推送" : "SSE 不可用，轮询兜底"}
          >
            <span
              className={cn(
                "size-1.5 rounded-full",
                h.transport === "sse" ? "bg-[var(--hx-ok)]" : "bg-muted-foreground/50",
              )}
            />
            {h.transport === "sse" ? "实时" : h.transport === "poll" ? "轮询" : "连接中"}
          </span>
          {/* 会话 token 预算：服务端有默认上限，用户不配置也有刹车；用尽后本轮自动收尾。 */}
          {h.budget &&
            h.budget.limit > 0 &&
            (() => {
              const used = h.tokens;
              const limit = h.budget.limit;
              const enforced = h.budget.mode === "enforce";
              const tier = !enforced
                ? "ok"
                : used >= limit
                  ? "exhausted"
                  : used * 100 >= limit * 85
                    ? "tight"
                    : used * 100 >= limit * 70
                      ? "watch"
                      : "ok";
              const fmt = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n));
              const cls =
                tier === "exhausted"
                  ? "text-red-500"
                  : tier === "tight"
                    ? "text-[var(--hx-warn)]"
                    : "text-muted-foreground";
              return (
                <span
                  className={cn("ml-1 shrink-0 text-[10px] tabular-nums", cls)}
                  title={
                    enforced
                      ? `会话 token 预算：已用 ${used} / 上限 ${limit}。达到上限本轮自动收尾（可在系统配置调整 default_conversation_token_budget）。`
                      : `仅统计，不拦截：已用 ${used}（参考上限 ${limit}）。在系统配置填写 default_conversation_token_budget 后才启用收尾。`
                  }
                >
                  {tier === "exhausted" ? "预算用尽" : `${enforced ? "预算" : "用量"} ${fmt(used)}/${fmt(limit)}`}
                </span>
              );
            })()}

          <div className="ml-auto flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
              className="hidden h-7 gap-1 px-2 text-[11.5px] sm:inline-flex"
              onClick={() => void copyLink()}
              disabled={!h.activeId}
            >
              <LinkIcon className="size-3" />
              {copied ? "已复制" : "链接"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-[11.5px]"
              disabled={!lastUserSeq || h.running}
              onClick={() => {
                if (!lastUserSeq) return;
                if (window.confirm(`回滚到 #${lastUserSeq} 之前？此后产生的消息将被撤销。`)) {
                  void h.rollback(lastUserSeq);
                }
              }}
            >
              <Undo2Icon className="size-3" /> 回滚
            </Button>
            <Button variant="ghost" size="sm" asChild className="h-7 gap-1 px-2 text-[11.5px]">
              <Link href="/dashboard">
                <ArrowLeftIcon className="size-3" /> 控制台
              </Link>
            </Button>
          </div>
        </header>

        <ErrorBar message={h.error} />

        <div ref={scrollRef} className="harness-scroll min-h-0 flex-1 overflow-y-auto">
          <Timeline
            items={h.items}
            pending={h.pending}
            onDecide={decide}
            comments={h.comments}
            onComment={(path, line, text) => h.addComment({ path, line, text })}
            deciding={deciding}
          />
        </div>

        <Composer
          onSend={h.send}
          onStop={h.stop}
          running={h.running}
          sending={h.sending}
          blocked={h.pending.length > 0}
          perm={h.perm}
          onPermChange={h.setPerm}
          comments={h.comments}
          onRemoveComment={h.removeComment}
          tokens={h.tokens}
        />
      </section>

      <div className="hidden min-h-0 xl:flex">
        <ReviewRail changes={h.changes} plan={h.plan} entries={h.entries} onLoadWorkspace={h.loadWorkspace} />
      </div>
    </div>
  );
}

export default function HarnessPage() {
  return (
    <React.Suspense fallback={<div className="h-full" />}>
      <Workspace />
    </React.Suspense>
  );
}
