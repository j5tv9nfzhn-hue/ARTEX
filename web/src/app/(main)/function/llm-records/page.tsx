"use client";

import * as React from "react";

import {
  CheckIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CopyIcon,
  Loader2Icon,
  RadioIcon,
  SearchIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";

import { EmptyState } from "@/components/empty-state";
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
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { LLMRecordDetail, LLMRecordItem, LLMTask } from "@/lib/types";
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

function fmtLatency(ms: number) {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function fmtTokens(n: number) {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

// 费用估算(USD)的自适应精度：金额越大保留越少小数位，避免展示一堆尾零；
// 小额最多 4 位，保证 $0.0012 这类分币消耗不显示成 $0.00。
function fmtCost(n: number): string {
  if (n >= 1) return `$${n.toFixed(2)}`;
  if (n >= 0.01) return `$${n.toFixed(3)}`;
  return `$${n.toFixed(4)}`;
}

function tryFormatJSON(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// 复制当前文本的小按钮；成功后短暂显示勾选。
// 文本为空或只有占位符时禁用。
function CopyButton({ text }: { text: string }) {
  const t = useTranslations("llmRecords");
  const [copied, setCopied] = React.useState(false);
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  React.useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const disabled = !text;
  const copy = async () => {
    if (disabled) return;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // 不安全上下文（如 HTTP 私网）无法使用 navigator.clipboard，回退到 execCommand。
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand("copy");
      } catch {
        /* 忽略：不支持时静默跳过。 */
      }
      document.body.removeChild(ta);
    }
    setCopied(true);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Button
      variant="ghost"
      size="icon"
      className="size-5 shrink-0"
      disabled={disabled}
      title={copied ? t("copyCopied") : t("copyTooltip")}
      onClick={copy}
    >
      {copied ? <CheckIcon className="size-3 text-emerald-600" /> : <CopyIcon className="size-3" />}
    </Button>
  );
}

const PAGE_SIZES = [25, 50, 100];

export default function LLMRecordsPage() {
  const t = useTranslations("llmRecords");
  const tp = useTranslations("pagination");
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [session, setSession] = React.useState("");
  const [sessionQ, setSessionQ] = React.useState("");
  const [model, setModel] = React.useState("");

  const [records, setRecords] = React.useState<LLMRecordItem[]>([]);
  const [total, setTotal] = React.useState(0);
  // 过滤全集的费用估算(USD)；接口返回 cost_total 才显示，未配价格表时后端省略本字段。
  const [costTotal, setCostTotal] = React.useState<number | null>(null);
  const [loading, setLoading] = React.useState(false);

  // Recording on/off toggle (settings.llm_record; default off). When off the
  // backend records nothing.
  const [recEnabled, setRecEnabled] = React.useState(false);
  const [recBusy, setRecBusy] = React.useState(false);

  // Inline detail panel (Burp-style split, not a dialog)
  const [selected, setSelected] = React.useState<LLMRecordItem | null>(null);
  const [detail, setDetail] = React.useState<LLMRecordDetail | null>(null);
  const [detailLoading, setDetailLoading] = React.useState(false);
  // 规范化视图与 HTTP 原文视图。原文是诊断 provider 问题的重要依据：
  // 规范化视图不包含工具 schema，响应也不包含 tool_use 块。
  const [rawView, setRawView] = React.useState(false);

  const hasRaw = !!(detail?.raw_request || detail?.raw_response);
  // 保留用户选择，但切换到无原文的旧记录时自动返回解析视图，避免空白页面。
  const showRaw = rawView && hasRaw;
  // 原始请求 JSON 可格式化缩进，布局改变不影响语义。
  // 原始响应是 SSE，tryFormatJSON 解析失败时原样返回，因此两者可共用函数。
  const reqText = showRaw
    ? detail?.raw_request && tryFormatJSON(detail.raw_request)
    : detail?.request_body && tryFormatJSON(detail.request_body);
  const respText = showRaw ? detail?.raw_response : detail?.response_body && tryFormatJSON(detail.response_body);

  // Per-task delete (task picker + confirm dialog)
  const [tasks, setTasks] = React.useState<LLMTask[]>([]);
  const [pickedTask, setPickedTask] = React.useState("");
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);
  const [reloadTick, setReloadTick] = React.useState(0); // manual refetch trigger

  // Load recording toggle state on mount.
  React.useEffect(() => {
    let alive = true;
    api
      .settings()
      .then((s) => {
        if (alive) setRecEnabled(!!s.llm_record);
      })
      .catch(() => {
        /* 查询失败时保留默认状态。 */
      });
    return () => {
      alive = false;
    };
  }, []);

  const toggleRecording = async (on: boolean) => {
    setRecBusy(true);
    setRecEnabled(on); // optimistic
    try {
      const s = await api.setSettings({ llm_record: on });
      setRecEnabled(!!s.llm_record);
    } catch {
      setRecEnabled(!on); // revert on failure
    } finally {
      setRecBusy(false);
    }
  };

  // Debounce session filter.
  React.useEffect(() => {
    const timer = setTimeout(() => setSessionQ(session.trim()), 300);
    return () => clearTimeout(timer);
  }, [session]);

  // Reset page on filter change.
  // biome-ignore lint/correctness/useExhaustiveDependencies: these values intentionally trigger a page reset.
  React.useEffect(() => {
    setPage(0);
  }, [sessionQ, model, size, pickedTask]);

  // Load list.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadTick is an explicit manual-refetch trigger.
  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .llmRecords({
        model: model || undefined,
        session: sessionQ || undefined,
        task: pickedTask || undefined,
        page,
        size,
      })
      .then((r) => {
        if (!alive) return;
        setRecords(r.records ?? []);
        setTotal(r.total ?? 0);
        setCostTotal(typeof r.cost_total === "number" ? r.cost_total : null);
      })
      .catch(() => {
        /* 查询失败忽略，finally 中解除加载状态。 */
      })
      .finally(() => alive && setLoading(false));
    api
      .llmTasks()
      .then((r) => {
        if (alive) setTasks(r.tasks ?? []);
      })
      .catch(() => {
        /* 忽略查询失败。 */
      });
    return () => {
      alive = false;
    };
  }, [page, size, sessionQ, model, pickedTask, reloadTick]);

  // Delete every LLM record for the picked task, then refetch.
  const confirmDelete = () => {
    setDeleting(true);
    api
      .llmRecordsDeleteTask(pickedTask)
      .then(() => {
        setDeleteOpen(false);
        setSelected(null);
        setDetail(null);
        setPickedTask("");
        setPage(0);
        setReloadTick((n) => n + 1);
      })
      .catch(() => {
        /* 删除后重新查询失败时忽略，finally 中解除删除状态。 */
      })
      .finally(() => setDeleting(false));
  };

  // Lazy-load full request/response when a row is selected.
  React.useEffect(() => {
    if (!selected) {
      setDetail(null);
      return;
    }
    let alive = true;
    setDetailLoading(true);
    setDetail(null);
    api
      .llmRecordDetail(selected.id)
      .then((d) => {
        if (alive) setDetail(d);
      })
      .catch(() => {
        /* 详情查询失败忽略，finally 中解除加载状态。 */
      })
      .finally(() => {
        if (alive) setDetailLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [selected]);

  const totalPages = Math.max(1, Math.ceil(total / size));
  const rangeStart = total === 0 ? 0 : page * size + 1;
  const rangeEnd = page * size + records.length;
  // 仅在接口返回了费用(价格表已配置且命中模型)时才显示费用列/合计，避免空白噪音。
  const showCost = records.some((rec) => (rec.cost ?? 0) > 0);
  const colCount = showCost ? 9 : 8;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      {/* Header */}
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <RadioIcon className="h-5 w-5 text-muted-foreground" />
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
            value={session}
            onChange={(e) => setSession(e.target.value)}
            className="h-8 pl-8"
          />
        </div>
        <Input
          placeholder={t("modelPlaceholder")}
          className="h-8 w-48"
          value={model}
          onChange={(e) => setModel(e.target.value)}
        />
        <Select value={pickedTask} onValueChange={setPickedTask}>
          <SelectTrigger size="sm" className="w-56">
            <SelectValue placeholder={t("taskPlaceholder")} />
          </SelectTrigger>
          <SelectContent>
            {tasks.length === 0 ? (
              <SelectItem value="__none__" disabled>
                {t("taskEmpty")}
              </SelectItem>
            ) : (
              tasks.map((task) => (
                <SelectItem key={task.task_id} value={task.task_id}>
                  <span className="font-mono">#{task.task_id}</span>
                  <span className="ml-2 text-muted-foreground">({task.count})</span>
                  {typeof task.cost === "number" && task.cost > 0 && (
                    <span className="ml-2 text-amber-600 tabular-nums">{fmtCost(task.cost)}</span>
                  )}
                </SelectItem>
              ))
            )}
          </SelectContent>
        </Select>
        <Button
          variant="destructive"
          size="sm"
          className="h-8"
          disabled={!pickedTask || deleting}
          title={pickedTask ? undefined : t("selectTaskFirst")}
          onClick={() => setDeleteOpen(true)}
        >
          <Trash2Icon className="size-3.5" />
          {t("deleteTaskConversations")}
        </Button>
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

        {/* Recording on/off — off means no LLM calls are recorded */}
        <div className="flex items-center gap-2 rounded-md border px-2.5 py-1">
          <Switch
            id="llm-rec-toggle"
            size="sm"
            checked={recEnabled}
            onCheckedChange={toggleRecording}
            disabled={recBusy}
          />
          <label
            htmlFor="llm-rec-toggle"
            className={cn(
              "cursor-pointer text-xs font-medium select-none",
              recEnabled ? "text-foreground" : "text-muted-foreground",
            )}
          >
            {recEnabled ? t("recording") : t("recordingOff")}
          </label>
        </div>

        <div className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          {/* totals 区：当前过滤条件的费用估算(USD)，由价格表读时计算。 */}
          {costTotal !== null && costTotal > 0 && (
            <span className="tabular-nums text-amber-600" title={t("costTooltip")}>
              {t("costEstimate", { cost: fmtCost(costTotal) })}
            </span>
          )}
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

      {/* History table + inline detail (Burp-style split) */}
      <div className="flex h-[calc(100vh-13rem)] min-h-0 flex-col gap-3">
        <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
          <div className="min-h-0 flex-1 overflow-auto">
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead className="w-[130px]">{t("colTime")}</TableHead>
                  <TableHead className="w-[60px]">{t("colTask")}</TableHead>
                  <TableHead className="w-[90px]">{t("colWorker")}</TableHead>
                  <TableHead className="w-[100px]">{t("colProfile")}</TableHead>
                  <TableHead className="w-[140px]">{t("colModel")}</TableHead>
                  <TableHead className="w-[70px]">{t("colLatency")}</TableHead>
                  <TableHead className="w-[90px]">{t("colTokens")}</TableHead>
                  {showCost && <TableHead className="w-[80px] text-right">{t("colCost")}</TableHead>}
                  <TableHead className="w-[60px]">{t("colStatus")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading && records.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={colCount} className="py-12 text-center">
                      <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
                    </TableCell>
                  </TableRow>
                ) : records.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={colCount} className="p-0">
                      <EmptyState illustration="terminal" title={t("empty")} />
                    </TableCell>
                  </TableRow>
                ) : (
                  records.map((rec) => (
                    <TableRow
                      key={rec.id}
                      className={cn("cursor-pointer", selected?.id === rec.id && "bg-accent hover:bg-accent")}
                      onClick={() => setSelected(rec)}
                    >
                      <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(rec.ts)}</TableCell>
                      <TableCell className="text-xs font-mono text-muted-foreground">
                        {rec.task_id ? `#${rec.task_id}` : "-"}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className="text-xs font-mono">
                          {rec.worker || "-"}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <span className="text-xs">{rec.profile_name || "-"}</span>
                      </TableCell>
                      <TableCell>
                        <span className="text-xs font-mono">{rec.model || "-"}</span>
                      </TableCell>
                      <TableCell>
                        <span className={cn("text-xs", rec.latency_ms > 30000 && "text-amber-500")}>
                          {fmtLatency(rec.latency_ms)}
                        </span>
                      </TableCell>
                      <TableCell>
                        <span className="text-xs">
                          {fmtTokens(rec.input_tokens)} / {fmtTokens(rec.output_tokens)}
                        </span>
                      </TableCell>
                      {showCost && (
                        <TableCell className="text-right">
                          <span className="text-xs tabular-nums">
                            {typeof rec.cost === "number" && rec.cost > 0 ? fmtCost(rec.cost) : "-"}
                          </span>
                        </TableCell>
                      )}
                      <TableCell>
                        {rec.status === "ok" ? (
                          <Badge variant="secondary" className="text-xs text-emerald-600">
                            {t("statusOk")}
                          </Badge>
                        ) : (
                          <Badge variant="destructive" className="text-xs">
                            {t("statusError")}
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

        {/* Inline detail panel */}
        {selected && (
          <Card className="flex h-[42%] min-h-0 flex-col overflow-hidden py-0">
            {/* Detail header */}
            <div className="flex items-center gap-2 border-b px-3 py-2">
              <Badge variant="outline" className="text-xs font-mono">
                #{selected.id}
              </Badge>
              <Badge variant="outline" className="text-xs font-mono">
                {selected.profile_name || "-"}
              </Badge>
              <Badge variant="outline" className="text-xs font-mono">
                {selected.model || "-"}
              </Badge>
              {selected.task_id && (
                <Badge variant="outline" className="text-xs font-mono">
                  {t("taskBadge", { id: selected.task_id })}
                </Badge>
              )}
              <span className="text-xs text-muted-foreground">{fmtTime(selected.ts)}</span>
              <span className={cn("text-xs", selected.latency_ms > 30000 && "text-amber-500")}>
                {fmtLatency(selected.latency_ms)}
              </span>
              {selected.status === "ok" ? (
                <Badge variant="secondary" className="text-xs text-emerald-600">
                  {t("statusOk")}
                </Badge>
              ) : (
                <Badge variant="destructive" className="text-xs">
                  {t("statusError")}
                </Badge>
              )}
              {/**
               * 旧记录没有原文时禁用原文切换，不静默伪装为原文，
               * 避免让用户误以为原文与解析结果相同。
               */}
              <Button
                variant={showRaw ? "secondary" : "ghost"}
                size="sm"
                className="ml-auto h-7 shrink-0 text-xs"
                disabled={!hasRaw}
                title={hasRaw ? t("rawTooltipHas") : t("rawTooltipNone")}
                onClick={() => setRawView((v) => !v)}
              >
                {t("raw")}
              </Button>
              <Button variant="ghost" size="icon" className="size-7 shrink-0" onClick={() => setSelected(null)}>
                <XIcon />
              </Button>
            </div>
            {/* Request / Response split */}
            <div className="grid min-h-0 flex-1 grid-cols-2 divide-x">
              <div className="flex min-h-0 min-w-0 flex-col">
                <div className="flex items-center gap-2 border-b py-0.5 pr-1.5 pl-3 text-[11px] font-medium text-muted-foreground">
                  <span>Request{showRaw && ` · ${t("raw")}`}</span>
                  <CopyButton text={reqText || ""} />
                </div>
                <div className="min-h-0 flex-1 overflow-auto">
                  {detailLoading ? (
                    <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" />
                      {t("loading")}
                    </div>
                  ) : (
                    <pre className="p-3 font-mono text-xs break-all whitespace-pre-wrap">
                      {reqText || t("emptyContent")}
                    </pre>
                  )}
                </div>
              </div>
              <div className="flex min-h-0 min-w-0 flex-col">
                <div className="flex items-center gap-2 border-b py-0.5 pr-1.5 pl-3 text-[11px] font-medium text-muted-foreground">
                  <span>Response{showRaw && ` · ${t("raw")}(SSE)`}</span>
                  <CopyButton text={respText || ""} />
                </div>
                <div className="min-h-0 flex-1 overflow-auto">
                  {detailLoading ? (
                    <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" />
                      {t("loading")}
                    </div>
                  ) : (
                    <pre
                      className={cn(
                        "p-3 font-mono text-xs break-all whitespace-pre-wrap",
                        selected.status !== "ok" && "text-red-600 dark:text-red-400",
                      )}
                    >
                      {respText || t("emptyContent")}
                    </pre>
                  )}
                </div>
              </div>
            </div>
          </Card>
        )}
      </div>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("deleteTitle", { taskId: pickedTask })}</AlertDialogTitle>
            <AlertDialogDescription>{t("deleteDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                confirmDelete();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? t("deleting") : t("confirmDelete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
