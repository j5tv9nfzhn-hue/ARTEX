"use client";
"use no memo";

import * as React from "react";

import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  type NodeProps,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  type Edge as RFEdge,
  type Node as RFNode,
  useEdgesState,
  useNodesState,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";

import { Bug, Compass, Flag, FlaskConical, Layers, Lightbulb, type LucideIcon, Target } from "lucide-react";
import { useTranslations } from "next-intl";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ResolvedThemeMode } from "@/lib/preferences/theme";
import { severityDotClass, severityHex, severitySoftChipClass } from "@/lib/severity";
import { type Tone, toneClasses } from "@/lib/status";
import type { Edge, ExploreKind, TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";
import { usePreferencesStore } from "@/stores/preferences/preferences-provider";

// Edge relation labels are translation keys (namespace "explorationGraph",
// sub-key "rel") resolved at render; an unknown relation falls back to its raw id.
const relLabelKey: Record<string, string> = {
  spawns: "rel.spawns",
  derived_from: "rel.derived_from",
  yields: "rel.yields",
  proves: "rel.proves",
};

// ---- 亮/暗两套画布色板(本文件所有画布级颜色的唯一事实源) ----
// 为什么收在顶部:ReactFlow 的边 stroke、箭头 marker、MiniMap 颜色走 SVG 属性/行内
// style,吃不到 Tailwind 的 dark: 变体;节点卡的类型色细边同理。所以统一成亮暗成对
// 的映射,运行时按 resolvedThemeMode(preferences store,即 html.dark 的解析结果)
// 取一套。暗色不是把亮色压暗:统一提亮一档(500→400 级),「发光感」交给低透明度
// 外发光(boxShadow 混入类型色)而不是纯荧光实色,避免深底刺眼。
type ColorPair = { light: string; dark: string };

const pick = (pair: ColorPair, mode: ResolvedThemeMode) => pair[mode];

// 节点类型色:卡细边 / 图标底 / 选中 ring / MiniMap / 图例色块共用。
// finding 直接取 severity 单一事实源(lib/severity.ts)的 critical 亮暗 hex。
const kindColor: Record<ExploreKind, ColorPair> = {
  task: { light: "#64748b", dark: "#94a3b8" },
  begin: { light: "#64748b", dark: "#94a3b8" },
  goal: { light: "#10b981", dark: "#34d399" },
  hint: { light: "#8b5cf6", dark: "#a78bfa" },
  intent: { light: "#3b82f6", dark: "#60a5fa" },
  fact: { light: "#f59e0b", dark: "#fbbf24" },
  finding: { light: severityHex("critical"), dark: severityHex("critical", "dark") },
  digest: { light: "#14b8a6", dark: "#2dd4bf" },
};

// 关系(边)色:三档强度的「中等」色;proves 稍强(更粗 + 高压制下限,见边样式)。
// 名字沿用原有的导出符号 relColor(原为亮色单套 string 值,仓库内无其他消费方)。
export const relColor: Record<string, ColorPair> = {
  spawns: { light: "#10b981", dark: "#34d399" },
  derived_from: { light: "#6366f1", dark: "#818cf8" },
  yields: { light: "#f59e0b", dark: "#fbbf24" },
  proves: { light: "#ef4444", dark: "#f87171" },
};

// 画布细节:未知关系的兜底连线色、MiniMap 视口遮罩。
const canvasColor = {
  edgeFallback: { light: "#94a3b8", dark: "#64748b" },
  minimapMask: { light: "rgb(148 163 184 / 0.18)", dark: "rgb(2 6 23 / 0.55)" },
} satisfies Record<string, ColorPair>;

type TypeMeta = {
  labelKey: string;
  icon: LucideIcon;
  chip: string;
};

// Node kind labels are translation keys (namespace "explorationGraph", sub-key
// "kind") resolved at render; styling fields stay in code.
const typeMeta: Record<ExploreKind, TypeMeta> = {
  task: {
    labelKey: "kind.task",
    icon: Flag,
    chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
  },
  begin: {
    labelKey: "kind.begin",
    icon: Flag,
    chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
  },
  goal: {
    labelKey: "kind.goal",
    icon: Target,
    chip: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  },
  hint: {
    labelKey: "kind.hint",
    icon: Lightbulb,
    chip: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
  },
  intent: {
    labelKey: "kind.intent",
    icon: Compass,
    chip: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
  },
  fact: {
    labelKey: "kind.fact",
    icon: FlaskConical,
    chip: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
  },
  finding: {
    labelKey: "kind.finding",
    icon: Bug,
    chip: severitySoftChipClass("critical"),
  },
  digest: {
    labelKey: "kind.digest",
    icon: Layers,
    chip: "bg-teal-500/15 text-teal-600 dark:text-teal-400",
  },
};

// The task root is an origin fact (kind='fact', state='origin'); map it to the "begin" kind.
function viewKind(n: TaskNode): ExploreKind {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

function prioTone(p: number): Tone {
  return p >= 8 ? "red" : p >= 5 ? "amber" : "slate";
}

// 卡片外框色:选中 = 类型色实线;运行中 = 半透明类型色(第二层提示,主信号是呼吸点);
// 其余透明(细边画在内层卡片上)。抽成函数而非嵌套三元,顺带过掉 noNestedTernary。
function nodeRingColor(selected: boolean | undefined, breathe: boolean, hex: string): string {
  if (selected) return hex;
  if (breathe) return `${hex}80`;
  return "transparent";
}

// 边不透明度:新增节点的相邻边最亮;proves 的 dim 下限高一档(0.7);
// 派生/产出沿用原有 0.55 / 活跃 0.9。
function edgeOpacity(strong: boolean, animated: boolean, freshEdge: boolean): number {
  if (freshEdge) return 0.95;
  if (!strong) return animated ? 0.9 : 0.55;
  return animated ? 0.95 : 0.7;
}

// 卡片头部的状态信号点(取代原先只对 running 意图做的蓝色 ping):
// - 进行中:蓝色圆点 + 柔和呼吸光晕(animate-pulse,低透明度;reduced-motion 隐藏,见 ExploreNode);
// - 已完成/达成:低饱和绿点,弱化存在感,不与进行中抢注意力;
// - 失败(blocked)与 finding.confirmed:severity 红系(单一事实源 lib/severity.ts);
// - exhausted(预算掐断,非失败)沿用状态徽章的 violet 域色,避免同一节点两处颜色打架;
// - 其余终态(open/stopped/deleted/abandoned/dismissed)不加信号,控制噪声。
function stateSignal(n: TaskNode): { cls: string; breathe: boolean } | null {
  if (n.inherited) return null;
  if (n.type === "intent") {
    switch (n.state) {
      case "running":
        return { cls: "bg-blue-500 dark:bg-blue-400", breathe: true };
      case "done":
        return { cls: "bg-emerald-500/45 dark:bg-emerald-400/40", breathe: false };
      case "paused":
        return { cls: "bg-amber-500/55 dark:bg-amber-400/45", breathe: false };
      case "blocked":
        return { cls: severityDotClass("critical"), breathe: false };
      case "exhausted":
        return { cls: "bg-violet-500/50 dark:bg-violet-400/45", breathe: false };
      default:
        return null;
    }
  }
  if (n.type === "goal") {
    switch (n.state) {
      case "met":
        return { cls: "bg-emerald-500/45 dark:bg-emerald-400/40", breathe: false };
      case "abandoned":
        return { cls: "bg-slate-400/60 dark:bg-slate-500/50", breathe: false };
      default:
        return null;
    }
  }
  if (n.type === "finding") {
    return n.state === "confirmed"
      ? { cls: severityDotClass("critical"), breathe: false }
      : { cls: "bg-slate-400/60 dark:bg-slate-500/50", breathe: false };
  }
  return null;
}

const summaryFields: Record<ExploreKind, string[]> = {
  task: ["summary", "description"],
  begin: ["summary", "description"],
  goal: ["text"],
  hint: [],
  intent: ["summary"],
  fact: ["summary"],
  finding: ["name", "summary"],
  digest: ["body"],
};

function nodeSummary(n: TaskNode): string {
  const raw = n.payload ?? "";
  const fields = summaryFields[viewKind(n)] ?? [];
  if (fields.length && raw.trim()) {
    try {
      const obj: unknown = JSON.parse(raw);
      if (obj && typeof obj === "object") {
        for (const f of fields) {
          const v = (obj as Record<string, unknown>)[f];
          if (typeof v === "string" && v.trim()) return v;
        }
      }
    } catch {
      /* 非 JSON：原样显示 */
    }
  }
  return raw;
}

// digestMemberIds reads the fact/intent ids a digest folds from its payload
// (member_ids.{facts,intents}); ids are numbers in JSON, node ids are strings.
function digestMemberIds(n: TaskNode): string[] {
  if (n.type !== "digest" || !n.payload) return [];
  try {
    const p = JSON.parse(n.payload) as {
      member_ids?: { facts?: Array<string | number>; intents?: Array<string | number> };
    };
    return [...(p.member_ids?.facts ?? []), ...(p.member_ids?.intents ?? [])].map(String);
  } catch {
    return [];
  }
}

// collapseDigestGraph contracts each active digest's covered members into the
// digest super-node: the members are dropped from the rendered set and every
// edge touching a member is rewired onto its digest (member→member internal
// edges collapse to a self-loop and are dropped; the resulting edges are
// deduped by src+rel+dst). covers edges are structural and never drawn.
// Superseded digests (no covers edges) are dropped entirely. A graph with no
// active digest passes through unchanged. The full node list is kept by the
// caller so the detail drawer can still resolve a folded member by id.
function collapseDigestGraph(nodes: TaskNode[], edges: Edge[]): { viewNodes: TaskNode[]; viewEdges: Edge[] } {
  const activeDigests = new Set(nodes.filter((n) => n.type === "digest" && n.state === "active").map((n) => n.id));
  const memberToDigest = new Map<string, string>();
  for (const e of edges) {
    if (e.rel === "covers" && activeDigests.has(e.src)) memberToDigest.set(e.dst, e.src);
  }
  const viewNodes = nodes.filter((n) => (n.type === "digest" ? n.state === "active" : !memberToDigest.has(n.id)));
  const remap = (id: string) => memberToDigest.get(id) ?? id;
  const seen = new Set<string>();
  const rewired: Edge[] = [];
  // src+dst of every rewired yields edge, so the inverse derived_from can be dropped.
  const produced = new Set<string>();
  for (const e of edges) {
    if (e.rel === "covers") continue;
    const src = remap(e.src);
    const dst = remap(e.dst);
    if (src === dst) continue;
    const key = `${src}\u0000${e.rel}\u0000${dst}`;
    if (seen.has(key)) continue;
    seen.add(key);
    if (e.rel === "yields") produced.add(`${src} ${dst}`);
    rewired.push(src === e.src && dst === e.dst ? e : { ...e, src, dst });
  }
  // yields(A→B) and derived_from(B→A) are inverse relations: a finding is
  // yielded by one intent but derived_from other intents, so the raw graph has
  // no 2-cycle. Folding those distinct intents into one digest collapses both
  // onto the digest↔member pair, leaving a redundant reverse edge the layered
  // layout draws as an overlapping back-edge. Drop each derived_from whose
  // reverse yields exists, keeping the production direction (→member).
  const viewEdges = rewired.filter((e) => !(e.rel === "derived_from" && produced.has(`${e.dst} ${e.src}`)));
  return { viewNodes, viewEdges };
}

const COL_W = 360;
const ROW_H = 140;

type ExploreNodeData = { node: TaskNode; enter?: boolean; latest?: boolean };
type ExploreRFNode = RFNode<ExploreNodeData, "explore">;

function pushMap(m: Map<string, string[]>, k: string, v: string) {
  const a = m.get(k);
  if (a) a.push(v);
  else m.set(k, [v]);
}

// computeLayout auto-layers nodes by longest path along edges (cycle-safe DFS
// break + barycenter ordering), so origin → … → finding reads left-to-right.
function computeLayout(nodes: TaskNode[], edges: Edge[]): Map<string, { x: number; y: number }> {
  const order = nodes.map((n) => n.id);
  const idset = new Set(order);
  const valid = edges.filter((e) => idset.has(e.src) && idset.has(e.dst));
  const key = (s: string, d: string) => `${s}->${d}`;

  const out = new Map<string, string[]>();
  for (const e of valid) pushMap(out, e.src, e.dst);

  const seen = new Map<string, 0 | 1 | 2>();
  const back = new Set<string>();
  for (const root of order) {
    if ((seen.get(root) ?? 0) !== 0) continue;
    const stack: Array<{ u: string; i: number }> = [{ u: root, i: 0 }];
    seen.set(root, 1);
    while (stack.length) {
      const top = stack[stack.length - 1];
      const nbrs = out.get(top.u) ?? [];
      if (top.i >= nbrs.length) {
        seen.set(top.u, 2);
        stack.pop();
        continue;
      }
      const v = nbrs[top.i++];
      const s = seen.get(v) ?? 0;
      if (s === 1) back.add(key(top.u, v));
      else if (s === 0) {
        seen.set(v, 1);
        stack.push({ u: v, i: 0 });
      }
    }
  }

  const dagSucc = new Map<string, string[]>();
  const indeg = new Map<string, number>(order.map((id) => [id, 0]));
  for (const e of valid) {
    if (back.has(key(e.src, e.dst))) continue;
    pushMap(dagSucc, e.src, e.dst);
    indeg.set(e.dst, (indeg.get(e.dst) ?? 0) + 1);
  }
  const layer = new Map<string, number>(order.map((id) => [id, 0]));
  const queue = order.filter((id) => (indeg.get(id) ?? 0) === 0);
  for (let qi = 0; qi < queue.length; qi++) {
    const u = queue[qi];
    for (const v of dagSucc.get(u) ?? []) {
      layer.set(v, Math.max(layer.get(v) ?? 0, (layer.get(u) ?? 0) + 1));
      const d = (indeg.get(v) ?? 0) - 1;
      indeg.set(v, d);
      if (d === 0) queue.push(v);
    }
  }

  const maxLayer = Math.max(0, ...layer.values());
  const columns: string[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const id of order) columns[layer.get(id) ?? 0].push(id);

  const colIndexOf = new Map<string, number>();
  columns.forEach((ids, c) => {
    ids.forEach((id) => {
      colIndexOf.set(id, c);
    });
  });
  const neighbors = new Map<string, string[]>();
  for (const e of valid) {
    const cs = colIndexOf.get(e.src);
    const cd = colIndexOf.get(e.dst);
    if (cs === undefined || cd === undefined || Math.abs(cs - cd) !== 1) continue;
    pushMap(neighbors, e.src, e.dst);
    pushMap(neighbors, e.dst, e.src);
  }
  const sweep = (dir: 1 | -1) => {
    const start = dir === 1 ? 1 : columns.length - 2;
    const end = dir === 1 ? columns.length : -1;
    for (let c = start; c !== end; c += dir) {
      const ref = c - dir;
      const refIndex = new Map(columns[ref].map((id, i) => [id, i]));
      const scored = columns[c].map((id, i) => {
        const ns = (neighbors.get(id) ?? [])
          .map((nid) => refIndex.get(nid))
          .filter((v): v is number => v !== undefined);
        const bary = ns.length ? ns.reduce((a, b) => a + b, 0) / ns.length : i;
        return { id, bary, i };
      });
      scored.sort((a, b) => a.bary - b.bary || a.i - b.i);
      columns[c] = scored.map((s) => s.id);
    }
  };
  for (let iter = 0; iter < 4; iter++) {
    sweep(1);
    sweep(-1);
  }

  const maxCount = Math.max(1, ...columns.map((c) => c.length));
  const midline = ((maxCount - 1) * ROW_H) / 2;
  const pos = new Map<string, { x: number; y: number }>();
  columns.forEach((ids, c) => {
    const startY = midline - ((ids.length - 1) * ROW_H) / 2;
    ids.forEach((id, i) => {
      pos.set(id, { x: c * COL_W, y: startY + i * ROW_H });
    });
  });
  return pos;
}

// Dify-style node card.
function ExploreNode({ data, selected }: NodeProps<ExploreRFNode>) {
  const t = useTranslations("explorationGraph");
  // 画布级颜色(SVG/行内 style)吃不到 dark: 变体,按当前解析主题取 hex(见顶部色板)。
  const mode = usePreferencesStore((s) => s.resolvedThemeMode);
  const n = data.node;
  const kind = viewKind(n);
  const meta = typeMeta[kind] ?? typeMeta.intent;
  const hex = pick(kindColor[kind] ?? kindColor.intent, mode);
  const label = t(meta.labelKey);
  const Icon = meta.icon;
  const showState = n.type === "goal" || n.type === "intent";
  const showPriority = (n.type === "goal" || n.type === "intent") && n.priority > 0;
  const signal = stateSignal(n);
  // 最新新增节点的一次性光晕:挂载时可见,双 rAF 后开始淡出(约 700ms),
  // 父层清掉 enter 标记后卸载;reduced-motion 直接不渲染(motion-reduce:hidden)。
  const glow = Boolean(data.enter && data.latest);
  const [glowFaded, setGlowFaded] = React.useState(false);
  React.useEffect(() => {
    if (!glow) return;
    let raf2 = 0;
    const raf1 = requestAnimationFrame(() => {
      raf2 = requestAnimationFrame(() => setGlowFaded(true));
    });
    return () => {
      cancelAnimationFrame(raf1);
      cancelAnimationFrame(raf2);
    };
  }, [glow]);

  return (
    <div
      className={cn(
        // 外层:选中 ring 用类型色(全色边框 + 低透明度外发光,发光感靠 boxShadow
        // 而不是高饱和填充);hover 抬升;新增节点播放一次 250ms 淡入+微缩放。
        "relative w-[240px] cursor-pointer rounded-2xl border-2 transition-[transform,border-color,box-shadow] duration-200 hover:-translate-y-0.5 motion-reduce:transition-none",
        data.enter && "fade-in zoom-in-95 animation-duration-250 animate-in motion-reduce:animate-none",
      )}
      style={{
        borderColor: nodeRingColor(selected, Boolean(signal?.breathe), hex),
        boxShadow: selected ? `0 0 0 3px ${hex}26, 0 12px 28px -16px ${hex}80` : undefined,
      }}
    >
      {glow && (
        <span
          aria-hidden
          className={cn(
            "pointer-events-none absolute -inset-1 rounded-[18px] transition-opacity duration-700 ease-out motion-reduce:hidden",
            glowFaded ? "opacity-0" : "opacity-100",
          )}
          style={{ boxShadow: `0 0 22px 5px ${hex}59` }}
        />
      )}
      <div
        className="group relative rounded-[14px] border bg-white shadow-sm transition-shadow hover:shadow-lg dark:bg-neutral-900"
        style={{ borderColor: `${hex}4D` }}
      >
        <Handle
          type="target"
          position={Position.Left}
          className="!size-2 !border-2 !border-neutral-300 !bg-white opacity-0 transition-opacity group-hover:opacity-100 dark:!border-neutral-600 dark:!bg-neutral-800"
        />
        <div className="flex items-center gap-2 px-3 pt-3 pb-1.5">
          <span
            className="flex size-6 shrink-0 items-center justify-center rounded-lg shadow-sm"
            style={{ backgroundColor: hex }}
          >
            <Icon className="size-3.5 text-white" />
          </span>
          <span className="grow truncate text-[13px] font-semibold text-neutral-700 dark:text-neutral-100">
            {label}
          </span>
          {signal && (
            <span className="relative flex size-2 shrink-0">
              {signal.breathe && (
                <span
                  aria-hidden
                  className="absolute -inset-1 animate-pulse rounded-full bg-blue-400/40 motion-reduce:hidden"
                />
              )}
              <span className={cn("relative inline-flex size-2 rounded-full", signal.cls)} />
            </span>
          )}
          {showPriority && (
            <span
              className={cn(
                "shrink-0 rounded-md px-1.5 py-0.5 text-[10px] font-semibold tabular-nums",
                toneClasses[prioTone(n.priority)],
              )}
            >
              P{n.priority}
            </span>
          )}
        </div>
        <div className="line-clamp-3 px-3 pb-2 text-[13px] leading-snug text-neutral-500 dark:text-neutral-400">
          {nodeSummary(n) || label}
        </div>
        <div className="flex items-center justify-between gap-2 px-3 pb-2.5">
          {n.inherited && n.source_task_id ? (
            <Badge
              variant="outline"
              className="max-w-32 truncate"
              title={t("detail.inheritedTooltip", { id: n.source_task_id })}
            >
              {t("detail.sourceBadge", { id: n.source_task_id })}
            </Badge>
          ) : showState ? (
            <StatusBadge
              domain={n.type === "goal" ? "goal" : "intent"}
              value={n.state}
              dot
              className="px-1.5 py-0 text-[10px]"
            />
          ) : (
            <span className="text-[10px] text-neutral-400 dark:text-neutral-500">{label}</span>
          )}
          <span className="shrink-0 rounded-md border border-border/60 bg-muted/50 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground tabular-nums leading-none">
            #{n.id}
          </span>
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className="!size-2 !border-2 !border-neutral-300 !bg-white opacity-0 transition-opacity group-hover:opacity-100 dark:!border-neutral-600 dark:!bg-neutral-800"
        />
      </div>
    </div>
  );
}

const nodeTypes = { explore: ExploreNode };

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground w-14 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

function NodeDetailSheet({
  node,
  nodesById,
  onSelect,
  onOpenChange,
}: {
  node: TaskNode | null;
  nodesById: Map<string, TaskNode>;
  onSelect: (n: TaskNode) => void;
  onOpenChange: (open: boolean) => void;
}) {
  const t = useTranslations("explorationGraph");
  const mode = usePreferencesStore((s) => s.resolvedThemeMode);
  const [copied, setCopied] = React.useState(false);
  const meta = node ? (typeMeta[viewKind(node)] ?? typeMeta.intent) : null;
  const metaHex = node ? pick(kindColor[viewKind(node)] ?? kindColor.intent, mode) : "";
  const Icon = meta?.icon;
  const raw = node ? JSON.stringify(node, null, 2) : "";
  const memberIds = node ? digestMemberIds(node) : [];
  const members = memberIds.map((id) => nodesById.get(id)).filter((m): m is TaskNode => !!m);

  const copy = () => {
    void navigator.clipboard?.writeText(raw).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <Sheet open={node !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {node && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span
                  className="flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm"
                  style={{ backgroundColor: metaHex }}
                >
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <SheetTitle className="leading-tight">{t(meta.labelKey)}</SheetTitle>
                  <span className="text-muted-foreground font-mono text-xs">{node.id}</span>
                </div>
                {node.inherited && node.source_task_id && (
                  <Badge variant="outline" className="ml-auto shrink-0">
                    {t("detail.sourceTaskBadge", { id: node.source_task_id })}
                  </Badge>
                )}
              </div>
            </SheetHeader>

            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">{t("detail.attributes")}</h4>
                  <DetailRow label={t("detail.type")}>{t(meta.labelKey)}</DetailRow>
                  <DetailRow label={t("detail.state")}>
                    {node.type === "goal" || node.type === "intent" ? (
                      <StatusBadge
                        domain={node.type === "goal" ? "goal" : "intent"}
                        value={node.state}
                        dot
                        className="px-1.5 py-0 text-[10px]"
                      />
                    ) : (
                      <span className="font-mono text-xs">{node.state}</span>
                    )}
                  </DetailRow>
                  <DetailRow label={t("detail.priority")}>
                    <span className="tabular-nums">{node.priority}</span>
                  </DetailRow>
                  <DetailRow label={t("detail.origin")}>
                    <span className="font-mono text-xs">{node.origin}</span>
                  </DetailRow>
                  {node.inherited && node.source_task_id && (
                    <DetailRow label={t("detail.sourceTask")}>
                      <span className="font-mono text-xs">
                        {t("detail.inheritedReadOnly", { id: node.source_task_id })}
                      </span>
                    </DetailRow>
                  )}
                  <DetailRow label={t("detail.time")}>{new Date(node.ts).toLocaleString("zh-CN")}</DetailRow>
                </section>

                {node.type === "digest" && (
                  <>
                    {nodeSummary(node) && (
                      <section className="border-t pt-3">
                        <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">
                          {t("detail.digestConclusion")}
                        </h4>
                        <p className="text-foreground text-sm leading-relaxed break-words whitespace-pre-wrap">
                          {nodeSummary(node)}
                        </p>
                      </section>
                    )}
                    <section className="border-t pt-3">
                      <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">
                        {t("detail.members", { count: memberIds.length })}
                      </h4>
                      <div className="flex flex-col gap-1.5">
                        {members.map((m) => {
                          const mKind = viewKind(m);
                          const mMeta = typeMeta[mKind] ?? typeMeta.intent;
                          const MIcon = mMeta.icon;
                          return (
                            <button
                              key={m.id}
                              type="button"
                              onClick={() => onSelect(m)}
                              className="hover:bg-accent flex items-start gap-2 rounded-md border p-2 text-left transition-colors"
                            >
                              <span
                                className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded"
                                style={{ backgroundColor: pick(kindColor[mKind] ?? kindColor.intent, mode) }}
                              >
                                <MIcon className="size-3 text-white" />
                              </span>
                              <span className="min-w-0 flex-1">
                                <span className="text-foreground line-clamp-2 text-xs leading-snug">
                                  {nodeSummary(m) || t(mMeta.labelKey)}
                                </span>
                                <span className="text-muted-foreground block font-mono text-[10px]">
                                  {t(mMeta.labelKey)} · #{m.id}
                                </span>
                              </span>
                            </button>
                          );
                        })}
                        {members.length < memberIds.length && (
                          <span className="text-muted-foreground text-[10px]">
                            {t("detail.membersNotLoaded", { count: memberIds.length - members.length })}
                          </span>
                        )}
                      </div>
                    </section>
                  </>
                )}

                <section className="border-t pt-3">
                  <div className="mb-1.5 flex items-center justify-between">
                    <h4 className="text-muted-foreground text-xs font-medium">{t("detail.raw")}</h4>
                    <button
                      type="button"
                      onClick={copy}
                      className="text-muted-foreground hover:text-foreground text-xs transition-colors"
                    >
                      {copied ? t("detail.copied") : t("detail.copy")}
                    </button>
                  </div>
                  <pre className="bg-muted/50 text-foreground max-w-full overflow-hidden rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                    {raw}
                  </pre>
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

// ExplorationGraphInner renders the given nodes/edges as a React Flow canvas.
// Positions are auto-laid-out and preserved across prop updates (so a polling
// caller's graph doesn't jump); click a node to open its detail drawer.
function ExplorationGraphInner({ nodes, edges, emptyHint }: { nodes: TaskNode[]; edges: Edge[]; emptyHint?: string }) {
  const t = useTranslations("explorationGraph");
  const hint = emptyHint ?? t("emptyHint");
  // ReactFlow 的画布级颜色随解析主题联动(见顶部色板注释)。
  const mode = usePreferencesStore((s) => s.resolvedThemeMode);
  // MiniMap 的 nodeColor 每次渲染都换新函数会让整张小地图跟着重算；节点上百时
  // 每次缩放/拖动都要多付一次。按主题缓存住即可（本文件有 "use no memo"，
  // React Compiler 不会替我们做这件事）。
  const minimapNodeColor = React.useCallback(
    (node: RFNode) => {
      const data = node.data as ExploreNodeData | undefined;
      const k = data?.node ? viewKind(data.node) : "intent";
      return pick(kindColor[k] ?? kindColor.intent, mode);
    },
    [mode],
  );
  const [rfNodes, setRfNodes, onNodesChange] = useNodesState<ExploreRFNode>([]);
  const [rfEdges, setRfEdges, onEdgesChange] = useEdgesState<RFEdge>([]);
  const [selected, setSelected] = React.useState<TaskNode | null>(null);
  // 增量新增节点 id:驱动卡片淡入、最新节点一次性光晕、相邻边短暂增亮;窗口后清空。
  // 首屏(prevIds 为空)不算增量,避免整图齐闪;只有后续刷新带来的新节点才有进场动画。
  // 增量窗口只用来给「新到的节点」打一次进场/光晕标记，它**不是**渲染输入，所以
  // 放 ref 而不是 state。原实现把它塞进 effect 依赖：写入窗口 → effect 重跑 →
  // 整图重新布局；1.5s 后清空窗口 → effect 再跑一次。等于每来一个新节点都白算
  // 两遍全图，这是刷新/切页卡顿的来源之一。
  const freshWindowRef = React.useRef<ReadonlySet<string>>(new Set<string>());
  const prevIdsRef = React.useRef<ReadonlySet<string>>(new Set<string>());
  const freshTimer = React.useRef<number | undefined>(undefined);
  // 上一次真正应用到画布上的结构 + 主题：用来跳过「输入没变」的空转重排。
  const appliedRef = React.useRef<{ viewNodes: TaskNode[]; viewEdges: Edge[]; mode: string } | null>(null);
  React.useEffect(() => () => window.clearTimeout(freshTimer.current), []);
  // Full node index (incl. folded members) so the digest drawer can resolve
  // covered members even though they are not rendered on the canvas.
  const nodesById = React.useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  // Collapsed view: folded members removed, their edges rewired onto the digest.
  const { viewNodes, viewEdges } = React.useMemo(() => collapseDigestGraph(nodes, edges), [nodes, edges]);
  const isEmpty = viewNodes.length === 0;

  React.useEffect(() => {
    // 输入没变（同一份 nodes/edges 引用 + 同一主题）就整段跳过：这一轮唯一会发生
    // 的事是把 rfNodes/rfEdges 全部换成新对象，React Flow 会因此把每个节点与每条
    // 边重新渲染一遍。没有这个闸门，主题刷新、翻译对象变化都会触发一次全量重排。
    if (
      appliedRef.current &&
      appliedRef.current.viewNodes === viewNodes &&
      appliedRef.current.viewEdges === viewEdges &&
      appliedRef.current.mode === mode
    ) {
      return;
    }
    appliedRef.current = { viewNodes, viewEdges, mode };
    setSelected((cur) => (cur ? (nodesById.get(cur.id) ?? cur) : cur));
    const liveTargets = new Set(
      viewNodes.filter((n) => !n.inherited && n.type === "intent" && n.state === "running").map((n) => n.id),
    );
    const layout = computeLayout(viewNodes, viewEdges);
    // 与上一轮渲染比对出增量节点;prevIds 为空(首帧/空图)不算增量。
    const prevIds = prevIdsRef.current;
    prevIdsRef.current = new Set(viewNodes.map((n) => n.id));
    const fresh = new Set<string>();
    if (prevIds.size > 0) {
      for (const n of viewNodes) if (!prevIds.has(n.id)) fresh.add(n.id);
    }
    // 窗口内累计:freshWindowRef 是本轮之前的窗口残留,和本轮新识别的一起生效
    // (prevIds 每轮刷新,新节点只在出现的当轮被识别,不能只靠本轮 fresh)。
    const freshWindow = new Set<string>([...freshWindowRef.current, ...fresh]);
    // 按 ts(ISO 字符串可直接比较)找最新节点,只给窗口内最新的那个打光晕。
    let latestId: string | null = null;
    let latestTs = "";
    for (const n of viewNodes) {
      if (n.ts > latestTs) {
        latestTs = n.ts;
        latestId = n.id;
      }
    }
    // 全量重建交给 transition：切页/新节点到达时先让「Tab 切过去、新节点进场」
    // 这一帧落屏，再把整图的 React Flow 更新放到并发渲染里分片完成，避免主线程
    // 被一个长任务占住（用户看到的就是切换标签页时 0.x 秒的卡顿）。
    React.startTransition(() => {
      setRfNodes((prev) => {
        const prevPos = new Map(prev.map((p) => [p.id, p.position]));
        return viewNodes.map((n) => ({
          id: n.id,
          type: "explore" as const,
          position: prevPos.get(n.id) ?? layout.get(n.id) ?? { x: 0, y: 0 },
          data: { node: n, enter: freshWindow.has(n.id), latest: freshWindow.has(n.id) && n.id === latestId },
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
        }));
      });
      setRfEdges(
        viewEdges.map((e, i) => {
          const color = pick(relColor[e.rel] ?? canvasColor.edgeFallback, mode);
          const animated = liveTargets.has(e.dst);
          // proves 是链路终局判定,强一档:更粗(2.5)且压低后的不透明度下限更高(0.7);
          // 派生/产出维持中等(2px);不活跃边沿用原有 dim(0.55)。新增节点的相邻边短暂增亮。
          const strong = e.rel === "proves";
          const freshEdge = freshWindow.has(e.src) || freshWindow.has(e.dst);
          return {
            id: `e${i}-${e.src}-${e.dst}`,
            source: e.src,
            target: e.dst,
            label: relLabelKey[e.rel] ? t(relLabelKey[e.rel]) : e.rel,
            type: "default",
            animated,
            labelShowBg: false,
            labelStyle: { fontSize: 10, fontWeight: 600, fill: color },
            style: {
              stroke: color,
              strokeWidth: strong ? 2.5 : 2,
              strokeOpacity: edgeOpacity(strong, animated, freshEdge),
            },
            markerEnd: { type: MarkerType.ArrowClosed, color, width: 14, height: 14 },
          } satisfies RFEdge;
        }),
      );
    });
    // 进场窗口:识别到增量节点时把窗口(累计集合)写入 state(窗口期间相邻边保持增亮),
    // 1.5s 后清空 —— 卡片类名/光晕随 data 更新卸载,边恢复常规压制。
    if (fresh.size > 0) {
      freshWindowRef.current = freshWindow;
      window.clearTimeout(freshTimer.current);
      freshTimer.current = window.setTimeout(() => {
        freshWindowRef.current = new Set<string>();
        setRfNodes((prev) =>
          prev.some((p) => p.data.enter || p.data.latest)
            ? prev.map((p) =>
                p.data.enter || p.data.latest ? { ...p, data: { ...p.data, enter: false, latest: false } } : p,
              )
            : prev,
        );
      }, 1500);
    }
  }, [viewNodes, viewEdges, nodesById, setRfNodes, setRfEdges, t, mode]);

  return (
    <>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={(_, node) => setSelected(node.data.node)}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        onlyRenderVisibleElements
        minZoom={0.1}
        colorMode={mode}
        className="!bg-muted dark:!bg-background"
      >
        <Background
          variant={BackgroundVariant.Dots}
          gap={16}
          size={1}
          className="text-muted-foreground/30 dark:text-foreground/15"
        />
        <Controls
          showInteractive={false}
          className="!rounded-lg !border !shadow-sm [&>button]:!border-border [&>button]:!bg-card [&>button:hover]:!bg-accent [&_svg]:!fill-foreground"
        />
        <MiniMap
          pannable
          zoomable
          className="!bg-card !rounded-lg !border !shadow-sm"
          maskColor={pick(canvasColor.minimapMask, mode)}
          nodeColor={minimapNodeColor}
          nodeStrokeWidth={0}
          nodeBorderRadius={4}
        />
        <Panel position="top-left">
          <div className="bg-card/95 flex flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
            {isEmpty && <span className="text-muted-foreground">{hint}</span>}
            <div className="flex flex-wrap gap-x-3 gap-y-1.5">
              {(["begin", "goal", "intent", "fact", "finding", "hint", "digest"] as ExploreKind[]).map((k) => {
                const m = typeMeta[k];
                const Icon = m.icon;
                return (
                  <span key={k} className="text-foreground inline-flex items-center gap-1.5">
                    <span
                      className="flex size-4 items-center justify-center rounded"
                      style={{ backgroundColor: pick(kindColor[k] ?? kindColor.intent, mode) }}
                    >
                      <Icon className="size-2.5 text-white" />
                    </span>
                    {t(m.labelKey)}
                  </span>
                );
              })}
            </div>
            <div className="border-border/60 text-muted-foreground flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-2">
              {Object.entries(relLabelKey).map(([k, keyName]) => (
                <span key={k} className="inline-flex items-center gap-1.5">
                  <span
                    className="h-0.5 w-4 rounded-full"
                    style={{ backgroundColor: pick(relColor[k] ?? canvasColor.edgeFallback, mode) }}
                  />
                  {t(keyName)}
                </span>
              ))}
            </div>
          </div>
        </Panel>
      </ReactFlow>
      <NodeDetailSheet
        node={selected}
        nodesById={nodesById}
        onSelect={setSelected}
        onOpenChange={(o) => !o && setSelected(null)}
      />
    </>
  );
}

// ExplorationGraph is the shared探索图/攻击链路图 canvas — used by the task graph
// tab (full graph, polled) and the finding lineage view (origin→finding subgraph).
export function ExplorationGraph({
  nodes,
  edges,
  className,
  emptyHint,
}: {
  nodes: TaskNode[];
  edges: Edge[];
  className?: string;
  emptyHint?: string;
}) {
  return (
    <div className={cn("w-full overflow-hidden rounded-xl", className)}>
      <ReactFlowProvider>
        <ExplorationGraphInner nodes={nodes} edges={edges} emptyHint={emptyHint} />
      </ReactFlowProvider>
    </div>
  );
}
