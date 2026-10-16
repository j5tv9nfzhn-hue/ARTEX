"use client";

// 检查点时间线（批次 6 / #29、#33、#34）。
// 会话页与任务详情共用：列出检查点（新→旧），可回滚到任一时间线节点。回滚 = 隐藏
// 该点之后的步骤 + 截断 transcript + 重排未完成意图（任务）；工具副作用不回滚，
// 回滚前用对话框明示。文案在 messages/zh.json 的 "checkpoints" 命名空间。

import * as React from "react";

import { HistoryIcon, RefreshCwIcon, RotateCcwIcon, TriangleAlertIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { Checkpoint } from "@/lib/types";
import { cn } from "@/lib/utils";

// 检查点来源 → 文案 key；未知值按 manual 呈现（与后端 CHECKPOINT_REASON 常量对应）。
const REASON_KEYS: Record<string, string> = {
  user_message: "reasonUserMessage",
  turn_complete: "reasonTurnComplete",
  admit: "reasonAdmit",
  intent_settled: "reasonIntentSettled",
  interrupt: "reasonInterrupt",
  compaction_recover: "reasonCompactionRecover",
  revert: "reasonRevert",
  manual: "reasonManual",
};

function shortTime(iso: string): string {
  if (!iso) return "";
  return new Date(iso).toLocaleString("zh-CN", { hour12: false });
}

export function CheckpointTimeline({
  scope,
  id,
  onReverted,
  className,
}: {
  scope: "conversation" | "task";
  id: string;
  onReverted?: () => void;
  className?: string;
}) {
  const t = useTranslations("checkpoints");
  const [items, setItems] = React.useState<Checkpoint[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [pendingRevert, setPendingRevert] = React.useState<Checkpoint | null>(null);
  const [reverting, setReverting] = React.useState(false);

  const reasonLabel = React.useCallback(
    (reason: string) => {
      const key = REASON_KEYS[reason] ?? REASON_KEYS.manual;
      return t(key);
    },
    [t],
  );

  const load = React.useCallback(() => {
    setLoading(true);
    const req = scope === "task" ? api.taskCheckpoints(id) : api.conversationCheckpoints(Number(id));
    req
      .then((rows) => {
        setItems(rows);
        setError("");
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [scope, id]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function confirmRevert() {
    if (!pendingRevert) return;
    setReverting(true);
    try {
      const result =
        scope === "task"
          ? await api.revertTaskCheckpoint(id, pendingRevert.id)
          : await api.revertConversationCheckpoint(Number(id), pendingRevert.id);
      const reopened = result.reopened?.length ? t("reopened", { count: result.reopened.length }) : "";
      toast.success(t("reverted", { hidden: result.hidden, superseded: result.superseded, reopened }));
      setPendingRevert(null);
      load();
      onReverted?.();
    } catch (e) {
      toast.error(t("revertFailed", { msg: (e as Error).message }));
    } finally {
      setReverting(false);
    }
  }

  let body: React.ReactNode;
  if (loading && items.length === 0) {
    body = (
      <div className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
        <Spinner /> {t("loading")}
      </div>
    );
  } else if (error) {
    body = (
      <Alert variant="destructive">
        <TriangleAlertIcon />
        <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
          <span>{t("loadError", { msg: error })}</span>
          <Button size="sm" variant="outline" onClick={load}>
            {t("retry")}
          </Button>
        </AlertDescription>
      </Alert>
    );
  } else if (items.length === 0) {
    body = (
      <div className="rounded-xl border border-dashed">
        <EmptyState illustration="flag" title={t("emptyTitle")} description={t("empty")} />
      </div>
    );
  } else {
    body = (
      <ol className="flex flex-col gap-2">
        {items.map((cp) => {
          const superseded = cp.status !== "active";
          return (
            <li key={cp.id} className={cn("rounded-lg border p-3", superseded && "opacity-60")}>
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <Badge variant="outline" className="font-mono">
                  #{cp.seq}
                </Badge>
                <span className="text-sm font-medium">{reasonLabel(cp.reason)}</span>
                {superseded && (
                  <Badge variant="secondary" title={t("supersededHint")}>
                    {t("superseded")}
                  </Badge>
                )}
                <span className="text-muted-foreground ml-auto text-xs tabular-nums">{shortTime(cp.created_at)}</span>
              </div>
              {cp.summary && <p className="text-muted-foreground mt-1.5 text-sm break-words">{cp.summary}</p>}
              <div className="text-muted-foreground mt-1.5 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                <span>{t("boundary", { id: cp.snapshot.activity_id })}</span>
                {typeof cp.snapshot.counts?.nodes === "number" && (
                  <span>
                    {t("graphCount", { nodes: cp.snapshot.counts.nodes, edges: cp.snapshot.counts.edges ?? 0 })}
                  </span>
                )}
                {cp.snapshot.compaction && <span>{t("compactionBoundary")}</span>}
              </div>
              <div className="mt-2 flex justify-end">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={superseded || reverting}
                  onClick={() => setPendingRevert(cp)}
                >
                  <RotateCcwIcon data-icon="inline-start" /> {t("revert")}
                </Button>
              </div>
            </li>
          );
        })}
      </ol>
    );
  }

  return (
    <div className={cn("flex min-w-0 flex-col gap-3", className)}>
      <div className="flex items-center justify-between gap-2">
        <p className="text-muted-foreground min-w-0 flex-1 text-xs">{t("boundaryHint")}</p>
        <Button size="sm" variant="ghost" onClick={load} disabled={loading} title={t("refresh")}>
          {loading ? <Spinner /> : <RefreshCwIcon />}
          {t("refresh")}
        </Button>
      </div>
      {body}

      <AlertDialog
        open={pendingRevert !== null}
        onOpenChange={(open) => {
          if (!open && !reverting) setPendingRevert(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{pendingRevert ? t("revertTo", { seq: pendingRevert.seq }) : ""}</AlertDialogTitle>
            <AlertDialogDescription>
              {scope === "task" ? t("revertDescTask") : t("revertDescConversation")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Alert>
            <TriangleAlertIcon />
            <AlertDescription>{t("sideEffects")}</AlertDescription>
          </Alert>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={reverting}>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={reverting}
              onClick={(event) => {
                event.preventDefault();
                void confirmRevert();
              }}
            >
              {reverting && <Spinner data-icon="inline-start" />}
              {reverting ? t("reverting") : t("confirmRevert")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// CheckpointTimelineButton 是会话页头部的入口：按钮 + 对话框里的时间线。
export function CheckpointTimelineButton({
  scope,
  id,
  onReverted,
}: {
  scope: "conversation" | "task";
  id: string;
  onReverted?: () => void;
}) {
  const t = useTranslations("checkpoints");
  const [open, setOpen] = React.useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" title={t("buttonTitle")}>
          <HistoryIcon data-icon="inline-start" />
          {t("button")}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <HistoryIcon className="size-4" /> {t("title")}
          </DialogTitle>
          <DialogDescription>{t("description")}</DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] overflow-y-auto pr-1">
          <CheckpointTimeline scope={scope} id={id} onReverted={onReverted} />
        </div>
      </DialogContent>
    </Dialog>
  );
}
