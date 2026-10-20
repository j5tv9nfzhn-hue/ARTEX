"use client";

// Harness 的单一状态源。
//
// 组件只调用这里暴露的动作，不直接 fetch —— 通信方式变化只改本文件。
//
// 传输：优先 SSE（GET /api/conversations/{id}/events，与控制台同一个后端、同一张
// 活动表），事件到达即入时间线；连接不上/端点不存在时自动退回轮询。
// 两者都是「同一份数据的两条投递路径」：SSE 断了以后用 ?since=<cursor> 补齐，
// 所以不会丢步骤，也不会出现前端自己攒状态的分叉。
//
// 轮询兜底策略：running 时 1.2s 一拍，空闲 5s 一拍；页面不可见时暂停。

import * as React from "react";

import { toast } from "sonner";

import { api, sseUrl } from "@/lib/api";
import type { Activity, Agent, Conversation, InterceptPending, WorkspaceEntry } from "@/lib/types";

import { patchPath } from "./diff";
import {
  buildPlan,
  type HarnessComment,
  type HarnessItem,
  mapActivity,
  type PermissionMode,
  type PlanStep,
} from "./types";

const POLL_MS = 1200;
const IDLE_POLL_MS = 5000;
const SSE_REOPEN_MS = 15_000;

// 后端是否提供会话级 SSE。每个页面生命周期只探一次：老版本后端没有这个端点，
// 如果每个会话都去 404 一遍，控制台网络面板会被失败请求刷满。
let sseProbe: Promise<boolean> | null = null;

function probeConversationStream(id: number): Promise<boolean> {
  sseProbe ??= (async () => {
    try {
      const ctl = new AbortController();
      const timer = window.setTimeout(() => ctl.abort(), 2500);
      const res = await fetch(sseUrl(`/api/conversations/${id}/events?since=0`), {
        headers: { Accept: "text/event-stream" },
        cache: "no-store",
        signal: ctl.signal,
      });
      window.clearTimeout(timer);
      const isStream = (res.headers.get("Content-Type") ?? "").includes("text/event-stream");
      // 只要响应头，不读正文：立刻断开，别占着一条长连接。
      void res.body?.cancel().catch(() => undefined);
      return res.ok && isStream;
    } catch {
      return false;
    }
  })();
  return sseProbe;
}

export interface HarnessChange {
  path: string;
  patch: string;
}

function upsertTool(prev: HarnessItem[], next: HarnessItem): HarnessItem[] {
  // tool_result 落到已存在的 tool_use 上：保留参数，补齐输出。
  if (next.type === "tool" && next.done && next.toolUseId) {
    const idx = prev.findIndex((it) => it.type === "tool" && it.toolUseId === next.toolUseId);
    if (idx >= 0) {
      const target = prev[idx] as Extract<HarnessItem, { type: "tool" }>;
      const merged: HarnessItem = {
        ...target,
        output: next.output || target.output,
        isError: next.isError,
        done: true,
        patch: next.patch ?? target.patch,
        tool: target.tool === "tool" ? next.tool : target.tool,
      };
      const copy = prev.slice();
      copy[idx] = merged;
      return copy;
    }
  }
  const at = prev.findIndex((it) => it.id === next.id);
  if (at >= 0) {
    const copy = prev.slice();
    copy[at] = next;
    return copy;
  }
  return [...prev, next];
}

function mergeAll(prev: HarnessItem[], incoming: HarnessItem[]): HarnessItem[] {
  let out = prev;
  for (const it of incoming) {
    // 服务端回吐的同一条用户消息到了，就把本地的乐观副本撤掉，避免出现两条。
    if (it.type === "user") {
      out = out.filter((p) => !(p.type === "user" && p.id.startsWith("local-") && p.text === it.text));
    }
    out = upsertTool(out, it);
  }
  return out;
}

export function useHarness(initialConversationId?: number) {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [conversations, setConversations] = React.useState<Conversation[]>([]);
  const [activeId, setActiveId] = React.useState<number | null>(null);
  const [items, setItems] = React.useState<HarnessItem[]>([]);
  const [running, setRunning] = React.useState(false);
  const [pending, setPending] = React.useState<InterceptPending[]>([]);
  const [entries, setEntries] = React.useState<WorkspaceEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [sending, setSending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [perm, setPerm] = React.useState<PermissionMode>("edit");
  const [comments, setComments] = React.useState<HarnessComment[]>([]);
  const [plan, setPlan] = React.useState<PlanStep[]>([]);
  const [workspace, setWorkspace] = React.useState("");
  const [transport, setTransport] = React.useState<"idle" | "sse" | "poll">("idle");
  // 会话 token 预算上限/模式：服务端有默认值，用户零配置也有刹车。
  const [budget, setBudget] = React.useState<{ limit: number; mode: string } | null>(null);

  const cursorRef = React.useRef(0);
  const activeRef = React.useRef<number | null>(null);
  const runningRef = React.useRef(false);
  const seenSeqs = React.useRef<Set<number>>(new Set());
  const activitiesRef = React.useRef<Activity[]>([]);

  React.useEffect(() => {
    activeRef.current = activeId;
  }, [activeId]);

  React.useEffect(() => {
    runningRef.current = running;
  }, [running]);

  // 切会话取一次上限，本轮结束（running→false）再刷一次：让"用尽"状态立刻反映到页头。
  React.useEffect(() => {
    if (!activeId) {
      setBudget(null);
      return;
    }
    // 运行中不刷新（服务端 used 滞后一轮，上限也不会变）；running 是这条
    // effect 的触发器，本轮结束时重新读一次即可。
    if (running) return;
    let alive = true;
    api
      .conversationBudget(activeId)
      .then((b) => {
        if (alive) setBudget({ limit: b.limit, mode: b.mode });
      })
      .catch(() => {
        if (alive) setBudget(null);
      });
    return () => {
      alive = false;
    };
  }, [activeId, running]);

  // ingest 是唯一的时间线写入点：轮询拉回的整页、SSE 推来的单条，都走这里。
  // 用 seq 去重，所以两条投递路径同时在线也不会出现重复步骤。
  const ingest = React.useCallback((incoming: Activity[]) => {
    const fresh = incoming.filter((a) => !seenSeqs.current.has(a.seq));
    if (!fresh.length) return;
    for (const a of fresh) seenSeqs.current.add(a.seq);
    const mapped = fresh.map(mapActivity).filter((x): x is HarnessItem => x !== null);
    if (mapped.length) setItems((prev) => mergeAll(prev, mapped));
    activitiesRef.current = [...activitiesRef.current, ...fresh];
    // 计划由 intent 活动折叠而来，随增量一起刷新。
    setPlan(buildPlan(activitiesRef.current));
    let cur = cursorRef.current;
    for (const a of fresh) if (a.seq > cur) cur = a.seq;
    cursorRef.current = cur;
  }, []);

  const pullMessages = React.useCallback(
    async (id: number, since: number) => {
      const r = await api.conversationMessages(id, since);
      ingest(r.items ?? []);
      cursorRef.current = Math.max(cursorRef.current, r.cursor ?? 0);
      setRunning(Boolean(r.running));
      return r;
    },
    [ingest],
  );

  const refreshPending = React.useCallback(async (id: number | null) => {
    if (!id) {
      setPending([]);
      return;
    }
    try {
      const rows = await api.interceptPending();
      setPending(rows.filter((p) => p.conversation_id === id && p.status === "pending"));
    } catch {
      // 拦截接口失败不影响主时间线。
      setPending([]);
    }
  }, []);

  const openConversation = React.useCallback(
    async (id: number) => {
      setActiveId(id);
      setError(null);
      setItems([]);
      setComments([]);
      setPending([]);
      cursorRef.current = 0;
      seenSeqs.current = new Set();
      activitiesRef.current = [];
      setPlan([]);
      try {
        await pullMessages(id, 0);
        await refreshPending(id);
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    },
    [pullMessages, refreshPending],
  );

  // 首次加载：agents + 会话列表 → 选中 ?conv= 或最近一条。
  React.useEffect(() => {
    let alive = true;
    void (async () => {
      try {
        const [nextAgents, nextConvs] = await Promise.all([api.agents(), api.conversations()]);
        if (!alive) return;
        setAgents(nextAgents);
        setConversations(nextConvs);
        const want = initialConversationId ?? 0;
        const hit = nextConvs.find((c) => c.id === want) ?? nextConvs[0];
        if (hit) await openConversation(hit.id);
      } catch (e) {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, [initialConversationId, openConversation]);

  // 实时通道：SSE 优先，失败自动退回轮询并定期重连。
  //
  // 两条通道读的都是同一张 conversation_activities：SSE 只负责「有新步骤」的即时提醒，
  // 断线期间漏掉的部分由下一次 ?since=<cursor> 补齐，因此不会出现「实时流与历史不一致」。
  React.useEffect(() => {
    const id = activeId;
    if (!id) return;

    let disposed = false;
    let es: EventSource | null = null;
    let pollTimer: number | undefined;
    let reopenTimer: number | undefined;

    const stopPolling = () => {
      if (pollTimer !== undefined) {
        window.clearTimeout(pollTimer);
        pollTimer = undefined;
      }
    };

    // 兜底轮询：跑着时密一点、空闲时稀一点；页面隐藏就跳过，不白打后端。
    const schedule = () => {
      if (disposed) return;
      const delay = runningRef.current ? POLL_MS : IDLE_POLL_MS;
      pollTimer = window.setTimeout(async () => {
        const cur = activeRef.current;
        if (cur && document.visibilityState === "visible") {
          try {
            await pullMessages(cur, cursorRef.current);
            await refreshPending(cur);
          } catch {
            // 单次失败不打断时间线，下一拍继续。
          }
        }
        schedule();
      }, delay);
    };

    const startPolling = () => {
      if (disposed) return;
      setTransport("poll");
      if (pollTimer === undefined) schedule();
    };

    const openStream = () => {
      if (disposed) return;
      // 带上 cursor：重连只补断线之后的部分。
      es = new EventSource(sseUrl(`/api/conversations/${id}/events?since=${cursorRef.current}`));
      es.onopen = () => {
        if (disposed) return;
        stopPolling();
        setTransport("sse");
      };
      es.onmessage = (ev) => {
        if (disposed) return;
        let a: Activity;
        try {
          a = JSON.parse(ev.data) as Activity;
        } catch {
          return; // 心跳/脏帧忽略
        }
        ingest([a]);
        // 用户消息与本轮结果会改变 running 状态，补一次轻量拉取校正；
        // 其余步骤只靠事件本身，不再额外打接口。
        if (a.kind === "user" || a.kind === "result") {
          void pullMessages(id, cursorRef.current).catch(() => undefined);
          void refreshPending(id);
        }
      };
      es.onerror = () => {
        if (disposed) return;
        es?.close();
        es = null;
        // 断流先兜底轮询，稍后再试 SSE；重连成功会自动停掉轮询。
        startPolling();
        reopenTimer = window.setTimeout(openStream, SSE_REOPEN_MS);
      };
    };

    void (async () => {
      const ok = await probeConversationStream(id);
      if (disposed) return;
      if (ok) openStream();
      else startPolling();
    })();

    return () => {
      disposed = true;
      es?.close();
      stopPolling();
      if (reopenTimer !== undefined) window.clearTimeout(reopenTimer);
    };
  }, [activeId, ingest, pullMessages, refreshPending]);

  const reloadList = React.useCallback(async () => {
    try {
      setConversations(await api.conversations());
    } catch {
      // 列表刷新失败保持旧值。
    }
  }, []);

  const send = React.useCallback(
    async (raw: string, files?: File[]) => {
      const id = activeId;
      const text = raw.trim();
      if (!id || !text || sending) return;

      let message = text;
      if (comments.length) {
        const lines = comments.map((c) => `- \`${c.path}:${c.line}\` ${c.text}`).join("\n");
        message = `${text}\n\n针对以下改动行：\n${lines}`;
      }

      const optimistic: HarnessItem = {
        id: `local-${Date.now()}`,
        seq: -Date.now(),
        at: new Date().toTimeString().slice(0, 5),
        type: "user",
        text: message,
      };
      setItems((prev) => [...prev, optimistic]);
      setComments([]);
      setSending(true);
      setError(null);
      try {
        // 图片先落到本会话的 uploads/（scope=session, id=conv-<id>），再随消息发出。
        // 后端按该轮实际配置声明的图片能力做视觉转写；没声明图片输入的配置会在
        // 发送前被模态闸门 400 拒绝——不会静默丢图。
        const up = files && files.length ? await api.chatUpload("session", `conv-${id}`, files) : null;
        await api.sendConversationMessage(id, message, up?.attachments);
        setRunning(true);
        await pullMessages(id, cursorRef.current);
        void reloadList();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
        throw e;
      } finally {
        setSending(false);
      }
    },
    [activeId, comments, pullMessages, reloadList, sending],
  );

  const stop = React.useCallback(async () => {
    const id = activeId;
    if (!id) return;
    await api.stopConversation(id);
    setRunning(false);
    await pullMessages(id, cursorRef.current);
    await refreshPending(id);
    void reloadList();
  }, [activeId, pullMessages, reloadList, refreshPending]);

  const decide = React.useCallback(
    async (decision: "allowed" | "denied") => {
      const id = activeId;
      const head = pending[0];
      if (!id || !head) return;
      await api.interceptDecide(head.id, decision);
      toast.success(decision === "allowed" ? "已允许，turn 继续" : "已拒绝，理由已回传给 agent");
      await refreshPending(id);
      await pullMessages(id, cursorRef.current);
    },
    [activeId, pending, pullMessages, refreshPending],
  );

  const newConversation = React.useCallback(async () => {
    const agent = agents.find((a) => a.key === "auto") ?? agents[0];
    if (!agent) {
      toast.error("没有可用的智能体，请先在控制台启用");
      return;
    }
    const conv = await api.createConversation(agent.key, "");
    setConversations((prev) => [conv, ...prev]);
    await openConversation(conv.id);
  }, [agents, openConversation]);

  const loadWorkspace = React.useCallback(async (path = "") => {
    try {
      const listing = await api.workspaceList(path);
      setEntries(listing.entries ?? []);
      setWorkspace(listing.path ?? "");
    } catch {
      setEntries([]);
    }
  }, []);

  // 工作区根路径与当前会话无关，挂载时取一次即可。
  React.useEffect(() => {
    void loadWorkspace("");
  }, [loadWorkspace]);

  const rollback = React.useCallback(
    async (seq: number) => {
      const id = activeId;
      if (!id) return;
      await api.rollbackConversationMessage(id, seq);
      toast.success(`已回滚到 #${seq}`);
      await openConversation(id);
    },
    [activeId, openConversation],
  );

  const addComment = React.useCallback((c: HarnessComment) => {
    setComments((prev) => [...prev, c]);
  }, []);
  const removeComment = React.useCallback((index: number) => {
    setComments((prev) => prev.filter((_, i) => i !== index));
  }, []);

  const activeConversation = React.useMemo(
    () => conversations.find((c) => c.id === activeId) ?? null,
    [conversations, activeId],
  );

  const agentByKey = React.useMemo(() => new Map(agents.map((a) => [a.key, a])), [agents]);

  // 变更清单：时间线里带 patch 的项按文件去重，后写覆盖先写 —— 与工作区最终状态一致。
  const changes = React.useMemo<HarnessChange[]>(() => {
    const byPath = new Map<string, string>();
    for (const it of items) {
      if (it.type === "diff") byPath.set(it.path, it.patch);
      else if (it.type === "tool" && it.patch) byPath.set(patchPath(it.patch) ?? it.tool, it.patch);
    }
    return [...byPath].map(([path, patch]) => ({ path, patch }));
  }, [items]);

  const tokens = React.useMemo(
    () => items.reduce((sum, it) => (it.type === "result" ? sum + it.tokens : sum), 0),
    [items],
  );

  return {
    agents,
    agentByKey,
    conversations,
    activeConversation,
    activeId,
    items,
    running,
    pending,
    entries,
    loading,
    sending,
    error,
    perm,
    comments,
    plan,
    changes,
    tokens,
    workspace,
    transport,
    budget,
    setPerm,
    openConversation,
    newConversation,
    send,
    stop,
    decide,
    rollback,
    addComment,
    removeComment,
    loadWorkspace,
  };
}

export type HarnessStore = ReturnType<typeof useHarness>;
