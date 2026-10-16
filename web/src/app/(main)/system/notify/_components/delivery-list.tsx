"use client";

import * as React from "react";

import { RefreshCwIcon, RotateCcwIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { NotificationChannel, NotificationDelivery } from "@/lib/types";

// 事件类型徽标文案键（notifyPage 命名空间）。只有非「新建漏洞」的事件需要
// 额外标注——新建是默认语义，标注它只会变成噪音。
const EVENT_KIND_LABEL_KEYS: Record<string, string> = {
  finding_status_changed: "delivery.statusChanged",
  task_status_changed: "delivery.taskStatusChanged",
  approval_pending: "delivery.approvalPending",
};

// DeliveryList 是投递记录表：可按渠道与状态筛选，失败项可手动重发。
export function DeliveryList({ channels }: { channels: NotificationChannel[] }) {
  const t = useTranslations("notifyPage");
  // 投递状态与严重等级标签来自共享 status 命名空间，与 StatusBadge 一致。
  // lib/status.ts 是静态回退，不直接用它替代运行时本地化标签。
  const ts = useTranslations("status");
  const tp = useTranslations("pagination");
  const [rows, setRows] = React.useState<NotificationDelivery[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [channelID, setChannelID] = React.useState<number | undefined>(undefined);
  const [state, setState] = React.useState<string | undefined>(undefined);
  const [loading, setLoading] = React.useState(false);
  const pageSize = 50;

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .notifyDeliveries({ channelId: channelID, state, page, pageSize })
      .then((r) => {
        setRows(r.deliveries);
        setTotal(r.total);
      })
      .catch((e) => toast.error(t("delivery.loadFailed", { msg: (e as Error).message })))
      .finally(() => setLoading(false));
    // t 是稳定引用，仍纳入依赖以满足 exhaustive-deps。
  }, [channelID, state, page, t]);
  React.useEffect(() => {
    load();
  }, [load]);

  async function retry(id: number) {
    try {
      await api.notifyRetryDelivery(id);
      toast.success(t("delivery.requeued"));
      load();
    } catch (e) {
      toast.error(t("delivery.resendFailed", { msg: (e as Error).message }));
    }
  }

  const maxPage = Math.max(1, Math.ceil(total / pageSize));

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={channelID ? String(channelID) : "all"}
          onValueChange={(v) => {
            setPage(1);
            setChannelID(v === "all" ? undefined : Number(v));
          }}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t("delivery.allChannels")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("delivery.allChannels")}</SelectItem>
            {channels.map((c) => (
              <SelectItem key={c.id} value={String(c.id)}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={state ?? "all"}
          onValueChange={(v) => {
            setPage(1);
            setState(v === "all" ? undefined : v);
          }}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder={t("delivery.allStates")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("delivery.allStates")}</SelectItem>
            {["pending", "sending", "sent", "failed", "skipped"].map((s) => (
              <SelectItem key={s} value={s}>
                {ts(`delivery.${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" variant="outline" onClick={load} disabled={loading}>
          <RefreshCwIcon className={loading ? "animate-spin" : ""} /> {t("delivery.refresh")}
        </Button>
        <span className="text-muted-foreground ml-auto text-xs">{t("delivery.totalCount", { total })}</span>
      </div>

      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-40">{t("delivery.colTime")}</TableHead>
              <TableHead>{t("delivery.colFinding")}</TableHead>
              <TableHead className="w-40">{t("delivery.colChannel")}</TableHead>
              <TableHead className="w-24">{t("delivery.colState")}</TableHead>
              <TableHead className="w-16">{t("delivery.colAttempts")}</TableHead>
              <TableHead>{t("delivery.colError")}</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="p-0">
                  {/* 加载态与空态拆开:加载中一行文案,加载完成为空才展示空态 */}
                  {loading ? (
                    <div className="text-muted-foreground py-8 text-center">{t("delivery.loading")}</div>
                  ) : (
                    <EmptyState illustration="chat" title={t("delivery.empty")} />
                  )}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                    {formatTime(d.created_at)}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      {/* 任务级事件没有严重等级（severity 为空），跳过级别徽标，
                          否则会渲染出一个空标签。 */}
                      {d.severity && <StatusBadge domain="severity" value={d.severity} />}
                      <span className="truncate text-sm">{d.title || t("delivery.noTitle")}</span>
                      {EVENT_KIND_LABEL_KEYS[d.event_kind] && (
                        <Badge variant="outline" className="shrink-0">
                          {t(EVENT_KIND_LABEL_KEYS[d.event_kind])}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="text-sm">{d.channel_name}</TableCell>
                  <TableCell>
                    <StatusBadge domain="delivery" value={d.state} />
                  </TableCell>
                  <TableCell className="text-muted-foreground text-sm">{d.attempts}</TableCell>
                  <TableCell className="text-muted-foreground max-w-md text-xs break-all">{d.last_error}</TableCell>
                  <TableCell>
                    {/* 只有失败/跳过的才给重发入口：已送达的重发会造成重复推送。 */}
                    {(d.state === "failed" || d.state === "skipped") && (
                      <Button size="sm" variant="outline" onClick={() => retry(d.id)}>
                        <RotateCcwIcon /> {t("delivery.resend")}
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      {maxPage > 1 && (
        <div className="flex items-center justify-end gap-2">
          <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            {tp("prev")}
          </Button>
          <span className="text-muted-foreground text-sm">
            {page} / {maxPage}
          </span>
          <Button size="sm" variant="outline" disabled={page >= maxPage} onClick={() => setPage((p) => p + 1)}>
            {tp("next")}
          </Button>
        </div>
      )}
    </div>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", { hour12: false });
}
