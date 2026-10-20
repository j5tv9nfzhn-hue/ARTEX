"use client";

import * as React from "react";

import { useRouter } from "next/navigation";

import {
  AlertTriangle,
  Bell,
  BellOff,
  CheckCircle2,
  ChevronDown,
  ExternalLink,
  Inbox,
  Info,
  Trash2,
  X,
  XCircle,
} from "lucide-react";
import { useTranslations } from "next-intl";

import { sseUrl } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { isSeverity, severityMeta } from "@/lib/severity";
import { cn } from "@/lib/utils";

// 全站应用内通知（右下角即时卡片 + 右侧详细面板 + 顶部横幅）。
//
// 数据来自后端 `GET /api/notifications/stream`（SSE，见 server/ui_notify.go）：
// 任务终态、#N 子代理结算、AI 回复结束、漏洞落库四类里程碑。**不落库**，
// 刷新即清空（服务端对新连接不补历史，只对 Last-Event-ID 断线重连补洞）。
//
// 设计约束（来自需求）：
//   - 任何页面、任何子页面都能看到 → 挂在 (main)/layout.tsx，位于 SidebarProvider
//     内、MainContent 外，任务详情页那种 full-bleed 布局也覆盖；
//   - 严重事件有提示音，且可以关掉 → 声音开关存 localStorage；
//   - 不加导航入口、不做通知中心页 → 面板是浮层，没有「全部通知」路由。
//
// 两级状态（这是面板能成立的前提）：
//   history = 本次会话收到过的全部事件（上限 MAX_KEPT），只有「删除/清空」会减少；
//   toasts  = 右下角正在展示的卡片（上限 MAX_STACK），到点自动消失。
// 若像早期版本那样只维护一份数组，ttl 到期会把事件从历史里一并删掉，面板就只剩
// 几秒的窗口——这正是本次整改要解决的问题。
export interface NotifyEvent {
  id: number;
  ts: number;
  kind: string;
  level: "success" | "info" | "warn" | "error";
  severity?: string;
  task_id?: string;
  task_title?: string;
  title: string;
  body?: string;
  href?: string;
}

const SOUND_KEY = "artex_ui_notify_sound";
const MAX_STACK = 4;
const MAX_KEPT = 40;
// 所有卡片都必须到点自动收起：早期版本把 error 的 TTL 设成 0（"严重事件不能被
// 悄悄吞掉"），结果子代理预算耗尽这类红色卡片会永久钉在右下角，连开三条就挡住
// 半个屏幕。严重事件的价值在于"及时看见"，不是在屏幕上常驻——历史记录仍然保留
// 在右侧面板里，随时可回看，所以自动收起不会丢信息。
const TTL_INFO = 5000;
const TTL_WARN = 8000;
const TTL_TASK = 8000; // 任务完成：够看清标题，又不至于长期占屏
const TTL_ERROR = 10000;

type FilterKey = "all" | "urgent" | "warn" | "info" | "finding";
const FILTERS: readonly FilterKey[] = ["all", "urgent", "warn", "info", "finding"];

type DayBucket = "today" | "yesterday" | "earlier";
const BUCKET_ORDER: readonly DayBucket[] = ["today", "yesterday", "earlier"];

const MINUTE = 60000;
const HOUR = 3600000;
const DAY = 86400000;

function levelIcon(level: NotifyEvent["level"]) {
  switch (level) {
    case "success":
      return CheckCircle2;
    case "error":
      return XCircle;
    case "warn":
      return AlertTriangle;
    default:
      return Info;
  }
}

// 严重度 → 浮层左侧色条。漏洞事件优先按 severity 上色（与全站色彩契约一致），
// 其余事件按 level。
function accentClass(ev: NotifyEvent): string {
  if (ev.kind === "finding" && ev.severity && isSeverity(ev.severity)) {
    return severityMeta(ev.severity).bar;
  }
  switch (ev.level) {
    case "success":
      return "bg-emerald-500 dark:bg-emerald-400";
    case "error":
      return "bg-red-600 dark:bg-red-400";
    case "warn":
      return "bg-amber-500 dark:bg-amber-400";
    default:
      return "bg-sky-500 dark:bg-sky-400";
  }
}

function iconTone(ev: NotifyEvent): string {
  if (ev.kind === "finding" && ev.severity && isSeverity(ev.severity)) {
    return severityMeta(ev.severity).text;
  }
  switch (ev.level) {
    case "success":
      return "text-emerald-600 dark:text-emerald-400";
    case "error":
      return "text-red-600 dark:text-red-400";
    case "warn":
      return "text-amber-600 dark:text-amber-400";
    default:
      return "text-muted-foreground";
  }
}

// 事件类型签。前缀与 server/ui_notify.go 的 Kind 约定一一对应：
// task_<status> / subagent_<state> / chat_<status> / finding。
function kindTone(kind: string): string {
  if (kind === "finding") {
    return "bg-red-600/10 text-red-700 dark:bg-red-400/10 dark:text-red-400";
  }
  if (kind === "approval_pending") {
    return "bg-orange-600/10 text-orange-700 dark:bg-orange-400/10 dark:text-orange-400";
  }
  if (kind.startsWith("task_")) {
    return "bg-sky-600/10 text-sky-700 dark:bg-sky-400/10 dark:text-sky-400";
  }
  if (kind.startsWith("subagent_")) {
    return "bg-violet-600/10 text-violet-700 dark:bg-violet-400/10 dark:text-violet-400";
  }
  if (kind.startsWith("chat_")) {
    return "bg-teal-600/10 text-teal-700 dark:bg-teal-400/10 dark:text-teal-400";
  }
  return "bg-muted text-muted-foreground";
}

function ttlFor(ev: NotifyEvent): number {
  if (ev.level === "error") return TTL_ERROR;
  if (ev.kind === "finding" && (ev.severity === "critical" || ev.severity === "high")) return TTL_ERROR;
  if (ev.kind === "task_done") return TTL_TASK;
  if (ev.level === "warn") return TTL_WARN;
  return TTL_INFO;
}

// 只有「需要人采取行动」的事件会弹卡片 + 响提示音：
//   - 漏洞落库（kind === "finding"，任何严重度都算，因为这是任务的产出）；
//   - 报错（level === "error"：任务失败 / 子代理预算耗尽 / AI 回复失败）；
//   - 任务完成（task_done：可以去验收、可以派下一条了）；
//   - 任务超时收尾（task_timeout：任务已经停了，不提醒会一直以为还在跑）；
//   - 等待人工审批（approval_pending：不点它，任务就停在原地等超时）。
// 子代理完成/受阻/被终止、AI 回复结束、任务开始等过程态一律静默，只进右侧面板
// 历史，不弹窗、不响铃、也不计入铃铛未读数。
function isNotifiable(ev: NotifyEvent): boolean {
  if (ev.kind === "finding") return true;
  if (ev.kind === "approval_pending") return true;
  if (ev.level === "error") return true;
  return ev.kind === "task_done" || ev.kind === "task_timeout";
}

// 「必须打断」的判定：error 级或高危/严重漏洞。横幅与提示音共用这一处口径。
function isUrgent(ev: NotifyEvent): boolean {
  return ev.level === "error" || ev.severity === "critical" || ev.severity === "high";
}

function matchesFilter(ev: NotifyEvent, f: FilterKey): boolean {
  switch (f) {
    case "urgent":
      return isUrgent(ev);
    case "warn":
      return ev.level === "warn";
    case "info":
      return ev.level === "info" || ev.level === "success";
    case "finding":
      return ev.kind === "finding";
    default:
      return true;
  }
}

function dayBucket(ts: number, now: number): DayBucket {
  const n = new Date(now);
  const startOfToday = new Date(n.getFullYear(), n.getMonth(), n.getDate()).getTime();
  if (ts >= startOfToday) return "today";
  if (ts >= startOfToday - DAY) return "yesterday";
  return "earlier";
}

function pad2(n: number): string {
  return n < 10 ? "0" + String(n) : String(n);
}

function clockTime(ts: number): string {
  const d = new Date(ts);
  return pad2(d.getHours()) + ":" + pad2(d.getMinutes());
}

function playTone(ctxRef: React.RefObject<AudioContext | null>, kind: "error" | "warn" | "success") {
  try {
    const Ctor =
      window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!Ctor) return;
    if (!ctxRef.current) ctxRef.current = new Ctor();
    const ctx = ctxRef.current;
    if (ctx.state === "suspended") void ctx.resume();
    let tones: number[];
    if (kind === "error") {
      tones = [880, 620];
    } else if (kind === "warn") {
      tones = [760];
    } else {
      tones = [988, 1245];
    }
    const now = ctx.currentTime;
    for (let i = 0; i < tones.length; i++) {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      const start = now + i * 0.15;
      osc.type = "sine";
      osc.frequency.value = tones[i];
      gain.gain.setValueAtTime(0.0001, start);
      gain.gain.exponentialRampToValueAtTime(0.11, start + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, start + 0.13);
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.start(start);
      osc.stop(start + 0.15);
    }
  } catch {
    /* 忽略：部分环境禁用 AudioContext */
  }
}

export function UINotifier() {
  const t = useTranslations("notifyUi");
  const router = useRouter();

  const [history, setHistory] = React.useState<NotifyEvent[]>([]);
  const [toasts, setToasts] = React.useState<NotifyEvent[]>([]);
  const [panelOpen, setPanelOpen] = React.useState(false);
  const [filter, setFilter] = React.useState<FilterKey>("all");
  const [expandedId, setExpandedId] = React.useState<number | null>(null);
  const [soundOn, setSoundOn] = React.useState(true);
  const [lastSeenTs, setLastSeenTs] = React.useState(() => Date.now());
  const [now, setNow] = React.useState(() => Date.now());

  const timers = React.useRef<Map<number, ReturnType<typeof setTimeout>>>(new Map());
  const ctxRef = React.useRef<AudioContext | null>(null);
  const soundRef = React.useRef(true);
  const seen = React.useRef<Set<number>>(new Set());

  React.useEffect(() => {
    const saved = getLocalStorageValue(SOUND_KEY);
    const on = saved !== "0";
    setSoundOn(on);
    soundRef.current = on;
  }, []);

  const clearTimer = React.useCallback((id: number) => {
    const tm = timers.current.get(id);
    if (!tm) return;
    clearTimeout(tm);
    timers.current.delete(id);
  }, []);

  // 只关卡片，不动历史。ttl 到点走这里。
  const dismissToast = React.useCallback(
    (id: number) => {
      clearTimer(id);
      setToasts((prev) => prev.filter((e) => e.id !== id));
    },
    [clearTimer],
  );

  // 真正删除一条：卡片与历史一起消失。
  const removeEvent = React.useCallback(
    (id: number) => {
      clearTimer(id);
      setToasts((prev) => prev.filter((e) => e.id !== id));
      setHistory((prev) => prev.filter((e) => e.id !== id));
      setExpandedId((cur) => (cur === id ? null : cur));
    },
    [clearTimer],
  );

  const clearAll = React.useCallback(() => {
    const map = timers.current;
    map.forEach((tm) => {
      clearTimeout(tm);
    });
    map.clear();
    setToasts([]);
    setHistory([]);
    setExpandedId(null);
  }, []);

  const push = React.useCallback((ev: NotifyEvent) => {
    if (typeof ev?.id !== "number" || seen.current.has(ev.id)) return;
    seen.current.add(ev.id);
    setHistory((prev) => [ev, ...prev].slice(0, MAX_KEPT));

    // 非「漏洞 / 报错 / 任务终态」事件到此为止：只留历史，不弹卡片、不响铃、不计未读。
    if (!isNotifiable(ev)) return;

    setToasts((prev) => [ev, ...prev].slice(0, MAX_STACK));

    if (soundRef.current) {
      if (ev.level === "error" || ev.severity === "critical" || ev.severity === "high") {
        playTone(ctxRef, "error"); // 下行双音：出事，要立刻看
      } else if (ev.kind === "task_timeout") {
        playTone(ctxRef, "warn"); // 单音：任务停了，但不是崩溃
      } else {
        playTone(ctxRef, "success"); // 上行双音：漏洞落库 / 任务完成
      }
    }

    const ttl = ttlFor(ev);
    if (ttl > 0) {
      const tm = setTimeout(() => {
        setToasts((prev) => prev.filter((e) => e.id !== ev.id));
        timers.current.delete(ev.id);
      }, ttl);
      timers.current.set(ev.id, tm);
    }
  }, []);

  React.useEffect(() => {
    const es = new EventSource(sseUrl("/api/notifications/stream"));
    es.onmessage = (e) => {
      try {
        push(JSON.parse(e.data) as NotifyEvent);
      } catch {
        /* 非 JSON 帧（如 : ping 注释）忽略 */
      }
    };
    return () => es.close();
  }, [push]);

  React.useEffect(() => {
    const map = timers.current;
    return () => {
      map.forEach((tm) => {
        clearTimeout(tm);
      });
      map.clear();
    };
  }, []);

  // 面板打开期间持续刷新「多久之前」，关掉就停表，避免全站待机时空转。
  React.useEffect(() => {
    if (!panelOpen) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(id);
  }, [panelOpen]);

  // 面板打开时（以及打开期间有新事件到达时）把历史标记为已读。
  // 合并成一个 effect：panelOpen 变化与新事件都走这里，避免两处重复赋值。
  // 面板打开期间（以及有新事件到达时）把历史标记为已读。
  // 依赖最新事件的时间戳而不是 history.length：既能在新事件到达时触发，
  // 又保证已读时间不会落后于最新一条（否则新事件会立刻显示成未读）。
  const latestEventTs = history.length ? history[0].ts : 0;
  React.useEffect(() => {
    if (!panelOpen) return;
    setLastSeenTs(Math.max(Date.now(), latestEventTs));
  }, [panelOpen, latestEventTs]);

  React.useEffect(() => {
    if (!panelOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setPanelOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [panelOpen]);

  const toggleSound = () => {
    const next = !soundOn;
    setSoundOn(next);
    soundRef.current = next;
    setLocalStorageValue(SOUND_KEY, next ? "1" : "0");
  };

  const openPanel = () => {
    setPanelOpen(true);
    setLastSeenTs(Date.now());
    setNow(Date.now());
  };

  // 点卡片/条目：只关掉它的即时卡片，历史仍留在面板里（否则「点一下跳转」
  // 就等于把这条记录销毁了）。
  const go = (ev: NotifyEvent) => {
    dismissToast(ev.id);
    if (ev.href) router.push(ev.href);
  };

  // 未读只数「会提醒」的事件；否则任务完成这类静默事件也会把铃铛点亮，
  // 让用户以为错过了什么。
  const unread = history.filter((e) => isNotifiable(e) && e.ts > lastSeenTs).length;
  const stack = toasts.slice(0, MAX_STACK);
  // 横幅取「最新的一条必须打断的事件」，而不是最新一条：低危漏洞若干扰了
  // 最新位，早两秒的高危事件也仍然要挂在顶部。
  const banner = toasts.find((e) => isUrgent(e));

  const empty = history.length === 0;

  // 无事件时不再 return null：铃铛必须常驻，否则用户永远找不到入口
  // （早期版本要等第一条通知到达，胶囊才出现）。空态只留一个圆形铃铛，
  // 点开就是空面板；声音开关则移进面板头部，随时可达。
  return (
    <>
      {/* 顶部横幅：只给最重的一条占位，点标题跳转，点 X 关闭。
          用 fixed + 居中，不挤压页面布局（full-bleed 页面也不受影响）。 */}
      {panelOpen || !banner ? null : <Banner event={banner} onGo={go} onClose={() => dismissToast(banner.id)} />}

      <div className="pointer-events-none fixed right-4 bottom-4 z-[90] flex w-[min(22rem,calc(100vw-2rem))] flex-col items-end gap-2">
        {empty ? (
          <button
            type="button"
            onClick={openPanel}
            title={t("openPanel")}
            aria-label={t("openPanel")}
            className="pointer-events-auto flex size-8 items-center justify-center rounded-full border bg-background/90 text-muted-foreground opacity-70 shadow-sm backdrop-blur transition-opacity hover:text-foreground hover:opacity-100 focus-visible:opacity-100"
          >
            {soundOn ? <Bell className="size-3.5" /> : <BellOff className="size-3.5" />}
          </button>
        ) : (
          <Pill
            unread={unread}
            total={history.length}
            soundOn={soundOn}
            onToggleSound={toggleSound}
            onOpen={openPanel}
          />
        )}

        {stack.map((ev) => (
          <Card key={ev.id} event={ev} onGo={go} onClose={() => dismissToast(ev.id)} />
        ))}
      </div>

      {panelOpen ? (
        <NotifyPanel
          events={history}
          filter={filter}
          onFilter={setFilter}
          expandedId={expandedId}
          onExpand={setExpandedId}
          onRemove={removeEvent}
          onClear={clearAll}
          onClose={() => setPanelOpen(false)}
          onGo={go}
          unread={unread}
          now={now}
          lastSeenTs={lastSeenTs}
          soundOn={soundOn}
          onToggleSound={toggleSound}
        />
      ) : null}
    </>
  );
}

// ---------------------------------------------------------------------------
// 详细面板
// ---------------------------------------------------------------------------

interface PanelProps {
  events: NotifyEvent[];
  filter: FilterKey;
  onFilter: (f: FilterKey) => void;
  expandedId: number | null;
  onExpand: (id: number | null) => void;
  onRemove: (id: number) => void;
  onClear: () => void;
  onClose: () => void;
  onGo: (ev: NotifyEvent) => void;
  unread: number;
  now: number;
  lastSeenTs: number;
  soundOn: boolean;
  onToggleSound: () => void;
}

function NotifyPanel(props: PanelProps) {
  const t = useTranslations("notifyUi");
  const {
    events,
    filter,
    onFilter,
    expandedId,
    onExpand,
    onRemove,
    onClear,
    onClose,
    onGo,
    unread,
    now,
    lastSeenTs,
    soundOn,
    onToggleSound,
  } = props;

  const filterText = (f: FilterKey): string => {
    switch (f) {
      case "urgent":
        return t("filterUrgent");
      case "warn":
        return t("filterWarn");
      case "info":
        return t("filterInfo");
      case "finding":
        return t("filterFinding");
      default:
        return t("filterAll");
    }
  };

  const kindText = (kind: string): string => {
    if (kind === "finding") return t("kindFinding");
    if (kind === "approval_pending") return t("kindApproval");
    if (kind.startsWith("task_")) return t("kindTask");
    if (kind.startsWith("subagent_")) return t("kindSubagent");
    if (kind.startsWith("chat_")) return t("kindChat");
    return t("kindOther");
  };

  const relText = (ts: number): string => {
    const diff = Math.max(0, now - ts);
    if (diff < MINUTE) return t("relJustNow");
    if (diff < HOUR) return t("relMinutes", { count: Math.floor(diff / MINUTE) });
    if (diff < DAY) return t("relHours", { count: Math.floor(diff / HOUR) });
    return t("relDays", { count: Math.floor(diff / DAY) });
  };

  const counts = React.useMemo(() => {
    const map: Record<FilterKey, number> = { all: events.length, urgent: 0, warn: 0, info: 0, finding: 0 };
    for (const ev of events) {
      if (isUrgent(ev)) map.urgent += 1;
      if (ev.level === "warn") map.warn += 1;
      if (ev.level === "info" || ev.level === "success") map.info += 1;
      if (ev.kind === "finding") map.finding += 1;
    }
    return map;
  }, [events]);

  const visible = React.useMemo(() => events.filter((ev) => matchesFilter(ev, filter)), [events, filter]);

  const groups = React.useMemo(() => {
    const map = new Map<DayBucket, NotifyEvent[]>();
    for (const ev of visible) {
      const b = dayBucket(ev.ts, now);
      const arr = map.get(b);
      if (arr) {
        arr.push(ev);
      } else {
        map.set(b, [ev]);
      }
    }
    const out: { key: DayBucket; items: NotifyEvent[] }[] = [];
    for (const key of BUCKET_ORDER) {
      const items = map.get(key);
      if (items) out.push({ key, items });
    }
    return out;
  }, [visible, now]);

  return (
    <div className="fixed inset-0 z-[100]">
      {/* 遮罩：点空白处关闭。用 button 而不是 div，键盘也能关。 */}
      <button
        type="button"
        aria-label={t("closePanel")}
        onClick={onClose}
        className="absolute inset-0 h-full w-full cursor-default bg-black/25 backdrop-blur-[1px]"
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={t("panelTitle")}
        className="absolute inset-y-0 right-0 flex w-[min(30rem,100vw)] flex-col border-l bg-background shadow-2xl"
      >
        {/* 头部 */}
        <header className="flex shrink-0 items-center gap-2 border-b px-4 py-3">
          <Bell className="size-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{t("panelTitle")}</div>
            <div className="text-xs text-muted-foreground">
              {unread > 0 ? t("unreadLine", { count: unread }) : t("allReadLine", { count: events.length })}
            </div>
          </div>
          <button
            type="button"
            onClick={onToggleSound}
            title={soundOn ? t("mute") : t("unmute")}
            aria-label={soundOn ? t("mute") : t("unmute")}
            aria-pressed={soundOn}
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            {soundOn ? <Bell className="size-4" /> : <BellOff className="size-4" />}
          </button>
          <button
            type="button"
            onClick={onClear}
            disabled={events.length === 0}
            className="rounded px-1.5 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
          >
            {t("clearAll")}
          </button>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("closePanel")}
            title={t("closePanel")}
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </header>

        {/* 筛选 */}
        <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b px-3 py-2">
          {FILTERS.map((f) => {
            const active = filter === f;
            return (
              <button
                key={f}
                type="button"
                onClick={() => onFilter(f)}
                aria-pressed={active}
                className={cn(
                  "shrink-0 rounded-full border px-2 py-0.5 text-xs transition-colors",
                  active
                    ? "border-transparent bg-foreground text-background"
                    : "border-border text-muted-foreground hover:bg-muted hover:text-foreground",
                )}
              >
                {filterText(f)}
                <span className="ml-1 tabular-nums opacity-60">{counts[f]}</span>
              </button>
            );
          })}
        </div>

        {/* 列表 */}
        <div className="min-h-0 flex-1 overflow-y-auto">
          {groups.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 px-6 py-16 text-center">
              <Inbox className="size-8 text-muted-foreground/50" />
              <div className="text-sm text-muted-foreground">{events.length === 0 ? t("empty") : t("emptyFilter")}</div>
            </div>
          ) : (
            groups.map((group) => (
              <section key={group.key}>
                <div className="sticky top-0 z-10 flex items-center gap-2 bg-background/95 px-4 py-1.5 text-[11px] font-medium text-muted-foreground backdrop-blur">
                  <BucketLabel bucket={group.key} />
                  <span className="tabular-nums opacity-60">{group.items.length}</span>
                </div>
                <ul>
                  {group.items.map((ev) => (
                    <Row
                      key={ev.id}
                      event={ev}
                      expanded={expandedId === ev.id}
                      onToggle={() => onExpand(expandedId === ev.id ? null : ev.id)}
                      onRemove={() => onRemove(ev.id)}
                      onGo={() => onGo(ev)}
                      unread={ev.ts > lastSeenTs}
                      rel={relText(ev.ts)}
                      kindText={kindText(ev.kind)}
                    />
                  ))}
                </ul>
              </section>
            ))
          )}
        </div>

        {/* 脚注：把「不落库」这件事说清楚，避免用户以为刷新后还能找回 */}
        <footer className="shrink-0 border-t px-4 py-2 text-[11px] leading-4 text-muted-foreground">
          {t("sessionNote", { count: MAX_KEPT })}
        </footer>
      </aside>
    </div>
  );
}

function BucketLabel({ bucket }: { bucket: DayBucket }) {
  const t = useTranslations("notifyUi");
  let label = t("groupEarlier");
  if (bucket === "today") {
    label = t("groupToday");
  } else if (bucket === "yesterday") {
    label = t("groupYesterday");
  }
  return <span>{label}</span>;
}

interface RowProps {
  event: NotifyEvent;
  expanded: boolean;
  onToggle: () => void;
  onRemove: () => void;
  onGo: () => void;
  unread: boolean;
  rel: string;
  kindText: string;
}

function Row({ event, expanded, onToggle, onRemove, onGo, unread, rel, kindText }: RowProps) {
  const t = useTranslations("notifyUi");
  const Icon = levelIcon(event.level);
  return (
    <li className="group border-b last:border-b-0">
      <div className="flex items-start gap-2.5 px-4 py-2.5 hover:bg-muted/40">
        <Icon className={cn("mt-0.5 size-4 shrink-0", iconTone(event))} />
        <button type="button" onClick={onToggle} aria-expanded={expanded} className="min-w-0 flex-1 text-left">
          <div className={cn("truncate text-[13px] leading-5", unread ? "font-semibold" : "font-medium")}>
            {event.title}
          </div>
          {event.body ? (
            <div className={cn("mt-0.5 text-xs leading-4 text-muted-foreground", expanded ? "" : "line-clamp-2")}>
              {event.body}
            </div>
          ) : null}
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            <span className={cn("rounded px-1 py-px", kindTone(event.kind))}>{kindText}</span>
            {unread ? (
              <span className="rounded bg-red-600/10 px-1 py-px font-medium text-red-700 dark:bg-red-400/10 dark:text-red-400">
                {t("unreadTag")}
              </span>
            ) : null}
            <span className="tabular-nums">{rel}</span>
            <span className="tabular-nums opacity-60">{clockTime(event.ts)}</span>
          </div>
        </button>
        <div className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100">
          {event.href ? (
            <button
              type="button"
              onClick={onGo}
              title={t("jump")}
              aria-label={t("jump")}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <ExternalLink className="size-3.5" />
            </button>
          ) : null}
          <button
            type="button"
            onClick={onRemove}
            title={t("remove")}
            aria-label={t("remove")}
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <Trash2 className="size-3.5" />
          </button>
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={expanded ? t("collapse") : t("expand")}
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <ChevronDown className={cn("size-3.5 transition-transform", expanded ? "rotate-180" : "")} />
          </button>
        </div>
      </div>

      {expanded ? (
        <dl className="grid grid-cols-[5.5rem_1fr] gap-x-2 gap-y-1 border-t bg-muted/30 px-4 py-2 text-[11px]">
          <dt className="text-muted-foreground">{t("metaId")}</dt>
          <dd className="tabular-nums">{event.id}</dd>
          <dt className="text-muted-foreground">{t("metaKind")}</dt>
          <dd className="font-mono">{event.kind}</dd>
          <dt className="text-muted-foreground">{t("metaTime")}</dt>
          <dd className="tabular-nums">{new Date(event.ts).toLocaleString()}</dd>
          {event.severity ? (
            <>
              <dt className="text-muted-foreground">{t("metaSeverity")}</dt>
              <dd>{event.severity}</dd>
            </>
          ) : null}
          {event.task_title || event.task_id ? (
            <>
              <dt className="text-muted-foreground">{t("metaTask")}</dt>
              <dd className="min-w-0 truncate">
                {event.task_title ?? ""}
                {event.task_id ? (
                  <span className="ml-1 tabular-nums text-muted-foreground">#{event.task_id}</span>
                ) : null}
              </dd>
            </>
          ) : null}
          {event.href ? (
            <>
              <dt className="text-muted-foreground">{t("metaHref")}</dt>
              <dd className="truncate font-mono">{event.href}</dd>
            </>
          ) : null}
        </dl>
      ) : null}
    </li>
  );
}

// 有事件时右下角的胶囊：左半是声音开关 + 未读数，右半是面板入口 + 总数。
function Pill({
  unread,
  total,
  soundOn,
  onToggleSound,
  onOpen,
}: {
  unread: number;
  total: number;
  soundOn: boolean;
  onToggleSound: () => void;
  onOpen: () => void;
}) {
  const t = useTranslations("notifyUi");
  return (
    <div className="pointer-events-auto flex items-center gap-1 rounded-full border bg-background/95 px-2 py-1 text-xs shadow-sm backdrop-blur">
      <button
        type="button"
        onClick={onToggleSound}
        title={soundOn ? t("mute") : t("unmute")}
        aria-label={soundOn ? t("mute") : t("unmute")}
        className="flex items-center gap-1 rounded-full px-1.5 py-0.5 text-muted-foreground hover:text-foreground"
      >
        {soundOn ? <Bell className="size-3.5" /> : <BellOff className="size-3.5" />}
        {unread > 0 ? (
          <span className="rounded-full bg-red-600 px-1 text-[10px] font-medium tabular-nums text-white dark:bg-red-500">
            {unread}
          </span>
        ) : null}
      </button>
      <span className="h-3 w-px bg-border" />
      <button
        type="button"
        onClick={onOpen}
        aria-label={t("openPanel")}
        className="flex items-center gap-1.5 rounded-full px-1.5 py-0.5 text-muted-foreground hover:text-foreground"
      >
        <span>{t("openPanel")}</span>
        <span className="tabular-nums">{total}</span>
      </button>
    </div>
  );
}

function Card({ event, onGo, onClose }: { event: NotifyEvent; onGo: (ev: NotifyEvent) => void; onClose: () => void }) {
  const Icon = levelIcon(event.level);
  return (
    <div
      role="status"
      className="pointer-events-auto relative w-full overflow-hidden rounded-lg border bg-background shadow-lg"
    >
      <span className={cn("absolute inset-y-0 left-0 w-1", accentClass(event))} aria-hidden />
      <div className="flex items-start gap-2 py-2.5 pr-2 pl-3.5">
        <Icon className={cn("mt-0.5 size-4 shrink-0", iconTone(event))} />
        <button type="button" className="min-w-0 flex-1 text-left" onClick={() => onGo(event)}>
          <div className="truncate text-[13px] font-medium leading-5">{event.title}</div>
          {event.body ? (
            <div className="mt-0.5 line-clamp-2 text-xs leading-4 text-muted-foreground">{event.body}</div>
          ) : null}
        </button>
        <button
          type="button"
          onClick={onClose}
          aria-label="close"
          className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      </div>
    </div>
  );
}

function Banner({
  event,
  onGo,
  onClose,
}: {
  event: NotifyEvent;
  onGo: (ev: NotifyEvent) => void;
  onClose: () => void;
}) {
  // 横幅只承接「必须打断」的事件：error 级或高危/严重漏洞。
  if (!isUrgent(event)) return null;
  const sev = event.kind === "finding" && event.severity && isSeverity(event.severity) ? event.severity : null;
  return (
    <div className="pointer-events-none fixed inset-x-0 top-0 z-[95] flex justify-center px-4 pt-2">
      <div className="pointer-events-auto flex w-full max-w-3xl items-center gap-3 rounded-lg border border-red-500/30 bg-red-600/10 px-3 py-2 text-sm shadow-sm backdrop-blur dark:border-red-400/30 dark:bg-red-400/10">
        <span
          className={cn("size-2 shrink-0 rounded-full", sev ? severityMeta(sev).dot : "bg-red-600 dark:bg-red-400")}
        />
        <button type="button" onClick={() => onGo(event)} className="min-w-0 flex-1 truncate text-left font-medium">
          {event.title}
        </button>
        <button
          type="button"
          onClick={onClose}
          aria-label="close"
          className="rounded p-0.5 text-muted-foreground hover:bg-background/60 hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      </div>
    </div>
  );
}
