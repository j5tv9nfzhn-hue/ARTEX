"use client";

import { useTranslations } from "next-intl";

import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { RetentionCount } from "@/lib/retention";

export function RetentionCounts({ rows, result = false }: { rows: RetentionCount[]; result?: boolean }) {
  const t = useTranslations("retentionPage");
  return (
    <Table>
      <TableCaption>{t(result ? "counts.resultCaption" : "counts.previewCaption")}</TableCaption>
      <TableHeader>
        <TableRow>
          <TableHead>{t("counts.table")}</TableHead>
          <TableHead className="text-right">{t("counts.selected")}</TableHead>
          {result ? (
            <>
              <TableHead className="text-right">{t("counts.deleted")}</TableHead>
              <TableHead className="text-right">{t("counts.skipped")}</TableHead>
            </>
          ) : (
            <TableHead>{t("counts.more")}</TableHead>
          )}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => (
          <TableRow key={row.table}>
            <TableCell>
              <div>{t(`table.${row.table}`)}</div>
              <code className="text-muted-foreground text-xs">{row.table}</code>
            </TableCell>
            <TableCell className="text-right tabular-nums">{row.selected}</TableCell>
            {result ? (
              <>
                <TableCell className="text-right tabular-nums">{row.deleted}</TableCell>
                <TableCell className="text-right tabular-nums">{row.skipped}</TableCell>
              </>
            ) : (
              <TableCell className="text-muted-foreground text-xs">
                {t(row.more_candidates ? "counts.hasMore" : "counts.noMore")}
              </TableCell>
            )}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
