"use client";

import * as React from "react";

import { BarChart3Icon, ChevronLeftIcon, ChevronRightIcon, Loader2Icon, SearchIcon, TerminalIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { CommandRecord, ToolStat } from "@/lib/types";
import { cn } from "@/lib/utils";

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// toolInput renders a tool's raw input for display. Bash's {"command":"..."} is
// unwrapped to the bare command; other tools show their pretty-printed JSON args.
function toolInput(raw: string): string {
  try {
    const obj = JSON.parse(raw);
    if (obj && typeof obj.command === "string") return obj.command;
    return JSON.stringify(obj, null, 2);
  } catch {
    /* not JSON */
  }
  return raw;
}

// truncate clips a string to maxLen characters.
function truncate(s: string, maxLen: number): string {
  const first = s.split("\n")[0]; // single-line preview
  if (first.length <= maxLen) return first;
  return first.slice(0, maxLen) + "…";
}

// error_class → 文案键。后端只存低基数标签（config / dialect / ...），文案在前端。
const CLASS_LABEL_KEY: Record<string, string> = {
  config: "classConfig",
  dialect: "classDialect",
  not_found: "classNotFound",
  param: "classParam",
  perm: "classPerm",
  env: "classEnv",
  timeout: "classTimeout",
  transport: "classTransport",
  quota: "classQuota",
};

const PAGE_SIZES = [25, 50, 100];
const CMD_MAX_LEN = 80;

export default function CommandsPage() {
  const t = useTranslations("commands");
  const tp = useTranslations("pagination");
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [taskFilter, setTaskFilter] = React.useState("");

  const [commands, setCommands] = React.useState<CommandRecord[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(false);

  // 各工具调用次数仅打开弹窗时加载，避免每次翻页追加聚合查询成本。
  const [statsOpen, setStatsOpen] = React.useState(false);
  const [stats, setStats] = React.useState<ToolStat[]>([]);
  const [statsLoading, setStatsLoading] = React.useState(false);

  // Selected execution is rendered in a right-side detail sheet.
  const [selected, setSelected] = React.useState<CommandRecord | null>(null);

  // Debounce search input.
  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // Reset page on filter change.
  // biome-ignore lint/correctness/useExhaustiveDependencies: every filter change intentionally resets pagination.
  React.useEffect(() => {
    setPage(0);
  }, [queryQ, taskFilter, size]);

  // Load data.
  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .commands({ task: taskFilter || undefined, q: queryQ || undefined, page, size })
      .then((r) => {
        if (!alive) return;
        setCommands(r.commands ?? []);
        setTotal(r.total ?? 0);
      })
      .catch(() => {
        if (!alive) return;
        setCommands([]);
        setTotal(0);
      })
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [page, size, queryQ, taskFilter]);

  // 统计与当前列表使用相同过滤条件，但不分页。
  React.useEffect(() => {
    if (!statsOpen) return;
    let alive = true;
    setStatsLoading(true);
    api
      .commandStats({ task: taskFilter || undefined, q: queryQ || undefined })
      .then((r) => alive && setStats(r.stats ?? []))
      .catch(() => alive && setStats([]))
      .finally(() => alive && setStatsLoading(false));
    return () => {
      alive = false;
    };
  }, [statsOpen, queryQ, taskFilter]);

  const statsTotal = stats.reduce((n, s) => n + s.total, 0);
  const statsErrors = stats.reduce((n, s) => n + s.errors, 0);
  const statsMax = stats.reduce((n, s) => Math.max(n, s.total), 0);
  // 错误归因汇总：把每个工具的分类计数合成全局分布，按次数降序。
  // 「Bash 错误率高」只有拆成写法/环境/额度几类之后才知道该修哪里。
  const classTotals = React.useMemo(() => {
    const acc = new Map<string, number>();
    for (const s of stats) {
      for (const [cls, n] of Object.entries(s.error_classes ?? {})) {
        acc.set(cls, (acc.get(cls) ?? 0) + n);
      }
    }
    return [...acc.entries()].sort((a, b) => b[1] - a[1]);
  }, [stats]);
  const classLabel = (cls: string) => {
    const key = CLASS_LABEL_KEY[cls];
    return key ? t(key) : cls;
  };
  const totalPages = Math.max(1, Math.ceil(total / size));
  const rangeStart = total === 0 ? 0 : page * size + 1;
  const rangeEnd = page * size + commands.length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      {/* Header */}
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <TerminalIcon className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold tracking-tight">{t("title")}</h1>
          <Badge variant="secondary">{total}</Badge>
        </div>
      </div>

      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder={t("searchPlaceholder")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 pl-8"
          />
        </div>
        <Input
          placeholder={t("taskIdPlaceholder")}
          className="h-8 w-28"
          value={taskFilter}
          onChange={(e) => setTaskFilter(e.target.value.replace(/\D/g, ""))}
        />
        <Select value={String(size)} onValueChange={(v) => setSize(Number(v))}>
          <SelectTrigger size="sm" className="w-28">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n}
                {tp("perPage")}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Button variant="outline" size="sm" className="h-8" onClick={() => setStatsOpen(true)}>
          <BarChart3Icon className="size-4" />
          {t("stats")}
        </Button>

        <div className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          <span className="tabular-nums">
            {rangeStart}–{rangeEnd} / {total}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page <= 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            <ChevronLeftIcon />
          </Button>
          <span className="tabular-nums">
            {page + 1} / {totalPages}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page + 1 >= totalPages}
            onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </div>

      {/* History table */}
      <div className="flex h-[calc(100vh-13rem)] min-h-0 flex-col">
        <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
          <div className="min-h-0 flex-1 overflow-auto">
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead className="w-[130px]">{t("colTime")}</TableHead>
                  <TableHead className="w-[60px]">{t("colTask")}</TableHead>
                  <TableHead className="w-[90px]">{t("colWorker")}</TableHead>
                  <TableHead className="w-[110px]">{t("colTool")}</TableHead>
                  <TableHead>{t("colInput")}</TableHead>
                  <TableHead className="w-[60px]">{t("colStatus")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading && commands.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-12 text-center">
                      <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
                    </TableCell>
                  </TableRow>
                ) : commands.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="p-0">
                      <EmptyState illustration="terminal" title={t("empty")} />
                    </TableCell>
                  </TableRow>
                ) : (
                  commands.map((cmd) => (
                    <TableRow
                      key={cmd.id}
                      className={cn("cursor-pointer", selected?.id === cmd.id && "bg-accent hover:bg-accent")}
                      onClick={() => setSelected(cmd)}
                    >
                      <TableCell className="text-xs text-muted-foreground tabular-nums">
                        {fmtTime(cmd.created_at)}
                      </TableCell>
                      <TableCell className="text-xs font-mono text-muted-foreground">#{cmd.exploration_id}</TableCell>
                      <TableCell>
                        <Badge variant="outline" className="text-xs font-mono">
                          {cmd.worker || "-"}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary" className="text-xs font-mono">
                          {cmd.tool || "-"}
                        </Badge>
                      </TableCell>
                      <TableCell className="max-w-0">
                        <code className="block truncate font-mono text-xs">
                          {truncate(toolInput(cmd.command), CMD_MAX_LEN)}
                        </code>
                      </TableCell>
                      <TableCell>
                        {cmd.is_error ? (
                          <Badge variant="destructive" className="text-xs">
                            {t("failed")}
                          </Badge>
                        ) : (
                          <Badge variant="secondary" className="text-xs text-emerald-600">
                            {t("success")}
                          </Badge>
                        )}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
        </Card>
      </div>

      {/* 工具调用统计：与表格相同过滤记录，不分页。 */}
      <Dialog open={statsOpen} onOpenChange={setStatsOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("statsTitle")}</DialogTitle>
            <DialogDescription>
              {taskFilter || queryQ ? t("statsDescFiltered") : t("statsDescAll")}
              {stats.length > 0 && (
                <>
                  {" · "}
                  <span className="tabular-nums">{stats.length}</span>
                  {t("unitTools")} · <span className="tabular-nums">{statsTotal}</span>
                  {t("unitCalls")}
                  {statsErrors > 0 && (
                    <>
                      {" · "}
                      {t("failed")} <span className="tabular-nums text-red-600 dark:text-red-400">{statsErrors}</span>
                    </>
                  )}
                </>
              )}
            </DialogDescription>
          </DialogHeader>

          {/* 错误归因：只看 errors 总数无法回答「为什么失败」，这里按 error_class 拆开。 */}
          {statsErrors > 0 && (
            <div className="rounded-md border bg-muted/30 p-3">
              <div className="mb-2 flex items-center justify-between text-xs font-medium">
                <span>{t("classTitle")}</span>
                <span className="tabular-nums text-muted-foreground">
                  {statsErrors} {t("failed")}
                </span>
              </div>
              {classTotals.length === 0 ? (
                // 旧账本行没有 error_class（归因上线前写入）：有失败但拆不开，
                // 明说一句，别让人以为归因坏了。
                <div className="text-[11px] text-muted-foreground">{t("classEmpty")}</div>
              ) : (
              <div className="flex flex-wrap gap-1.5">
                {classTotals.map(([cls, n]) => (
                  <span
                    key={cls}
                    className="inline-flex items-center gap-1 rounded-full border bg-background px-2 py-0.5 text-[11px]"
                  >
                    <span className="text-muted-foreground">{classLabel(cls)}</span>
                    <span className="font-semibold tabular-nums">{n}</span>
                    <span className="tabular-nums text-muted-foreground">
                      {statsErrors > 0 ? `${((n / statsErrors) * 100).toFixed(0)}%` : ""}
                    </span>
                  </span>
                ))}
              </div>
              )}
            </div>
          )}

          {statsLoading && stats.length === 0 ? (
            <div className="py-10 text-center">
              <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : stats.length === 0 ? (
            <div className="py-10 text-center text-sm text-muted-foreground">{t("statsEmpty")}</div>
          ) : (
            <div className="-mr-2 max-h-[55vh] space-y-1 overflow-auto pr-2">
              {stats.map((s) => (
                <div key={s.tool} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-md p-2">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate font-mono text-xs font-medium">{s.tool}</span>
                      {s.errors > 0 && (
                        <span className="text-[11px] tabular-nums text-red-600 dark:text-red-400">
                          {t("failed")} {s.errors}
                        </span>
                      )}
                    </div>
                    <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full bg-primary"
                        style={{ width: `${statsMax > 0 ? (s.total / statsMax) * 100 : 0}%` }}
                      />
                    </div>
                    {s.error_classes && Object.keys(s.error_classes).length > 0 && (
                      <div className="mt-1 flex flex-wrap gap-1">
                        {Object.entries(s.error_classes)
                          .sort((a, b) => b[1] - a[1])
                          .map(([cls, n]) => (
                            <span
                              key={cls}
                              className="rounded-sm bg-red-50 px-1.5 py-0.5 text-[10px] text-red-700 dark:bg-red-950/40 dark:text-red-300"
                            >
                              {classLabel(cls)} {n}
                            </span>
                          ))}
                      </div>
                    )}
                  </div>
                  <div className="text-right">
                    <div className="text-xs font-semibold tabular-nums">{s.total}</div>
                    <div className="text-[11px] tabular-nums text-muted-foreground">
                      {statsTotal > 0 ? ((s.total / statsTotal) * 100).toFixed(1) : "0.0"}%
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </DialogContent>
      </Dialog>

      <Sheet open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <SheetContent className="w-full! max-w-none! gap-0 p-0 sm:w-[46rem]! sm:max-w-[46rem]!">
          {selected && (
            <>
              <SheetHeader className="border-b px-5 py-4">
                <SheetTitle className="pr-8">{t("detailTitle")}</SheetTitle>
                <SheetDescription>{fmtTime(selected.created_at)}</SheetDescription>
                <div className="flex flex-wrap items-center gap-2 pt-2">
                  <Badge variant="outline" className="text-xs font-mono">
                    #{selected.exploration_id}
                  </Badge>
                  <Badge variant="outline" className="text-xs font-mono">
                    {selected.worker || "-"}
                  </Badge>
                  <Badge variant="secondary" className="text-xs font-mono">
                    {selected.tool || "-"}
                  </Badge>
                  {selected.is_error ? (
                    <Badge variant="destructive" className="text-xs">
                      {t("failed")}
                    </Badge>
                  ) : (
                    <Badge variant="secondary" className="text-xs text-emerald-600">
                      {t("success")}
                    </Badge>
                  )}
                </div>
              </SheetHeader>
              <div className="grid min-h-0 flex-1 grid-rows-2 divide-y">
                <div className="flex min-h-0 min-w-0 flex-col">
                  <div className="border-b px-5 py-2 text-xs font-medium text-muted-foreground">{t("inputLabel")}</div>
                  <div className="min-h-0 flex-1 overflow-auto">
                    <pre className="p-5 font-mono text-xs break-all whitespace-pre-wrap">
                      {toolInput(selected.command)}
                    </pre>
                  </div>
                </div>
                <div className="flex min-h-0 min-w-0 flex-col">
                  <div className="border-b px-5 py-2 text-xs font-medium text-muted-foreground">{t("outputLabel")}</div>
                  <div className="min-h-0 flex-1 overflow-auto">
                    <pre
                      className={cn(
                        "p-5 font-mono text-xs break-all whitespace-pre-wrap",
                        selected.is_error && "text-red-600 dark:text-red-400",
                      )}
                    >
                      {selected.output || t("emptyOutput")}
                    </pre>
                  </div>
                </div>
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
