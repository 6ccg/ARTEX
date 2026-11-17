"use client";
import { startPolling } from "@/hooks/use-polling";

import * as React from "react";

import { Card, CardContent } from "@/components/ui/card";
import { ExplorationGraph } from "@/components/exploration-graph";
import { api, type ExplorationGraphSnapshot } from "@/lib/api";

const emptyGraph = { nodes: [], edges: [] };

export function GraphTab({ taskId }: { taskId: string }) {
  const [graph, setGraph] = React.useState<{ taskId: string; snapshot: ExplorationGraphSnapshot }>();

  React.useEffect(() => {
    const controller = new AbortController();
    let current: ExplorationGraphSnapshot | undefined;
    const load = async () => {
      const next = await api.explorationGraph(taskId, current, controller.signal);
      if (controller.signal.aborted || next === current) return;
      current = next;
      setGraph({ taskId, snapshot: next });
    };
    const stopPolling = startPolling(load, 20000);
    return () => {
      controller.abort();
      stopPolling();
    };
  }, [taskId]);
  const { nodes, edges } = graph?.taskId === taskId ? graph.snapshot : emptyGraph;

  return (
    <Card>
      <CardContent className="p-0">
        <ExplorationGraph nodes={nodes} edges={edges} className="h-[72vh]" />
      </CardContent>
    </Card>
  );
}
