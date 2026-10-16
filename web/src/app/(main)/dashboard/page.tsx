"use client";

import * as React from "react";

import Link from "next/link";

import {
  ActivityIcon,
  ArrowUpRightIcon,
  BugIcon,
  ClockIcon,
  NetworkIcon,
  ShieldCheckIcon,
  TargetIcon,
  ZapIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, Tooltip as RechartsTooltip, XAxis, YAxis } from "recharts";

import { EmptyState } from "@/components/empty-state";
import { useNewFindingsAlert } from "@/components/finding-alert";
import { LiveDot } from "@/components/live-dot";
import { FadeIn, Stagger } from "@/components/motion";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { type ChartConfig, ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart";
import { Progress } from "@/components/ui/progress";
import { api } from "@/lib/api";
import { severityTextClass } from "@/lib/severity";
import type {
  Activity,
  Agent,
  ConvTokenSummary,
  Finding,
  InterceptPending,
  LLMProfile,
  MCPServer,
  ReliabilityMetrics,
  Settings,
  SkillItem,
  Stats,
  Task,
  TokenTotal,
  Tool,
  TrafficExchange,
  UsageStats,
} from "@/lib/types";
import { cn } from "@/lib/utils";

// 辅助函数。
// 紧凑相对时间返回数量与单位，无时间戳时返回 null，调用方添加本地化“前”后缀。
function fmtRel(ts?: string | number): string | null {
  if (!ts) return null;
  const ms = Date.now() - (typeof ts === "number" ? ts * 1000 : Date.parse(ts as string));
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(1) + "k";
  return String(n);
}

// fmtPct 把 0..1 的比率渲染为百分比文本（保留一位小数）。
function fmtPct(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`;
}

// rateClass 按可用性给率上色：≥99% 绿、≥95% 琥珀、其余红。
function rateClass(rate: number): string {
  if (rate >= 0.99) return "text-emerald-400";
  if (rate >= 0.95) return "text-amber-400";
  return "text-red-400";
}

// 资产分布条的系列色:与图表统一走 --chart-* 令牌(经 @theme 映射为 bg-chart-*)。
// 资产类型有 6 种而令牌只有 5 个:让 ip/subdomain 之一改用完全同色的 chart-2 会让两行无法区分,
// 故 subdomain 用 chart-5(玫瑰,与紫同族);root_domain 与 service 同属蓝/青系,复用 chart-1。
const ASSET_COLORS: Record<string, string> = {
  root_domain: "bg-chart-1",
  ip: "bg-chart-2",
  subdomain: "bg-chart-5",
  app: "bg-chart-4",
  service: "bg-chart-1",
  endpoint: "bg-chart-3",
};

function statusColor(code: number): string {
  if (code < 300) return "text-emerald-500";
  if (code < 400) return "text-blue-400";
  if (code < 500) return "text-amber-400";
  return "text-red-400";
}

function statusBg(code: number): string {
  if (code < 300) return "bg-emerald-500";
  if (code < 400) return "bg-blue-400";
  if (code < 500) return "bg-amber-400";
  return "bg-red-400";
}

// ── sub-components ────────────────────────────────────────────────────────────

function SectionTitle({
  icon: Icon,
  children,
  sub,
}: {
  icon: React.ElementType;
  children: React.ReactNode;
  sub?: React.ReactNode;
}) {
  return (
    <div className="mb-3 flex items-center justify-between">
      <div className="flex items-center gap-1.5 text-xs font-semibold">
        <Icon className="size-3.5 text-muted-foreground" />
        {children}
      </div>
      {sub && <span className="text-[10px] text-muted-foreground">{sub}</span>}
    </div>
  );
}

// ── page ─────────────────────────────────────────────────────────────────────

export default function DashboardPage() {
  const t = useTranslations("dashboard");
  const tStatus = useTranslations("status");
  const rel = (ts?: string | number) => {
    const v = fmtRel(ts);
    return v === null ? "—" : t("ago", { value: v });
  };
  const dailyTrendConfig = React.useMemo(
    () =>
      ({
        // 图表系列色令牌化:改为引用 --chart-*,不再硬编码 hsl(原值蓝/紫/翠)。
        // var() 表达式会被 ChartContainer 原样写入 --color-*,再随 .dark / data-theme-preset
        // 重解析——图表、左侧分栏条(style.background)、图例色块复用同一 config,一并跟随主题。
        input: { label: t("chartInput"), color: "var(--chart-1)" },
        output: { label: t("chartOutput"), color: "var(--chart-2)" },
        cacheRead: { label: t("chartCacheRead"), color: "var(--chart-3)" },
      }) satisfies ChartConfig,
    [t],
  );

  // data state
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [settings, setSettings] = React.useState<Settings | null>(null);
  const [pending, setPending] = React.useState<InterceptPending[]>([]);
  const [activity, setActivity] = React.useState<Activity[]>([]);
  const [tokens, setTokens] = React.useState<TokenTotal | null>(null);
  const [convTokens, setConvTokens] = React.useState<ConvTokenSummary[]>([]);
  const [usageStats, setUsageStats] = React.useState<UsageStats | null>(null);
  const [assetCounts, setAssetCounts] = React.useState<Record<string, number>>({});
  const [traffic, setTraffic] = React.useState<TrafficExchange[]>([]);
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [mcpServers, setMcpServers] = React.useState<MCPServer[]>([]);
  const [skills, setSkills] = React.useState<SkillItem[]>([]);
  const [tools, setTools] = React.useState<Tool[]>([]);
  const [llmProfiles, setLLMProfiles] = React.useState<LLMProfile[]>([]);
  const [reliability, setReliability] = React.useState<ReliabilityMetrics | null>(null);

  // 发现战报卡接线(第 4 条):对同一批轮询里的 findings 做"上一轮 id 集合"差集。
  // stats 与 findings 由 Fast-poll 的同一个 Promise.all 一起写入,stats 从 null 变非空
  // 恰好等价于"首批数据已就绪" —— ready 用它表达,首载只记基线、绝不弹。
  // 轮询/签名比对语义零改动,这里只是旁路观察 setFindings 之后的数组。
  useNewFindingsAlert(findings, stats !== null);

  // The task list is the most expensive dashboard source. Poll it independently
  // so a large history cannot hold back every other dashboard panel.
  React.useEffect(() => {
    let alive = true;
    let loading = false;
    let signature = "";
    const load = async () => {
      if (loading) return;
      loading = true;
      try {
        const tr = await api.tasks();
        if (!alive) return;
        const nextSignature = JSON.stringify(tr.tasks);
        if (nextSignature !== signature) {
          signature = nextSignature;
          setTasks(tr.tasks);
        }
      } catch {
        /* transient errors — next poll retries */
      } finally {
        loading = false;
      }
    };
    void load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  // The remaining fast sources stay batched so one poll causes a single render.
  React.useEffect(() => {
    let alive = true;
    let loading = false;
    const load = async () => {
      if (loading) return;
      loading = true;
      try {
        const [findings, stats, settings, pending, activity, tokens, conversationTokens, reliabilityMetrics] =
          await Promise.all([
            api.findings(),
            api.stats(),
            api.settings(),
            api.interceptPending(),
            api.activity(undefined, { limit: 30 }),
            api.tokenStats(),
            api.conversationTokens(),
            api.metrics(),
          ]);
        if (!alive) return;
        setFindings(findings);
        setStats(stats);
        setSettings(settings);
        setPending(pending);
        setActivity(activity.items);
        setTokens(tokens.total ?? null);
        setConvTokens(conversationTokens);
        setReliability(reliabilityMetrics);
      } catch {
        // Preserve stale data and retry on the next interval.
      } finally {
        loading = false;
      }
    };
    void load();
    const timer = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  // slow poll: traffic, assets, system-static (every 15s)
  React.useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const [traf, counts, agentList, mcpList, skillList, toolList, profileList, usage] = await Promise.all([
          api.traffic(0, 50),
          api.assetCounts(),
          api.agents(),
          api.mcpServers(),
          api.skills(),
          api.tools(),
          api.llmProfiles(),
          api.usageStats(365),
        ]);
        if (!alive) return;
        setTraffic(traf.exchanges ?? []);
        setAssetCounts(counts ?? {});
        setAgents(agentList);
        setMcpServers(mcpList);
        setSkills(skillList);
        setTools(toolList);
        setLLMProfiles(profileList);
        setUsageStats(usage);
      } catch {
        /* transient errors */
      }
    };
    void load();
    const t = setInterval(load, 15000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  // ── derived ───────────────────────────────────────────────────────────────

  const tasksByStatus = React.useMemo(() => {
    const m: Record<string, number> = {};
    for (const t of tasks) m[t.status] = (m[t.status] ?? 0) + 1;
    return m;
  }, [tasks]);

  const findingsBySev = React.useMemo(() => {
    const m = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) {
      if (f.severity in m) m[f.severity as keyof typeof m]++;
    }
    return m;
  }, [findings]);

  const sortedTasks = React.useMemo(
    () =>
      [...tasks]
        .sort((a, b) => (b.last_activity_unix ?? b.created_unix ?? 0) - (a.last_activity_unix ?? a.created_unix ?? 0))
        .slice(0, 5),
    [tasks],
  );

  const recentFindings = React.useMemo(
    () => [...findings].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 8),
    [findings],
  );

  const recentActivity = React.useMemo(
    () =>
      [...activity]
        .filter((a) => a.kind !== "usage")
        .sort((a, b) => b.seq - a.seq)
        .slice(0, 6),
    [activity],
  );

  const totalAssets = React.useMemo(() => Object.values(assetCounts).reduce((a, b) => a + b, 0), [assetCounts]);

  // asset type breakdown
  const assetByType = React.useMemo(() => {
    return Object.entries(assetCounts)
      .filter(([, n]) => n > 0)
      .sort(([, a], [, b]) => b - a)
      .slice(0, 8);
  }, [assetCounts]);

  const assetMax = assetByType[0]?.[1] ?? 1;

  // traffic status code breakdown
  const trafficByCodes = React.useMemo(() => {
    const m: Record<number, number> = {};
    for (const e of traffic) {
      const bucket = Math.floor(e.status / 100) * 100;
      m[bucket] = (m[bucket] ?? 0) + 1;
    }
    return Object.entries(m)
      .sort(([a], [b]) => Number(a) - Number(b))
      .map(([code, n]) => ({ code: Number(code), n }));
  }, [traffic]);

  const trafficMax = Math.max(...trafficByCodes.map((x) => x.n), 1);
  const recentTraffic = [...traffic].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 5);

  // system
  const activeProfile = llmProfiles.find((p) => p.is_default);
  const enabledTools = tools.filter((t) => t.enabled);
  const pendingCount = pending.length;

  // 页头「作战状态摘要」:探索中/暂停/待审批/发现,全部复用上面的派生值(零新增请求)。
  // 计数为 0 的段省略;全空(数据未到或确为 0)时整行不渲染,避免空数据下的噪音。
  const headerSummary = [
    (tasksByStatus.running ?? 0) > 0 ? `${tStatus("engine.exploring")} ${tasksByStatus.running}` : null,
    (tasksByStatus.paused ?? 0) > 0 ? `${t("statPaused")} ${tasksByStatus.paused}` : null,
    pendingCount > 0 ? t("pendingApprovals", { count: pendingCount }) : null,
    findings.length > 0 ? `${t("findings")} ${findings.length}` : null,
  ].filter((s): s is string => s !== null);

  // ── token stats per LLM profile ──────────────────────────────────────────
  // tasks whose llm_profile_id is null/undefined used the active default profile
  const defaultProfileId = activeProfile ? Number(activeProfile.id) : null;

  // 数据源开关：旧版 = activity（task.tokens + 会话），新版 = llm_usage 计量账本。
  const [tokenVersion, setTokenVersion] = React.useState<"old" | "new">("old");
  // selected profile tab: "all" = 全部; number = specific profile id
  const [tokenTab, setTokenTab] = React.useState<number | null | "all">("all");
  // day range for the daily bar chart
  const [tokenDays, setTokenDays] = React.useState<7 | 30 | 90 | 180 | 365>(30);

  // profile 名 → id，用于把 llm_usage 的 profile_name 映射到现有 profile 分栏。
  const profileIdByName = React.useMemo(() => {
    const m = new Map<string, number>();
    for (const p of llmProfiles) m.set(p.name, Number(p.id));
    return m;
  }, [llmProfiles]);

  type Bucket = { input: number; output: number; cacheRead: number; cacheWrite: number; taskCount: number };

  // 旧版：按 profile 归桶（来自 activity 的 task.tokens + 会话用量）。
  const tokenByProfileOld = React.useMemo<Map<number | null, Bucket>>(() => {
    const m = new Map<number | null, Bucket>();
    const fold = (key: number | null, inp: number, out: number, cr: number, cw: number, addTask: boolean) => {
      const prev = m.get(key) ?? { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, taskCount: 0 };
      m.set(key, {
        input: prev.input + inp,
        output: prev.output + out,
        cacheRead: prev.cacheRead + cr,
        cacheWrite: prev.cacheWrite + cw,
        taskCount: prev.taskCount + (addTask ? 1 : 0),
      });
    };
    for (const t of tasks) {
      fold(
        t.llm_profile_id ?? defaultProfileId,
        t.tokens?.input_tokens ?? 0,
        t.tokens?.output_tokens ?? 0,
        t.tokens?.cache_read_tokens ?? 0,
        t.tokens?.cache_write_tokens ?? 0,
        true,
      );
    }
    for (const c of convTokens) {
      fold(
        c.llm_profile_id ?? defaultProfileId,
        c.input_tokens,
        c.output_tokens,
        c.cache_read_tokens,
        c.cache_write_tokens,
        false,
      );
    }
    return m;
  }, [tasks, convTokens, defaultProfileId]);

  // 新版：按 profile 归桶（来自 llm_usage 全局聚合，逐次调用精确）。
  const tokenByProfileNew = React.useMemo<Map<number | null, Bucket>>(() => {
    const m = new Map<number | null, Bucket>();
    for (const p of usageStats?.by_profile ?? []) {
      // 未匹配到现有 profile（改名/删除/空名）→ 落到默认桶，仍计入「全部」。
      const key = profileIdByName.get(p.profile_name) ?? defaultProfileId;
      const prev = m.get(key) ?? { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, taskCount: 0 };
      m.set(key, {
        input: prev.input + p.input_tokens,
        output: prev.output + p.output_tokens,
        cacheRead: prev.cacheRead + p.cache_read_tokens,
        cacheWrite: prev.cacheWrite + p.cache_write_tokens,
        taskCount: prev.taskCount + p.tasks,
      });
    }
    return m;
  }, [usageStats, profileIdByName, defaultProfileId]);

  const tokenByProfile = tokenVersion === "new" ? tokenByProfileNew : tokenByProfileOld;

  const displayedTokens = React.useMemo(() => {
    if (tokenTab === "all") {
      let input = 0,
        output = 0,
        cacheRead = 0,
        cacheWrite = 0,
        taskCount = 0;
      for (const v of tokenByProfile.values()) {
        input += v.input;
        output += v.output;
        cacheRead += v.cacheRead;
        cacheWrite += v.cacheWrite;
        taskCount += v.taskCount;
      }
      return { input, output, cacheRead, cacheWrite, taskCount };
    }
    const v = tokenByProfile.get(tokenTab as number | null);
    return v
      ? { input: v.input, output: v.output, cacheRead: v.cacheRead, cacheWrite: v.cacheWrite, taskCount: v.taskCount }
      : { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, taskCount: 0 };
  }, [tokenTab, tokenByProfile]);

  // 旧版每日：把任务/会话的总量按其创建日期归桶（近似，非真实每日消耗）。
  const dailyTokenDataOld = React.useMemo(() => {
    const now = new Date();
    now.setDate(now.getDate() - tokenDays);
    const cutoff = now.toISOString().slice(0, 10);
    const m = new Map<string, { input: number; output: number; cacheRead: number }>();
    const fold = (date: string, inp: number, out: number, cr: number) => {
      if (date < cutoff) return;
      const prev = m.get(date) ?? { input: 0, output: 0, cacheRead: 0 };
      m.set(date, { input: prev.input + inp, output: prev.output + out, cacheRead: prev.cacheRead + cr });
    };
    for (const t of tasks) {
      if (tokenTab !== "all" && (t.llm_profile_id ?? defaultProfileId) !== tokenTab) continue;
      fold(
        t.created_at.slice(0, 10),
        t.tokens?.input_tokens ?? 0,
        t.tokens?.output_tokens ?? 0,
        t.tokens?.cache_read_tokens ?? 0,
      );
    }
    for (const c of convTokens) {
      if (tokenTab !== "all" && (c.llm_profile_id ?? defaultProfileId) !== tokenTab) continue;
      fold(c.created_at.slice(0, 10), c.input_tokens, c.output_tokens, c.cache_read_tokens);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, v]) => ({ date: date.slice(5), ...v }));
  }, [tasks, convTokens, tokenDays, tokenTab, defaultProfileId]);

  // 新版每日：来自 llm_usage 的真实每日消耗（ts 是实际调用时刻）。
  const dailyTokenDataNew = React.useMemo(() => {
    const now = new Date();
    now.setDate(now.getDate() - tokenDays);
    const cutoff = now.toISOString().slice(0, 10);
    const m = new Map<string, { input: number; output: number; cacheRead: number }>();
    for (const d of usageStats?.daily ?? []) {
      if (d.date < cutoff) continue;
      if (tokenTab !== "all" && (profileIdByName.get(d.profile_name) ?? defaultProfileId) !== tokenTab) continue;
      const prev = m.get(d.date) ?? { input: 0, output: 0, cacheRead: 0 };
      m.set(d.date, {
        input: prev.input + d.input_tokens,
        output: prev.output + d.output_tokens,
        cacheRead: prev.cacheRead + d.cache_read_tokens,
      });
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, v]) => ({ date: date.slice(5), ...v }));
  }, [usageStats, tokenDays, tokenTab, profileIdByName, defaultProfileId]);

  const dailyTokenData = tokenVersion === "new" ? dailyTokenDataNew : dailyTokenDataOld;

  // 可靠性卡片的派生视图（本次启动以来）：阶段细分按次数降序，top 工具只留出过错的。
  const stageRows = React.useMemo(
    () => Object.entries(reliability?.tools.by_stage ?? {}).sort((a, b) => b[1] - a[1]),
    [reliability],
  );
  const errorTools = React.useMemo(
    () => (reliability?.tools.top_tools ?? []).filter((row) => row.errors > 0).slice(0, 5),
    [reliability],
  );
  const protocolTotal = reliability
    ? reliability.protocol.unpaired_results +
      reliability.protocol.unknown_results +
      reliability.protocol.missing_results
    : 0;

  // activity kind label
  function kindLabel(a: Activity): string {
    if (a.kind === "tool_use") return a.tool ?? "tool_use";
    if (a.kind === "tool_result") return "tool_result";
    return a.kind;
  }

  function workerColor(w: string): string {
    if (w === "planner") return "text-violet-400";
    if (w === "mainagent") return "text-cyan-400";
    return "text-blue-400";
  }

  function workerBg(w: string): string {
    if (w === "planner") return "bg-violet-500/10 text-violet-400 border-violet-500/20";
    if (w === "mainagent") return "bg-cyan-500/10 text-cyan-400 border-cyan-500/20";
    return "bg-blue-500/10 text-blue-400 border-blue-500/20";
  }

  // ── render ────────────────────────────────────────────────────────────────

  return (
    <div className="flex flex-1 flex-col gap-5 pb-6">
      {/* ── Header ── */}
      <div>
        <div>
          <h1 className="text-lg font-semibold tracking-tight">{t("title")}</h1>
          <p className="text-xs text-muted-foreground">{t("subtitle")}</p>
          {/* 作战状态摘要(页头小字):数据为 0 的段已省略,全空不渲染 */}
          {headerSummary.length > 0 && (
            <p className="mt-1 text-[11px] text-muted-foreground tabular-nums">{headerSummary.join(" · ")}</p>
          )}
        </div>
      </div>

      {/* ── Row 1: 战况行 —— 主 KPI + 次级指标矩阵(非对称层级) ──
          主 KPI 选「活跃任务」:指挥室视角下先回答「系统现在在跑什么」再看出什么结果,
          running 是行动量,且这张卡自带探索/暂停/完成三段细分,天然承接主 KPI 的状态细分行;
          「确认发现」等产出指标降为右侧紧凑矩阵 —— 数值与细分全部保留,零丢失。 */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
        {/* 主 KPI:活跃任务(running 大排印,数字走 Space Grotesk + 等宽数字)。
            进场用 .animate-fade-up 直接挂 Card(不包 div)—— 该卡是栅格子项、要跟着
            右侧矩阵拉伸对齐,包装层会把"卡片本身"替换成 div、破坏等高。 */}
        <Card className="animate-fade-up justify-between gap-4 p-5">
          <div className="flex items-center gap-1.5 font-medium text-[10px] text-muted-foreground uppercase tracking-widest">
            <TargetIcon className="size-3" /> {t("activeTasks")}
          </div>
          <div className="flex items-baseline gap-2.5">
            <span className="font-display font-semibold text-5xl tabular-nums leading-none tracking-tight lg:text-6xl">
              {tasksByStatus.running ?? 0}
            </span>
            <span className="font-display text-lg text-muted-foreground tabular-nums">/ {tasks.length}</span>
          </div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
            {(tasksByStatus.running ?? 0) > 0 && (
              <span className="text-blue-400">
                {t("statExplore")} {tasksByStatus.running}
              </span>
            )}
            {(tasksByStatus.paused ?? 0) > 0 && (
              <span className="text-amber-400">
                {t("statPaused")} {tasksByStatus.paused}
              </span>
            )}
            {(tasksByStatus.done ?? 0) > 0 && (
              <span className="text-emerald-400">
                {t("statDone")} {tasksByStatus.done}
              </span>
            )}
            {tasks.length === 0 && <span className="text-muted-foreground">{t("noTasks")}</span>}
          </div>
        </Card>

        {/* 次级紧凑指标 2×2:确认发现 / 资产节点 / 流量交互 / Token 用量。
            整块作为一个"面板"错峰进场(60ms):FadeIn 的包装层直接带上原本的 grid 类,
            与不包时的布局逐像素一致,不引入等高问题。 */}
        <FadeIn delay={60} className="grid grid-cols-2 gap-3">
          {/* 确认发现 */}
          <Card size="sm" className="gap-1">
            <CardHeader className="pb-0">
              <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground uppercase tracking-wider">
                <BugIcon className="size-3" /> {t("confirmedFindings")}
              </div>
              <div className="font-display font-semibold text-2xl tabular-nums leading-none">{findings.length}</div>
            </CardHeader>
            {/* 严重度色统一走 lib/severity.ts(修旧版 critical=rose-500 / high=red-400 的亮度倒挂) */}
            <CardContent className="flex flex-wrap gap-x-2 gap-y-0.5 text-[10px]">
              <span className={severityTextClass("critical")}>
                {tStatus("severity.critical")} {findingsBySev.critical}
              </span>
              <span className={severityTextClass("high")}>
                {tStatus("severity.high")} {findingsBySev.high}
              </span>
              <span className={severityTextClass("medium")}>
                {tStatus("severity.medium")} {findingsBySev.medium}
              </span>
              <span className={severityTextClass("low")}>
                {tStatus("severity.low")} {findingsBySev.low}
              </span>
            </CardContent>
          </Card>

          {/* 资产节点 */}
          <Card size="sm" className="gap-1">
            <CardHeader className="pb-0">
              <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground uppercase tracking-wider">
                <NetworkIcon className="size-3" /> {t("assetNodes")}
              </div>
              <div className="font-display font-semibold text-2xl tabular-nums leading-none">{totalAssets}</div>
            </CardHeader>
            <CardContent className="text-[10px] text-muted-foreground">{t("crossTaskShared")}</CardContent>
          </Card>

          {/* 流量交互 */}
          <Card size="sm" className="gap-1">
            <CardHeader className="pb-0">
              <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground uppercase tracking-wider">
                <ActivityIcon className="size-3" /> {t("trafficExchanges")}
              </div>
              <div className="font-display font-semibold text-2xl tabular-nums leading-none">{traffic.length}</div>
            </CardHeader>
            <CardContent className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
              {settings?.traffic_capture ? (
                <>
                  <LiveDot />
                  <span>{t("recording")}</span>
                </>
              ) : (
                <span>{t("captureOff")}</span>
              )}
            </CardContent>
          </Card>

          {/* LLM 用量 */}
          <Card size="sm" className="gap-1">
            <CardHeader className="pb-0">
              <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground uppercase tracking-wider">
                <ZapIcon className="size-3" /> {t("tokenUsage")}
              </div>
              <div className="font-display font-semibold text-2xl tabular-nums leading-none">
                {fmtTokens(displayedTokens.input + displayedTokens.output) || "—"}
              </div>
            </CardHeader>
            <CardContent className="text-[10px] text-muted-foreground">
              {t("statTokenDetail", {
                input: fmtTokens(displayedTokens.input),
                cache: fmtTokens(displayedTokens.cacheRead),
                output: fmtTokens(displayedTokens.output),
              })}
            </CardContent>
          </Card>
        </FadeIn>
      </div>

      {/* ── Row 2: LLM Token 消耗 ── */}
      <Card className="animate-fade-up p-4" style={{ animationDelay: "120ms" }}>
        {/* Header */}
        <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold">
            <ZapIcon className="size-3.5 text-muted-foreground" />
            {t("tokenConsumption")}
            {/* 数据源开关：旧版=activity 统计（含历史任务），新版=llm_usage 计量账本（更准，仅覆盖启用后） */}
            <div className="ml-1 flex gap-0.5 rounded-md border bg-muted/30 p-0.5">
              {(
                [
                  { v: "old", label: t("verOld") },
                  { v: "new", label: t("verNew") },
                ] as const
              ).map(({ v, label }) => (
                <button
                  type="button"
                  key={v}
                  onClick={() => setTokenVersion(v)}
                  title={v === "new" ? t("tooltipNew") : t("tooltipOld")}
                  className={cn(
                    "rounded px-2 py-0.5 text-[9px] font-medium transition-colors",
                    tokenVersion === v
                      ? "bg-background text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {label}
                </button>
              ))}
            </div>
            <span className="text-[10px] font-normal text-muted-foreground">
              {tokenVersion === "new" ? t("srcNew") : t("srcOld")}
            </span>
          </div>
          {/* Profile tabs */}
          <div className="flex flex-wrap items-center gap-1">
            <button
              type="button"
              onClick={() => setTokenTab("all")}
              className={cn(
                "rounded-md px-2.5 py-1 text-[10px] font-medium transition-colors",
                tokenTab === "all"
                  ? "bg-foreground text-background"
                  : "bg-muted/30 text-muted-foreground hover:text-foreground",
              )}
            >
              {t("all")}
            </button>
            {llmProfiles.map((p) => {
              const key = Number(p.id);
              const hasData = tokenByProfile.has(key) || (p.is_default && tokenByProfile.has(defaultProfileId));
              const resolvedKey = tokenByProfile.has(key) ? key : p.is_default ? defaultProfileId : key;
              return (
                <button
                  type="button"
                  key={p.id}
                  onClick={() => setTokenTab(resolvedKey)}
                  className={cn(
                    "flex items-center gap-1 rounded-md px-2.5 py-1 text-[10px] font-medium transition-colors",
                    tokenTab === resolvedKey
                      ? "bg-foreground text-background"
                      : "bg-muted/30 text-muted-foreground hover:text-foreground",
                    !hasData && "opacity-40",
                  )}
                >
                  {p.name}
                  {p.is_default && (
                    <span
                      className={cn(
                        "rounded px-1 py-0 text-[8px]",
                        tokenTab === resolvedKey
                          ? "bg-background/20 text-background"
                          : "bg-emerald-500/20 text-emerald-400",
                      )}
                    >
                      {t("default")}
                    </span>
                  )}
                </button>
              );
            })}
          </div>
        </div>

        {/* Body: metrics left + bar chart right */}
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[220px_1fr]">
          {/* Left: key metrics */}
          <div className="flex flex-col gap-4">
            {/* Total */}
            <div>
              <div className="text-[10px] text-muted-foreground">{t("totalInOut")}</div>
              {/* 合计数字:与全页数字排印对齐(Space Grotesk + 等宽数字),leading-none 收紧行高拉开层级 */}
              <div className="mt-1 font-bold font-display text-3xl tabular-nums leading-none tracking-tight">
                {fmtTokens(displayedTokens.input + displayedTokens.output) || "—"}
              </div>
              <div className="mt-0.5 text-[10px] text-muted-foreground">
                {t("taskCount", { count: displayedTokens.taskCount })}
              </div>
            </div>

            {/* Per-type bars */}
            <div className="space-y-3">
              {(() => {
                // input 已含缓存；拆成不重叠三段：未命中输入 + 缓存命中 + 输出 = 总量。
                const total = displayedTokens.input + displayedTokens.output;
                return [
                  {
                    label: t("inputUncached"),
                    value: displayedTokens.input - displayedTokens.cacheRead,
                    barColor: dailyTrendConfig.input.color!,
                    text: "text-chart-1",
                  },
                  {
                    label: t("cacheHit"),
                    value: displayedTokens.cacheRead,
                    barColor: dailyTrendConfig.cacheRead.color!,
                    text: "text-chart-3",
                  },
                  {
                    label: t("output"),
                    value: displayedTokens.output,
                    barColor: dailyTrendConfig.output.color!,
                    text: "text-chart-2",
                  },
                ].map(({ label, value, barColor, text }) => {
                  const pct = total > 0 ? (value / total) * 100 : 0;
                  return (
                    <div key={label}>
                      <div className="mb-1 flex items-center justify-between text-[10px]">
                        <span className="text-muted-foreground">{label}</span>
                        <span className={cn("font-display font-semibold tabular-nums", text)}>{fmtTokens(value)}</span>
                      </div>
                      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                        <div
                          className="h-full rounded-full transition-all duration-500"
                          style={{ width: `${pct}%`, background: barColor }}
                        />
                      </div>
                    </div>
                  );
                });
              })()}
            </div>

            {/* Cache hit rate */}
            {(() => {
              // input 已含缓存 → 命中率 = 缓存命中 / 总输入。
              const denominator = displayedTokens.input;
              const hitPct = denominator > 0 ? Math.round((displayedTokens.cacheRead / denominator) * 100) : 0;
              return (
                <div className="flex items-center justify-between rounded-lg border bg-muted/20 px-3 py-2 text-[10px]">
                  <span className="text-muted-foreground">{t("cacheHitRate")}</span>
                  <span
                    className={cn(
                      "font-display font-semibold tabular-nums",
                      hitPct > 50 ? "text-emerald-400" : "text-amber-400",
                    )}
                  >
                    {hitPct}%
                  </span>
                </div>
              );
            })()}
          </div>

          {/* Right: daily bar chart */}
          <div className="flex min-h-0 flex-col">
            <div className="mb-2 flex items-center justify-between">
              <div className="flex gap-3 text-[9px] text-muted-foreground">
                {(["input", "output", "cacheRead"] as const).map((k) => (
                  <span key={k} className="flex items-center gap-1">
                    <span
                      className="inline-block size-2 rounded-sm"
                      style={{ background: dailyTrendConfig[k].color }}
                    />
                    {dailyTrendConfig[k].label}
                  </span>
                ))}
              </div>
              <div className="flex gap-0.5 rounded-md border bg-muted/30 p-0.5">
                {(
                  [
                    { days: 7, label: t("range7") },
                    { days: 30, label: t("range30") },
                    { days: 90, label: t("range90") },
                    { days: 180, label: t("range180") },
                    { days: 365, label: t("range365") },
                  ] as const
                ).map(({ days, label }) => (
                  <button
                    type="button"
                    key={days}
                    onClick={() => setTokenDays(days)}
                    className={cn(
                      "rounded px-2.5 py-0.5 text-[9px] font-medium transition-colors",
                      tokenDays === days
                        ? "bg-background text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
            </div>

            {dailyTokenData.length === 0 ? (
              <div
                className="flex flex-1 items-center justify-center rounded-lg border bg-muted/10"
                style={{ minHeight: 180 }}
              >
                <EmptyState compact illustration="terminal" title={t("noData")} />
              </div>
            ) : (
              <ChartContainer config={dailyTrendConfig} className="h-[200px] w-full">
                <BarChart data={dailyTokenData} margin={{ top: 4, right: 4, bottom: 0, left: 0 }} maxBarSize={40}>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey="date"
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    tickMargin={6}
                    interval="preserveStartEnd"
                  />
                  <YAxis
                    tickFormatter={(v) => fmtTokens(Number(v))}
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    width={44}
                  />
                  <ChartTooltip
                    cursor={false}
                    content={
                      <ChartTooltipContent
                        indicator="line"
                        formatter={(value, name) => (
                          <div className="flex w-full items-center justify-between gap-4">
                            <span>
                              {(dailyTrendConfig as Record<string, { label: string }>)[String(name)]?.label ??
                                String(name)}
                            </span>
                            <span className="font-mono font-semibold tabular-nums">{fmtTokens(Number(value))}</span>
                          </div>
                        )}
                      />
                    }
                  />
                  <Bar dataKey="input" stackId="1" fill="var(--color-input)" radius={[0, 0, 0, 0]} />
                  <Bar dataKey="output" stackId="1" fill="var(--color-output)" radius={[0, 0, 0, 0]} />
                  <Bar dataKey="cacheRead" stackId="1" fill="var(--color-cacheRead)" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ChartContainer>
            )}
          </div>
        </div>
      </Card>

      {/* ── Row 3: 活动流 | 发现(不等宽:活动流更宽,减少摘要截断) ── */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
        {/* 活动流 */}
        <Card className="animate-fade-up p-4" style={{ animationDelay: "160ms" }}>
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-1.5 text-xs font-semibold">
              <ActivityIcon className="size-3.5 text-muted-foreground" />
              {t("activityStream")}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-[10px] text-muted-foreground">
                {t("eventCount", { count: activity.filter((a) => a.kind !== "usage").length })}
              </span>
              <Link
                href="/function/tasks"
                className="flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
              >
                {t("viewTasks")} <ArrowUpRightIcon className="size-3" />
              </Link>
            </div>
          </div>

          <div className="mb-2.5 flex flex-wrap gap-1.5">
            {(["tool_use", "tool_result", "text", "thinking", "result"] as const).map((kind) => {
              const count = activity.filter((a) => a.kind === kind).length;
              if (count === 0) return null;
              return (
                <span key={kind} className="rounded border bg-muted/20 px-1.5 py-0.5 text-[9px] text-muted-foreground">
                  {kind} <strong className="text-foreground/70">{count}</strong>
                </span>
              );
            })}
          </div>

          <div className="divide-y">
            {recentActivity.length === 0 ? (
              <EmptyState compact illustration="chat" className="py-4" title={t("noActivity")} />
            ) : (
              /* 活动行:Stagger 包装层直接带上原行的布局类,divide-y 的分隔语义不变;
                 key 稳定(seq),轮询重渲染只更新内容,不会重播进场。 */
              recentActivity.map((a, index) => (
                <Stagger key={a.seq} delay={index * 50} className="flex gap-2.5 py-2">
                  <span
                    className={cn(
                      "shrink-0 self-start rounded border px-1.5 py-0.5 font-mono text-[9px]",
                      workerBg(a.worker),
                    )}
                  >
                    {a.worker}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="text-[11px] font-medium">{kindLabel(a)}</div>
                    <div className="mt-0.5 truncate text-[10px] text-muted-foreground">{a.summary}</div>
                  </div>
                  <div className="shrink-0 text-right">
                    <Badge variant="outline" className="px-1.5 py-0 text-[9px] text-muted-foreground">
                      {a.kind}
                    </Badge>
                    <div className="mt-0.5 text-[9px] tabular-nums text-muted-foreground">{rel(a.ts)}</div>
                  </div>
                </Stagger>
              ))
            )}
          </div>
        </Card>

        {/* 发现 */}
        <Card className="animate-fade-up p-4" style={{ animationDelay: "200ms" }}>
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-1.5 text-xs font-semibold">
              <BugIcon className="size-3.5 text-muted-foreground" />
              {t("findings")}
            </div>
            <Link
              href="/function/findings"
              className="flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
            >
              {t("all")} <ArrowUpRightIcon className="size-3" />
            </Link>
          </div>

          <div className="divide-y">
            {recentFindings.length === 0 ? (
              <EmptyState compact illustration="radar" className="py-4" title={t("noFindings")} />
            ) : (
              recentFindings.map((f, index) => (
                <Stagger key={f.id} delay={index * 50} className="flex items-start gap-2 py-2">
                  <StatusBadge
                    domain="severity"
                    value={f.severity}
                    className="mt-0.5 shrink-0 px-1.5 py-0 text-[10px]"
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-[11px] font-semibold">{f.vulnclass}</span>
                      {f.task_description && (
                        <span className="truncate text-[10px] text-muted-foreground">{f.task_description}</span>
                      )}
                    </div>
                    <div className="mt-0.5 line-clamp-2 text-[10px] leading-snug text-muted-foreground">
                      {f.summary}
                    </div>
                  </div>
                  <span className="shrink-0 text-[9px] tabular-nums text-muted-foreground">{rel(f.ts)}</span>
                </Stagger>
              ))
            )}
          </div>
        </Card>
      </div>

      {/* ── Row 4: 任务表格 ── */}
      <Card className="animate-fade-up overflow-hidden p-0" style={{ animationDelay: "240ms" }}>
        <div className="flex items-center justify-between border-b px-4 py-3">
          <div className="flex items-center gap-1.5 text-xs font-semibold">
            <ClockIcon className="size-3.5 text-muted-foreground" />
            {t("tasks")}
          </div>
          <div className="flex items-center gap-2">
            <span className="text-[10px] text-muted-foreground">{t("taskCount", { count: tasks.length })}</span>
            <Link
              href="/function/tasks"
              className="flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
            >
              {t("all")} <ArrowUpRightIcon className="size-3" />
            </Link>
          </div>
        </div>
        <table className="w-full border-collapse text-xs">
          <thead>
            <tr className="border-b">
              {[
                t("colTask"),
                t("colStatus"),
                t("colEngine"),
                t("colGoalProgress"),
                t("colInFlight"),
                t("colLastActivity"),
              ].map((h) => (
                <th
                  key={h}
                  className="px-4 py-2 text-left text-[9px] font-semibold uppercase tracking-widest text-muted-foreground first:pl-4"
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {sortedTasks.length === 0 ? (
              <tr>
                <td colSpan={6} className="p-0">
                  <EmptyState compact illustration="flag" className="py-4" title={t("noTasks")} />
                </td>
              </tr>
            ) : (
              sortedTasks.map((t, index) => {
                const goalsPct = t.goals_total ? Math.round(((t.goals_met ?? 0) / t.goals_total) * 100) : null;
                return (
                  /* 表格行(<tr>)不能包 div:入场类直接挂在行上,延迟内联;
                     key 稳定(id),开头新任务只会让新行进场,已有行不重播。 */
                  <tr
                    key={t.id}
                    className="animate-fade-up border-b transition-colors last:border-0 hover:bg-muted/30"
                    style={{ animationDelay: `${Math.min(index * 50, 200)}ms` }}
                  >
                    <td className="max-w-xs px-4 py-3">
                      <Link href={`/function/tasks/detail?id=${t.id}`} className="group flex flex-col">
                        <span className="truncate font-medium group-hover:underline">{t.description}</span>
                        <span className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">{t.id}</span>
                      </Link>
                    </td>
                    <td className="px-4 py-3">
                      <StatusBadge domain="task" value={t.status} dot className="px-1.5 py-0 text-[10px]" />
                    </td>
                    <td className="px-4 py-3">
                      <StatusBadge
                        domain="engine"
                        value={t.engine_mode ?? "idle"}
                        className="px-1.5 py-0 text-[10px]"
                      />
                    </td>
                    <td className="px-4 py-3">
                      {goalsPct !== null ? (
                        <div className="flex items-center gap-2">
                          <Progress value={goalsPct} className="h-1 w-14" />
                          <span className="tabular-nums text-muted-foreground">
                            {t.goals_met ?? 0}/{t.goals_total}
                          </span>
                        </div>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3 tabular-nums">
                      {(t.in_flight ?? 0) > 0 ? (
                        <span className="font-semibold">{t.in_flight}</span>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3 tabular-nums text-muted-foreground">
                      {rel(t.last_activity_unix ?? t.created_unix)}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </Card>

      {/* ── Row 5: 资产分布 | 流量状态码 | 拦截 & 待审批 ── */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        {/* 资产分布 */}
        <Card className="animate-fade-up p-4" style={{ animationDelay: "280ms" }}>
          <SectionTitle icon={NetworkIcon} sub={t("byType")}>
            {t("assetDistribution")}
          </SectionTitle>

          {assetByType.length === 0 ? (
            <EmptyState compact illustration="nodes" className="py-4" title={t("noAssetData")} />
          ) : (
            <div className="flex flex-col gap-2">
              {assetByType.map(([type, count]) => (
                <div key={type} className="flex items-center gap-2 text-xs">
                  <span className="w-8 shrink-0 text-right text-[10px] text-muted-foreground">
                    {t.has(`assetType.${type}`) ? t(`assetType.${type}`) : type}
                  </span>
                  <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
                    <div
                      className={cn("h-full rounded-full", ASSET_COLORS[type] ?? "bg-slate-400")}
                      style={{ width: `${(count / assetMax) * 100}%` }}
                    />
                  </div>
                  <span className="w-4 text-right tabular-nums">{count}</span>
                </div>
              ))}
            </div>
          )}

          <div className="mt-3 border-t pt-3 text-[10px] text-muted-foreground">
            {t("totalNodes", { count: totalAssets })}
          </div>
        </Card>

        {/* 流量状态码 */}
        <Card className="animate-fade-up p-4" style={{ animationDelay: "320ms" }}>
          <SectionTitle icon={ActivityIcon} sub={t("requestCount", { count: traffic.length })}>
            {t("trafficStatusCodes")}
          </SectionTitle>

          {/* bar chart */}
          {trafficByCodes.length === 0 ? (
            <EmptyState compact illustration="radar" className="py-4" title={t("noTrafficData")} />
          ) : (
            <>
              <div className="mb-3 flex items-end gap-2" style={{ height: 52 }}>
                {trafficByCodes.map(({ code, n }) => (
                  <div key={code} className="flex flex-1 flex-col items-center gap-1">
                    <span className="text-[9px] tabular-nums text-muted-foreground">{n}</span>
                    <div
                      className={cn("w-full min-h-1 rounded-sm", statusBg(code), "opacity-80")}
                      style={{ height: Math.max(4, (n / trafficMax) * 36) }}
                    />
                    <span className={cn("text-[9px] font-mono", statusColor(code))}>{code}</span>
                  </div>
                ))}
              </div>

              <div className="border-t pt-2.5">
                <div className="mb-1.5 text-[10px] text-muted-foreground">{t("recentRequests")}</div>
                <div className="flex flex-col gap-1.5">
                  {recentTraffic.map((e) => (
                    <div key={e.id} className="flex items-center gap-1.5 text-[10px]">
                      <span
                        className={cn(
                          "shrink-0 rounded border px-1 py-0 font-mono text-[9px]",
                          e.method === "GET"
                            ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                            : "border-blue-500/30 bg-blue-500/10 text-blue-400",
                        )}
                      >
                        {e.method}
                      </span>
                      <span className="min-w-0 flex-1 truncate text-muted-foreground">
                        {e.host}
                        {e.url.replace(/^https?:\/\/[^/]+/, "").substring(0, 30)}
                      </span>
                      <span className={cn("shrink-0 font-mono text-[9px]", statusColor(e.status))}>{e.status}</span>
                    </div>
                  ))}
                </div>
              </div>
            </>
          )}
        </Card>

        {/* 系统状态 & 待审批 */}
        <Card className="animate-fade-up p-4" style={{ animationDelay: "360ms" }}>
          <SectionTitle icon={ShieldCheckIcon}>{t("systemStatus")}</SectionTitle>

          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between rounded-lg border bg-muted/20 px-3 py-2">
              <div className="text-[10px] text-muted-foreground">{t("llmConfig")}</div>
              <Badge
                variant="outline"
                className={cn(
                  "text-[10px]",
                  stats?.llm_configured
                    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                    : "border-red-500/30 bg-red-500/10 text-red-400",
                )}
              >
                {stats?.llm_configured ? t("configured") : t("notConfigured")}
              </Badge>
            </div>

            <div className="flex items-center justify-between rounded-lg border bg-muted/20 px-3 py-2">
              <div className="text-[10px] text-muted-foreground">{t("trafficCapture")}</div>
              <Badge
                variant="outline"
                className={cn(
                  "text-[10px]",
                  settings?.traffic_capture
                    ? "border-violet-500/30 bg-violet-500/10 text-violet-400"
                    : "text-muted-foreground",
                )}
              >
                {settings?.traffic_capture ? t("on") : t("off")}
              </Badge>
            </div>

            {activeProfile && (
              <div className="flex items-center justify-between rounded-lg border bg-muted/20 px-3 py-2">
                <div className="text-[10px] text-muted-foreground">{t("activeModel")}</div>
                <span className="font-mono text-[10px]">{activeProfile.model}</span>
              </div>
            )}
          </div>

          {/* pending approvals */}
          {pendingCount > 0 && (
            <div className="mt-3">
              <div className="mb-1.5 text-[10px] font-medium text-amber-400">
                {t("pendingApprovals", { count: pendingCount })}
              </div>
              <div className="flex flex-col gap-1.5">
                {pending.slice(0, 3).map((p) => (
                  <Link
                    key={p.id}
                    href="/system/intercept/approvals"
                    className="flex items-center justify-between rounded-lg border border-amber-500/20 bg-amber-500/5 px-2.5 py-2 hover:bg-amber-500/10"
                  >
                    <div className="min-w-0">
                      <div className="text-[10px] font-medium">{p.tool_name}</div>
                      <div className="text-[9px] text-muted-foreground">{p.agent_name}</div>
                    </div>
                    <ArrowUpRightIcon className="size-3 shrink-0 text-amber-400" />
                  </Link>
                ))}
                {pendingCount > 3 && (
                  <Link
                    href="/system/intercept/approvals"
                    className="text-center text-[10px] text-muted-foreground hover:text-foreground"
                  >
                    {t("morePending", { count: pendingCount - 3 })}
                  </Link>
                )}
              </div>
            </div>
          )}

          {pendingCount === 0 && (
            <div className="mt-3 rounded-lg border bg-muted/10 px-3 py-3 text-center text-[10px] text-muted-foreground">
              <ShieldCheckIcon className="mx-auto mb-1 size-4 text-emerald-500/50" />
              {t("noPendingIntercepts")}
            </div>
          )}
        </Card>
      </div>

      {/* ── Row 6: 可靠性（工具调用/崩溃重放/中断恢复，#1–3、#36、#165–171） ── */}
      <Card className="animate-fade-up p-4" style={{ animationDelay: "400ms" }}>
        <SectionTitle icon={ShieldCheckIcon} sub={t("sinceStart")}>
          {t("reliability")}
        </SectionTitle>
        {!reliability || reliability.tools.calls === 0 ? (
          <div className="rounded-lg border bg-muted/10 px-3 py-3 text-center text-[10px] text-muted-foreground">
            {t("noReliabilityData")}
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              {/* 工具错误率 */}
              <div className="rounded-lg border bg-muted/20 px-3 py-2">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-[10px] text-muted-foreground">{t("toolErrorRate")}</span>
                  <span className={cn("text-lg font-semibold tabular-nums", rateClass(reliability.tools.error_rate))}>
                    {fmtPct(reliability.tools.error_rate)}
                  </span>
                </div>
                <div className="mt-0.5 text-[9px] text-muted-foreground">
                  {t("toolCallsDetail", { calls: reliability.tools.calls, errors: reliability.tools.errors })}
                </div>
              </div>

              {/* 崩溃重放成功率 */}
              <div className="rounded-lg border bg-muted/20 px-3 py-2">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-[10px] text-muted-foreground">{t("replaySuccessRate")}</span>
                  {reliability.replay.succeeded + reliability.replay.failed > 0 ? (
                    <span
                      className={cn("text-lg font-semibold tabular-nums", rateClass(reliability.replay.success_rate))}
                    >
                      {fmtPct(reliability.replay.success_rate)}
                    </span>
                  ) : (
                    <span className="text-lg font-semibold tabular-nums text-muted-foreground">—</span>
                  )}
                </div>
                <div className="mt-0.5 text-[9px] text-muted-foreground">
                  {t("replayDetail", {
                    attempts: reliability.replay.attempts,
                    succeeded: reliability.replay.succeeded,
                    failed: reliability.replay.failed,
                  })}
                </div>
              </div>

              {/* 中断恢复率 */}
              <div className="rounded-lg border bg-muted/20 px-3 py-2">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-[10px] text-muted-foreground">{t("interruptRecoveryRate")}</span>
                  {reliability.interrupts.succeeded + reliability.interrupts.failed > 0 ? (
                    <span
                      className={cn(
                        "text-lg font-semibold tabular-nums",
                        rateClass(reliability.interrupts.success_rate),
                      )}
                    >
                      {fmtPct(reliability.interrupts.success_rate)}
                    </span>
                  ) : (
                    <span className="text-lg font-semibold tabular-nums text-muted-foreground">—</span>
                  )}
                </div>
                <div className="mt-0.5 text-[9px] text-muted-foreground">
                  {t("interruptDetail", {
                    attempts: reliability.interrupts.attempts,
                    succeeded: reliability.interrupts.succeeded,
                    failed: reliability.interrupts.failed,
                  })}
                </div>
              </div>
            </div>

            {/* 错误阶段细分（hook/parse/validate/exec/timeout） */}
            {stageRows.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5">
                {stageRows.map(([stage, count]) => (
                  <span
                    key={stage}
                    className="rounded border bg-muted/20 px-1.5 py-0.5 text-[9px] text-muted-foreground"
                  >
                    {t.has(`stage.${stage}`) ? t(`stage.${stage}`) : stage} {count}
                  </span>
                ))}
              </div>
            )}

            {/* 错误最多的工具（错误数 / 调用数） */}
            {errorTools.length > 0 && (
              <div className="border-t pt-2.5">
                <div className="mb-1.5 text-[10px] text-muted-foreground">{t("topErrorTools")}</div>
                <div className="flex flex-col gap-1">
                  {errorTools.map((row) => (
                    <div key={row.tool} className="flex items-center justify-between gap-2 text-[10px]">
                      <span className="min-w-0 truncate font-mono">{row.tool}</span>
                      <span className="shrink-0 tabular-nums text-red-400">
                        {row.errors} / {row.calls}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* 协议异常（无配对/未知/缺失的工具结果）；全部为 0 时不打扰 */}
            {protocolTotal > 0 && (
              <div className="border-t pt-2.5 text-[9px] text-amber-400/90">
                {t("protocolDetail", {
                  unpaired: reliability.protocol.unpaired_results,
                  unknown: reliability.protocol.unknown_results,
                  missing: reliability.protocol.missing_results,
                })}
              </div>
            )}
          </div>
        )}
      </Card>
    </div>
  );
}
