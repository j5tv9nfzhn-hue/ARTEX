"use client";

import * as React from "react";
import { useTranslations } from "next-intl";

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { IntentHandoffView } from "@/lib/types";

// 交接单查看器（P3）：把一条分支「上次到底跑到哪、写了什么、留下了什么判断」摊开。
//
// 展示顺序刻意是「事实 → 主观 → 审计 → 原始 JSON」：
// 先让人看到系统直读的可信部分，再看到模型自己的猜测（带可推翻标签），
// 最后才是审计与原始记录，避免把主观判断读成结论。
export function HandoffDialog({
  taskId,
  intentId,
  open,
  onOpenChange,
}: {
  taskId: string;
  intentId: number | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const t = useTranslations("taskDetail.overviewTab.handoff");
  const [data, setData] = React.useState<IntentHandoffView | null>(null);
  const [err, setErr] = React.useState("");
  const [loading, setLoading] = React.useState(false);

  React.useEffect(() => {
    if (!open || !intentId) return;
    let alive = true;
    setLoading(true);
    setErr("");
    setData(null);
    api
      .intentHandoff(taskId, String(intentId))
      .then((r) => {
        if (alive) setData(r);
      })
      .catch((e) => {
        if (alive) setErr((e as Error).message);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [open, taskId, intentId]);

  const h = data?.handoff ?? null;
  const rec = h?.record;
  const ow = rec?.open_work;
  const written = rec?.progress?.written ?? {};

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("title", { id: intentId ?? 0 })}</DialogTitle>
          <DialogDescription>{t("desc")}</DialogDescription>
        </DialogHeader>

        {loading && <p className="text-muted-foreground text-sm">{t("loading")}</p>}
        {err && <p className="text-red-500 text-sm">{err}</p>}

        {!loading && !err && !h && (
          <p className="text-muted-foreground text-sm">{t("empty")}</p>
        )}

        {h && (
          <div className="flex flex-col gap-4 text-sm">
            <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
              <span className="rounded border border-border/60 bg-muted/50 px-1.5 py-0.5 font-mono">
                {h.schema}
              </span>
              <span>run #{h.run_id}</span>
              <span>{new Date(h.created_at).toLocaleString()}</span>
              <span className="font-mono" title={t("mechTip")}>
                mech:{h.mech_sha256.slice(0, 12)}
              </span>
            </div>

            <section className="flex flex-col gap-1">
              <h4 className="font-medium">{t("facts")}</h4>
              <p className="text-muted-foreground text-xs">
                {t("written", {
                  facts: written.facts ?? 0,
                  assets: written.assets ?? 0,
                  findings: written.findings ?? 0,
                  steps: rec?.progress?.steps_total ?? 0,
                })}
              </p>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-xs">
                {h.summary}
              </pre>
            </section>

            <section className="flex flex-col gap-1">
              <h4 className="font-medium">
                {t("openWork")}
                <span className="ml-2 font-normal text-muted-foreground text-xs">{t("openWorkHint")}</span>
              </h4>
              {!ow || (!ow.next_actions?.length && !ow.hypotheses?.length && !ow.negative_results?.length) ? (
                <p className="text-muted-foreground text-xs">{t("openWorkEmpty")}</p>
              ) : (
                <div className="flex flex-col gap-2">
                  {!!ow.next_actions?.length && (
                    <ul className="list-disc pl-5">
                      {ow.next_actions.map((s, i) => (
                        <li key={`n-${i}`} className="text-xs">
                          {s}
                        </li>
                      ))}
                    </ul>
                  )}
                  {!!ow.hypotheses?.length && (
                    <ul className="list-disc pl-5">
                      {ow.hypotheses.map((s, i) => (
                        <li key={`h-${i}`} className="text-xs">
                          {s}
                        </li>
                      ))}
                    </ul>
                  )}
                  {!!ow.negative_results?.length && (
                    <ul className="list-disc pl-5">
                      {ow.negative_results.map((s, i) => (
                        <li key={`r-${i}`} className="text-xs text-muted-foreground">
                          {s}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
              {!!data?.open_work?.length && (
                <details className="mt-1">
                  <summary className="cursor-pointer text-muted-foreground text-xs">
                    {t("patchHistory", { n: data.open_work.length })}
                  </summary>
                  <ul className="mt-1 flex flex-col gap-1">
                    {data.open_work.map((w) => (
                      <li key={w.id} className="rounded border border-border/50 p-1.5 text-xs">
                        <span className="text-muted-foreground">
                          {w.author || "—"} · {new Date(w.created_at).toLocaleString()}
                        </span>
                        <div className="whitespace-pre-wrap">
                          {[w.next_actions, w.hypotheses, w.negative_results].filter(Boolean).join("\n")}
                        </div>
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </section>

            {!!data?.closures?.length && (
              <section className="flex flex-col gap-1">
                <h4 className="font-medium">{t("closures")}</h4>
                <ul className="flex flex-col gap-1">
                  {data.closures.map((c) => (
                    <li key={c.id} className="rounded border border-border/50 p-1.5 text-xs">
                      <span className="font-mono">{c.action}</span>
                      <span className="text-muted-foreground">
                        {" "}
                        · {c.actor} · {c.reason_code} · {new Date(c.created_at).toLocaleString()}
                      </span>
                      {c.note && <div className="whitespace-pre-wrap">{c.note}</div>}
                    </li>
                  ))}
                </ul>
              </section>
            )}

            <details>
              <summary className="cursor-pointer text-muted-foreground text-xs">{t("rawJson")}</summary>
              <pre className="mt-1 max-h-72 overflow-auto rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-[11px]">
                {JSON.stringify(rec, null, 2)}
              </pre>
            </details>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
