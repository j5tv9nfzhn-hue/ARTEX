"use client";

import * as React from "react";

import Link from "next/link";

import { AlertTriangleIcon, DatabaseIcon, RefreshCwIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { type CompactResult, type GcResult, type StorageStats, storageApi } from "@/lib/storage-api";
import type { WorkspaceListing } from "@/lib/types";

// 系统 → 存储与清理。
//
// 三块内容对应三个真实存在的问题，彼此不重叠：
//   数据库：库文件和空闲页（VACUUM 是唯一能立刻把文件缩回去的动作）；
//   内存：Go 运行时 + 进程内缓存（只能观察 + 主动归还，不存在「清空缓存」魔法）；
//   记忆库：探索图与活动流的体积（可清理的历史垃圾），漏洞/资产不在这里删。
//
// 删除动作一律人工触发（预览 → 确认）。唯一的例外是「活动流自动保留」：默认关闭，
// 用户显式设了天数后由后台按批兜底清理（server/activity_retention.go）。

const TABLE_PREVIEW = 15;

type ConfirmState = {
  title: string;
  body: string;
  danger?: boolean;
  onConfirm: () => void;
};

function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  let digits = 2;
  if (v >= 100) digits = 0;
  else if (v >= 10) digits = 1;
  return `${v.toFixed(digits)} ${units[i]}`;
}

function fmtNum(n: number): string {
  return Number.isFinite(n) ? n.toLocaleString("zh-CN") : "-";
}

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b py-1.5 last:border-b-0">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="font-medium text-sm tabular-nums">{value}</span>
    </div>
  );
}

export default function StoragePage() {
  const t = useTranslations("storagePage");
  const [stats, setStats] = React.useState<StorageStats | null>(null);
  const [ws, setWs] = React.useState<WorkspaceListing | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [notice, setNotice] = React.useState<string | null>(null);
  const [confirm, setConfirm] = React.useState<ConfirmState | null>(null);
  const [showAll, setShowAll] = React.useState(false);
  const [days, setDays] = React.useState(30);
  // null = 跟着服务端值走；用户一改就变成草稿，保存成功后回到 null。
  const [keepDays, setKeepDays] = React.useState<number | null>(null);

  const load = React.useCallback(async () => {
    setError(null);
    const [nextStats, nextWs] = await Promise.allSettled([storageApi.stats(), api.workspaceList("")]);
    if (nextStats.status === "fulfilled") setStats(nextStats.value);
    else setError(nextStats.reason instanceof Error ? nextStats.reason.message : "读取失败");
    setWs(nextWs.status === "fulfilled" ? nextWs.value : null);
    setLoading(false);
    // 依赖里刻意不放 t：useTranslations 每次渲染都可能返回新函数，
    // 放进 useCallback 会让 load 反复重建 → effect 无限重取。
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  const run = React.useCallback(
    async (key: string, fn: () => Promise<string | null>) => {
      setBusy(key);
      setError(null);
      setNotice(null);
      try {
        const msg = await fn();
        await load();
        if (msg) setNotice(msg);
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(null);
      }
    },
    [load],
  );

  const db = stats?.db;
  const mem = stats?.mem;
  const mdb = stats?.memory_db;
  const wq = stats?.memory_db?.write_queue;
  const keep = stats?.activity_retention;
  const savedKeepDays = keep?.days ?? 0;
  const effectiveKeepDays = keepDays ?? savedKeepDays;
  const freeRatio = db && db.page_count > 0 ? db.freelist / db.page_count : 0;
  const tables = db?.tables ?? [];
  const visibleTables = showAll ? tables : tables.slice(0, TABLE_PREVIEW);
  const topDirs = [...(ws?.entries ?? [])]
    .filter((e) => e.dir)
    .sort((a, b) => b.size - a.size)
    .slice(0, 6);
  const wsTotal = ws?.total_size ?? 0;
  const wsFiles = ws?.total_files ?? 0;

  const tablesBody = (() => {
    if (loading) return <p className="text-muted-foreground text-sm">{t("loading")}</p>;
    if (tables.length === 0) return <p className="text-muted-foreground text-sm">{t("tablesEmpty")}</p>;
    return (
      <>
        <div className="grid gap-x-6 sm:grid-cols-2 lg:grid-cols-3">
          {visibleTables.map((row) => (
            <Stat key={row.name} label={row.name} value={fmtNum(row.rows)} />
          ))}
        </div>
        {tables.length > TABLE_PREVIEW ? (
          <Button variant="ghost" size="sm" className="mt-3" onClick={() => setShowAll((v) => !v)}>
            {showAll ? t("showLess") : t("showAll", { count: tables.length })}
          </Button>
        ) : null}
      </>
    );
  })();

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="font-semibold text-xl tracking-tight">{t("title")}</h1>
            <Badge variant="secondary">{t("badgeManual")}</Badge>
          </div>
          <p className="mt-1 max-w-3xl text-muted-foreground text-sm">{t("subtitle")}</p>
        </div>
        <Button variant="outline" disabled={loading || busy !== null} onClick={() => void load()}>
          <RefreshCwIcon className="size-4" />
          {t("refresh")}
        </Button>
      </div>

      <Alert>
        <ShieldCheckIcon />
        <AlertTitle>{t("boundaryTitle")}</AlertTitle>
        <AlertDescription>
          <p>{t("boundary")}</p>
          <p>{t("boundary2")}</p>
        </AlertDescription>
      </Alert>

      {error ? (
        <Alert variant="destructive">
          <AlertTriangleIcon />
          <AlertTitle>{t("errorTitle")}</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      ) : null}

      {notice ? (
        <Alert>
          <DatabaseIcon />
          <AlertDescription>{notice}</AlertDescription>
        </Alert>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-3">
        {/* 数据库 */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <DatabaseIcon className="size-4" />
              {t("dbTitle")}
            </CardTitle>
            <CardDescription>{t("dbDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {loading || !db ? (
              <p className="text-muted-foreground text-sm">{t("loading")}</p>
            ) : (
              <>
                <div>
                  <Stat label={t("dbSize")} value={fmtBytes(db.size)} />
                  <Stat label={t("dbWal")} value={fmtBytes(db.wal_size)} />
                  <Stat label={t("dbShm")} value={fmtBytes(db.shm_size)} />
                  <Stat label={t("dbPages")} value={fmtNum(db.page_count)} />
                  <Stat label={t("dbFreePages")} value={fmtNum(db.freelist)} />
                  <Stat label={t("dbFreeRatio")} value={`${(freeRatio * 100).toFixed(1)}%`} />
                  <Stat label={t("dbAutoVacuum")} value={db.auto_vacuum} />
                  <Stat label={t("dbJournal")} value={db.journal_mode || "-"} />
                </div>
                <p className="break-all text-[11px] text-muted-foreground">{db.path}</p>
                <Button
                  variant="outline"
                  className="w-full"
                  disabled={busy !== null}
                  onClick={() =>
                    setConfirm({
                      title: t("compactConfirmTitle"),
                      body: t("compactConfirmDesc"),
                      onConfirm: () =>
                        void run("compact", async () => {
                          const r: CompactResult = await storageApi.compact();
                          return t("dbCompactDone", {
                            before: fmtBytes(r.before),
                            after: fmtBytes(r.after),
                            freed: fmtBytes(r.freed),
                            ms: r.duration_ms,
                          });
                        }),
                    })
                  }
                >
                  <Trash2Icon className="size-4" />
                  {busy === "compact" ? t("running") : t("dbCompact")}
                </Button>
                <p className="text-[11px] text-muted-foreground">{t("dbCompactHint")}</p>
              </>
            )}
          </CardContent>
        </Card>

        {/* 内存 */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("memTitle")}</CardTitle>
            <CardDescription>{t("memDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {loading || !mem ? (
              <p className="text-muted-foreground text-sm">{t("loading")}</p>
            ) : (
              <>
                <div>
                  <Stat label={t("heapAlloc")} value={fmtBytes(mem.heap_alloc)} />
                  <Stat label={t("heapSys")} value={fmtBytes(mem.heap_sys)} />
                  <Stat label={t("stackInuse")} value={fmtBytes(mem.stack_inuse)} />
                  <Stat label={t("sysTotal")} value={fmtBytes(mem.sys)} />
                  <Stat label={t("gcCount")} value={fmtNum(mem.num_gc)} />
                  <Stat label={t("goroutines")} value={fmtNum(mem.goroutines)} />
                  <Stat
                    label={t("cacheNotify")}
                    value={`${fmtNum(mem.notify_ring)} / ${fmtNum(mem.notify_ring_cap)}`}
                  />
                  <Stat label={t("cacheTasks")} value={fmtNum(mem.task_handles)} />
                  <Stat label={t("cacheTransports")} value={fmtNum(mem.http_transports)} />
                </div>
                <Button
                  variant="outline"
                  className="w-full"
                  disabled={busy !== null}
                  onClick={() =>
                    void run("gc", async () => {
                      const r: GcResult = await storageApi.gc();
                      return t("gcDone", { before: fmtBytes(r.heap_before), after: fmtBytes(r.heap_after) });
                    })
                  }
                >
                  {busy === "gc" ? t("running") : t("gcNow")}
                </Button>
                <Button
                  variant="ghost"
                  className="w-full"
                  disabled={busy !== null}
                  onClick={() =>
                    setConfirm({
                      title: t("notifyConfirmTitle"),
                      body: t("notifyConfirmDesc"),
                      onConfirm: () =>
                        void run("notify", async () => {
                          const r = await storageApi.clearNotify();
                          return t("notifyDone", { count: r.cleared });
                        }),
                    })
                  }
                >
                  {busy === "notify" ? t("running") : t("clearNotify")}
                </Button>
              </>
            )}
          </CardContent>
        </Card>

        {/* 记忆库 */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("mdbTitle")}</CardTitle>
            <CardDescription>{t("mdbDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {loading || !mdb ? (
              <p className="text-muted-foreground text-sm">{t("loading")}</p>
            ) : (
              <>
                <div>
                  <Stat label={t("mdbFacts")} value={fmtNum(mdb.facts)} />
                  <Stat label={t("mdbEdges")} value={fmtNum(mdb.edges)} />
                  <Stat label={t("mdbActivity")} value={fmtNum(mdb.activity)} />
                  <Stat label={t("mdbFindings")} value={fmtNum(mdb.findings)} />
                  <Stat label={t("mdbAssets")} value={fmtNum(mdb.assets)} />
                  <Stat label={t("mdbLinks")} value={fmtNum(mdb.links)} />
                  <Stat
                    label={t("orphanLabel")}
                    value={`${fmtNum(mdb.orphan_facts)} / ${fmtNum(mdb.orphan_edges)} / ${fmtNum(mdb.orphan_activity)}`}
                  />
                </div>
                <Button
                  variant="outline"
                  className="w-full"
                  disabled={busy !== null}
                  onClick={() =>
                    void run("orphanPreview", async () => {
                      const p = await storageApi.orphanPreview();
                      setConfirm({
                        title: t("orphanConfirmTitle"),
                        body: t("orphanConfirmDesc", {
                          facts: fmtNum(p.counts.facts),
                          edges: fmtNum(p.counts.edges),
                          activity: fmtNum(p.counts.activity),
                        }),
                        danger: true,
                        onConfirm: () =>
                          void run("orphan", async () => {
                            const r = await storageApi.orphanPurge();
                            const rows = (r.rows ?? { facts: 0, edges: 0, activity: 0 }) as {
                              facts: number;
                              edges: number;
                              activity: number;
                            };
                            return t("orphanDone", {
                              facts: fmtNum(rows.facts),
                              edges: fmtNum(rows.edges),
                              activity: fmtNum(rows.activity),
                              freed: fmtBytes(r.freed ?? 0),
                            });
                          }),
                      });
                      return null;
                    })
                  }
                >
                  <Trash2Icon className="size-4" />
                  {busy === "orphanPreview" || busy === "orphan" ? t("running") : t("purgeOrphans")}
                </Button>
                <div className="flex items-end gap-2">
                  <div className="flex-1">
                    <Label htmlFor="activity-days" className="text-xs">
                      {t("activityDaysLabel")}
                    </Label>
                    <Input
                      id="activity-days"
                      type="number"
                      min={1}
                      max={3650}
                      value={days}
                      onChange={(e) => setDays(Math.max(1, Math.min(3650, Number(e.target.value) || 30)))}
                    />
                  </div>
                  <Button
                    variant="outline"
                    disabled={busy !== null}
                    onClick={() =>
                      void run("activityPreview", async () => {
                        const p = await storageApi.activityPreview(days);
                        setConfirm({
                          title: t("activityConfirmTitle"),
                          body: t("activityConfirmDesc", { days, rows: fmtNum(p.rows) }),
                          danger: true,
                          onConfirm: () =>
                            void run("activity", async () => {
                              const r = await storageApi.activityPurge(days);
                              const rows = typeof r.rows === "number" ? r.rows : 0;
                              return t("activityDone", { rows: fmtNum(rows), freed: fmtBytes(r.freed ?? 0) });
                            }),
                        });
                        return null;
                      })
                    }
                  >
                    {busy === "activityPreview" || busy === "activity" ? t("running") : t("purgeActivity")}
                  </Button>
                </div>
                <p className="text-[11px] text-muted-foreground">{t("mdbNote")}</p>
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* 写入管道 + 活动自动保留 */}
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("wqTitle")}</CardTitle>
            <CardDescription>{t("wqDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {loading || !wq ? (
              <p className="text-muted-foreground text-sm">{t("loading")}</p>
            ) : (
              <>
                <div className="grid gap-x-6 sm:grid-cols-2">
                  <Stat label={t("wqBatches")} value={fmtNum(wq.batches)} />
                  <Stat label={t("wqJobs")} value={fmtNum(wq.jobs)} />
                  <Stat label={t("wqAvgBatch")} value={(wq.avg_batch ?? 0).toFixed(2)} />
                  <Stat label={t("wqLastBatch")} value={fmtNum(wq.last_batch)} />
                  <Stat label={t("wqMaxBatch")} value={fmtNum(wq.max_batch)} />
                  <Stat label={t("wqWait")} value={`${(wq.avg_wait_ms ?? 0).toFixed(2)} ms`} />
                  <Stat label={t("wqRollbacks")} value={fmtNum(wq.rollbacks)} />
                  <Stat label={t("wqDepth")} value={fmtNum(wq.queue_depth)} />
                </div>
                <p className="text-[11px] text-muted-foreground">{t("wqNote")}</p>
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("retentionTitle")}</CardTitle>
            <CardDescription>{t("retentionDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex items-end gap-2">
              <div className="flex-1">
                <Label htmlFor="activity-retention-days" className="text-xs">
                  {t("retentionDaysLabel")}
                </Label>
                <Input
                  id="activity-retention-days"
                  type="number"
                  min={0}
                  max={3650}
                  value={effectiveKeepDays}
                  onChange={(e) => setKeepDays(Math.max(0, Math.min(3650, Number(e.target.value) || 0)))}
                />
              </div>
              <Button
                variant="outline"
                disabled={busy !== null || keepDays === null || keepDays === savedKeepDays}
                onClick={() =>
                  void run("retention", async () => {
                    const days = effectiveKeepDays;
                    await storageApi.activityRetention(days);
                    setKeepDays(null);
                    return t("retentionSaved", { days });
                  })
                }
              >
                {busy === "retention" ? t("running") : t("retentionSave")}
              </Button>
            </div>
            <div>
              <Stat
                label={t("retentionLast")}
                value={
                  keep?.last_run_unix_ms
                    ? new Date(keep.last_run_unix_ms).toLocaleString("zh-CN")
                    : t("retentionNever")
                }
              />
              <Stat label={t("retentionLastRows")} value={fmtNum(keep?.last_deleted ?? 0)} />
            </div>
            <p className="text-[11px] text-muted-foreground">{t("retentionNote")}</p>
          </CardContent>
        </Card>
      </div>

      {/* 表行数 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("tablesTitle")}</CardTitle>
          <CardDescription>
            {t("tablesDesc")}
            {db?.tables_partial ? ` · ${t("tablesPartial")}` : ""}
          </CardDescription>
        </CardHeader>
        <CardContent>{tablesBody}</CardContent>
      </Card>

      {/* 文件 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("filesTitle")}</CardTitle>
          <CardDescription>{t("filesDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {!ws ? (
            <p className="text-muted-foreground text-sm">{t("filesEmpty")}</p>
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-3 text-sm">
                <span className="font-medium">{t("filesTotal", { size: fmtBytes(wsTotal) })}</span>
                <span className="text-muted-foreground">{t("filesCount", { count: wsFiles })}</span>
                <Button asChild variant="link" size="sm" className="px-0">
                  <Link href="/function/workspace">{t("filesOpen")}</Link>
                </Button>
              </div>
              <div className="grid gap-x-6 sm:grid-cols-2 lg:grid-cols-3">
                {topDirs.map((e) => (
                  <Stat key={e.path} label={e.name} value={fmtBytes(e.size)} />
                ))}
              </div>
            </>
          )}
        </CardContent>
      </Card>

      <AlertDialog open={confirm !== null} onOpenChange={(open) => !open && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirm?.body}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="outline" onClick={() => setConfirm(null)}>
              {t("cancel")}
            </Button>
            <Button
              variant={confirm?.danger ? "destructive" : "default"}
              onClick={() => {
                const action = confirm?.onConfirm;
                setConfirm(null);
                action?.();
              }}
            >
              {t("confirm")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
