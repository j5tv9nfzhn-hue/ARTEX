"use client";

import * as React from "react";

import { ExplorationGraph } from "@/components/exploration-graph";
import { Card, CardContent } from "@/components/ui/card";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { Edge, TaskNode } from "@/lib/types";

export function GraphTab({ taskId }: { taskId: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  // 上一次图数据的签名:轮询拿到相同数据时跳过 setState,避免整图无谓重建(拖动时
  // 才不会被 20s 轮询打断而顿挫)。只取影响渲染的字段。
  const sigRef = React.useRef("");

  // 换任务时清掉签名，强制下一次刷新重建。
  React.useEffect(() => {
    sigRef.current = "";
  }, []);

  // 图数据 20s 刷新一次：节点/边数量大，重建成本高，保持原间隔。
  // 签名比对避免无变化时重建整张力导向图（拖动会被无谓重建打断）。
  usePolling(
    async () => {
      const g = await api.explorationGraph(taskId);
      const ns = g.nodes ?? [];
      const es = g.edges ?? [];
      const sig = JSON.stringify([
        ns.map((n) => [n.id, n.type, n.state, n.priority, n.payload]),
        es.map((e) => [e.src, e.dst, e.rel]),
      ]);
      if (sig === sigRef.current) return;
      sigRef.current = sig;
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
