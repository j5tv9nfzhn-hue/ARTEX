"use client";

import * as React from "react";

import { RefreshCwIcon, ShieldCheckIcon, WrenchIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { HarnessChange, HarnessClientStatus, HarnessScan } from "@/lib/types";

// 「已检测到的客户端」：把 MCPExposureCard 从「给你一段配置自己贴」升级成
// 「扫描 → 预览 diff → 一键写入」。
//
// 三条交互原则：
//  1. 扫描是只读的，自动触发一次；重扫要用户点。
//  2. 写入前必定给 diff——用户看不到改了什么就不点确认。
//  3. 写不了的客户端（C 级 / 只有 UI / 项目级但没给目录）仍然列出来，
//     标清原因并给片段，而不是从列表里消失。

function stateVariant(state: string): "default" | "secondary" | "outline" | "destructive" {
  switch (state) {
    case "linked":
      return "default";
    case "configured":
      return "secondary";
    case "installed":
      return "outline";
    default:
      return "outline";
  }
}

function actionVariant(action: string): "default" | "secondary" | "outline" | "destructive" {
  switch (action) {
    case "create":
    case "update":
      return "default";
    case "manual":
      return "secondary";
    case "skip":
      return "destructive";
    default:
      return "outline";
  }
}

export function HarnessTargetsCard() {
  const t = useTranslations("externalPage");
  const [scan, setScan] = React.useState<HarnessScan | null>(null);
  const [projectDir, setProjectDir] = React.useState("");
  const [selected, setSelected] = React.useState<Record<string, boolean>>({});
  const [changes, setChanges] = React.useState<HarnessChange[] | null>(null);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback((dir: string) => {
    api
      .harnessScan(dir)
      .then(setScan)
      .catch((e) => toast.error(String(e)));
  }, []);

  React.useEffect(() => {
    load("");
  }, [load]);

  const clients = scan?.clients ?? [];
  const targetIds = React.useMemo(() => Object.keys(selected).filter((id) => selected[id]), [selected]);

  // 只有「有可写落点」的客户端才配复选框；其余行只展示。
  const selectable = React.useCallback((c: HarnessClientStatus) => {
    if (c.tier === "C" || c.artex === "manual") return false;
    return c.configs.some((cfg) => cfg.strategy !== "manual" && cfg.strategy !== "cli-only");
  }, []);

  const toggle = (id: string, on: boolean) => {
    setSelected((cur) => {
      const next = { ...cur };
      if (on) next[id] = true;
      else delete next[id];
      return next;
    });
  };

  const selectAll = () => {
    const next: Record<string, boolean> = {};
    for (const c of clients) {
      if (selectable(c)) next[c.id] = true;
    }
    setSelected(next);
  };

  const preview = async () => {
    if (targetIds.length === 0) {
      toast.error(t("harnessNothingSelected"));
      return;
    }
    setBusy(true);
    try {
      const res = await api.harnessPlan(targetIds, projectDir);
      setChanges(res.changes);
    } catch (e) {
      toast.error(String(e));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setBusy(true);
    try {
      const res = await api.harnessApply(targetIds, projectDir);
      const ok = res.summary.applied ?? 0;
      const failed = res.summary.failed ?? 0;
      if (failed > 0) toast.error(t("harnessFailed", { n: failed }));
      if (ok > 0) toast.success(t("harnessApplied", { n: ok }));
      if (ok === 0 && failed === 0) toast.message(t("harnessPreviewEmpty"));
      setChanges(null);
      load(projectDir);
    } catch (e) {
      toast.error(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="gap-3">
      <CardHeader className="pb-1">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2">
            <WrenchIcon className="size-4 shrink-0 text-primary/80" />
            <CardTitle className="text-base">{t("harnessTitle")}</CardTitle>
            {scan ? (
              <Badge variant="secondary">
                {scan.stats.linked}/{scan.stats.total}
              </Badge>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" onClick={selectAll} disabled={busy || clients.length === 0}>
              {t("harnessSelectAll")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => load(projectDir)}
              disabled={busy}
              aria-label={t("harnessRefresh")}
            >
              <RefreshCwIcon className="size-4" />
            </Button>
          </div>
        </div>
        <p className="text-muted-foreground text-sm">{t("harnessSubtitle")}</p>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={projectDir}
            onChange={(e) => setProjectDir(e.target.value)}
            placeholder={t("harnessProjectDir")}
            className="h-8 w-full max-w-md font-mono text-xs"
          />
          <span className="text-muted-foreground text-xs">{t("harnessProjectDirHint")}</span>
        </div>

        <div className="max-h-96 divide-y divide-border/60 overflow-y-auto rounded-lg border border-border/70">
          {clients.length === 0 ? (
            <div className="p-4 text-muted-foreground text-sm">{t("harnessScanEmpty")}</div>
          ) : (
            clients.map((c) => {
              const canPick = selectable(c);
              return (
                <div key={c.id} className="flex items-start gap-3 px-3 py-2.5">
                  <Checkbox
                    className="mt-0.5"
                    checked={Boolean(selected[c.id])}
                    disabled={!canPick}
                    onCheckedChange={(v) => toggle(c.id, v === true)}
                    aria-label={c.name}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="font-medium text-sm">{c.name}</span>
                      <span className="text-muted-foreground text-xs">{c.vendor}</span>
                      <Badge variant={stateVariant(c.state)}>{t(`harnessState_${c.state}`)}</Badge>
                      <Badge variant="outline" className="text-[10px]">
                        {c.tier}
                      </Badge>
                      {c.needs_verify ? (
                        <Badge variant="outline" className="text-[10px]">
                          {t("harnessTierB")}
                        </Badge>
                      ) : null}
                      {c.stale ? <Badge variant="destructive">{t("harnessStale")}</Badge> : null}
                    </div>
                    <div className="mt-1 space-y-0.5 text-muted-foreground">
                      {c.configs.slice(0, 2).map((cfg) => (
                        <div key={cfg.display + cfg.scope} className="truncate font-mono text-[11px]">
                          {cfg.exists ? "✓" : "·"} {cfg.display}
                        </div>
                      ))}
                      {c.configs.length > 2 ? (
                        <div className="font-mono text-[11px]">+{c.configs.length - 2}</div>
                      ) : null}
                    </div>
                  </div>
                  {!canPick ? (
                    <span className="shrink-0 text-muted-foreground text-xs">{t("harnessManual")}</span>
                  ) : null}
                </div>
              );
            })
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={preview} disabled={busy || targetIds.length === 0}>
            {t("harnessPreview")}
          </Button>
          <span className="text-muted-foreground text-xs">{t("harnessBackup")}</span>
        </div>
      </CardContent>

      <Dialog open={changes !== null} onOpenChange={(open) => (open ? null : setChanges(null))}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("harnessPreviewTitle")}</DialogTitle>
            <DialogDescription>{t("harnessSubtitle")}</DialogDescription>
          </DialogHeader>

          <div className="max-h-[60vh] space-y-3 overflow-y-auto">
            {(changes ?? []).map((ch) => (
              <div
                key={`${ch.client_id}-${ch.scope}-${ch.path}-${ch.action}`}
                className="rounded-lg border border-border/70 p-3"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-sm">{ch.client_name || ch.client_id}</span>
                  <Badge variant={actionVariant(ch.action)}>{t(`harnessChange_${ch.action}`)}</Badge>
                  <span className="truncate font-mono text-[11px] text-muted-foreground">
                    {ch.path || ch.display_path}
                  </span>
                </div>
                {ch.reason ? <p className="mt-1 text-muted-foreground text-xs">{ch.reason}</p> : null}
                {ch.diff ? (
                  <pre className="mt-2 max-h-56 overflow-auto rounded bg-muted p-2 font-mono text-[11px] leading-relaxed">
                    {ch.diff}
                  </pre>
                ) : null}
                {ch.snippet ? (
                  <pre className="mt-2 max-h-56 overflow-auto rounded bg-muted p-2 font-mono text-[11px] leading-relaxed">
                    {ch.snippet}
                  </pre>
                ) : null}
              </div>
            ))}
            {changes !== null && changes.length === 0 ? (
              <p className="text-muted-foreground text-sm">{t("harnessPreviewEmpty")}</p>
            ) : null}
          </div>

          <DialogFooter>
            <Button variant="ghost" onClick={() => setChanges(null)} disabled={busy}>
              {t("harnessCancel")}
            </Button>
            <Button onClick={apply} disabled={busy}>
              <ShieldCheckIcon className="size-4" />
              {t("harnessConfirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
