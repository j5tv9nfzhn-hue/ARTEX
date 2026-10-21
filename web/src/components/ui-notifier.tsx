"use client";

import * as React from "react";

import { useRouter } from "next/navigation";

import { X } from "lucide-react";
import { useTranslations } from "next-intl";

import { getNotifyPrefs, useNotifyPrefs } from "@/hooks/use-notify-prefs";
import { sseUrl } from "@/lib/api";
import { MAX_STACK, type NotifyEvent } from "@/lib/notify-event";
import { notifyInbox, pushNotifyEvent, setToastHandler } from "@/lib/notify-inbox";
import { type NotifyDock, resolveTtlMs, shouldPopup, soundFor } from "@/lib/notify-prefs";
import { accentClass, iconTone, levelIcon } from "@/lib/notify-visuals";
import { cn } from "@/lib/utils";

// 站内通知的「卡片宿主」。
//
// 数据来自后端 `GET /api/notifications/stream`（SSE，见 server/ui_notify.go）：
// 任务终态、#N 子代理结算、AI 回复结束、漏洞落库四类里程碑。**不落库**，刷新即清空
// （服务端对新连接不补历史，只对 Last-Event-ID 断线重连补洞）。
//
// 本文件只负责「把一条事件变成一张会自动收起的卡片」；事件本身写进
// lib/notify-inbox.ts 的单例，历史列表、未读角标都在别处读它。
//
// 与改造前的三处差别（都是这次「铃铛压住创建按钮」的整改内容）：
//   1. 不再有空态浮层。旧版 history 为空时会在 right-4 bottom-4 画一个 32px 的
//      圆形铃铛，全站常驻——它压在页面右下角的「创建」类按钮上。通知中心已经
//      搬到侧边栏「通知推送」页，浮层不需要再兼任入口。
//   2. 落点由用户设置在 lib/notify-prefs.ts 里决定（默认右上角，避开右下角按钮）。
//   3. 顶部的红色横幅也一并去掉了：严重事件在卡片本身已经有红色色条 + 双击音，
//      再叠一条居顶横幅既重复，又会跟顶栏右侧的控件抢位置。
export function UINotifier() {
  const t = useTranslations("notifyUi");
  const router = useRouter();
  const prefs = useNotifyPrefs();

  const [toasts, setToasts] = React.useState<NotifyEvent[]>([]);
  const timers = React.useRef(new Map<number, ReturnType<typeof setTimeout>>());
  const ctxRef = React.useRef<AudioContext | null>(null);

  const dismissToast = React.useCallback((id: number) => {
    const tm = timers.current.get(id);
    if (tm) {
      clearTimeout(tm);
      timers.current.delete(id);
    }
    setToasts((prev) => prev.filter((e) => e.id !== id));
  }, []);

  // 收到一条事件后的「怎么弹」。延迟、声音、存活时长全部读当时的偏好：
  // 已经发出去的事件按到达时的规则走完，用户中途改设置不会让半张卡片改寿命。
  const showToast = React.useCallback((ev: NotifyEvent) => {
    const p = getNotifyPrefs();
    if (!shouldPopup(p, ev)) return;
    const show = () => {
      timers.current.delete(ev.id);
      // 延迟期间被用户从列表里删掉了，就别再弹出来。
      if (!notifyInbox.has(ev.id)) return;
      setToasts((prev) => [ev, ...prev.filter((e) => e.id !== ev.id)].slice(0, MAX_STACK));
      const sound = soundFor(p, ev);
      if (sound) playTone(ctxRef, sound);
      const ttl = resolveTtlMs(p, ev);
      const tm = setTimeout(() => {
        timers.current.delete(ev.id);
        setToasts((prev) => prev.filter((e) => e.id !== ev.id));
      }, ttl);
      timers.current.set(ev.id, tm);
    };
    if (p.delaySec > 0) {
      timers.current.set(ev.id, setTimeout(show, p.delaySec * 1000));
    } else {
      show();
    }
  }, []);

  React.useEffect(() => setToastHandler(showToast), [showToast]);

  React.useEffect(() => {
    const es = new EventSource(sseUrl("/api/notifications/stream"));
    es.onmessage = (e) => {
      try {
        pushNotifyEvent(JSON.parse(e.data) as NotifyEvent);
      } catch {
        /* 非 JSON 帧（如 : ping 注释）忽略 */
      }
    };
    return () => es.close();
  }, []);

  React.useEffect(() => {
    const map = timers.current;
    return () => {
      map.forEach((tm) => {
        clearTimeout(tm);
      });
      map.clear();
    };
  }, []);

  // 点卡片：只收卡片、跳到详情。事件本身留在列表里，点一下不该等于销毁记录。
  const go = (ev: NotifyEvent) => {
    dismissToast(ev.id);
    if (ev.href) router.push(ev.href);
  };

  if (toasts.length === 0) return null;

  return (
    <div
      className={cn(
        "pointer-events-none fixed z-[90] flex w-[min(22rem,calc(100vw-2rem))] flex-col gap-2",
        DOCK_CLASS[prefs.dock],
      )}
      // 卡片容器只是视觉容器，读屏按 role="status" 逐条播报即可。
      aria-live="polite"
    >
      {toasts.map((ev) => (
        <ToastCard key={ev.id} event={ev} onGo={go} onClose={() => dismissToast(ev.id)} label={t("remove")} />
      ))}
    </div>
  );
}

// 三个落点都写死成完整类名，Tailwind 才扫得到（拼接出来的类名会被摇掉）。
const DOCK_CLASS: Record<NotifyDock, string> = {
  "top-right": "top-14 right-4 items-end",
  "top-center": "top-14 inset-x-0 items-center px-4",
  "bottom-right": "bottom-4 right-4 items-end",
};

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

function ToastCard({
  event,
  onGo,
  onClose,
  label,
}: {
  event: NotifyEvent;
  onGo: (ev: NotifyEvent) => void;
  onClose: () => void;
  label: string;
}) {
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
          <div className="truncate font-medium text-[13px] leading-5">{event.title}</div>
          {event.body ? (
            <div className="mt-0.5 line-clamp-2 text-muted-foreground text-xs leading-4">{event.body}</div>
          ) : null}
        </button>
        <button
          type="button"
          onClick={onClose}
          title={label}
          aria-label={label}
          className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      </div>
    </div>
  );
}
