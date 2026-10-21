"use client";

import * as React from "react";

import Link from "next/link";

import { ShieldCheckIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { EmptyState } from "@/components/empty-state";
import { FindingVerifyDialog } from "@/components/finding-verify-dialog";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { FindingVerification } from "@/lib/types";

function active(v: FindingVerification) {
  return v.status === "pending" || v.status === "running";
}

// FindingVerifyPanel 展示一条发现的所有独立复核记录，并提供「发起复核」入口。
//
// 与复测面板的差别只有一处语义：复核结论会改 findings.validation，而 validation
// 决定这条发现进不进报告正文，所以这里每次有记录从「进行中」变成终态都要回调
// onCompleted 让上层重新拉取详情（徽标要跟着变）。
export function FindingVerifyPanel({
  findingId,
  findingName,
  readOnly = false,
  onCompleted,
}: {
  findingId: string;
  findingName?: string;
  readOnly?: boolean;
  onCompleted?: () => void;
}) {
  const t = useTranslations("verify.panel");
  const [items, setItems] = React.useState<FindingVerification[] | null>(null);
  const [error, setError] = React.useState("");
  const [open, setOpen] = React.useState(false);
  const requestSeq = React.useRef(0);
  const previousItems = React.useRef<FindingVerification[]>([]);

  const load = React.useCallback(async () => {
    const seq = ++requestSeq.current;
    try {
      const rows = await api.findingVerifications(findingId);
      if (seq !== requestSeq.current) return;
      const completed = previousItems.current.some(
        (item) => active(item) && rows.some((row) => row.id === item.id && !active(row)),
      );
      previousItems.current = rows;
      setItems(rows);
      setError("");
      if (completed) onCompleted?.();
    } catch (e) {
      if (seq === requestSeq.current) setError((e as Error).message);
    }
  }, [findingId, onCompleted]);

  React.useEffect(() => {
    void load();
    return () => {
      requestSeq.current++;
    };
  }, [load]);

  React.useEffect(() => {
    if (error || !items?.some(active)) return;
    const timer = setTimeout(() => void load(), 3000);
    return () => clearTimeout(timer);
  }, [error, items, load]);

  const running = items?.find(active);

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1.5">
          <CardTitle>{t("title")}</CardTitle>
          <CardDescription>{t("desc")}</CardDescription>
        </div>
        {running?.conversation_id != null ? (
          <Button asChild variant="outline" size="sm">
            <Link href={`/chat?c=${running.conversation_id}`} title={t("viewRunning")}>
              <Spinner data-icon="inline-start" aria-hidden="true" />
              {t("status.running")}
            </Link>
          </Button>
        ) : null}
        {!running && !readOnly ? (
          <Button size="sm" onClick={() => setOpen(true)} disabled={items === null || !!error}>
            <ShieldCheckIcon data-icon="inline-start" />
            {t("start")}
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {error ? (
          <Alert variant="destructive">
            <AlertDescription>
              {t("loadError", { error })}
              <Button variant="outline" size="sm" onClick={() => void load()}>
                {t("retry")}
              </Button>
            </AlertDescription>
          </Alert>
        ) : null}
        {!error && items === null ? <Skeleton className="h-16 w-full" /> : null}
        {!error && items?.length === 0 ? (
          <EmptyState compact illustration="flag" title={t("emptyTitle")} description={t("emptyDesc")} />
        ) : null}
        {!error && items
          ? items.map((item) => (
              <div key={item.id} className="flex min-w-0 flex-col gap-2 rounded-lg border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  {item.status === "completed" && item.verdict ? (
                    <StatusBadge
                      domain="validation"
                      value={item.verdict === "inconclusive" ? "unverified" : item.verdict}
                      dot
                    />
                  ) : null}
                  <Badge variant="secondary">{t(`status.${item.status}`)}</Badge>
                  {item.status === "completed" && item.verdict ? (
                    <span className="text-xs font-medium">{t(`verdict.${item.verdict}`)}</span>
                  ) : null}
                  <span className="text-muted-foreground text-xs">
                    #{item.id} · {new Date(item.created_at).toLocaleString()}
                  </span>
                  {item.reason ? (
                    <span className="text-muted-foreground text-xs">· {t(`reason.${reasonKey(item.reason)}`)}</span>
                  ) : null}
                  {item.conversation_id != null ? (
                    <Button asChild variant="ghost" size="sm" className="ml-auto">
                      <Link href={`/chat?c=${item.conversation_id}`}>{t("viewSession")}</Link>
                    </Button>
                  ) : (
                    <span className="text-muted-foreground text-xs">{t("sessionDeleted")}</span>
                  )}
                </div>
                {item.status === "completed" && item.summary ? (
                  <p className="whitespace-pre-wrap break-words text-sm">{item.summary}</p>
                ) : null}
                {item.error ? (
                  <p className="whitespace-pre-wrap break-words text-destructive text-sm">{item.error}</p>
                ) : null}
                {item.notes ? (
                  <p className="whitespace-pre-wrap break-words text-muted-foreground text-xs">
                    {t("notes", { notes: item.notes })}
                  </p>
                ) : null}
                {item.status === "completed" && item.evidence ? (
                  <details className="min-w-0">
                    <summary className="cursor-pointer text-sm">{t("evidence")}</summary>
                    <div className="mt-3 overflow-x-auto">
                      <Markdown text={item.evidence} />
                    </div>
                  </details>
                ) : null}
              </div>
            ))
          : null}
      </CardContent>
      {open ? (
        <FindingVerifyDialog
          findingId={findingId}
          findingName={findingName}
          onClose={() => setOpen(false)}
          onStarted={(verification) => {
            requestSeq.current++;
            const rows = [verification, ...previousItems.current.filter((item) => item.id !== verification.id)];
            previousItems.current = rows;
            setItems(rows);
            setError("");
            void load();
          }}
        />
      ) : null}
    </Card>
  );
}

// reasonKey 把后端写的触发原因收敛成有限的几个 i18n 键：后端可能带参数
// （auto:severity=high），词库不跟着枚举取值走。
function reasonKey(reason: string): "severity" | "sample" | "manual" {
  if (reason.startsWith("auto:severity")) return "severity";
  if (reason.startsWith("auto:sample")) return "sample";
  return "manual";
}
