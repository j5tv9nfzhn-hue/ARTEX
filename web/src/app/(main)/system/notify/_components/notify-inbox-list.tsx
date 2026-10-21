"use client";

import * as React from "react";

import Link from "next/link";

import { ChevronDown, ExternalLink, Inbox, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useNotifyInbox } from "@/hooks/use-notify-inbox";
import {
  BUCKET_ORDER,
  clockTime,
  type DayBucket,
  dayBucket,
  FILTERS,
  type FilterKey,
  kindTextKey,
  MAX_KEPT,
  matchesFilter,
  type NotifyEvent,
  relBucket,
} from "@/lib/notify-event";
import { notifyInbox } from "@/lib/notify-inbox";
import { iconTone, kindTone, levelIcon } from "@/lib/notify-visuals";
import { cn } from "@/lib/utils";

// 站内通知列表 —— 原先是一个从右下角铃铛打开的右侧抽屉，现在整块搬到
// 侧边栏「通知推送」页。功能一一对应：筛选、按天分组、展开详情、单条删除、
// 清空、跳转。
//
// 进入本页即视为已读（这里就是通知中心），所以没有「全部已读」按钮，也不需要
// 未读小圆点在本页重复出现以外的地方——侧边栏角标会跟着自动清零。

export function NotifyInboxList() {
  const t = useTranslations("notifyUi");
  const tp = useTranslations("notifyPage");
  const state = useNotifyInbox();
  const [filter, setFilter] = React.useState<FilterKey>("all");
  const [expandedId, setExpandedId] = React.useState<number | null>(null);
  const [now, setNow] = React.useState(() => Date.now());

  // 「多久之前」每 30 秒刷一次；页面不可见时浏览器会降频定时器，不必额外处理。
  React.useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(id);
  }, []);

  // 看着列表就算已读。水位线推到「最新一条」，因此依赖 latestTs：
  // 没有新事件就不重跑；markAllRead 自己幂等（水位线不回退），不会造成循环。
  const { events } = state;
  const latestTs = events.length > 0 ? events[0].ts : 0;
  React.useEffect(() => {
    notifyInbox.markAllRead(latestTs);
  }, [latestTs]);

  const counts = React.useMemo(() => {
    const map: Record<FilterKey, number> = { all: events.length, urgent: 0, warn: 0, info: 0, finding: 0 };
    for (const ev of events) {
      if (ev.level === "error" || ev.severity === "critical" || ev.severity === "high") map.urgent += 1;
      if (ev.level === "warn") map.warn += 1;
      if (ev.level === "info" || ev.level === "success") map.info += 1;
      if (ev.kind === "finding") map.finding += 1;
    }
    return map;
  }, [events]);

  const groups = React.useMemo(() => {
    const map = new Map<DayBucket, NotifyEvent[]>();
    for (const ev of events) {
      if (!matchesFilter(ev, filter)) continue;
      const b = dayBucket(ev.ts, now);
      const arr = map.get(b);
      if (arr) arr.push(ev);
      else map.set(b, [ev]);
    }
    const out: { key: DayBucket; items: NotifyEvent[] }[] = [];
    for (const key of BUCKET_ORDER) {
      const items = map.get(key);
      if (items) out.push({ key, items });
    }
    return out;
  }, [events, filter, now]);

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

  const relText = (ts: number): string => {
    const { unit, count } = relBucket(ts, now);
    if (unit === "justNow") return t("relJustNow");
    if (unit === "minutes") return t("relMinutes", { count });
    if (unit === "hours") return t("relHours", { count });
    return t("relDays", { count });
  };

  return (
    <Card className="gap-3">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle className="text-base">{tp("inbox.title")}</CardTitle>
          <span className="text-muted-foreground text-xs tabular-nums">
            {tp("inbox.total", { count: events.length })}
          </span>
          <Button
            variant="outline"
            size="sm"
            className="ml-auto"
            onClick={() => notifyInbox.clear()}
            disabled={events.length === 0}
          >
            {t("clearAll")}
          </Button>
        </div>
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="flex flex-wrap items-center gap-1">
          {FILTERS.map((f) => {
            const active = filter === f;
            return (
              <button
                key={f}
                type="button"
                onClick={() => setFilter(f)}
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

        <div className="max-h-[32rem] min-h-56 overflow-y-auto rounded-lg border">
          {groups.length === 0 ? (
            <div className="flex h-full min-h-56 flex-col items-center justify-center gap-2 px-6 py-10 text-center">
              <Inbox className="size-8 text-muted-foreground/50" />
              <div className="text-muted-foreground text-sm">{events.length === 0 ? t("empty") : t("emptyFilter")}</div>
              {events.length === 0 ? (
                <p className="max-w-sm text-muted-foreground/80 text-xs leading-5">{tp("inbox.emptyHint")}</p>
              ) : null}
            </div>
          ) : (
            groups.map((group) => (
              <section key={group.key}>
                <div className="sticky top-0 z-10 flex items-center gap-2 bg-background/95 px-4 py-1.5 font-medium text-[11px] text-muted-foreground backdrop-blur">
                  <BucketLabel bucket={group.key} />
                  <span className="tabular-nums opacity-60">{group.items.length}</span>
                </div>
                <ul>
                  {group.items.map((ev) => (
                    <Row
                      key={ev.id}
                      event={ev}
                      expanded={expandedId === ev.id}
                      onToggle={() => setExpandedId(expandedId === ev.id ? null : ev.id)}
                      onRemove={() => notifyInbox.remove(ev.id)}
                      unread={ev.ts > state.lastSeenTs}
                      rel={relText(ev.ts)}
                    />
                  ))}
                </ul>
              </section>
            ))
          )}
        </div>

        <p className="text-[11px] text-muted-foreground leading-4">{t("sessionNote", { count: MAX_KEPT })}</p>
      </CardContent>
    </Card>
  );
}

function BucketLabel({ bucket }: { bucket: DayBucket }) {
  const t = useTranslations("notifyUi");
  let label = t("groupEarlier");
  if (bucket === "today") label = t("groupToday");
  else if (bucket === "yesterday") label = t("groupYesterday");
  return <span>{label}</span>;
}

function Row({
  event,
  expanded,
  onToggle,
  onRemove,
  unread,
  rel,
}: {
  event: NotifyEvent;
  expanded: boolean;
  onToggle: () => void;
  onRemove: () => void;
  unread: boolean;
  rel: string;
}) {
  const t = useTranslations("notifyUi");
  const Icon = levelIcon(event.level);
  const kind = t(kindTextKey(event.kind));

  return (
    <li className="group border-b last:border-b-0">
      <div className="flex items-start gap-2.5 px-4 py-2.5 hover:bg-muted/40">
        <Icon className={cn("mt-0.5 size-4 shrink-0", iconTone(event))} />
        <button type="button" onClick={onToggle} aria-expanded={expanded} className="min-w-0 flex-1 text-left">
          <div className={cn("truncate text-[13px] leading-5", unread ? "font-semibold" : "font-medium")}>
            {event.title}
          </div>
          {event.body ? (
            <div className={cn("mt-0.5 text-muted-foreground text-xs leading-4", expanded ? "" : "line-clamp-2")}>
              {event.body}
            </div>
          ) : null}
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            <span className={cn("rounded px-1 py-px", kindTone(event.kind))}>{kind}</span>
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
            <Button variant="ghost" size="icon" asChild>
              <Link href={event.href} title={t("jump")} aria-label={t("jump")}>
                <ExternalLink className="size-3.5" />
              </Link>
            </Button>
          ) : null}
          <Button variant="ghost" size="icon" onClick={onRemove} title={t("remove")} aria-label={t("remove")}>
            <Trash2 className="size-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={expanded ? t("collapse") : t("expand")}
          >
            <ChevronDown className={cn("size-3.5 transition-transform", expanded ? "rotate-180" : "")} />
          </Button>
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
                  <span className="ml-1 text-muted-foreground tabular-nums">#{event.task_id}</span>
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
