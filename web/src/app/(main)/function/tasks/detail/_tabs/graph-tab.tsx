"use client";

import * as React from "react";

import { ExplorationGraph } from "@/components/exploration-graph";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { Edge, TaskNode } from "@/lib/types";

// 探索链路页的重建成本很高（React Flow 全量节点 + 自动布局 + 边标签），而标签页
// 切换会卸载/重挂本组件。这里保留最近一次拿到的图数据：再次进入同一任务时同步
// 初始化 state，画布立刻可见，20s 轮询随后照常校正。
//
// 命中缓存时也会预置签名，所以「切回本页」触发的那一次轮询若数据没变就不会
// setState —— 否则每次切页都要白跑一遍整图重建（这正是切换标签页卡顿的主因）。
type GraphSnapshot = { nodes: TaskNode[]; edges: Edge[]; sig: string };
const GRAPH_CACHE_MAX = 3;
const graphCache = new Map<string, GraphSnapshot>();

function readGraphCache(taskId: string): GraphSnapshot | undefined {
  return graphCache.get(taskId);
}

// 只留最近 3 个任务：图谱里带着每个节点的 payload 文本，留太多会白白吃内存。
function writeGraphCache(taskId: string, snap: GraphSnapshot) {
  graphCache.delete(taskId);
  graphCache.set(taskId, snap);
  while (graphCache.size > GRAPH_CACHE_MAX) {
    const oldest = graphCache.keys().next().value;
    if (oldest === undefined) break;
    graphCache.delete(oldest);
  }
}

// 只取影响渲染的字段；payload 变化会改变节点摘要文本，必须纳入签名。
function graphSignature(ns: TaskNode[], es: Edge[]) {
  return JSON.stringify([
    ns.map((n) => [n.id, n.type, n.state, n.priority, n.payload]),
    es.map((e) => [e.src, e.dst, e.rel]),
  ]);
}

export function GraphTab({ taskId }: { taskId: string }) {
  const cached = readGraphCache(taskId);
  const [nodes, setNodes] = React.useState<TaskNode[]>(cached?.nodes ?? []);
  const [edges, setEdges] = React.useState<Edge[]>(cached?.edges ?? []);
  // 上一次的图数据签名：轮询拿到相同数据时跳过 setState，避免整图无谓重建
  // （拖动时也不会被 20s 轮询打断而顿挫）。
  const sigRef = React.useRef<string>(cached?.sig ?? "");
  // 首屏由 useState 的惰性初值处理；这个 effect 只负责「同一实例换了 taskId」。
  const loadedForRef = React.useRef<string>(taskId);

  React.useEffect(() => {
    if (loadedForRef.current === taskId) return;
    loadedForRef.current = taskId;
    const snap = readGraphCache(taskId);
    sigRef.current = snap?.sig ?? "";
    setNodes(snap?.nodes ?? []);
    setEdges(snap?.edges ?? []);
  }, [taskId]);

  // 图数据 20s 刷新一次：节点/边数量大，重建成本高，保持原间隔。
  usePolling(
    async () => {
      const g = await api.explorationGraph(taskId);
      const ns = g.nodes ?? [];
      const es = g.edges ?? [];
      const sig = graphSignature(ns, es);
      if (sig === sigRef.current) return;
      sigRef.current = sig;
      writeGraphCache(taskId, { nodes: ns, edges: es, sig });
      setNodes(ns);
      setEdges(es);
    },
    { intervalMs: 20_000, enabled: Boolean(taskId) },
  );

  return (
    <Card>
      <CardContent className="p-0">
        <ExplorationGraph nodes={nodes} edges={edges} className="h-[72vh]" />
      </CardContent>
    </Card>
  );
}
