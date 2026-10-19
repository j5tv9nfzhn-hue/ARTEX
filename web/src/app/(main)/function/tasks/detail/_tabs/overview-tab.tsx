"use client";

import * as React from "react";

import {
  ActivityIcon,
  AlertTriangleIcon,
  BugIcon,
  CheckIcon,
  ClockIcon,
  CoinsIcon,
  ListChecksIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  TargetIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Progress } from "@/components/ui/progress";
import { Switch } from "@/components/ui/switch";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type {
  AssetInterceptKind,
  AssetInterceptRule,
  Finding,
  ModelTokenStat,
  Stats,
  Task,
  TaskConstraint,
  TaskGoal,
  TaskNode,
  TaskScopeRow,
  WorkerTokenStat,
} from "@/lib/types";

// 紧凑 token 数量：12345 → 12.3k，2000000 → 2M。
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

// fmtFirstFindingMS 把「首发现」毫秒差渲染成紧凑字符串（45s / 4m / 1h20m）。0=未产出。
function fmtFirstFindingMS(ms: number): string {
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

// fmtPerHour 渲染一位小数的产出速率，去掉多余的 ".0"（12.0 → 12）。
function fmtPerHour(v: number): string {
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

// 费用估算(USD)的自适应精度：金额越大保留越少小数位；小额最多 4 位，
// 保证 $0.0012 这类分币消耗不显示成 $0.00。
function fmtCost(n: number): string {
  if (n >= 1) return `$${n.toFixed(2)}`;
  if (n >= 0.01) return `$${n.toFixed(3)}`;
  return `$${n.toFixed(4)}`;
}

// 缓存命中率 = 缓存读取 / 输入；InputTokens 已包含 cache_read 子集，比例为 0~100%。
function cacheHitRate(cacheRead: number, input: number): string {
  if (input <= 0) return "—";
  return `${Math.round((cacheRead / input) * 100)}%`;
}

// 单条测试范围的展示值：域名、网段或公司。
function scopeValue(row: TaskScopeRow, companyFallback: (id: number | string) => string): string {
  if (row.value) return row.value;
  if (row.domain) return row.domain;
  if (row.net) return row.net;
  if (row.company_id) return row.company_name?.trim() ? row.company_name : companyFallback(row.company_id);
  return "—";
}

function StatCard({
  label,
  value,
  sub,
  icon: Icon,
}: {
  label: string;
  value: React.ReactNode;
  sub?: string;
  icon: React.ElementType;
}) {
  return (
    <Card className="gap-1.5">
      <CardHeader className="pb-0">
        <CardDescription className="flex items-center gap-1.5">
          <Icon className="size-3.5" /> {label}
        </CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {sub && <CardContent className="text-muted-foreground text-xs">{sub}</CardContent>}
    </Card>
  );
}

export function OverviewTab({ taskId }: { taskId: string }) {
  const t = useTranslations("taskDetail.overviewTab");
  const [task, setTask] = React.useState<Task | null>(null);
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [coverage, setCoverage] = React.useState<{
    enabled: boolean;
    scope_rows: number;
    denominator: number;
    tested: number;
    pct: number | null;
    by_type: { type: string; total: number; tested: number }[];
  } | null>(null);
  // 正在重跑的意图 ID，批量使用 __all__，用于禁用按钮并显示 spinner。
  const [rerunning, setRerunning] = React.useState<Set<string>>(new Set());
  // 测试范围列表与添加表单状态。
  const [scope, setScope] = React.useState<TaskScopeRow[]>([]);
  const [scopeKind, setScopeKind] = React.useState<TaskScopeRow["kind"]>("root_domain");
  const [scopeValueInput, setScopeValueInput] = React.useState("");
  const [scopeBusy, setScopeBusy] = React.useState(false);
  const [scopeErr, setScopeErr] = React.useState("");
  // 按模型统计 token，数据来自始终开启的 llm_usage 计量账本，每次调用均准确记录。
  const [modelTokens, setModelTokens] = React.useState<ModelTokenStat[]>([]);
  // 按角色(agent 泳道)统计 token,与按模型同源:角色可以被单独绑定模型,这一维回答
  // 「哪个角色烧的 token」(llmrec 从 session 前缀解析:worker/planner/main/goals/…)。
  const [workerTokens, setWorkerTokens] = React.useState<WorkerTokenStat[]>([]);
  // 任务级 Token 预算的行内编辑(0=不限)；预算用尽后调高预算再恢复任务即可继续。
  const [budgetEditing, setBudgetEditing] = React.useState(false);
  const [budgetInput, setBudgetInput] = React.useState("");
  const [budgetSaving, setBudgetSaving] = React.useState(false);
  // 目标管理：列表、添加表单、行内编辑状态。
  const [goals, setGoals] = React.useState<TaskGoal[]>([]);
  const [goalText, setGoalText] = React.useState("");
  const [goalVuln, setGoalVuln] = React.useState("");
  const [goalBusy, setGoalBusy] = React.useState(false);
  const [goalErr, setGoalErr] = React.useState("");
  const [editingGoalId, setEditingGoalId] = React.useState<string | null>(null);
  const [editText, setEditText] = React.useState("");
  const [editVuln, setEditVuln] = React.useState("");
  // 约束管理：列表、添加表单、行内编辑状态。
  const [constraints, setConstraints] = React.useState<TaskConstraint[]>([]);
  const [conText, setConText] = React.useState("");
  const [conKind, setConKind] = React.useState<TaskConstraint["kind"]>("deny");
  const [conBusy, setConBusy] = React.useState(false);
  const [conErr, setConErr] = React.useState("");
  const [editingConId, setEditingConId] = React.useState<string | null>(null);
  const [editConText, setEditConText] = React.useState("");
  const [editConKind, setEditConKind] = React.useState<TaskConstraint["kind"]>("deny");

  const loadTokens = React.useCallback(async () => {
    try {
      // 两张表同源(常开的 llm_usage 账本),并行取回,避免先后到达造成的闪动。
      const [byModel, byWorker] = await Promise.all([api.tokensByModel(taskId), api.tokensByWorker(taskId)]);
      setModelTokens(byModel.models);
      setWorkerTokens(byWorker.workers);
    } catch {
      // 没有 PostgreSQL 时 API 返回错误，忽略并保持卡片为空。
    }
  }, [taskId]);

  const loadScope = React.useCallback(async () => {
    try {
      const resp = await api.taskScope(taskId);
      setScope(resp.scope);
    } catch {
      // 没有资产库时 API 返回 503，忽略并保持范围卡片为空。
    }
  }, [taskId]);

  const loadGoals = React.useCallback(async () => {
    try {
      const resp = await api.taskGoals(taskId);
      setGoals(resp.goals);
    } catch {
      // 忽略短暂错误，下次轮询重试。
    }
  }, [taskId]);

  const addGoal = async () => {
    const text = goalText.trim();
    if (!text) return;
    setGoalBusy(true);
    setGoalErr("");
    try {
      await api.addGoal(taskId, text, goalVuln.trim() || undefined);
      setGoalText("");
      setGoalVuln("");
      await loadGoals();
    } catch (e) {
      setGoalErr(e instanceof Error ? e.message : t("addFailed"));
    } finally {
      setGoalBusy(false);
    }
  };

  const startEditGoal = (g: TaskGoal) => {
    setEditingGoalId(g.id);
    setEditText(g.text);
    setEditVuln(g.vulnclass ?? "");
  };

  const cancelEditGoal = () => {
    setEditingGoalId(null);
    setEditText("");
    setEditVuln("");
  };

  const saveEditGoal = async (g: TaskGoal) => {
    const text = editText.trim();
    if (!text) return;
    setGoalBusy(true);
    setGoalErr("");
    try {
      await api.updateGoal(taskId, g.id, text, editVuln.trim() || undefined);
      cancelEditGoal();
      await loadGoals();
    } catch (e) {
      setGoalErr(e instanceof Error ? e.message : t("saveFailed"));
    } finally {
      setGoalBusy(false);
    }
  };

  const removeGoal = async (g: TaskGoal) => {
    setGoals((prev) => prev.filter((x) => x.id !== g.id));
    try {
      await api.deleteGoal(taskId, g.id);
    } catch {
      await loadGoals(); // 删除失败，重新查询恢复状态。
    }
  };

  const loadConstraints = React.useCallback(async () => {
    try {
      const resp = await api.taskConstraints(taskId);
      setConstraints(resp.constraints);
    } catch {
      // 忽略短暂错误，下次轮询重试。
    }
  }, [taskId]);

  const addConstraint = async () => {
    const text = conText.trim();
    if (!text) return;
    setConBusy(true);
    setConErr("");
    try {
      await api.addConstraint(taskId, text, conKind);
      setConText("");
      await loadConstraints();
    } catch (e) {
      setConErr(e instanceof Error ? e.message : t("addFailed"));
    } finally {
      setConBusy(false);
    }
  };

  const startEditConstraint = (c: TaskConstraint) => {
    setEditingConId(c.id);
    setEditConText(c.text);
    setEditConKind(c.kind);
  };

  const cancelEditConstraint = () => {
    setEditingConId(null);
    setEditConText("");
    setEditConKind("deny");
  };

  const saveEditConstraint = async (c: TaskConstraint) => {
    const text = editConText.trim();
    if (!text) return;
    setConBusy(true);
    setConErr("");
    try {
      await api.updateConstraint(taskId, c.id, text, editConKind);
      cancelEditConstraint();
      await loadConstraints();
    } catch (e) {
      setConErr(e instanceof Error ? e.message : t("saveFailed"));
    } finally {
      setConBusy(false);
    }
  };

  const removeConstraint = async (c: TaskConstraint) => {
    setConstraints((prev) => prev.filter((x) => x.id !== c.id));
    try {
      await api.deleteConstraint(taskId, c.id);
    } catch {
      await loadConstraints(); // 删除失败，重新查询恢复状态。
    }
  };

  const addScope = async () => {
    const value = scopeValueInput.trim();
    if (!value) return;
    setScopeBusy(true);
    setScopeErr("");
    try {
      await api.addTaskScope(taskId, scopeKind, value);
      setScopeValueInput("");
      await loadScope();
    } catch (e) {
      setScopeErr(e instanceof Error ? e.message : t("addFailed"));
    } finally {
      setScopeBusy(false);
    }
  };

  const removeScope = async (row: TaskScopeRow) => {
    setScope((prev) => prev.filter((s) => s.id !== row.id));
    try {
      await api.deleteTaskScope(taskId, row.id);
    } catch {
      await loadScope(); // 删除失败，重新查询恢复状态。
    }
  };

  const markRerun = (key: string, on: boolean) =>
    setRerunning((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });

  // 单条重跑将状态设为 open，乐观更新本地状态，由三秒轮询校正。
  // Worker 重新领取后从头执行。
  const rerunOne = async (id: string) => {
    markRerun(id, true);
    try {
      await api.rerunIntent(taskId, id);
      setIntents((prev) => prev.map((i) => (i.id === id ? { ...i, state: "open" } : i)));
    } catch {
      // 失败时忽略，下次轮询仍为 blocked，用户可再次点击。
    } finally {
      markRerun(id, false);
    }
  };

  // 批量重跑当前任务的所有 blocked 意图。
  const rerunAll = async () => {
    markRerun("__all__", true);
    try {
      await api.rerunBlocked(taskId);
      setIntents((prev) => prev.map((i) => (i.state === "blocked" ? { ...i, state: "open" } : i)));
    } catch {
      // ignore
    } finally {
      markRerun("__all__", false);
    }
  };

  // ---- Token 预算行内编辑 ----
  const startBudgetEdit = () => {
    setBudgetInput(String(Math.round((task?.token_budget ?? 0) / 1000)));
    setBudgetEditing(true);
  };

  const saveBudget = async () => {
    // 单位千 token(k)；0=不限。必须为非负整数,避免 0.5k 这类模糊语义。
    const k = Number(budgetInput);
    if (!Number.isInteger(k) || k < 0) {
      toast.error(t("tokens.budgetInvalid"));
      return;
    }
    setBudgetSaving(true);
    try {
      const updated = await api.updateTask(taskId, { token_budget: k * 1000 });
      setTask((prev) => (prev ? { ...prev, token_budget: updated.token_budget } : updated));
      setBudgetEditing(false);
      toast.success(t("tokens.budgetSaved"));
    } catch (e) {
      toast.error(t("tokens.budgetSaveFailed", { error: (e as Error).message }));
    } finally {
      setBudgetSaving(false);
    }
  };

  // load 提到 useCallback：条件变化时立即刷新一次，周期刷新交给 usePolling。
  // 两者共用同一份请求逻辑，usePolling 保证同一时刻只有一轮在跑。
  const load = React.useCallback(async () => {
    const [taskResp, statsResp, intentsResp, findingsResp] = await Promise.all([
      api.task(taskId),
      api.stats(taskId),
      api.intents(taskId),
      api.findings(taskId),
    ]);
    const activeTask = statsResp.active_task;
    setTask(
      activeTask
        ? {
            ...taskResp,
            in_flight: activeTask.in_flight,
            goals_total: activeTask.goals_total,
            goals_met: activeTask.goals_met,
            engine_mode: statsResp.engine_mode ?? activeTask.engine_mode,
            paused: activeTask.paused,
          }
        : taskResp,
    );
    setStats(statsResp);
    setIntents(intentsResp);
    setFindings(findingsResp);
    // coverage 独立且在没有资产库时可能 503——单独拉，失败不影响其它面板。
    api
      .taskCoverage(taskId)
      .then(setCoverage)
      .catch(() => {
        // 没有资产库时 coverage 可能 503——下一轮轮询会重试，这里不打断其它面板。
      });
  }, [taskId]);

  // 首次加载：把 scope / tokens / goals / constraints 一起拉齐。
  React.useEffect(() => {
    void load();
    void loadScope();
    void loadTokens();
    void loadGoals();
    void loadConstraints();
  }, [load, loadScope, loadTokens, loadGoals, loadConstraints]);

  // 3s 刷新：任务本体、token、目标、约束一起更新。
  usePolling(
    async () => {
      await Promise.all([load(), loadTokens(), loadGoals(), loadConstraints()]);
    },
    { intervalMs: 3000, enabled: Boolean(taskId) },
  );

  const running = intents.filter((i) => i.state === "running");
  const open = intents.filter((i) => i.state === "open");
  const blocked = intents.filter((i) => i.state === "blocked");
  const taskFindings = findings.filter((f) => f.task_id === taskId);
  const goalsPct = task?.goals_total ? Math.round(((task.goals_met ?? 0) / task.goals_total) * 100) : 0;
  // 所有模型的 token 总计，用于卡片概览。
  const tokenTotals = modelTokens.reduce(
    (acc, m) => {
      acc.input += m.input_tokens;
      acc.output += m.output_tokens;
      acc.cacheRead += m.cache_read_tokens;
      acc.cacheWrite += m.cache_write_tokens;
      acc.calls += m.calls;
      return acc;
    },
    { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, calls: 0 },
  );
  // 预算行显示值：预算(0=不限)、已用(input+output,与后端预算闸同口径)、百分比。
  const budget = task?.token_budget ?? 0;
  const budgetUsed = tokenTotals.input + tokenTotals.output;
  const budgetPct = budget > 0 ? Math.round((budgetUsed / budget) * 100) : 0;
  // 费用估算(USD)：接口按价格表逐模型返回 cost，总计=各行之和；只有配置了价格表
  // 且确实命中模型(>0)时才展示列与合计，避免未配置时的一片 "-"。
  const modelCostTotal = modelTokens.reduce((sum, m) => sum + (m.cost ?? 0), 0);
  const showModelCost = modelTokens.some((m) => (m.cost ?? 0) > 0);

  return (
    <div className="flex flex-col gap-4">
      {/**
       * blocked 意图可能因错误或拦截中止，如 LLM 网络问题。
       * 批量重跑设为 open，Worker 从头执行，已写入图的数据保留。
       * 任务为终态或暂停时会自动恢复。
       */}
      {blocked.length > 0 && (
        <Card className="border-red-500/30">
          <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-red-500" /> {t("blocked.title")}
              <span className="font-normal text-muted-foreground text-xs">
                {t("blocked.hint", { count: blocked.length })}
              </span>
            </CardTitle>
            <Button size="sm" variant="outline" disabled={rerunning.has("__all__")} onClick={() => void rerunAll()}>
              <RefreshCwIcon className={`size-3.5 ${rerunning.has("__all__") ? "animate-spin" : ""}`} />
              {t("blocked.rerunAll")}
            </Button>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {blocked.slice(0, 20).map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="min-w-0 flex-1 truncate">{i.payload}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 shrink-0 px-2 text-xs"
                  disabled={rerunning.has(i.id)}
                  onClick={() => void rerunOne(i.id)}
                >
                  <RefreshCwIcon className={`size-3 ${rerunning.has(i.id) ? "animate-spin" : ""}`} />
                  {t("blocked.rerun")}
                </Button>
              </div>
            ))}
            {blocked.length > 20 && (
              <p className="text-muted-foreground text-xs">{t("blocked.truncated", { rest: blocked.length - 20 })}</p>
            )}
          </CardContent>
        </Card>
      )}
      {/* Heartbeat */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ActivityIcon className="size-4 text-blue-500" /> {t("heartbeat.title")}
          </CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div>
            <div className="text-muted-foreground text-xs">{t("heartbeat.engineState")}</div>
            <StatusBadge
              domain="engine"
              value={stats?.engine_mode ?? task?.engine_mode ?? "idle"}
              dot
              className="mt-1"
            />
          </div>
          <div>
            <div className="text-muted-foreground text-xs">{t("heartbeat.runningWorker")}</div>
            <div className="mt-1 font-semibold text-lg tabular-nums">{running.length}</div>
          </div>
          <div>
            <div className="text-muted-foreground text-xs">{t("heartbeat.lastActivity")}</div>
            <div className="mt-1 inline-flex items-center gap-1 text-sm">
              <ClockIcon className="size-3.5" />
              {task?.last_activity ? new Date(task.last_activity).toLocaleTimeString("zh-CN") : "—"}
            </div>
          </div>
          <div>
            <div className="text-muted-foreground text-xs">
              {t("heartbeat.goals", { met: task?.goals_met ?? 0, total: task?.goals_total ?? 0 })}
            </div>
            <Progress value={goalsPct} className="mt-2" />
          </div>
          {task?.completed_unix && task.completed_unix > 0 ? (
            <div>
              <div className="text-muted-foreground text-xs">{t("heartbeat.completedAt")}</div>
              <div className="mt-1 inline-flex items-center gap-1 text-sm">
                <ClockIcon className="size-3.5" />
                {new Date(task.completed_unix * 1000).toLocaleString("zh-CN")}
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>
      {/* Stat cards */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        <StatCard label={t("stat.pending")} value={open.length} icon={ShieldCheckIcon} sub={t("stat.pendingSub")} />
        <StatCard label={t("stat.confirmed")} value={taskFindings.length} icon={BugIcon} sub={t("stat.confirmedSub")} />
        <StatCard label={t("stat.total")} value={intents.length} icon={AlertTriangleIcon} sub={t("stat.totalSub")} />
        {/* 价值指标：从任务开始到首个漏洞登记的耗时 + 产出速率（漏洞数 ÷ 已运行小时）。 */}
        <StatCard
          label={t("stat.firstFinding")}
          value={task?.first_finding_ms && task.first_finding_ms > 0 ? fmtFirstFindingMS(task.first_finding_ms) : "—"}
          icon={ClockIcon}
          sub={t("stat.firstFindingSub")}
        />
        <StatCard
          label={t("stat.perHour")}
          value={
            task?.findings_per_hour && task.findings_per_hour > 0 ? (
              <>
                {fmtPerHour(task.findings_per_hour)}
                <span className="ml-1 font-normal text-muted-foreground text-sm">{t("stat.perHourUnit")}</span>
              </>
            ) : (
              "—"
            )
          }
          icon={ActivityIcon}
          sub={t("stat.perHourSub")}
        />
      </div>
      {/* Work set */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <TargetIcon className="size-4" /> {t("running.title")}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {running.slice(0, 6).map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="min-w-0 flex-1 truncate">{i.payload}</span>
              </div>
            ))}
            {running.length === 0 && (
              <EmptyState compact illustration="flag" className="py-4" title={t("running.empty")} />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-amber-500" /> {t("attention.title")}
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-2 gap-3 text-sm">
            <div>
              <div className="font-semibold text-2xl text-red-600 tabular-nums">{taskFindings.length}</div>
              <div className="text-muted-foreground text-xs">{t("attention.confirmedVuln")}</div>
            </div>
            <div>
              <div className="font-semibold text-2xl text-blue-600 tabular-nums">{running.length}</div>
              <div className="text-muted-foreground text-xs">{t("attention.executing")}</div>
            </div>
            <div>
              <div className="font-semibold text-2xl tabular-nums">{open.length}</div>
              <div className="text-muted-foreground text-xs">{t("attention.frontierPending")}</div>
            </div>
            <div>
              <div className="font-semibold text-2xl text-red-600 tabular-nums">{blocked.length}</div>
              <div className="text-muted-foreground text-xs">{t("attention.blockedIntent")}</div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <BugIcon className="size-4 text-red-500" /> {t("recent.title")}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {taskFindings.slice(0, 6).map((f) => (
              <div key={f.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="severity" value={f.severity} dot />
                <span className="min-w-0 flex-1 truncate">{f.summary}</span>
              </div>
            ))}
            {taskFindings.length === 0 && (
              <EmptyState compact illustration="radar" className="py-4" title={t("recent.empty")} />
            )}
          </CardContent>
        </Card>
      </div>
      {coverage?.enabled && coverage.scope_rows > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <TargetIcon className="size-4 text-emerald-500" /> {t("coverage.title")}
              <span className="font-normal text-muted-foreground text-xs">{t("coverage.rough")}</span>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-baseline gap-3">
              <span className="font-semibold text-2xl tabular-nums">
                {coverage.pct != null ? `${Math.round(coverage.pct * 100)}%` : "—"}
              </span>
              <span className="text-muted-foreground text-sm">
                {t("coverage.testedOf", { tested: coverage.tested, total: coverage.denominator })}
              </span>
            </div>
            {coverage.pct != null && <Progress value={Math.round(coverage.pct * 100)} />}
            {coverage.by_type.length > 0 && (
              <div className="flex flex-wrap gap-1.5 text-xs">
                {coverage.by_type.map((b) => (
                  <span key={b.type} className="rounded bg-muted px-1.5 py-0.5">
                    <span className="text-muted-foreground">{b.type}</span>{" "}
                    <span className="font-medium tabular-nums">
                      {b.tested}/{b.total}
                    </span>
                  </span>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-12">
        <div className="flex flex-col gap-4 xl:col-span-6">
          {/* 创建时的原始任务描述、目标放在顶部，便于随时查看。 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <TargetIcon className="size-4 text-primary" /> {t("desc.title")}
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="flex flex-col gap-1.5">
                <div className="font-medium text-muted-foreground text-xs">{t("desc.descLabel")}</div>
                <p className="whitespace-pre-wrap break-words text-sm">{task?.description?.trim() || "—"}</p>
              </div>
              <div className="flex flex-col gap-1.5">
                <div className="font-medium text-muted-foreground text-xs">{t("desc.goalLabel")}</div>
                <p className="whitespace-pre-wrap break-words text-sm">{task?.goal?.trim() || "—"}</p>
              </div>
            </CardContent>
          </Card>
          {/**
           * 目标管理支持查询、添加、修改、删除。添加、修改通知 Planner 并恢复任务；
           * 删除只通知 Planner，不恢复。目标是最终可验证成果，而非执行步骤或侦察动作。
           */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <ListChecksIcon className="size-4 text-primary" /> {t("goals.title")}
                <span className="font-normal text-muted-foreground text-xs">
                  {t("goals.hint", { count: goals.length })}
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {/* 添加表单。 */}
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  className="h-7 min-w-56 flex-1 text-sm"
                  placeholder={t("goals.addPlaceholder")}
                  value={goalText}
                  onChange={(e) => setGoalText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void addGoal();
                  }}
                  disabled={goalBusy}
                />
                <Input
                  className="h-7 w-32 text-sm"
                  placeholder={t("goals.vulnPlaceholder")}
                  value={goalVuln}
                  onChange={(e) => setGoalVuln(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void addGoal();
                  }}
                  disabled={goalBusy}
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={goalBusy || !goalText.trim()}
                  onClick={() => void addGoal()}
                >
                  <PlusIcon className="size-3.5" /> {t("add")}
                </Button>
                {goalErr && <span className="text-red-500 text-xs">{goalErr}</span>}
              </div>
              {/* 目标列表。 */}
              {goals.length > 0 ? (
                <div className="flex flex-col gap-1.5">
                  {goals.map((g) =>
                    editingGoalId === g.id ? (
                      <div key={g.id} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                        <Input
                          className="h-7 min-w-56 flex-1 text-sm"
                          value={editText}
                          onChange={(e) => setEditText(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") void saveEditGoal(g);
                            if (e.key === "Escape") cancelEditGoal();
                          }}
                          disabled={goalBusy}
                          autoFocus
                        />
                        <Input
                          className="h-7 w-32 text-sm"
                          placeholder={t("goals.vulnPlaceholder")}
                          value={editVuln}
                          onChange={(e) => setEditVuln(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") void saveEditGoal(g);
                            if (e.key === "Escape") cancelEditGoal();
                          }}
                          disabled={goalBusy}
                        />
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={goalBusy || !editText.trim()}
                          onClick={() => void saveEditGoal(g)}
                        >
                          <CheckIcon className="size-3.5 text-emerald-500" />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={goalBusy}
                          onClick={cancelEditGoal}
                        >
                          <XIcon className="size-3.5" />
                        </Button>
                      </div>
                    ) : (
                      <div key={g.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                        <StatusBadge domain="goal" value={g.state} />
                        <span className="min-w-0 flex-1 break-words">{g.text}</span>
                        {g.vulnclass && (
                          <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-muted-foreground text-xs">
                            {g.vulnclass}
                          </span>
                        )}
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={goalBusy}
                          onClick={() => startEditGoal(g)}
                        >
                          <PencilIcon className="size-3.5" />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={goalBusy}
                          onClick={() => void removeGoal(g)}
                        >
                          <Trash2Icon className="size-3.5 text-red-500" />
                        </Button>
                      </div>
                    ),
                  )}
                </div>
              ) : (
                <p className="text-muted-foreground text-sm">{t("goals.empty")}</p>
              )}
            </CardContent>
          </Card>
        </div>
        <div className="flex flex-col gap-4 xl:col-span-6">
          {/**
           * 行为约束：allow=允许、deny=禁止。下一轮将约束注入 Planner、Worker 系统提示，
           * 各角色是否注入由系统设置控制。修改不会立即中断运行，下一轮自然读取。
           */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <ShieldAlertIcon className="size-4 text-amber-500" /> {t("constraints.title")}
                <span className="font-normal text-muted-foreground text-xs">
                  {t("constraints.hint", { count: constraints.length })}
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {/* 添加表单。 */}
              <div className="flex flex-wrap items-center gap-2">
                <NativeSelect
                  size="sm"
                  value={conKind}
                  onChange={(e) => setConKind(e.target.value as TaskConstraint["kind"])}
                >
                  <NativeSelectOption value="deny">{t("constraints.deny")}</NativeSelectOption>
                  <NativeSelectOption value="allow">{t("constraints.allow")}</NativeSelectOption>
                </NativeSelect>
                <Input
                  className="h-7 min-w-56 flex-1 text-sm"
                  placeholder={t("constraints.addPlaceholder")}
                  value={conText}
                  onChange={(e) => setConText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void addConstraint();
                  }}
                  disabled={conBusy}
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={conBusy || !conText.trim()}
                  onClick={() => void addConstraint()}
                >
                  <PlusIcon className="size-3.5" /> {t("add")}
                </Button>
                {conErr && <span className="text-red-500 text-xs">{conErr}</span>}
              </div>
              {/* 约束列表。 */}
              {constraints.length > 0 ? (
                <div className="flex flex-col gap-1.5">
                  {constraints.map((c) =>
                    editingConId === c.id ? (
                      <div key={c.id} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                        <NativeSelect
                          size="sm"
                          value={editConKind}
                          onChange={(e) => setEditConKind(e.target.value as TaskConstraint["kind"])}
                        >
                          <NativeSelectOption value="deny">{t("constraints.deny")}</NativeSelectOption>
                          <NativeSelectOption value="allow">{t("constraints.allow")}</NativeSelectOption>
                        </NativeSelect>
                        <Input
                          className="h-7 min-w-56 flex-1 text-sm"
                          value={editConText}
                          onChange={(e) => setEditConText(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") void saveEditConstraint(c);
                            if (e.key === "Escape") cancelEditConstraint();
                          }}
                          disabled={conBusy}
                          autoFocus
                        />
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={conBusy || !editConText.trim()}
                          onClick={() => void saveEditConstraint(c)}
                        >
                          <CheckIcon className="size-3.5 text-emerald-500" />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={conBusy}
                          onClick={cancelEditConstraint}
                        >
                          <XIcon className="size-3.5" />
                        </Button>
                      </div>
                    ) : (
                      <div key={c.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                        <span
                          className={`shrink-0 rounded px-1.5 py-0.5 text-xs ${
                            c.kind === "allow"
                              ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
                              : "bg-red-500/15 text-red-600 dark:text-red-400"
                          }`}
                        >
                          {c.kind === "allow" ? t("constraints.allow") : t("constraints.deny")}
                        </span>
                        <span className="min-w-0 flex-1 break-words">{c.text}</span>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={conBusy}
                          onClick={() => startEditConstraint(c)}
                        >
                          <PencilIcon className="size-3.5" />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          disabled={conBusy}
                          onClick={() => void removeConstraint(c)}
                        >
                          <Trash2Icon className="size-3.5 text-red-500" />
                        </Button>
                      </div>
                    ),
                  )}
                </div>
              ) : (
                <p className="text-muted-foreground text-sm">{t("constraints.empty")}</p>
              )}
            </CardContent>
          </Card>
          {/* 测试范围既是覆盖率分母，也是授权边界，可手工添加、删除。 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <ShieldCheckIcon className="size-4 text-emerald-500" /> {t("scope.title")}
                <span className="font-normal text-muted-foreground text-xs">
                  {t("scope.hint", { count: scope.length })}
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {/* 添加表单。 */}
              <div className="flex flex-wrap items-center gap-2">
                <NativeSelect
                  size="sm"
                  value={scopeKind}
                  onChange={(e) => setScopeKind(e.target.value as TaskScopeRow["kind"])}
                >
                  <NativeSelectOption value="root_domain">{t("scope.kind.root_domain")}</NativeSelectOption>
                  <NativeSelectOption value="subdomain">{t("scope.kind.subdomain")}</NativeSelectOption>
                  <NativeSelectOption value="ip">{t("scope.kind.ip")}</NativeSelectOption>
                  <NativeSelectOption value="cidr">{t("scope.kind.cidr")}</NativeSelectOption>
                  <NativeSelectOption value="icp">{t("scope.kind.icp")}</NativeSelectOption>
                  <NativeSelectOption value="keyword">{t("scope.kind.keyword")}</NativeSelectOption>
                  <NativeSelectOption value="company">{t("scope.kind.company")}</NativeSelectOption>
                </NativeSelect>
                <Input
                  className="h-7 w-56 text-sm"
                  placeholder={t(
                    (
                      {
                        company: "scope.placeholder.company",
                        ip: "scope.placeholder.ipCidr",
                        cidr: "scope.placeholder.ipCidr",
                        icp: "scope.placeholder.icp",
                        keyword: "scope.placeholder.keyword",
                        root_domain: "scope.placeholder.domain",
                        subdomain: "scope.placeholder.domain",
                      } as const
                    )[scopeKind],
                  )}
                  value={scopeValueInput}
                  onChange={(e) => setScopeValueInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void addScope();
                  }}
                  disabled={scopeBusy}
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={scopeBusy || !scopeValueInput.trim()}
                  onClick={() => void addScope()}
                >
                  <PlusIcon className="size-3.5" /> {t("add")}
                </Button>
                {scopeErr && <span className="text-red-500 text-xs">{scopeErr}</span>}
              </div>
              {/* 范围列表。 */}
              {scope.length > 0 ? (
                <div className="flex flex-col gap-1.5">
                  {scope.map((row) => (
                    <div key={row.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                      <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-muted-foreground text-xs">
                        {t(`scope.kind.${row.kind}`)}
                      </span>
                      <span className="min-w-0 flex-1 truncate font-mono text-xs">
                        {scopeValue(row, (id) => t("scope.companyFallback", { id }))}
                      </span>
                      <span className="shrink-0 text-muted-foreground text-xs">{t(`scope.source.${row.source}`)}</span>
                      {row.task_id.toString() === taskId ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          onClick={() => void removeScope(row)}
                        >
                          <Trash2Icon className="size-3.5 text-red-500" />
                        </Button>
                      ) : (
                        <span className="shrink-0 text-muted-foreground text-xs">{t("scope.inherited")}</span>
                      )}
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-muted-foreground text-sm">{t("scope.empty")}</p>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-12">
        <div className="flex flex-col gap-4 xl:col-span-5">
          <TaskInterceptRulesCard taskId={taskId} />
        </div>
        <div className="flex flex-col gap-4 xl:col-span-7">
          {/* LLM token 用量按模型分组，来自 llm_usage 计量账本，不依赖是否开启 LLM 录制。 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <CoinsIcon className="size-4 text-amber-500" /> {t("tokens.title")}
                <span className="font-normal text-muted-foreground text-xs">
                  {t("tokens.hint", { calls: tokenTotals.calls })}
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {/* 任务级 Token 预算:按 llm_usage 累计 input+output 计量,与后端预算闸同口径;
              达到预算时后端自动暂停任务。这里的编辑是「调高预算后恢复」的回环入口,0=不限。 */}
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border bg-muted/40 px-3 py-2">
                <span className="text-muted-foreground text-sm">{t("tokens.budgetLabel")}</span>
                {budgetEditing ? (
                  <>
                    <Input
                      type="number"
                      min={0}
                      step={1}
                      autoFocus
                      className="h-7 w-28 text-sm"
                      value={budgetInput}
                      disabled={budgetSaving}
                      onChange={(e) => setBudgetInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") void saveBudget();
                        if (e.key === "Escape") setBudgetEditing(false);
                      }}
                    />
                    <span className="text-muted-foreground text-xs">k</span>
                    <Button
                      size="sm"
                      variant="outline"
                      className="h-7"
                      disabled={budgetSaving}
                      onClick={() => void saveBudget()}
                    >
                      <CheckIcon className="size-3.5" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7"
                      disabled={budgetSaving}
                      onClick={() => setBudgetEditing(false)}
                    >
                      <XIcon className="size-3.5" />
                    </Button>
                  </>
                ) : (
                  <>
                    <span className="font-semibold text-sm tabular-nums">
                      {budget > 0 ? fmtTokens(budget) : t("tokens.budgetUnlimited")}
                    </span>
                    {budget > 0 && (
                      <span
                        className={
                          budgetPct >= 100
                            ? "text-destructive text-xs tabular-nums"
                            : "text-muted-foreground text-xs tabular-nums"
                        }
                      >
                        {t("tokens.budgetUsage", { used: fmtTokens(budgetUsed), pct: budgetPct })}
                      </span>
                    )}
                    <Button size="sm" variant="ghost" className="h-7 gap-1 px-2 text-xs" onClick={startBudgetEdit}>
                      <PencilIcon className="size-3.5" /> {t("tokens.budgetEdit")}
                    </Button>
                  </>
                )}
              </div>
              {modelTokens.length > 0 || workerTokens.length > 0 ? (
                <>
                  {modelTokens.length > 0 && (
                    <>
                      {/* 总计概览。 */}
                      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-sm">
                        <span className="tabular-nums">
                          <span className="text-muted-foreground">{t("tokens.input")} </span>
                          <span className="font-semibold">{fmtTokens(tokenTotals.input)}</span>
                        </span>
                        <span className="tabular-nums">
                          <span className="text-muted-foreground">{t("tokens.output")} </span>
                          <span className="font-semibold">{fmtTokens(tokenTotals.output)}</span>
                        </span>
                        <span className="tabular-nums">
                          <span className="text-muted-foreground">{t("tokens.cacheRead")} </span>
                          <span className="font-semibold">{fmtTokens(tokenTotals.cacheRead)}</span>
                        </span>
                        <span className="tabular-nums">
                          <span className="text-muted-foreground">{t("tokens.cacheHitRate")} </span>
                          <span className="font-semibold text-emerald-500">
                            {cacheHitRate(tokenTotals.cacheRead, tokenTotals.input)}
                          </span>
                        </span>
                        {/* 费用(估算)：读时按模型价格表计算，未配价格表/未命中时不显示。 */}
                        {modelCostTotal > 0 && (
                          <span className="tabular-nums">
                            <span className="text-muted-foreground">{t("tokens.costEstimate")} </span>
                            <span className="font-semibold text-amber-600">{fmtCost(modelCostTotal)}</span>
                          </span>
                        )}
                      </div>
                      {/* 按模型明细。 */}
                      <div className="overflow-x-auto">
                        <table className="w-full text-sm">
                          <thead>
                            <tr className="border-b text-left text-muted-foreground text-xs">
                              <th className="py-1.5 pr-3 font-medium">{t("tokens.colModel")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.colCalls")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.input")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.output")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.cacheRead")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.colHitRate")}</th>
                              {showModelCost && (
                                <th className="py-1.5 text-right font-medium">{t("tokens.colCost")}</th>
                              )}
                            </tr>
                          </thead>
                          <tbody>
                            {modelTokens.map((m) => (
                              <tr key={m.model} className="border-b last:border-0">
                                <td className="max-w-[16rem] truncate py-1.5 pr-3 font-mono text-xs" title={m.model}>
                                  {m.model}
                                </td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">{m.calls}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(m.input_tokens)}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(m.output_tokens)}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">
                                  {fmtTokens(m.cache_read_tokens)}
                                </td>
                                <td className="py-1.5 pr-3 text-right text-emerald-500 tabular-nums">
                                  {cacheHitRate(m.cache_read_tokens, m.input_tokens)}
                                </td>
                                {showModelCost && (
                                  <td className="py-1.5 text-right tabular-nums">
                                    {typeof m.cost === "number" && m.cost > 0 ? fmtCost(m.cost) : "-"}
                                  </td>
                                )}
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    </>
                  )}
                  {/* 按角色(agent 泳道)明细:角色可被单独绑定模型,这一维回答「谁在消耗」。 */}
                  {workerTokens.length > 0 && (
                    <div className="grid gap-2">
                      <p className="text-muted-foreground text-xs">
                        {t("tokens.byWorkerTitle")} <span className="opacity-70">{t("tokens.byWorkerHint")}</span>
                      </p>
                      <div className="overflow-x-auto">
                        <table className="w-full text-sm">
                          <thead>
                            <tr className="border-b text-left text-muted-foreground text-xs">
                              <th className="py-1.5 pr-3 font-medium">{t("tokens.colWorker")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.colCalls")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.input")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.output")}</th>
                              <th className="py-1.5 pr-3 text-right font-medium">{t("tokens.cacheRead")}</th>
                              <th className="py-1.5 text-right font-medium">{t("tokens.colHitRate")}</th>
                            </tr>
                          </thead>
                          <tbody>
                            {workerTokens.map((w) => (
                              <tr key={w.worker} className="border-b last:border-0">
                                <td className="py-1.5 pr-3 font-mono text-xs">{w.worker}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">{w.calls}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(w.input_tokens)}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(w.output_tokens)}</td>
                                <td className="py-1.5 pr-3 text-right tabular-nums">
                                  {fmtTokens(w.cache_read_tokens)}
                                </td>
                                <td className="py-1.5 text-right text-emerald-500 tabular-nums">
                                  {cacheHitRate(w.cache_read_tokens, w.input_tokens)}
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    </div>
                  )}
                </>
              ) : (
                <p className="text-muted-foreground text-sm">{t("tokens.empty")}</p>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}

const TASK_RULE_KIND_OPTIONS: { value: AssetInterceptKind; placeholder: string }[] = [
  { value: "exact_domain", placeholder: "example.gov.cn" },
  { value: "exact_ip", placeholder: "203.0.113.10" },
  { value: "exact_url", placeholder: "https://example.com/login" },
  { value: "fuzzy_domain", placeholder: ".gov.cn" },
  { value: "fuzzy_ip", placeholder: "203.0.113." },
  { value: "fuzzy_url", placeholder: "/admin" },
  { value: "cidr", placeholder: "192.168.0.0/16" },
];

// TaskInterceptRulesCard 管理任务详情中的任务级资产拦截、允许规则：
// 列表、添加、行内编辑、删除和启用开关。仅影响当前任务，不进入全局规则表。
function TaskInterceptRulesCard({ taskId }: { taskId: string }) {
  const t = useTranslations("taskDetail.overviewTab");
  const [rules, setRules] = React.useState<AssetInterceptRule[]>([]);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState("");
  const [newAction, setNewAction] = React.useState<"block" | "allow">("block");
  const [newKind, setNewKind] = React.useState<AssetInterceptKind>("fuzzy_domain");
  const [newPattern, setNewPattern] = React.useState("");
  const [newNote, setNewNote] = React.useState("");
  const [editId, setEditId] = React.useState<number | null>(null);
  const [editAction, setEditAction] = React.useState<"block" | "allow">("block");
  const [editKind, setEditKind] = React.useState<AssetInterceptKind>("fuzzy_domain");
  const [editPattern, setEditPattern] = React.useState("");
  const [editNote, setEditNote] = React.useState("");

  const load = React.useCallback(async () => {
    try {
      setRules(await api.taskInterceptRules(taskId));
    } catch {
      // 忽略短暂错误。
    }
  }, [taskId]);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function add() {
    if (!newPattern.trim()) return;
    setBusy(true);
    setErr("");
    try {
      await api.createTaskInterceptRule(taskId, {
        action: newAction,
        kind: newKind,
        pattern: newPattern.trim(),
        note: newNote.trim(),
        enabled: true,
      });
      setNewPattern("");
      setNewNote("");
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function startEdit(r: AssetInterceptRule) {
    setEditId(r.id);
    setEditAction(r.action ?? "block");
    setEditKind(r.kind);
    setEditPattern(r.pattern);
    setEditNote(r.note);
    setErr("");
  }

  async function saveEdit(r: AssetInterceptRule) {
    if (!editPattern.trim()) return;
    setBusy(true);
    setErr("");
    try {
      await api.updateTaskInterceptRule(taskId, r.id, {
        action: editAction,
        kind: editKind,
        pattern: editPattern.trim(),
        note: editNote.trim(),
        enabled: r.enabled,
      });
      setEditId(null);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(r: AssetInterceptRule) {
    setRules((prev) => prev.filter((x) => x.id !== r.id));
    try {
      await api.deleteTaskInterceptRule(taskId, r.id);
    } catch {
      await load();
    }
  }

  async function toggle(r: AssetInterceptRule) {
    setRules((prev) => prev.map((x) => (x.id === r.id ? { ...x, enabled: !x.enabled } : x)));
    try {
      await api.toggleTaskInterceptRule(taskId, r.id, !r.enabled);
    } catch {
      await load();
    }
  }

  const placeholder = TASK_RULE_KIND_OPTIONS.find((o) => o.value === newKind)?.placeholder ?? "";

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldCheckIcon className="size-4 text-sky-500" /> {t("rule.title")}
          <span className="font-normal text-muted-foreground text-xs">{t("rule.hint", { count: rules.length })}</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* 添加表单。 */}
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect size="sm" value={newAction} onChange={(e) => setNewAction(e.target.value as "block" | "allow")}>
            <NativeSelectOption value="block">{t("rule.block")}</NativeSelectOption>
            <NativeSelectOption value="allow">{t("rule.allow")}</NativeSelectOption>
          </NativeSelect>
          <NativeSelect size="sm" value={newKind} onChange={(e) => setNewKind(e.target.value as AssetInterceptKind)}>
            {TASK_RULE_KIND_OPTIONS.map((o) => (
              <NativeSelectOption key={o.value} value={o.value}>
                {t(`rule.kind.${o.value}`)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <Input
            className="h-7 min-w-56 flex-1 text-sm"
            placeholder={placeholder}
            value={newPattern}
            onChange={(e) => setNewPattern(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void add();
            }}
            disabled={busy}
          />
          <Input
            className="h-7 w-36 text-sm"
            placeholder={t("rule.notePlaceholder")}
            value={newNote}
            onChange={(e) => setNewNote(e.target.value)}
            disabled={busy}
          />
          <Button size="sm" variant="outline" disabled={busy || !newPattern.trim()} onClick={() => void add()}>
            <PlusIcon className="size-3.5" /> {t("add")}
          </Button>
          {err && <span className="text-red-500 text-xs">{err}</span>}
        </div>
        {/* 规则列表。 */}
        {rules.length > 0 ? (
          <div className="flex flex-col gap-1.5">
            {rules.map((r) =>
              editId === r.id ? (
                <div key={r.id} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                  <NativeSelect
                    size="sm"
                    value={editAction}
                    onChange={(e) => setEditAction(e.target.value as "block" | "allow")}
                  >
                    <NativeSelectOption value="block">{t("rule.block")}</NativeSelectOption>
                    <NativeSelectOption value="allow">{t("rule.allow")}</NativeSelectOption>
                  </NativeSelect>
                  <NativeSelect
                    size="sm"
                    value={editKind}
                    onChange={(e) => setEditKind(e.target.value as AssetInterceptKind)}
                  >
                    {TASK_RULE_KIND_OPTIONS.map((o) => (
                      <NativeSelectOption key={o.value} value={o.value}>
                        {t(`rule.kind.${o.value}`)}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <Input
                    className="h-7 min-w-56 flex-1 text-sm"
                    value={editPattern}
                    onChange={(e) => setEditPattern(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void saveEdit(r);
                      if (e.key === "Escape") setEditId(null);
                    }}
                    disabled={busy}
                    autoFocus
                  />
                  <Input
                    className="h-7 w-36 text-sm"
                    placeholder={t("rule.notePlaceholder")}
                    value={editNote}
                    onChange={(e) => setEditNote(e.target.value)}
                    disabled={busy}
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy || !editPattern.trim()}
                    onClick={() => void saveEdit(r)}
                  >
                    <CheckIcon className="size-3.5 text-emerald-500" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy}
                    onClick={() => setEditId(null)}
                  >
                    <XIcon className="size-3.5" />
                  </Button>
                </div>
              ) : (
                <div key={r.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                  <span
                    className={`shrink-0 rounded px-1.5 py-0.5 text-xs ${
                      r.action === "allow"
                        ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
                        : "bg-red-500/15 text-red-600 dark:text-red-400"
                    }`}
                  >
                    {r.action === "allow" ? t("rule.allow") : t("rule.block")}
                  </span>
                  <span className="shrink-0 text-muted-foreground text-xs">{t(`rule.kind.${r.kind}`)}</span>
                  <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-0.5 text-xs">{r.pattern}</code>
                  {r.note && (
                    <span className="max-w-[120px] shrink-0 truncate text-muted-foreground text-xs">{r.note}</span>
                  )}
                  <Switch checked={r.enabled} onCheckedChange={() => void toggle(r)} />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy}
                    onClick={() => startEdit(r)}
                  >
                    <PencilIcon className="size-3.5" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy}
                    onClick={() => void remove(r)}
                  >
                    <Trash2Icon className="size-3.5 text-red-500" />
                  </Button>
                </div>
              ),
            )}
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">{t("rule.empty")}</p>
        )}
      </CardContent>
    </Card>
  );
}
