"use client";

import * as React from "react";

import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { AlertTriangle, Bell, BellOff, CheckCircle2, Info, X, XCircle } from "lucide-react";

import { sseUrl } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { isSeverity, severityMeta } from "@/lib/severity";
import { cn } from "@/lib/utils";

// 全站应用内通知（右上/右下浮层 + 顶部横幅）。
//
// 数据来自后端 `GET /api/notifications/stream`（SSE，见 server/ui_notify.go）：
// 任务终态、#N 子代理结算、AI 回复结束、漏洞落库四类里程碑。**不落库**，
// 刷新即清空（服务端对新连接不补历史，只对 Last-Event-ID 断线重连补洞）。
//
// 设计约束（来自需求）：
//   - 任何页面、任何子页面都能看到 → 挂在 (main)/layout.tsx，位于 SidebarProvider
//     内、MainContent 外，任务详情页那种 full-bleed 布局也覆盖；
//   - 严重事件有提示音，且可以关掉 → 声音开关存 localStorage；
//   - 不加导航入口、不做通知中心 → 只有浮层本身，没有「全部通知」页面。
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
const TTL_INFO = 6000;
const TTL_WARN = 10000;
const TTL_ERROR = 0; // 0 = 不自动消失，必须手动关（严重事件不能被悄悄吞掉）

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

function ttlFor(ev: NotifyEvent): number {
  if (ev.kind === "finding" && (ev.severity === "critical" || ev.severity === "high")) return TTL_ERROR;
  return ev.level === "error" ? TTL_ERROR : ev.level === "warn" ? TTL_WARN : TTL_INFO;
}

// 提示音用 WebAudio 现场合成，不引入静态音频文件（静态导出下资源路径最容易坏）。
// 浏览器在用户交互前会挂起 AudioContext；这里 best-effort 尝试 resume，
// 失败就静默跳过——声音是增强，不是功能本身。
function playTone(ctxRef: { current: AudioContext | null }, kind: "error" | "warn" | "success") {
  try {
    const Ctor =
      window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!Ctor) return;
    if (!ctxRef.current) ctxRef.current = new Ctor();
    const ctx = ctxRef.current;
    if (ctx.state === "suspended") void ctx.resume();
    const tones = kind === "error" ? [880, 620] : kind === "warn" ? [760] : [988, 1245];
    const now = ctx.currentTime;
    tones.forEach((freq, i) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      const start = now + i * 0.15;
      osc.type = "sine";
      osc.frequency.value = freq;
      gain.gain.setValueAtTime(0.0001, start);
      gain.gain.exponentialRampToValueAtTime(0.11, start + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, start + 0.13);
      osc.connect(gain);
      gain.connect(ctx.destination);
      osc.start(start);
      osc.stop(start + 0.15);
    });
  } catch {
    /* 忽略：部分环境禁用 AudioContext */
  }
}

export function UINotifier() {
  const t = useTranslations("notifyUi");
  const router = useRouter();
  const [events, setEvents] = React.useState<NotifyEvent[]>([]);
  const [soundOn, setSoundOn] = React.useState(true);
  const [open, setOpen] = React.useState(false); // 只有「历史浮层」需要展开
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

  const dismiss = React.useCallback((id: number) => {
    const tm = timers.current.get(id);
    if (tm) {
      clearTimeout(tm);
      timers.current.delete(id);
    }
    setEvents((prev) => prev.filter((e) => e.id !== id));
  }, []);

  const push = React.useCallback(
    (ev: NotifyEvent) => {
      if (typeof ev?.id !== "number" || seen.current.has(ev.id)) return;
      seen.current.add(ev.id);
      setEvents((prev) => [ev, ...prev].slice(0, MAX_KEPT));

      const severe =
        ev.level === "error" || ev.severity === "critical" || ev.severity === "high";
      const kind = severe ? "error" : ev.level === "warn" ? "warn" : "success";
      // 只给「需要人工立刻知道」的事件发声：成功类不响，避免任务多开时噪音。
      if (soundRef.current && (severe || ev.level === "warn")) playTone(ctxRef, kind);

      const ttl = ttlFor(ev);
      if (ttl > 0) {
        const tm = setTimeout(() => dismiss(ev.id), ttl);
        timers.current.set(ev.id, tm);
      }
    },
    [dismiss],
  );

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
      map.forEach((tm) => clearTimeout(tm));
      map.clear();
    };
  }, []);

  const toggleSound = () => {
    const next = !soundOn;
    setSoundOn(next);
    soundRef.current = next;
    setLocalStorageValue(SOUND_KEY, next ? "1" : "0");
  };

  const go = (ev: NotifyEvent) => {
    dismiss(ev.id);
    if (ev.href) router.push(ev.href);
  };

  const stack = events.slice(0, MAX_STACK);
  const hidden = events.length - stack.length;
  // 横幅取「最新的一条必须打断的事件」，而不是最新一条：低危漏洞若干扰了
  // 最新位，早两秒的高危事件也仍然要挂在顶部。
  const banner = events.find(
    (e) => e.level === "error" || e.severity === "critical" || e.severity === "high",
  );

  if (events.length === 0) return null;

  return (
    <>
      {/* 顶部横幅：只给最重的一条占位，点标题跳转，点 X 关闭。
          用 fixed + 居中，不挤压页面布局（full-bleed 页面也不受影响）。 */}
      {open || !banner ? null : <Banner event={banner} onGo={go} onClose={() => dismiss(banner.id)} />}

      <div className="pointer-events-none fixed right-4 bottom-4 z-[90] flex w-[min(22rem,calc(100vw-2rem))] flex-col items-end gap-2">
        <div className="pointer-events-auto flex items-center gap-1 rounded-full border bg-background/95 px-2 py-1 text-xs shadow-sm backdrop-blur">
          <button
            type="button"
            onClick={toggleSound}
            className="flex items-center gap-1 rounded-full px-1.5 py-0.5 text-muted-foreground hover:text-foreground"
            title={soundOn ? t("mute") : t("unmute")}
          >
            {soundOn ? <Bell className="size-3.5" /> : <BellOff className="size-3.5" />}
            <span className="tabular-nums">{events.length}</span>
          </button>
          <span className="h-3 w-px bg-border" />
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="rounded-full px-1.5 py-0.5 text-muted-foreground hover:text-foreground"
          >
            {open ? t("collapse") : hidden > 0 ? t("more", { count: hidden }) : t("expand")}
          </button>
        </div>

        {(open ? events.slice(0, 10) : stack).map((ev) => (
          <Card key={ev.id} event={ev} onGo={go} onClose={() => dismiss(ev.id)} />
        ))}
      </div>
    </>
  );
}

function Card({
  event,
  onGo,
  onClose,
}: {
  event: NotifyEvent;
  onGo: (ev: NotifyEvent) => void;
  onClose: () => void;
}) {
  const Icon = levelIcon(event.level);
  const sev =
    event.kind === "finding" && event.severity && isSeverity(event.severity) ? event.severity : null;
  return (
    <div
      role="status"
      className="pointer-events-auto relative w-full overflow-hidden rounded-lg border bg-background shadow-lg"
    >
      <span className={cn("absolute inset-y-0 left-0 w-1", accentClass(event))} aria-hidden />
      <div className="flex items-start gap-2 py-2.5 pr-2 pl-3.5">
        <Icon
          className={cn(
            "mt-0.5 size-4 shrink-0",
            sev ? severityMeta(sev).text : event.level === "error" ? "text-red-600 dark:text-red-400" : "text-muted-foreground",
          )}
        />
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

function Banner({ event, onGo, onClose }: { event: NotifyEvent; onGo: (ev: NotifyEvent) => void; onClose: () => void }) {
  // 横幅只承接「必须打断」的事件：error 级或高危/严重漏洞。
  const urgent =
    event.level === "error" || event.severity === "critical" || event.severity === "high";
  if (!urgent) return null;
  const sev = event.kind === "finding" && event.severity && isSeverity(event.severity) ? event.severity : null;
  return (
    <div className="pointer-events-none fixed inset-x-0 top-0 z-[95] flex justify-center px-4 pt-2">
      <div className="pointer-events-auto flex w-full max-w-3xl items-center gap-3 rounded-lg border border-red-500/30 bg-red-600/10 px-3 py-2 text-sm shadow-sm backdrop-blur dark:border-red-400/30 dark:bg-red-400/10">
        <span className={cn("size-2 shrink-0 rounded-full", sev ? severityMeta(sev).dot : "bg-red-600 dark:bg-red-400")} />
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
