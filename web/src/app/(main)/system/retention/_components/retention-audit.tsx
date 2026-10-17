"use client";

import { RefreshCwIcon, ScrollTextIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { parseRetentionPolicy, RETENTION_TABLES, type RetentionAuditEntry, type RetentionTable } from "@/lib/retention";

const actionNames = [
  "policy_saved",
  "preview_started",
  "preview_created",
  "execute_started",
  "execute_committed",
  "policy_read_failed",
  "policy_save_failed",
  "preview_failed",
  "execute_failed",
  "audit_read_failed",
] as const;
function knownAction(action: string): action is (typeof actionNames)[number] {
  return (actionNames as readonly string[]).includes(action);
}

// A whitelist summary, not a JSON dump: do not accidentally expose future secrets.
function detailParts(
  detail: unknown,
):
  | { kind: "counts"; rows: { table: RetentionTable; selected: number; deleted: number; skipped: number }[] }
  | { kind: "policy"; enabled: boolean; usage: number; logs: number; notifications: number; maximum: number }
  | { kind: "failed" | "unknown" } {
  if (Array.isArray(detail) && detail.length <= 4) {
    const rows: { table: RetentionTable; selected: number; deleted: number; skipped: number }[] = [];
    for (const raw of detail) {
      if (!raw || typeof raw !== "object") return { kind: "unknown" };
      const row = raw as Record<string, unknown>;
      if (
        !RETENTION_TABLES.some((table) => table === row.table) ||
        ![row.selected, row.deleted, row.skipped].every(
          (n) => typeof n === "number" && Number.isSafeInteger(n) && n >= 0 && n <= 500,
        )
      )
        return { kind: "unknown" };
      rows.push({
        table: row.table as RetentionTable,
        selected: row.selected as number,
        deleted: row.deleted as number,
        skipped: row.skipped as number,
      });
    }
    return { kind: "counts", rows };
  }
  if (detail && typeof detail === "object") {
    if ((detail as Record<string, unknown>).failed === true) return { kind: "failed" };
    try {
      const p = parseRetentionPolicy(detail);
      return {
        kind: "policy",
        enabled: p.enabled,
        usage: p.usage_days,
        logs: p.log_days,
        notifications: p.notification_days,
        maximum: p.max_rows_per_table,
      };
    } catch {
      /* Unknown detail is not a reason to invent a successful result. */
    }
  }
  return { kind: "unknown" };
}

export function RetentionAudit({
  entries,
  busy,
  loaded,
  error,
  disabled,
  refresh,
}: {
  entries: RetentionAuditEntry[];
  busy: boolean;
  loaded: boolean;
  error: string | null;
  disabled: boolean;
  refresh: () => void;
}) {
  const t = useTranslations("retentionPage");
  const detailText = (entry: RetentionAuditEntry) => {
    const detail = detailParts(entry.detail);
    if (detail.kind === "counts")
      return detail.rows
        .map((row) =>
          t("audit.countDetail", {
            table: t(`table.${row.table}`),
            selected: row.selected,
            deleted: row.deleted,
            skipped: row.skipped,
          }),
        )
        .join("；");
    if (detail.kind === "policy")
      return t("audit.policyDetail", {
        enabled: t(detail.enabled ? "audit.on" : "audit.off"),
        usage: detail.usage,
        logs: detail.logs,
        notifications: detail.notifications,
        maximum: detail.maximum,
      });
    return t(detail.kind === "failed" ? "audit.failedDetail" : "audit.unknownDetail");
  };
  let emptyMessage: string | null = null;
  if (!busy && !error) emptyMessage = loaded ? t("audit.empty") : t("audit.notLoaded");
  return (
    <Card>
      <CardHeader className="flex flex-wrap items-start justify-between gap-3 sm:flex-row">
        <div className="space-y-1.5">
          <CardTitle className="flex items-center gap-2">
            <ScrollTextIcon className="size-4" />
            {t("audit.title")}
          </CardTitle>
          <CardDescription>{t("audit.description")}</CardDescription>
        </div>
        <Button variant="outline" size="sm" disabled={disabled || busy} onClick={refresh}>
          <RefreshCwIcon className="size-4" />
          {t("audit.refresh")}
        </Button>
      </CardHeader>
      <CardContent className="space-y-3" aria-busy={busy}>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {t("audit.loadFailed", { error })}
          </p>
        )}
        {busy && (
          <p role="status" className="text-muted-foreground text-sm">
            {t("audit.loading")}
          </p>
        )}
        {loaded && entries.length > 0 && (
          <Table>
            <TableCaption>{t("audit.caption")}</TableCaption>
            <TableHeader>
              <TableRow>
                <TableHead>{t("audit.at")}</TableHead>
                <TableHead>{t("audit.action")}</TableHead>
                <TableHead>{t("audit.detail")}</TableHead>
                <TableHead>{t("audit.plan")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map((entry) => (
                <TableRow key={entry.id}>
                  <TableCell className="align-top">
                    <time dateTime={entry.at} title={entry.at} className="whitespace-nowrap text-xs">
                      {new Date(entry.at).toLocaleString("zh-CN", { hour12: false })}
                    </time>
                    <div className="text-muted-foreground text-xs">
                      #{entry.id} · {entry.actor}
                    </div>
                  </TableCell>
                  <TableCell className="align-top text-xs">
                    {knownAction(entry.action) ? t(`action.${entry.action}`) : t("audit.otherAction")}
                    <code className="mt-1 block text-muted-foreground">{entry.action}</code>
                  </TableCell>
                  <TableCell className="min-w-48 max-w-lg whitespace-normal align-top text-xs leading-relaxed">
                    {detailText(entry)}
                  </TableCell>
                  <TableCell className="align-top">
                    <code title={entry.plan_hash || undefined} className="text-xs">
                      {entry.plan_hash ? `${entry.plan_hash.slice(0, 12)}…` : "—"}
                    </code>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {(!loaded || entries.length === 0) && emptyMessage && (
          <p className="text-muted-foreground text-sm">{emptyMessage}</p>
        )}
        <p className="text-muted-foreground text-xs">{t("audit.actorLimit")}</p>
      </CardContent>
    </Card>
  );
}
