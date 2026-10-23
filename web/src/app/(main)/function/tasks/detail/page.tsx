"use client";

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ActivityIcon,
  ArchiveIcon,
  ArrowLeftIcon,
  BrainIcon,
  CheckIcon,
  CircleAlertIcon,
  CrosshairIcon,
  FlameIcon,
  PauseIcon,
  PlayIcon,
  ShieldAlertIcon,
  SparklesIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { TaskLLMProfileChain } from "@/components/task-llm-profile-chain";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";
import { cn } from "@/lib/utils";

import { AssetsTab } from "./_tabs/assets-tab";
import { BroadcastTab } from "./_tabs/broadcast-tab";
import { CoverageGraphTab } from "./_tabs/coverage-graph-tab";
import { FindingsTab } from "./_tabs/findings-tab";
import { GraphTab } from "./_tabs/graph-tab";
import { InterceptTab } from "./_tabs/intercept-tab";
import { OverviewTab } from "./_tabs/overview-tab";
import { ReportTab } from "./_tabs/report-tab";
import { RetestsTab } from "./_tabs/retests-tab";
import { SessionsTab } from "./_tabs/sessions-tab";
import { SoftwareRETab } from "./_tabs/software-re-tab";
import { TimelineTab } from "./_tabs/timeline-tab";

// Tactical tab clusters: preserves all 11 tabs and their URL/value contracts while
// grouping them visually by operational mental model (Command | Findings | Surface | Governance).
const SOFTWARE_RE_TAB_CLUSTERS = [
  ["software", "sessions", "overview", "graph"],
  ["report"],
  ["intercept", "timeline"],
] as const;

const TAB_CLUSTERS = [
  ["sessions", "overview", "graph"],
  ["findings", "retests", "report"],
  ["assets", "coverage", "broadcast"],
  ["intercept", "timeline"],
] as const;

function taskProfileIDs(task: Task): string[] {
  if (task.llm_profile_ids && task.llm_profile_ids.length > 0) {
    return task.llm_profile_ids.map(String);
  }
  return task.llm_profile_id ? [String(task.llm_profile_id)] : [];
}

function TaskLLMControl({ task, profiles, onUpdated }: { task: Task; profiles: LLMProfile[]; onUpdated: () => void }) {
  const t = useTranslations("taskDetail.llm");
  const [open, setOpen] = React.useState(false);
  const [profileIDs, setProfileIDs] = React.useState<string[]>(() => taskProfileIDs(task));
  const [activeProfileID, setActiveProfileID] = React.useState(
    task.active_llm_profile_id ? String(task.active_llm_profile_id) : (taskProfileIDs(task)[0] ?? ""),
  );
  const [saving, setSaving] = React.useState(false);
  const popoverContentRef = React.useRef<HTMLDivElement>(null);

  const chain = taskProfileIDs(task);
  const exhausted = task.llm_failover_state === "chain_exhausted";
  // 任何状态都可以改链,终态也不例外:任务结束后主 Agent 对话仍走这条链,
  // 链上模型出问题时必须能换掉,否则已完成任务就没法继续交互。
  const terminal = ["done", "failed", "timeout"].includes(task.status);
  // A null active profile on an exhausted, non-empty chain is a persisted end
  // cursor. Keep the status display honest; choosing the first profile is only
  // the editor's reset draft and does not mean it is currently active.
  let activeID = chain[0] ?? "";
  if (exhausted) activeID = "";
  if (task.active_llm_profile_id) activeID = String(task.active_llm_profile_id);
  const activeProfile = profiles.find((profile) => profile.id === activeID);
  const currentLabel = exhausted
    ? t("exhausted")
    : (activeProfile?.name ?? (activeID ? t("profileRef", { id: activeID }) : t("followDefault")));
  const activeIndex = chain.indexOf(activeID);
  const backupCount = activeIndex >= 0 ? Math.max(0, chain.length - activeIndex - 1) : 0;
  const currentTitle = [
    currentLabel,
    activeProfile?.model,
    backupCount > 0 ? t("backupCount", { count: backupCount }) : "",
  ]
    .filter(Boolean)
    .join(" · ");
  let editorDescription = t("editorDescription");
  if (terminal) editorDescription = t("editorDescriptionTerminal");
  let saveLabel = t("save");
  if (exhausted) saveLabel = t("saveReset");
  if (saving) saveLabel = t("saving");

  const syncDraft = React.useCallback(() => {
    const next = taskProfileIDs(task);
    setProfileIDs(next);
    setActiveProfileID(task.active_llm_profile_id ? String(task.active_llm_profile_id) : (next[0] ?? ""));
  }, [task]);

  const handleOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) syncDraft();
  };

  const handleProfileIDsChange = (next: string[]) => {
    setProfileIDs(next);
    setActiveProfileID((current) => (next.includes(current) ? current : (next[0] ?? "")));
  };

  const save = async () => {
    setSaving(true);
    try {
      const result = await api.updateTaskLLMProfiles(
        task.id,
        profileIDs.map(Number),
        activeProfileID ? Number(activeProfileID) : undefined,
      );
      if (result.switch_event) {
        toast.info(result.switch_event.summary, { id: `task-${task.id}-llm-${result.switch_event.seq}` });
      } else {
        toast.success(
          result.reopened_intents > 0 ? t("updatedWithReopened", { count: result.reopened_intents }) : t("updated"),
        );
      }
      setOpen(false);
      onUpdated();
    } catch (error) {
      toast.error(t("updateFailed", { message: (error as Error).message }));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <Button
          size="sm"
          variant={exhausted ? "destructive" : "outline"}
          className="h-7 gap-1.5 rounded-lg px-2.5 text-xs"
          aria-label={t("controlAria")}
          title={currentTitle}
        >
          <BrainIcon className="size-3.5 text-primary" data-icon="inline-start" />
          <span className="hidden max-w-36 truncate lg:inline">{currentLabel}</span>
          {backupCount > 0 && <span className="hidden text-muted-foreground xl:inline">+{backupCount}</span>}
        </Button>
      </PopoverTrigger>
      <PopoverContent ref={popoverContentRef} align="start" className="w-[min(28rem,calc(100vw-2rem))] gap-4 p-4">
        <PopoverHeader>
          <PopoverTitle>{t("chainTitle")}</PopoverTitle>
          <PopoverDescription>{editorDescription}</PopoverDescription>
        </PopoverHeader>

        {exhausted && (
          <Alert variant="destructive">
            <CircleAlertIcon />
            <AlertTitle>{t("exhaustedTitle")}</AlertTitle>
            <AlertDescription>{task.llm_failover_reason ?? t("exhaustedDesc")}</AlertDescription>
          </Alert>
        )}

        <TaskLLMProfileChain
          profiles={profiles}
          value={profileIDs}
          onValueChange={handleProfileIDsChange}
          activeProfileId={activeProfileID}
          onActiveProfileChange={setActiveProfileID}
          inputId="task-llm-profiles"
          disabled={saving}
          portalContainer={popoverContentRef}
        />

        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => setOpen(false)}>
            {t("close")}
          </Button>
          <Button type="button" size="sm" onClick={() => void save()} disabled={saving}>
            {saving && <Spinner data-icon="inline-start" />}
            {saveLabel}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function TaskDetailInner() {
  const t = useTranslations("taskDetail");
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const [task, setTask] = React.useState<Task | null>(null);
  const [paused, setPaused] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState("sessions");
  // 未收口分支数（完成闸门）：坏终态且没被续派/收口的分支。进页面拉一次 + 15s 轮询，
  // 数字掉了说明分支已回炉跑完或被人工收口。
  const [openBranches, setOpenBranches] = React.useState(0);
  React.useEffect(() => {
    const id = task?.id;
    if (!id) return;
    let alive = true;
    const load = () => {
      api
        .taskBranches(id)
        .then((r) => {
          if (alive) setOpenBranches(r.count || 0);
        })
        .catch(() => {
          /* 角标是增强信息，读不到就不显示，不影响主流程 */
        });
    };
    load();
    const timer = window.setInterval(load, 15000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [task?.id]);
  const [interceptPendingCount, setInterceptPendingCount] = React.useState(0);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [archiving, setArchiving] = React.useState(false);
  const [distilling, setDistilling] = React.useState(false);

  React.useEffect(() => {
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => setProfiles([]));
  }, []);

  // 待审批计数：5s 轮询改为 usePolling 统一管理（可见性暂停 / 去重 / 退避）。
  usePolling(
    async () => {
      const rows = await api.interceptTask(id);
      setInterceptPendingCount(rows.filter((r) => r.status === "pending").length);
    },
    { intervalMs: 5000 },
  );

  // 任务本体 + stats：在途去重原先靠 taskLoadInFlight 手工维护，现在由
  // usePolling 保证同一时刻只有一轮在跑；这里只保留「结果是否仍属于当前 id」
  // 的守卫，防止切任务后旧响应覆盖新页面。
  const loadTask = React.useCallback(async () => {
    const [taskResp, s] = await Promise.all([api.task(id), api.stats(id).catch(() => null)]);
    const base = { ...taskResp };
    const at = s?.active_task;
    if (at) {
      base.in_flight = at.in_flight;
      base.goals_total = at.goals_total;
      base.goals_met = at.goals_met;
      base.engine_mode = s?.engine_mode ?? at.engine_mode;
      base.paused = at.paused;
    }
    setTask(base);
    setPaused(at?.paused ?? base.paused ?? false);
    setLoaded(true);
  }, [id]);
  usePolling(loadTask, { intervalMs: 5000, enabled: Boolean(id) });

  async function togglePause() {
    if (task && ["done", "failed", "timeout"].includes(task.status)) return;
    const next = !paused;
    try {
      await api.controlTask(id, next ? "pause" : "resume");
      setPaused(next);
      toast.success(next ? t("control.paused") : t("control.resumed"));
    } catch (e) {
      toast.error(t("control.failed", { message: (e as Error).message }));
    }
  }

  async function archiveTask() {
    if (!task || archiving) return;
    setArchiving(true);
    try {
      await api.archiveTask(task.id);
      toast.success(t("archive.queued"));
      router.push("/function/tasks");
    } catch (error) {
      toast.error(t("archive.failed", { message: (error as Error).message }));
      setArchiving(false);
    }
  }

  async function distillTask() {
    if (!task || distilling) return;
    setDistilling(true);
    try {
      const res = await api.distillTaskSkill({ task_id: Number(task.id) });
      const prop = res.proposal;
      toast.success(
        prop.status === "applied"
          ? t("distill.applied", { skill: prop.skill })
          : t("distill.pending", { skill: prop.skill }),
        {
          action: {
            label: t("distill.openReview"),
            onClick: () => router.push(`/system/skills?proposal=${encodeURIComponent(prop.id)}`),
          },
        },
      );
    } catch (error) {
      toast.error(t("distill.failed", { message: (error as Error).message }));
    } finally {
      setDistilling(false);
    }
  }

  if (!task) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
        <p className="text-muted-foreground">{loaded ? t("header.notFound", { id }) : t("header.loading")}</p>
        {loaded && (
          <Button asChild variant="outline">
            <Link href="/function/tasks">
              <ArrowLeftIcon /> {t("header.backToList")}
            </Link>
          </Button>
        )}
      </div>
    );
  }

  const completed = task.status === "done";
  const terminal = ["done", "failed", "timeout"].includes(task.status);
  const archiveLifecycleEligible = terminal || paused || task.status === "paused";
  const canArchive = archiveLifecycleEligible && !task.archive_blocked_by_task_id;
  let archiveDisabledReason = task.queued ? t("archive.disabledQueued") : t("archive.disabledRunning");
  if (archiveLifecycleEligible && task.archive_blocked_by_task_id) {
    archiveDisabledReason = t("archive.disabledInherited", { id: task.archive_blocked_by_task_id });
  }
  const engineMode = paused ? "paused" : (task.engine_mode ?? "idle");
  let controlVariant: "default" | "secondary" | "outline" = "outline";
  let controlIcon = <PauseIcon data-icon="inline-start" />;
  let controlLabel = t("control.pause");
  if (terminal) {
    controlVariant = "secondary";
    controlIcon = <CheckIcon data-icon="inline-start" />;
    controlLabel = completed ? t("control.completed") : t("control.ended");
  } else if (paused) {
    controlVariant = "default";
    controlIcon = <PlayIcon data-icon="inline-start" />;
    controlLabel = t("control.resume");
  }

  const totalFindings =
    (task.findings?.critical ?? 0) +
    (task.findings?.high ?? 0) +
    (task.findings?.medium ?? 0) +
    (task.findings?.low ?? 0);
  const inFlightCount = task.in_flight ?? 0;
  const goalsTotal = task.goals_total ?? 0;
  const goalsMet = task.goals_met ?? 0;

  const archiveTrigger = (
    <Button
      size="icon-sm"
      variant="ghost"
      className="size-7"
      disabled={!canArchive || archiving}
      aria-label={canArchive ? t("archive.triggerAria") : archiveDisabledReason}
    >
      {archiving ? <Spinner /> : <ArchiveIcon className="size-3.5" />}
    </Button>
  );

  return (
    <Tabs value={tab} onValueChange={setTab} className="flex flex-1 flex-col gap-0">
      {task.analysis_domain === "software_re" && (
        <Alert className="rounded-none border-x-0 border-t-0">
          <BrainIcon className="size-4" />
          <AlertTitle>{t("analysisDomain.title")}</AlertTitle>
          <AlertDescription>{t("analysisDomain.hint")}</AlertDescription>
        </Alert>
      )}
      {/* Unified Mission Control Header */}
      <header className="sticky top-0 z-10 flex flex-col gap-2 border-border/80 border-b bg-background/90 px-4 py-2 backdrop-blur-md lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Button asChild variant="ghost" size="icon-sm" className="size-7 rounded-lg">
            <Link href="/function/tasks">
              <ArrowLeftIcon className="size-4" />
            </Link>
          </Button>
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <h1 className="min-w-0 truncate font-semibold text-sm tracking-tight" title={task.name || task.description}>
              {task.name || task.description}
            </h1>
            <code className="hidden shrink-0 rounded-md border border-border/60 bg-muted/70 px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground sm:inline">
              #{task.id}
            </code>
            <Badge variant="outline">
              {t(task.analysis_domain === "software_re" ? "analysisDomain.software" : "analysisDomain.web")}
            </Badge>
            <StatusBadge domain={terminal ? "task" : "engine"} value={terminal ? task.status : engineMode} dot />
            {openBranches > 0 && (
              <Badge variant="destructive" title={t("openBranchesTip")}>
                {t("openBranches", { n: openBranches })}
              </Badge>
            )}
          </div>

          {/* Live Mission Pulse Pills (Quick-Switch Telemetry) */}
          <div className="hidden items-center gap-1.5 md:flex">
            {task.analysis_domain !== "software_re" && (
              <button
                type="button"
                onClick={() => setTab("findings")}
                className={cn(
                  "inline-flex h-7 items-center gap-1.5 rounded-lg border px-2.5 font-medium text-xs tabular-nums transition-colors",
                  totalFindings > 0
                    ? "border-rose-500/35 bg-rose-500/10 text-rose-600 hover:bg-rose-500/20 dark:text-rose-400"
                    : "border-border/70 bg-muted/40 text-muted-foreground hover:bg-accent hover:text-foreground",
                )}
                title={t("tabs.findings")}
              >
                <FlameIcon className="size-3.5" />
                <span>{t("tabs.findings")}</span>
                <span className="font-semibold">{totalFindings}</span>
              </button>
            )}

            <button
              type="button"
              onClick={() => setTab("graph")}
              className={cn(
                "inline-flex h-7 items-center gap-1.5 rounded-lg border px-2.5 font-medium text-xs tabular-nums transition-colors",
                inFlightCount > 0
                  ? "border-sky-500/35 bg-sky-500/10 text-sky-600 hover:bg-sky-500/20 dark:text-sky-400"
                  : "border-border/70 bg-muted/40 text-muted-foreground hover:bg-accent hover:text-foreground",
              )}
              title={t("tabs.graph")}
            >
              <ActivityIcon className="size-3.5" />
              <span>Worker</span>
              <span className="font-semibold">{inFlightCount}</span>
            </button>

            {goalsTotal > 0 && (
              <button
                type="button"
                onClick={() => setTab("overview")}
                className="inline-flex h-7 items-center gap-1.5 rounded-lg border border-border/70 bg-muted/40 px-2.5 font-medium text-muted-foreground text-xs tabular-nums transition-colors hover:bg-accent hover:text-foreground"
                title={t("tabs.overview")}
              >
                <CrosshairIcon className="size-3.5 text-primary" />
                <span>
                  {goalsMet}/{goalsTotal}
                </span>
              </button>
            )}
          </div>

          <Separator orientation="vertical" className="mx-0.5 hidden h-4 sm:block" />
          <TaskLLMControl task={task} profiles={profiles} onUpdated={loadTask} />

          {interceptPendingCount > 0 && (
            <Link
              href="/system/intercept/approvals"
              className="shrink-0 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              title={t("header.pendingApprovalsTitle", { n: interceptPendingCount })}
            >
              <Badge
                variant="outline"
                className="gap-1 border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400"
              >
                <ShieldAlertIcon className="size-3.5" />
                <span className="hidden sm:inline">{t("header.pendingApprovals", { n: interceptPendingCount })}</span>
                <span className="sm:hidden">×{interceptPendingCount}</span>
              </Badge>
            </Link>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                size="sm"
                variant="outline"
                className="h-7 gap-1.5 rounded-lg px-2.5 text-xs"
                disabled={distilling}
                onClick={() => void distillTask()}
                aria-label={t("distill.triggerAria")}
              >
                {distilling ? <Spinner data-icon="inline-start" /> : <SparklesIcon className="size-3.5 text-primary" />}
                <span className="hidden sm:inline">{t("distill.button")}</span>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{t("distill.tooltip")}</TooltipContent>
          </Tooltip>
          {canArchive ? (
            <AlertDialog>
              <AlertDialogTrigger asChild>{archiveTrigger}</AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>{t("archive.dialogTitle", { id: task.id })}</AlertDialogTitle>
                  <AlertDialogDescription>{t("archive.dialogDesc")}</AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>{t("archive.cancel")}</AlertDialogCancel>
                  <AlertDialogAction onClick={() => void archiveTask()}>{t("archive.confirm")}</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          ) : (
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex">{archiveTrigger}</span>
              </TooltipTrigger>
              <TooltipContent>{archiveDisabledReason}</TooltipContent>
            </Tooltip>
          )}
          <Button
            size="sm"
            variant={controlVariant}
            className="h-7 rounded-lg px-2.5 text-xs"
            onClick={() => void togglePause()}
            disabled={terminal}
          >
            {controlIcon}
            {controlLabel}
          </Button>
        </div>

        {/* Sub-row: Compact target goal pill + Clustered Tactical Tabs */}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="no-scrollbar min-w-0 flex-1 overflow-x-auto">
            <TabsList variant="default" className="h-8 min-w-max gap-0.5 bg-muted/55 p-0.5">
              {(task.analysis_domain === "software_re" ? SOFTWARE_RE_TAB_CLUSTERS : TAB_CLUSTERS).map(
                (cluster, clusterIdx) => (
                  <React.Fragment key={cluster[0]}>
                    {clusterIdx > 0 && <span className="mx-1 h-4 w-px shrink-0 self-center bg-border/80" />}
                    {cluster.map((value) => (
                      <TabsTrigger key={value} value={value} className="h-7 px-2.5 text-xs">
                        {t(`tabs.${value}`)}
                        {value === "findings" && totalFindings > 0 && (
                          <span className="ml-1.5 inline-flex h-4 min-w-[16px] items-center justify-center rounded-full bg-rose-500/20 px-1 font-mono font-semibold text-[10px] text-rose-600 leading-none dark:text-rose-400">
                            {totalFindings > 99 ? "99+" : totalFindings}
                          </span>
                        )}
                        {value === "intercept" && interceptPendingCount > 0 && (
                          <span className="ml-1.5 inline-flex h-4 min-w-[16px] items-center justify-center rounded-full bg-amber-500 px-1 font-semibold text-[10px] text-white leading-none">
                            {interceptPendingCount > 99 ? "99+" : interceptPendingCount}
                          </span>
                        )}
                      </TabsTrigger>
                    ))}
                  </React.Fragment>
                ),
              )}
            </TabsList>
          </div>
          {task.goal && (
            <div
              className="hidden max-w-md items-center gap-1.5 truncate rounded-lg border border-border/60 bg-muted/35 px-2.5 py-1 text-muted-foreground text-xs xl:flex"
              title={task.goal}
            >
              <CrosshairIcon className="size-3 shrink-0 text-primary" />
              <span className="truncate">{task.goal}</span>
            </div>
          )}
        </div>
      </header>

      {/* Tab content */}
      <div className={cn("flex-1", tab === "sessions" ? "p-2.5 lg:p-4" : "p-4 lg:p-6")}>
        {task.analysis_domain === "software_re" && (
          <TabsContent value="software" className="mt-0">
            <SoftwareRETab key={id} taskId={id} />
          </TabsContent>
        )}
        <TabsContent value="sessions" className="mt-0">
          <SessionsTab taskId={id} />
        </TabsContent>
        <TabsContent value="overview" className="mt-0">
          <OverviewTab taskId={id} />
        </TabsContent>
        <TabsContent value="graph" className="mt-0">
          <GraphTab taskId={id} />
        </TabsContent>
        <TabsContent value="broadcast" className="mt-0">
          <BroadcastTab taskId={id} />
        </TabsContent>
        <TabsContent value="findings" className="mt-0">
          <FindingsTab taskId={id} />
        </TabsContent>
        <TabsContent value="retests" className="mt-0">
          <RetestsTab key={id} taskId={id} />
        </TabsContent>
        <TabsContent value="assets" className="mt-0">
          <AssetsTab taskId={id} />
        </TabsContent>
        <TabsContent value="coverage" className="mt-0">
          <CoverageGraphTab taskId={id} coverageEnabled={task?.coverage_enabled !== false} />
        </TabsContent>
        <TabsContent value="intercept" className="mt-0">
          <InterceptTab taskId={id} />
        </TabsContent>
        <TabsContent value="timeline" className="mt-0">
          <TimelineTab taskId={id} onReverted={loadTask} />
        </TabsContent>
        <TabsContent value="report" className="mt-0">
          <ReportTab taskId={id} analysisDomain={task.analysis_domain} />
        </TabsContent>
      </div>
    </Tabs>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function TaskDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <TaskDetailInner />
    </React.Suspense>
  );
}
