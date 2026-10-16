"use client";

import * as React from "react";

import { useRouter } from "next/navigation";

import { ShieldAlertIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { SEVERITY_RANK, severityBarClass, severitySoftChipClass, severityTextClass } from "@/lib/severity";
import type { Finding, Severity } from "@/lib/types";
import { cn } from "@/lib/utils";

// ── 发现「战报卡」(主题重塑第 8 项,第 4 条)──────────────────────────────────
// 轮询发现"新增"发现(调用方按 id 集合差集计算,首载绝不触发)时,用 sonner 的
// 自定义 toast 弹一张战报:左侧严重度色条 + 图标 + 标题 + 来源任务/摘要 + 「查看」。
// 视觉全部走主题令牌(bg-popover / border / text-muted-foreground)与 lib/severity.ts,
// 亮暗与 7 套预设下都成立;入场只动 transform/opacity(reduced-motion 降级见 globals.css)。
//
// 聚合与限速(模块级状态):同页多次新增合并为一张卡(「又发现 N 个新漏洞」),
// 滚动 60s 内最多新开 3 张卡,超出的先累积、窗口滑出后合并补报 —— 信息不丢、不叠卡。
// 放模块级而非组件内:发现页与仪表盘共用同一条时间线,路由来回切不重置限速。

const TOAST_DURATION_MS = 6000; // 战报卡停留时长(~6s)
const MERGE_WINDOW_MS = 6000; // 与停留时长一致:卡还亮着,后续新增就并进同一张
const RATE_WINDOW_MS = 60_000; // 限速滚动窗口(1 分钟)
const RATE_MAX_CARDS = 3; // 窗口内最多新开 3 张卡
const MAX_ITEMS_IN_CARD = 4; // 卡内最多携带的条目,其余只计数

interface ActiveCard {
  id: string | number;
  /** 最近一次展示/合并的时刻,MERGE_WINDOW_MS 以此起算。 */
  at: number;
  count: number;
  /** 最新在前,最多 MAX_ITEMS_IN_CARD 条。 */
  items: Finding[];
}

let activeCard: ActiveCard | null = null;
let shownAt: number[] = [];
let deferred: Finding[] = [];
let flushTimer: ReturnType<typeof setTimeout> | null = null;

/** 组内最高严重级别:色条/图标取最重的一条,聚合卡不会被低危条目"稀释"。 */
function topSeverity(items: Finding[]): Severity {
  let best: Severity = "low";
  for (const item of items) {
    if (SEVERITY_RANK[item.severity] > SEVERITY_RANK[best]) best = item.severity;
  }
  return best;
}

/** 新发现优先展示(ts 非法时退化为 0,保持稳定排序不抛错)。 */
function byRecency(items: Finding[]): Finding[] {
  return [...items].sort((left, right) => (Date.parse(right.ts) || 0) - (Date.parse(left.ts) || 0));
}

// ── 战报卡本体 ───────────────────────────────────────────────────────────────

/**
 * 卡面:严重度色条 + 图标 + 标题 + 来源摘要 + 「查看」。
 * 单条直达发现详情(沿用列表的路由格式);多条落到发现列表,由用户逐条查看。
 */
function FindingAlertCard({ count, items }: { count: number; items: Finding[] }) {
  const t = useTranslations("findingAlert");
  const router = useRouter();
  const latest = items[0];
  const severity = topSeverity(items);
  const single = count === 1;
  const title = single
    ? latest.name || latest.vulnclass || t("titleSingle")
    : t("titleMany", { count });
  // 来源行:优先任务描述(与发现列表"所属任务"列一致),退回漏洞摘要/类别。
  const source = single
    ? latest.task_description || latest.summary || latest.vulnclass
    : latest.name || latest.vulnclass || latest.summary;
  const target = single && latest.finding_id ? `/function/findings/detail?id=${latest.finding_id}` : "/function/findings";

  return (
    <div
      className={cn(
        "pointer-events-auto relative w-[356px] max-w-[calc(100vw-2rem)] overflow-hidden rounded-xl border bg-popover text-popover-foreground shadow-lg",
        // 入场:只动 opacity + translateY;reduced-motion 下退化为纯淡入(globals.css)。
        "animate-fade-up",
      )}
    >
      {/* 一次性高亮:与卡片同色的严重度覆盖层淡出一次(只动 opacity)。
          reduced-motion 下 globals.css 会将其静态隐藏,不会残留色块。 */}
      <span
        aria-hidden="true"
        className={cn("animate-glow-once pointer-events-none absolute inset-0", severitySoftChipClass(severity))}
      />
      <div className="relative flex items-stretch">
        {/* 严重度色条:与 severity 契约同源(severityBarClass),亮暗/预设自动跟随。 */}
        <span aria-hidden="true" className={cn("w-1 shrink-0 self-stretch", severityBarClass(severity))} />
        <div className="flex min-w-0 flex-1 items-start gap-2.5 p-3">
          <span
            className={cn(
              "mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-lg",
              severitySoftChipClass(severity),
            )}
          >
            <ShieldAlertIcon className="size-4" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium" title={title}>
              {title}
            </div>
            {source ? (
              <div className="mt-0.5 line-clamp-2 text-xs text-muted-foreground" title={source}>
                {source}
              </div>
            ) : null}
            <div className="mt-1.5 flex flex-wrap items-center gap-2">
              <StatusBadge domain="severity" value={severity} dot className="px-1.5 py-0 text-[10px]" />
              {count > 1 ? (
                <span className={cn("text-[10px] tabular-nums", severityTextClass(severity))}>
                  {t("moreCount", { count: count - 1 })}
                </span>
              ) : null}
            </div>
          </div>
          {/* 「查看」用 <button> 而非 <a>:sonner 的拖拽逻辑只对 BUTTON 短路,
              锚点会被指针捕获影响点击 —— 这里以 router.push 完成同样的跳转。 */}
          <Button size="sm" variant="secondary" className="mt-0.5 shrink-0" onClick={() => router.push(target)}>
            {single ? t("view") : t("viewAll")}
          </Button>
        </div>
      </div>
    </div>
  );
}

// ── 通知编排 ─────────────────────────────────────────────────────────────────

function pruneShown(now: number) {
  shownAt = shownAt.filter((at) => now - at < RATE_WINDOW_MS);
}

/** 渲染或原位更新一张战报卡(带 id 时 sonner 合并同一张,并重置停留计时)。 */
function show(payload: { id?: string | number; count: number; items: Finding[] }) {
  const { id, count, items } = payload;
  const render = () => <FindingAlertCard count={count} items={items} />;
  return id === undefined
    ? toast.custom(render, { duration: TOAST_DURATION_MS })
    : toast.custom(render, { id, duration: TOAST_DURATION_MS });
}

function openCard(items: Finding[], now: number) {
  const ordered = byRecency(items);
  const listed = ordered.slice(0, MAX_ITEMS_IN_CARD);
  const id = show({ count: items.length, items: listed });
  activeCard = { id, at: now, count: items.length, items: listed };
  shownAt.push(now);
  pruneShown(now);
}

/** 限速期间累积的新增:等最早一张卡滑出 60s 窗口后,合并成一张"补报卡"。 */
function scheduleFlush() {
  if (flushTimer !== null || deferred.length === 0 || shownAt.length === 0) return;
  const now = Date.now();
  const oldest = shownAt.reduce((min, at) => Math.min(min, at), Number.POSITIVE_INFINITY);
  const waitMs = Math.max(500, oldest + RATE_WINDOW_MS - now + 50);
  flushTimer = setTimeout(() => {
    flushTimer = null;
    const at = Date.now();
    pruneShown(at);
    if (deferred.length === 0) return;
    if (shownAt.length >= RATE_MAX_CARDS) {
      scheduleFlush();
      return;
    }
    const batch = deferred;
    deferred = [];
    openCard(batch, at);
  }, waitMs);
}

/**
 * notifyNewFindings:把一轮"新增发现"(id 集合差集,首载由调用方排除)编排成战报卡。
 * 调用方无需关心限速;本函数负责合并、限速与延后补报。
 */
export function notifyNewFindings(findings: Finding[]) {
  const fresh = findings.filter((item) => item.finding_id);
  if (fresh.length === 0) return;
  const now = Date.now();
  pruneShown(now);

  // 1) 上一张卡仍在停留期(或刚被合并):并进同一张,标题走「又发现 N 个新漏洞」。
  if (activeCard && now - activeCard.at <= MERGE_WINDOW_MS) {
    const count = activeCard.count + fresh.length;
    const items = byRecency([...fresh, ...activeCard.items]).slice(0, MAX_ITEMS_IN_CARD);
    show({ id: activeCard.id, count, items });
    activeCard = { id: activeCard.id, at: now, count, items };
    return;
  }

  // 2) 限速:窗口内已经开满 RATE_MAX_CARDS 张 → 先累积,等窗口滑出后一张卡补报。
  if (shownAt.length >= RATE_MAX_CARDS) {
    deferred.push(...fresh);
    scheduleFlush();
    return;
  }

  // 3) 正常开卡。
  openCard(fresh, now);
}

/**
 * useNewFindingsAlert:页面接线点(仪表盘 / 发现页各接一处)。
 * 内部维护"上一轮 id 集合"的 ref,并在 ready 后才建立基线:
 *  · ready=false(首载前 / 刚切换结果集)→ 只清基线,不弹;
 *  · ready=true 后的第一批数据 → 记录基线,不弹(首载绝不触发的闸门);
 *  · 后续批次与基线求差集 → 仅把新增 id 交给 notifyNewFindings。
 * 纯旁路观察:不改动调用方的轮询、签名比对与请求时序。
 */
export function useNewFindingsAlert(findings: Finding[], ready: boolean) {
  const seen = React.useRef<Set<string> | null>(null);
  React.useEffect(() => {
    if (!ready) {
      seen.current = null;
      return;
    }
    const next = new Set<string>();
    for (const item of findings) {
      if (item.finding_id) next.add(item.finding_id);
    }
    const previous = seen.current;
    if (previous === null) {
      seen.current = next;
      return;
    }
    const fresh = findings.filter((item) => item.finding_id && !previous.has(item.finding_id));
    seen.current = next;
    if (fresh.length > 0) notifyNewFindings(fresh);
  }, [findings, ready]);
}
