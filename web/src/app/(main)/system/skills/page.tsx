"use client";

import * as React from "react";

import {
  AlertTriangleIcon,
  CheckCircle2Icon,
  ChevronRightIcon,
  DownloadIcon,
  FilePlusIcon,
  FileTextIcon,
  FolderIcon,
  FolderOpenIcon,
  FolderPlusIcon,
  PencilIcon,
  PlusIcon,
  SparklesIcon,
  Trash2Icon,
  UploadIcon,
  XCircleIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

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
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, api } from "@/lib/api";
import type { Agent, MCPServer, MissingSkill, SkillCall, SkillItem, SkillProposal } from "@/lib/types";
import { cn } from "@/lib/utils";

const BUILTIN_SKILL_NAMES = new Set([
  "api-recon",
  "playwright-cli",
  "scopesentry-mcp",
  "web-vuln-tactics",
  "reverse-engineering",
  "exploit-kb-patterns",
  "expert-triage-pivot",
  "human-verification",
  "ai-coding-sandbox-tactics",
  "wasm-cloud-saas-tactics",
]);

const REPORT_TACTIC_SKILL_NAMES = new Set(["ai-coding-sandbox-tactics", "wasm-cloud-saas-tactics"]);

function fmtTime(ts: string | undefined, never: string) {
  if (!ts) return never;
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// ── Tree node ──────────────────────────────────────────────────────────────
interface TreeNode {
  name: string;
  path: string; // relative to skill root, dirs WITHOUT trailing slash
  type: "file" | "dir";
  children: TreeNode[];
}

// Backend returns dirs as "scripts/" (trailing slash) and files as "scripts/a.py".
function buildTree(entries: string[]): TreeNode[] {
  const root: TreeNode[] = [];
  const dirMap = new Map<string, TreeNode>();

  function ensureDir(dirPath: string, _parentNodes: TreeNode[]): TreeNode[] {
    const existing = dirMap.get(dirPath);
    if (existing) return existing.children;
    const parts = dirPath.split("/");
    let nodes = root;
    let cur = "";
    for (const part of parts) {
      cur = cur ? `${cur}/${part}` : part;
      let dir = dirMap.get(cur);
      if (!dir) {
        dir = { name: part, path: cur, type: "dir", children: [] };
        nodes.push(dir);
        dirMap.set(cur, dir);
      }
      nodes = dir.children;
    }
    return nodes;
  }

  for (const entry of [...entries].sort()) {
    if (entry.endsWith("/")) {
      // Explicit directory entry — ensure the node exists (may already be created)
      ensureDir(entry.slice(0, -1), root);
    } else {
      // File — ensure parent dirs exist, then push file node
      const parts = entry.split("/");
      let nodes = root;
      let cur = "";
      for (let i = 0; i < parts.length - 1; i++) {
        cur = cur ? `${cur}/${parts[i]}` : parts[i];
        let dir = dirMap.get(cur);
        if (!dir) {
          dir = { name: parts[i], path: cur, type: "dir", children: [] };
          nodes.push(dir);
          dirMap.set(cur, dir);
        }
        nodes = dir.children;
      }
      const fname = parts[parts.length - 1];
      if (fname) nodes.push({ name: fname, path: entry, type: "file", children: [] });
    }
  }
  sortNodes(root);
  return root;
}

// sortNodes orders every level like a file explorer: directories before files,
// then case-insensitive by name. Recurses into children.
function sortNodes(nodes: TreeNode[]): void {
  nodes.sort((a, b) => {
    if (a.type !== b.type) return a.type === "dir" ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
  for (const n of nodes) {
    if (n.children.length > 0) sortNodes(n.children);
  }
}

// ── State types ───────────────────────────────────────────────────────────
type Selected = { skill: string; path: null } | { skill: string; path: string };

type Creating = {
  skill: string;
  inDir: string;
  kind: "file" | "dir";
} | null;

type PendingDelete =
  | { kind: "skill"; skill: string }
  | { kind: "file"; skill: string; path: string }
  | { kind: "dir"; skill: string; path: string }
  | null;

// ── Overview (empty-state) ──────────────────────────────────────────────────
// Shown when nothing is selected: a library-wide snapshot from data already loaded
// (the skill list carries per-skill usage; missing is fetched alongside). No extra
// requests — this is pure aggregation over props.
function SkillsOverview({
  skills,
  missing,
  pendingProposalsCount,
  onSelect,
  onOpenProposals,
}: {
  skills: SkillItem[];
  missing: MissingSkill[];
  pendingProposalsCount: number;
  onSelect: (name: string) => void;
  onOpenProposals: () => void;
}) {
  const t = useTranslations("skillsPage");
  const agg = React.useMemo(() => {
    const totalCalls = skills.reduce((n, s) => n + s.calls, 0);
    const used = skills.filter((s) => s.calls > 0);
    const ranked = [...used].sort((a, b) => b.calls - a.calls);
    const neverUsed = skills.filter((s) => s.calls === 0);
    const recent = skills
      .filter((s) => s.last_used)
      .sort((a, b) => (a.last_used! < b.last_used! ? 1 : -1))
      .slice(0, 6);
    const missingCalls = missing.reduce((n, m) => n + m.calls, 0);
    return {
      totalCalls,
      usedCount: used.length,
      ranked,
      topCalls: ranked[0]?.calls ?? 0,
      neverUsed,
      recent,
      missingCalls,
    };
  }, [skills, missing]);

  if (skills.length === 0) {
    return (
      <div className="flex h-full items-center justify-center">
        <EmptyState illustration="terminal" title={t("emptyOverview")} />
      </div>
    );
  }

  const stats: { label: string; value: React.ReactNode; hint?: string }[] = [
    { label: t("statTotal"), value: skills.length, hint: t("statTotalHint", { count: agg.usedCount }) },
    { label: t("statCalls"), value: agg.totalCalls },
    {
      label: t("statUnused"),
      value: agg.neverUsed.length,
      hint: agg.neverUsed.length > 0 ? t("statUnusedHintSome") : t("statUnusedHintNone"),
    },
    {
      label: t("statMissing"),
      value: agg.missingCalls,
      hint: missing.length > 0 ? t("statMissingHintSome", { count: missing.length }) : t("statMissingHintNone"),
    },
  ];

  return (
    <div className="mx-auto w-full max-w-3xl space-y-6">
      <div>
        <h2 className="font-semibold text-base">{t("overviewTitle")}</h2>
        <p className="text-muted-foreground text-sm">{t("overviewDesc")}</p>
      </div>

      {pendingProposalsCount > 0 && (
        <div className="flex items-center justify-between gap-3 rounded-lg border border-primary/30 bg-primary/5 px-3.5 py-2.5">
          <div className="flex items-center gap-2 text-xs">
            <SparklesIcon className="size-4 shrink-0 text-primary" />
            <span className="font-medium">{t("pendingBannerText", { count: pendingProposalsCount })}</span>
          </div>
          <Button size="sm" variant="default" className="h-7 text-xs" onClick={onOpenProposals}>
            {t("pendingBannerAction")}
          </Button>
        </div>
      )}

      {/* 指标卡 */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {stats.map((s) => (
          <div key={s.label} className="rounded-lg border p-3">
            <p className="font-semibold text-2xl tabular-nums">{s.value}</p>
            <p className="font-medium text-xs">{s.label}</p>
            {s.hint && <p className="mt-0.5 text-[11px] text-muted-foreground">{s.hint}</p>}
          </div>
        ))}
      </div>

      {/* 调用排行 */}
      <div className="space-y-2">
        <Label className="text-muted-foreground text-xs">{t("ranking")}</Label>
        {agg.ranked.length === 0 ? (
          <p className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-xs">
            {t("rankingEmpty")}
          </p>
        ) : (
          <div className="space-y-1.5">
            {agg.ranked.slice(0, 8).map((s) => (
              <button
                key={s.name}
                type="button"
                onClick={() => onSelect(s.name)}
                className="group flex w-full items-center gap-3 rounded-md px-2 py-1.5 text-left hover:bg-muted"
              >
                <span className="w-40 shrink-0 truncate font-mono text-xs" title={s.name}>
                  {s.name}
                </span>
                <span className="relative h-2 flex-1 overflow-hidden rounded-full bg-muted">
                  <span
                    className="absolute inset-y-0 left-0 rounded-full bg-primary/70"
                    style={{ width: `${agg.topCalls > 0 ? (s.calls / agg.topCalls) * 100 : 0}%` }}
                  />
                </span>
                <span className="w-16 shrink-0 text-right text-muted-foreground text-xs tabular-nums">
                  {t("callsCount", { count: s.calls })}
                </span>
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="grid gap-6 sm:grid-cols-2">
        {/* 最近调用 */}
        <div className="space-y-2">
          <Label className="text-muted-foreground text-xs">{t("recent")}</Label>
          {agg.recent.length === 0 ? (
            <p className="text-muted-foreground text-xs">{t("recentEmpty")}</p>
          ) : (
            <div className="space-y-1">
              {agg.recent.map((s) => (
                <button
                  key={s.name}
                  type="button"
                  onClick={() => onSelect(s.name)}
                  className="flex w-full items-center gap-2 rounded px-1.5 py-1 text-left hover:bg-muted"
                >
                  <span className="min-w-0 flex-1 truncate font-mono text-xs" title={s.name}>
                    {s.name}
                  </span>
                  <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">
                    {fmtTime(s.last_used, t("neverCalled"))}
                  </span>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* 未使用（可清理 / 需曝光） */}
        <div className="space-y-2">
          <Label className="text-muted-foreground text-xs">
            {t("unusedSkills")}
            {agg.neverUsed.length > 0 && <span className="ml-1 font-normal">({agg.neverUsed.length})</span>}
          </Label>
          {agg.neverUsed.length === 0 ? (
            <p className="text-muted-foreground text-xs">{t("allUsed")}</p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {agg.neverUsed.map((s) => (
                <button key={s.name} type="button" onClick={() => onSelect(s.name)} title={s.name}>
                  <Badge
                    variant="outline"
                    className="max-w-[12rem] cursor-pointer truncate font-mono font-normal text-xs hover:bg-muted"
                  >
                    {s.name}
                  </Badge>
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ── Page ──────────────────────────────────────────────────────────────────
export default function SkillsPage() {
  const t = useTranslations("skillsPage");
  const [skills, setSkills] = React.useState<SkillItem[]>([]);
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [selected, setSelected] = React.useState<Selected | null>(null);
  const [visibility, setVisibility] = React.useState<Record<string, string[]>>({});
  const [expanded, setExpanded] = React.useState<Set<string>>(new Set());

  // file editor
  const [fileContent, setFileContent] = React.useState("");
  const [fileLoading, setFileLoading] = React.useState(false);
  const [dirty, setDirty] = React.useState(false);
  const [saving, setSaving] = React.useState(false);

  // inline create — using a ref to avoid stale closure in onBlur
  const [creating, setCreating] = React.useState<Creating>(null);
  const [newEntryName, setNewEntryName] = React.useState("");
  const inlineRef = React.useRef<HTMLInputElement>(null);
  // cancelRef prevents onBlur from committing when Escape was pressed
  const cancelRef = React.useRef(false);

  // new skill dialog
  const [newOpen, setNewOpen] = React.useState(false);
  const [newName, setNewName] = React.useState("");
  const [newDesc, setNewDesc] = React.useState("");
  const [newLicense, setNewLicense] = React.useState("");
  const [newCompat, setNewCompat] = React.useState("");
  const [newInst, setNewInst] = React.useState("");
  const [newMcps, setNewMcps] = React.useState<string[]>([]);
  const [newVisibility, setNewVisibility] = React.useState<string[]>([]);
  const [mcpOptions, setMcpOptions] = React.useState<MCPServer[]>([]);
  const [creatingSkill, setCreatingSkill] = React.useState(false);
  const [uploading, setUploading] = React.useState(false);
  const uploadRef = React.useRef<HTMLInputElement>(null);

  // skill detail — MCP edit state (optimistic, rolls back on error)
  const [detailMcps, setDetailMcps] = React.useState<string[]>([]);

  // delete confirmation
  const [pendingDelete, setPendingDelete] = React.useState<PendingDelete>(null);
  const [deleting, setDeleting] = React.useState(false);
  const [renaming, setRenaming] = React.useState<{ skill: string; value: string } | null>(null);
  const [renameBusy, setRenameBusy] = React.useState(false);

  // 调用统计：列表页的次数/最近调用随 api.skills() 一起回来；选中某个 skill 时再拉它的
  // 最近调用明细。missing = 被点名但不存在的 skill（想用但没有）。
  const [usageCalls, setUsageCalls] = React.useState<SkillCall[]>([]);
  const [usageLoading, setUsageLoading] = React.useState(false);
  const [missing, setMissing] = React.useState<MissingSkill[]>([]);

  // AI 自主沉淀：技能提议审阅队列与任务手动提炼
  const [proposalsOpen, setProposalsOpen] = React.useState(false);
  const [proposals, setProposals] = React.useState<SkillProposal[]>([]);
  const [proposalCounts, setProposalCounts] = React.useState<Record<string, number>>({});
  const [proposalFilter, setProposalFilter] = React.useState<"pending" | "applied" | "rejected" | "all">("pending");
  const [selectedProposalId, setSelectedProposalId] = React.useState<string | null>(null);
  const [proposalContent, setProposalContent] = React.useState("");
  const [reviewNote, setReviewNote] = React.useState("");
  const [reviewingAction, setReviewingAction] = React.useState<"approve" | "reject" | null>(null);
  const [distillTaskId, setDistillTaskId] = React.useState("");
  const [distillApplyDirect, setDistillApplyDirect] = React.useState(false);
  const [distilling, setDistilling] = React.useState(false);
  const [checkedProposalIds, setCheckedProposalIds] = React.useState<string[]>([]);
  const [batchBusy, setBatchBusy] = React.useState(false);
  const [mergeOpen, setMergeOpen] = React.useState(false);
  const [mergeName, setMergeName] = React.useState("merged-case-tactics");
  const [mergeDesc, setMergeDesc] = React.useState("");
  const [mergeApplyDirect, setMergeApplyDirect] = React.useState(false);
  const [mergeRejectSources, setMergeRejectSources] = React.useState(true);
  const [merging, setMerging] = React.useState(false);

  // 外部报告（TXT / JSON / MD）导入沉淀为多文件技能包
  const [importReportOpen, setImportReportOpen] = React.useState(false);
  const [importFiles, setImportFiles] = React.useState<File[]>([]);
  const [importPaste, setImportPaste] = React.useState("");
  const [importName, setImportName] = React.useState("");
  const [importDomain, setImportDomain] = React.useState<"auto" | "web_security" | "software_re">("auto");
  const [importFocus, setImportFocus] = React.useState("");
  const [importUseLLM, setImportUseLLM] = React.useState(true);
  const [importApplyDirect, setImportApplyDirect] = React.useState(true);
  const [importOverwrite, setImportOverwrite] = React.useState(true);
  const [importingReport, setImportingReport] = React.useState(false);
  const importFileRef = React.useRef<HTMLInputElement>(null);
  const [exportingSkill, setExportingSkill] = React.useState(false);

  const loadProposals = React.useCallback((status: "pending" | "applied" | "rejected" | "all" = "all") => {
    api
      .skillProposals(status, 100)
      .then((res) => {
        const list = res.proposals ?? [];
        setProposals(list);
        setProposalCounts(res.counts ?? {});
        setCheckedProposalIds((cur) => cur.filter((id) => list.some((p) => p.id === id)));
        setSelectedProposalId((cur) => {
          if (cur && list.some((p) => p.id === cur)) return cur;
          return list[0]?.id ?? null;
        });
      })
      .catch(() => {
        /* 忽略提议查询失败。 */
      });
  }, []);

  // ── Data ──────────────────────────────────────────────────────────────────
  const load = React.useCallback(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => {
        /* 查询失败时保留默认状态。 */
      });
    api
      .mcpServers()
      .then(setMcpOptions)
      .catch(() => {
        /* 查询失败时保留默认状态。 */
      });
    api
      .missingSkills()
      .then(setMissing)
      .catch(() => {
        /* 查询失败时保留默认状态。 */
      });
    loadProposals("all");
    api
      .skills()
      .then((ss) => {
        setSkills(ss);
        ss.forEach((s) => {
          void api
            .skillVisibility(s.name)
            .then((ids) => setVisibility((v) => ({ ...v, [s.name]: ids })))
            .catch(() => {
              /* 忽略可见性查询失败。 */
            });
        });
      })
      .catch(() => {
        /* 查询失败时保留默认状态。 */
      });
  }, [loadProposals]);

  React.useEffect(() => {
    load();
    if (typeof window !== "undefined") {
      const sp = new URLSearchParams(window.location.search);
      const pid = sp.get("proposal");
      const openFlag = sp.get("openProposals");
      if (pid) {
        setProposalFilter("all");
        setSelectedProposalId(pid);
        setProposalsOpen(true);
      } else if (openFlag === "1" || openFlag === "true") {
        setProposalsOpen(true);
      }
    }
  }, [load]);

  // ── Upload a .zip skill ───────────────────────────────────────────────────
  async function uploadZip(file: File, overwrite = false) {
    setUploading(true);
    try {
      const r = await api.uploadSkill(file, overwrite);
      toast.success(t("installed", { name: r.name, files: r.files }));
      load();
    } catch (e) {
      const msg = (e as Error).message;
      // Skill 已存在时 server_mgmt.go 返回 409，提示覆盖。
      // 消息随显示语言变化，因此按 status 判断，不依赖本地化文本。
      if (!overwrite && e instanceof ApiError && e.status === 409) {
        if (window.confirm(t("overwriteConfirm", { msg }))) {
          await uploadZip(file, true);
          return;
        }
      } else {
        toast.error(t("uploadFailed", { msg }));
      }
    } finally {
      setUploading(false);
    }
  }
  function onUploadPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    e.target.value = ""; // reset so picking the same file again re-fires
    if (f) void uploadZip(f);
  }

  React.useEffect(() => {
    if (creating) {
      // Small timeout so the element is actually in the DOM before focusing
      setTimeout(() => inlineRef.current?.focus(), 20);
    }
  }, [creating]);

  // Sync detail-panel MCP state when the selected skill changes.
  React.useEffect(() => {
    if (!selected || selected.path !== null) return;
    const sk = skills.find((s) => s.name === selected.skill);
    setDetailMcps(sk?.mcps ?? []);
  }, [selected, skills]);

  // Recent calls for the selected skill (detail panel only).
  React.useEffect(() => {
    if (!selected || selected.path !== null) {
      setUsageCalls([]);
      return;
    }
    const name = selected.skill;
    setUsageLoading(true);
    api
      .skillUsage(name, 20)
      .then((calls) => setUsageCalls(calls))
      .catch(() => setUsageCalls([]))
      .finally(() => setUsageLoading(false));
  }, [selected]);

  React.useEffect(() => {
    if (!selected || selected.path === null) {
      setFileContent("");
      setDirty(false);
      return;
    }
    setFileLoading(true);
    api
      .readSkillFile(selected.skill, selected.path)
      .then((c) => {
        setFileContent(c);
        setDirty(false);
      })
      .catch(() => toast.error(t("readFileFailed")))
      .finally(() => setFileLoading(false));
  }, [selected, t]);

  // ── Expand helpers ────────────────────────────────────────────────────────
  function toggleExpanded(key: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }
  function ensureExpanded(skill: string, dirPath: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      next.add(skill);
      if (dirPath) {
        let cur = "";
        for (const part of dirPath.split("/")) {
          cur = cur ? `${cur}/${part}` : part;
          next.add(`${skill}:${cur}`);
        }
      }
      return next;
    });
  }

  // ── Inline create ─────────────────────────────────────────────────────────
  function startCreate(skill: string, inDir: string, kind: "file" | "dir") {
    cancelRef.current = false;
    ensureExpanded(skill, inDir);
    setCreating({ skill, inDir, kind });
    setNewEntryName("");
  }

  // Use a ref snapshot so commitCreate reads the latest creating value
  // without depending on potentially stale closure state.
  const creatingRef = React.useRef<Creating>(null);
  React.useEffect(() => {
    creatingRef.current = creating;
  }, [creating]);
  const newEntryRef = React.useRef("");
  React.useEffect(() => {
    newEntryRef.current = newEntryName;
  }, [newEntryName]);

  async function commitCreate() {
    if (cancelRef.current) {
      cancelRef.current = false;
      return;
    }
    const c = creatingRef.current;
    const name = newEntryRef.current.trim();
    setCreating(null);
    setNewEntryName("");
    if (!c || !name) return;

    const fullPath = c.inDir ? `${c.inDir}/${name}` : name;
    try {
      if (c.kind === "dir") {
        await api.createSkillDir(c.skill, fullPath);
        toast.success(t("folderCreated", { path: fullPath }));
        ensureExpanded(c.skill, fullPath);
      } else {
        await api.writeSkillFile(c.skill, fullPath, "");
        toast.success(t("fileCreated", { path: fullPath }));
        setSelected({ skill: c.skill, path: fullPath });
        ensureExpanded(c.skill, c.inDir);
      }
      load();
    } catch (e) {
      toast.error(t("createFailed", { msg: (e as Error).message }));
    }
  }

  function cancelCreate() {
    cancelRef.current = true;
    setCreating(null);
    setNewEntryName("");
  }

  async function deletePath(skill: string, path: string) {
    try {
      await api.deleteSkillPath(skill, path);
      toast.success(t("deleted", { path }));
      if (selected?.skill === skill && selected.path === path) setSelected(null);
      load();
    } catch (e) {
      toast.error(t("deleteFailed", { msg: (e as Error).message }));
    }
  }

  async function commitSkillRename() {
    if (!renaming) return;
    const next = renaming.value.trim();
    if (!next || next === renaming.skill) {
      setRenaming(null);
      return;
    }
    setRenameBusy(true);
    try {
      const res = await api.renameSkill(renaming.skill, next);
      toast.success(t("renameSuccess", { name: res.name }));
      setRenaming(null);
      setSelected((cur) => (cur && cur.skill === renaming.skill ? { skill: res.name, path: cur.path } : cur));
      load();
    } catch (e) {
      toast.error(t("renameFailed", { msg: (e as Error).message }));
    } finally {
      setRenameBusy(false);
    }
  }

  async function deleteSkill(name: string) {
    try {
      await api.deleteSkill(name);
      toast.success(t("skillDeleted", { name }));
      if (selected?.skill === name) setSelected(null);
      load();
    } catch (e) {
      toast.error(t("deleteFailed", { msg: (e as Error).message }));
    }
  }

  async function runPendingDelete() {
    const p = pendingDelete;
    if (!p) return;
    setDeleting(true);
    try {
      if (p.kind === "skill") await deleteSkill(p.skill);
      else await deletePath(p.skill, p.path);
    } finally {
      setDeleting(false);
      setPendingDelete(null);
    }
  }

  async function saveFile() {
    if (!selected || selected.path === null) return;
    setSaving(true);
    try {
      await api.writeSkillFile(selected.skill, selected.path, fileContent);
      toast.success(t("saved"));
      setDirty(false);
    } catch (e) {
      toast.error(t("saveFailed", { msg: (e as Error).message }));
    } finally {
      setSaving(false);
    }
  }

  async function toggleSkillMcp(skillName: string, mcpName: string, mcpOn: boolean) {
    const next = mcpOn ? [...detailMcps, mcpName] : detailMcps.filter((n) => n !== mcpName);
    setDetailMcps(next);
    try {
      await api.updateSkillMeta(skillName, { mcps: next });
      toast.success(mcpOn ? t("mcpLinked", { name: mcpName }) : t("mcpUnlinked", { name: mcpName }));
      load();
    } catch (e) {
      // roll back on error
      setDetailMcps(detailMcps);
      toast.error(t("opFailed", { msg: (e as Error).message }));
    }
  }

  async function toggleVisibility(skillName: string, agentId: string, agentName: string) {
    const on = (visibility[skillName] ?? []).includes(agentId);
    try {
      await api.toggleSkillVisibility(agentId, skillName, !on);
      toast.success(on ? t("visRevoked", { name: agentName }) : t("visGranted", { name: agentName }));
      const ids = await api.skillVisibility(skillName);
      setVisibility((v) => ({ ...v, [skillName]: ids }));
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    }
  }

  async function createNewSkill() {
    if (!newName.trim()) {
      toast.error(t("needName"));
      return;
    }
    if (!newDesc.trim()) {
      toast.error(t("needDesc"));
      return;
    }
    setCreatingSkill(true);
    try {
      const name = newName.trim();
      await api.createSkill({
        name,
        description: newDesc.trim(),
        license: newLicense.trim() || undefined,
        compatibility: newCompat.trim() || undefined,
        mcps: newMcps.length ? newMcps : undefined,
        instructions: newInst.trim() || undefined,
      });
      // apply initial visibility (fire-and-forget per agent; best-effort)
      await Promise.all(newVisibility.map((id) => api.toggleSkillVisibility(id, name, true)));
      toast.success(t("skillCreated"));
      setNewOpen(false);
      setNewName("");
      setNewDesc("");
      setNewLicense("");
      setNewCompat("");
      setNewInst("");
      setNewMcps([]);
      setNewVisibility([]);
      load();
    } catch (e) {
      toast.error(t("createFailed", { msg: (e as Error).message }));
    } finally {
      setCreatingSkill(false);
    }
  }

  const filteredProposals = React.useMemo(() => {
    if (proposalFilter === "all") return proposals;
    return proposals.filter((p) => p.status === proposalFilter);
  }, [proposals, proposalFilter]);

  const selectedProposal = React.useMemo(
    () => filteredProposals.find((p) => p.id === selectedProposalId) ?? filteredProposals[0] ?? null,
    [filteredProposals, selectedProposalId],
  );

  React.useEffect(() => {
    if (!selectedProposal) {
      setProposalContent("");
      setReviewNote("");
      return;
    }
    setProposalContent(selectedProposal.content ?? "");
    setReviewNote(selectedProposal.note ?? "");
  }, [selectedProposal]);

  async function handleReviewProposal(decision: "approve" | "reject") {
    if (!selectedProposal) return;
    setReviewingAction(decision);
    try {
      const res = await api.reviewSkillProposal(selectedProposal.id, {
        decision,
        note: reviewNote.trim() || undefined,
        content: decision === "approve" ? proposalContent : undefined,
      });
      toast.success(
        decision === "approve"
          ? t("proposalApprovedToast", { skill: res.proposal?.skill ?? selectedProposal.skill })
          : t("proposalRejectedToast", { skill: res.proposal?.skill ?? selectedProposal.skill }),
      );
      load();
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    } finally {
      setReviewingAction(null);
    }
  }

  async function handleDistillTask() {
    const taskId = Number(distillTaskId.trim());
    if (!Number.isInteger(taskId) || taskId <= 0) {
      toast.error(t("distillNeedTaskId"));
      return;
    }
    setDistilling(true);
    try {
      const res = await api.distillTaskSkill({ task_id: taskId, apply: distillApplyDirect });
      const prop = res.proposal;
      toast.success(
        prop.status === "applied"
          ? t("distillSuccessApplied", { skill: prop.skill })
          : t("distillSuccessPending", { skill: prop.skill }),
      );
      setDistillTaskId("");
      setProposalFilter(prop.status === "applied" ? "applied" : "pending");
      setSelectedProposalId(prop.id);
      load();
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    } finally {
      setDistilling(false);
    }
  }

  function toggleCheckedProposal(id: string, on: boolean) {
    setCheckedProposalIds((cur) => (on ? (cur.includes(id) ? cur : [...cur, id]) : cur.filter((x) => x !== id)));
  }

  async function handleBatchReview(decision: "approve" | "reject") {
    const pendingSelected = proposals
      .filter((p) => checkedProposalIds.includes(p.id) && p.status === "pending")
      .map((p) => p.id);
    if (pendingSelected.length === 0) {
      toast.error(t("batchNeedPending"));
      return;
    }
    setBatchBusy(true);
    try {
      const res = await api.batchReviewSkillProposals({
        ids: pendingSelected,
        decision,
        note: reviewNote.trim() || undefined,
      });
      toast.success(
        decision === "approve"
          ? t("batchApprovedToast", { count: res.succeeded?.length ?? pendingSelected.length })
          : t("batchRejectedToast", { count: res.succeeded?.length ?? pendingSelected.length }),
      );
      setCheckedProposalIds([]);
      load();
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    } finally {
      setBatchBusy(false);
    }
  }

  async function handleMergeProposals() {
    if (checkedProposalIds.length < 2) {
      toast.error(t("mergeNeedTwo"));
      return;
    }
    if (!mergeName.trim()) {
      toast.error(t("needName"));
      return;
    }
    setMerging(true);
    try {
      const res = await api.mergeSkillProposals({
        ids: checkedProposalIds,
        name: mergeName.trim(),
        description: mergeDesc.trim() || undefined,
        apply: mergeApplyDirect,
        reject_sources: mergeRejectSources,
      });
      const prop = res.proposal;
      toast.success(t("mergeSuccessToast", { skill: prop.skill }));
      setMergeOpen(false);
      setCheckedProposalIds([]);
      setProposalFilter(prop.status === "applied" ? "applied" : "pending");
      setSelectedProposalId(prop.id);
      load();
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    } finally {
      setMerging(false);
    }
  }

  function onImportFilesPick(e: React.ChangeEvent<HTMLInputElement>) {
    const picked = Array.from(e.target.files ?? []);
    e.target.value = "";
    if (picked.length === 0) return;
    setImportFiles((prev) => {
      const next = [...prev];
      for (const f of picked) {
        if (!next.some((x) => x.name === f.name && x.size === f.size)) {
          next.push(f);
        }
      }
      return next.slice(0, 12);
    });
    if (!importName.trim() && picked[0]) {
      const stem = picked[0].name
        .replace(/\.(md|markdown|txt|json|log|csv)$/i, "")
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, "");
      if (stem && /^[a-z]/.test(stem)) {
        setImportName(stem.startsWith("report-") ? stem.slice(0, 56) : `report-${stem}`.slice(0, 56));
      }
    }
  }

  async function handleImportReport() {
    if (importFiles.length === 0 && !importPaste.trim()) {
      toast.error(t("importReportNeedInput"));
      return;
    }
    setImportingReport(true);
    try {
      const res = await api.importReportSkill({
        files: importFiles,
        content: importPaste.trim() || undefined,
        name: importName.trim() || undefined,
        domain: importDomain,
        focus: importFocus.trim() || undefined,
        use_llm: importUseLLM,
        apply: importApplyDirect,
        overwrite: importOverwrite,
      });
      if (res.status === "applied") {
        toast.success(
          t("importReportSuccessApplied", {
            skill: res.skill,
            pitfalls: res.pitfalls_count,
            findings: res.findings_count,
            files: res.files_written?.length ?? 3,
          }),
        );
        setImportReportOpen(false);
        setImportFiles([]);
        setImportPaste("");
        setImportName("");
        setImportFocus("");
        load();
        ensureExpanded(res.skill, "references");
        setSelected({ skill: res.skill, path: "SKILL.md" });
      } else {
        toast.success(
          t("importReportSuccessPending", {
            skill: res.skill,
            pitfalls: res.pitfalls_count,
            findings: res.findings_count,
          }),
        );
        setImportReportOpen(false);
        setImportFiles([]);
        setImportPaste("");
        setProposalFilter("pending");
        if (res.proposal?.id) setSelectedProposalId(res.proposal.id);
        load();
        setProposalsOpen(true);
      }
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    } finally {
      setImportingReport(false);
    }
  }

  async function handleExportSkillZip(skillName: string) {
    setExportingSkill(true);
    try {
      await api.downloadSkillZip(skillName);
      toast.success(t("exportZipSuccess", { name: skillName }));
    } catch (e) {
      toast.error(t("opFailed", { msg: (e as Error).message }));
    } finally {
      setExportingSkill(false);
    }
  }

  // ── Inline input JSX helper (NOT a React component — avoids remount on re-render) ──
  // Defined as a plain function returning JSX so React never sees a new component type.
  function inlineInputJSX(indent: number) {
    return (
      <div key="__inline_create__" className="flex items-center gap-1 py-0.5 pr-2" style={{ paddingLeft: indent }}>
        {creating?.kind === "dir" ? (
          <FolderIcon className="size-3.5 shrink-0 text-amber-500" />
        ) : (
          <FileTextIcon className="size-3.5 shrink-0 text-muted-foreground" />
        )}
        <Input
          ref={inlineRef}
          className="h-6 flex-1 px-1 py-0 font-mono text-xs"
          placeholder={creating?.kind === "dir" ? "folder-name" : "filename.py"}
          value={newEntryName}
          onChange={(e) => setNewEntryName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              cancelRef.current = false;
              void commitCreate();
            }
            if (e.key === "Escape") cancelCreate();
          }}
          onBlur={() => {
            void commitCreate();
          }}
        />
      </div>
    );
  }

  // ── Recursive tree renderer ───────────────────────────────────────────────
  function renderTree(nodes: TreeNode[], skill: string, depth: number): React.ReactNode {
    // depth+1: the skill root sits at 8px (px-2); its children indent one step
    // further in so the tree reads as nested under the skill folder.
    const baseIndent = 8 + (depth + 1) * 14;
    return nodes.map((node) => {
      if (node.type === "dir") {
        const key = `${skill}:${node.path}`;
        const open = expanded.has(key);
        return (
          <div key={node.path}>
            <div
              role="treeitem"
              tabIndex={0}
              aria-expanded={open}
              className="group relative flex cursor-pointer select-none items-center gap-1 rounded py-0.5 pr-1 text-sm hover:bg-muted"
              style={{ paddingLeft: baseIndent }}
              onClick={() => toggleExpanded(key)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  toggleExpanded(key);
                }
              }}
            >
              <ChevronRightIcon className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-90")} />
              {open ? (
                <FolderOpenIcon className="size-3.5 shrink-0 text-amber-500" />
              ) : (
                <FolderIcon className="size-3.5 shrink-0 text-amber-500" />
              )}
              <span className="min-w-0 flex-1 truncate" title={node.path}>
                {node.name}
              </span>
              {/* Absolute so a long name can never push the actions out of view */}
              <span className="absolute inset-y-0 right-1 hidden items-center gap-0.5 rounded bg-muted pl-1 group-hover:flex">
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-5"
                  title={t("newFile")}
                  onClick={(e) => {
                    e.stopPropagation();
                    startCreate(skill, node.path, "file");
                  }}
                >
                  <FilePlusIcon className="size-3 text-muted-foreground" />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-5"
                  title={t("newFolder")}
                  onClick={(e) => {
                    e.stopPropagation();
                    startCreate(skill, node.path, "dir");
                  }}
                >
                  <FolderPlusIcon className="size-3 text-muted-foreground" />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-5"
                  title={t("deleteFolder")}
                  onClick={(e) => {
                    e.stopPropagation();
                    setPendingDelete({ kind: "dir", skill, path: node.path });
                  }}
                >
                  <Trash2Icon className="size-3 text-destructive" />
                </Button>
              </span>
            </div>
            {open && (
              <>
                {renderTree(node.children, skill, depth + 1)}
                {creating?.skill === skill && creating.inDir === node.path && inlineInputJSX(baseIndent + 14)}
              </>
            )}
          </div>
        );
      }

      // file node
      const isSelected = selected?.skill === skill && selected.path === node.path;
      return (
        <div
          key={node.path}
          role="treeitem"
          tabIndex={0}
          aria-selected={isSelected}
          className={cn(
            "group relative flex cursor-pointer select-none items-center gap-1 rounded py-0.5 pr-1 text-sm",
            isSelected ? "bg-accent text-accent-foreground" : "hover:bg-muted",
          )}
          style={{ paddingLeft: baseIndent + 16 }}
          onClick={() => setSelected({ skill, path: node.path })}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              setSelected({ skill, path: node.path });
            }
          }}
        >
          <FileTextIcon className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="min-w-0 flex-1 truncate font-mono text-xs" title={node.path}>
            {node.name}
          </span>
          <span
            className={cn(
              "absolute inset-y-0 right-1 hidden items-center rounded pl-1 group-hover:flex",
              isSelected ? "bg-accent" : "bg-muted",
            )}
          >
            <Button
              size="icon"
              variant="ghost"
              className="size-5"
              title={t("deleteFile")}
              onClick={(e) => {
                e.stopPropagation();
                setPendingDelete({ kind: "file", skill, path: node.path });
              }}
            >
              <Trash2Icon className="size-3 text-destructive" />
            </Button>
          </span>
        </div>
      );
    });
  }

  const selectedSkill = selected ? (skills.find((s) => s.name === selected.skill) ?? null) : null;
  const pendingProposalsCount = proposalCounts.pending ?? 0;

  return (
    <div data-content-padding="false" className="flex flex-1 flex-col overflow-hidden">
      <div className="flex items-center gap-3 border-b px-4 py-2.5 lg:px-6">
        <div className="flex flex-col gap-0.5">
          <h1 className="font-semibold text-sm leading-tight">{t("title")}</h1>
          <p className="text-muted-foreground text-xs">{t("subtitle")}</p>
        </div>
        <div className="ml-auto flex items-center gap-2">
          {/* 缺口清单：agent 点名调用、但库里没有的 skill —— 直接是该补什么的依据。 */}
          {missing.length > 0 && (
            <Popover>
              <PopoverTrigger asChild>
                <Button size="sm" variant="outline">
                  <AlertTriangleIcon className="size-3.5 text-amber-500" />
                  {t("missingCount", { count: missing.length })}
                </Button>
              </PopoverTrigger>
              <PopoverContent align="end" className="w-96">
                <p className="mb-2 text-muted-foreground text-xs">{t("missingHint")}</p>
                <div className="space-y-1.5">
                  {missing.map((m) => (
                    <div key={m.skill} className="flex items-center gap-2 text-sm">
                      <code className="min-w-0 flex-1 truncate font-mono text-xs" title={m.skill}>
                        {m.skill}
                      </code>
                      <span className="shrink-0 text-muted-foreground text-xs tabular-nums">
                        {t("callsCount", { count: m.calls })}
                      </span>
                      <span className="shrink-0 text-muted-foreground text-xs">
                        {fmtTime(m.last_used, t("neverCalled"))}
                      </span>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 px-2 text-xs"
                        onClick={() => {
                          setNewName(m.skill);
                          setNewDesc(`针对未命中技能缺口 ${m.skill}（被调用 ${m.calls} 次）补充的标准作业规程。`);
                          setNewOpen(true);
                        }}
                      >
                        <PlusIcon className="size-3" />
                        {t("draftFromMissing")}
                      </Button>
                    </div>
                  ))}
                </div>
              </PopoverContent>
            </Popover>
          )}
          <Button size="sm" variant="outline" onClick={() => setImportReportOpen(true)}>
            <SparklesIcon className="size-3.5 text-primary" />
            {t("importReportBtn")}
          </Button>
          <Button
            size="sm"
            variant={pendingProposalsCount > 0 ? "default" : "outline"}
            onClick={() => {
              loadProposals("all");
              setProposalsOpen(true);
            }}
          >
            <SparklesIcon className="size-3.5" />
            {t("proposalsBtn")}
            {pendingProposalsCount > 0 && (
              <span className="ml-1 rounded-full bg-background/20 px-1.5 py-0.2 font-mono text-[11px] tabular-nums">
                {pendingProposalsCount}
              </span>
            )}
          </Button>
        </div>
      </div>
      <div className="flex flex-1 overflow-hidden">
        {/* ── 左侧文件树 ── */}
        <div className="flex w-64 shrink-0 flex-col border-r">
          <div className="flex flex-col gap-2 border-b p-2">
            <Button size="sm" variant="outline" className="w-full" onClick={() => setNewOpen(true)}>
              <PlusIcon className="size-3.5" />
              {t("newSkill")}
            </Button>
            <input
              ref={uploadRef}
              type="file"
              accept=".zip,application/zip"
              className="hidden"
              onChange={onUploadPick}
            />
            <Button
              size="sm"
              variant="outline"
              className="w-full"
              disabled={uploading}
              onClick={() => uploadRef.current?.click()}
              title={t("uploadTitle")}
            >
              <UploadIcon className="size-3.5" />
              {uploading ? t("uploading") : t("uploadZip")}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              className="w-full"
              onClick={() => setImportReportOpen(true)}
              title={t("importReportTitle")}
            >
              <SparklesIcon className="size-3.5 text-primary" />
              {t("importReportBtn")}
            </Button>
          </div>
          {/* Radix viewport wraps children in a display:table div that grows with
              content — force it to block so long names truncate instead of
              widening the rows past the sidebar. */}
          <ScrollArea className="[&>[data-slot=scroll-area-viewport]>div]:!block flex-1">
            <div className="p-1">
              {skills.map((s) => {
                const isOpen = expanded.has(s.name);
                const tree = buildTree(s.files);
                const isSkillSelected = selected?.skill === s.name && selected.path === null;
                return (
                  <div key={s.name}>
                    {/* skill 根节点 */}
                    <div
                      role="treeitem"
                      tabIndex={0}
                      aria-expanded={isOpen}
                      aria-selected={isSkillSelected}
                      className={cn(
                        "group relative flex cursor-pointer select-none items-center gap-1 rounded px-2 py-1 text-sm",
                        isSkillSelected ? "bg-accent text-accent-foreground" : "hover:bg-muted",
                      )}
                      onClick={() => {
                        toggleExpanded(s.name);
                        setSelected({ skill: s.name, path: null });
                      }}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          toggleExpanded(s.name);
                          setSelected({ skill: s.name, path: null });
                        }
                      }}
                    >
                      <ChevronRightIcon
                        className={cn("size-3.5 shrink-0 transition-transform", isOpen && "rotate-90")}
                      />
                      {isOpen ? (
                        <FolderOpenIcon className="size-3.5 shrink-0 text-blue-500" />
                      ) : (
                        <FolderIcon className="size-3.5 shrink-0 text-blue-500" />
                      )}
                      <span className="min-w-0 flex-1 truncate font-semibold" title={s.name}>
                        {s.name}
                      </span>
                      {s.calls > 0 && (
                        <span
                          className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground tabular-nums"
                          title={t("callCountTitle", { count: s.calls, last: fmtTime(s.last_used, t("neverCalled")) })}
                        >
                          {s.calls}
                        </span>
                      )}
                      <span
                        className={cn(
                          "absolute inset-y-0 right-1 hidden items-center gap-0.5 rounded pl-1 group-hover:flex",
                          isSkillSelected ? "bg-accent" : "bg-muted",
                        )}
                      >
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title={t("newFile")}
                          onClick={(e) => {
                            e.stopPropagation();
                            startCreate(s.name, "", "file");
                          }}
                        >
                          <FilePlusIcon className="size-3 text-muted-foreground" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title={t("newFolder")}
                          onClick={(e) => {
                            e.stopPropagation();
                            startCreate(s.name, "", "dir");
                          }}
                        >
                          <FolderPlusIcon className="size-3 text-muted-foreground" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title={t("renameAction")}
                          onClick={(e) => {
                            e.stopPropagation();
                            setRenaming({ skill: s.name, value: s.name });
                          }}
                        >
                          <PencilIcon className="size-3 text-muted-foreground" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title={t("deleteSkillAction")}
                          onClick={(e) => {
                            e.stopPropagation();
                            setPendingDelete({ kind: "skill", skill: s.name });
                          }}
                        >
                          <Trash2Icon className="size-3 text-destructive" />
                        </Button>
                      </span>
                    </div>

                    {/* 展开：递归文件树 */}
                    {isOpen && (
                      <>
                        {renderTree(tree, s.name, 0)}
                        {creating?.skill === s.name && creating.inDir === "" && inlineInputJSX(22)}
                      </>
                    )}
                  </div>
                );
              })}

              {skills.length === 0 && (
                <EmptyState compact illustration="terminal" className="p-3" title={t("emptySidebar")} />
              )}
            </div>
          </ScrollArea>
        </div>

        {/* ── 右侧面板 ── */}
        <div className="flex flex-1 flex-col overflow-auto p-4">
          {!selected && (
            <SkillsOverview
              skills={skills}
              missing={missing}
              pendingProposalsCount={pendingProposalsCount}
              onSelect={(name) => setSelected({ skill: name, path: null })}
              onOpenProposals={() => {
                loadProposals("all");
                setProposalsOpen(true);
              }}
            />
          )}

          {selected && selected.path === null && selectedSkill && (
            <div className="max-w-5xl space-y-5">
              <div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <h2 className="font-mono font-semibold text-base">{selectedSkill.name}</h2>
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-7 text-xs"
                    onClick={() => setRenaming({ skill: selectedSkill.name, value: selectedSkill.name })}
                  >
                    <PencilIcon className="size-3.5" />
                    {t("renameAction")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-7 text-xs"
                    disabled={exportingSkill}
                    onClick={() => void handleExportSkillZip(selectedSkill.name)}
                  >
                    <DownloadIcon className="size-3.5" />
                    {t("exportZipBtn")}
                  </Button>
                </div>
                {selectedSkill.description && (
                  <p className="mt-1 text-muted-foreground text-sm">{selectedSkill.description}</p>
                )}
                <div className="mt-2 flex flex-wrap gap-2">
                  {BUILTIN_SKILL_NAMES.has(selectedSkill.name) && (
                    <Badge variant="secondary" className="font-normal text-xs">
                      Built-in
                    </Badge>
                  )}
                  {(selectedSkill.name.startsWith("report-") || REPORT_TACTIC_SKILL_NAMES.has(selectedSkill.name)) && (
                    <Badge variant="outline" className="border-primary/40 font-normal text-primary text-xs">
                      {t("reportDistilledBadge")}
                    </Badge>
                  )}
                  {selectedSkill.name.startsWith("case-") && (
                    <Badge variant="outline" className="border-primary/40 font-normal text-primary text-xs">
                      {t("distilledCaseBadge")}
                    </Badge>
                  )}
                  {selectedSkill.name.startsWith("merged-") && (
                    <Badge
                      variant="outline"
                      className="border-emerald-500/40 font-normal text-emerald-600 text-xs dark:text-emerald-400"
                    >
                      {t("mergedCaseBadge")}
                    </Badge>
                  )}
                  {selectedSkill.mcps?.map((m) => (
                    <Badge key={m} variant="outline" className="font-mono font-normal text-xs">
                      MCP: {m}
                    </Badge>
                  ))}
                  {selectedSkill.license && (
                    <Badge variant="outline" className="font-normal text-xs">
                      License: {selectedSkill.license}
                    </Badge>
                  )}
                  {selectedSkill.compatibility && (
                    <Badge variant="secondary" className="font-normal text-xs">
                      {selectedSkill.compatibility}
                    </Badge>
                  )}
                </div>
              </div>

              {/* 左右分栏：配置（MCP/可见性）在左为主，调用统计在右为辅。
                  lg 以下放不下时用 flex-row-reverse 回落到单列——统计因 DOM 顺序在前，
                  窄屏时自然落到配置上方（与改版前的上下顺序一致）。 */}
              <div className="flex flex-col gap-6 lg:flex-row-reverse lg:items-start">
                {/* ── 右侧：调用统计 ── */}
                <div className="space-y-2 lg:w-80 lg:shrink-0">
                  <Label className="text-muted-foreground text-xs">{t("callStats")}</Label>
                  <div className="grid grid-cols-3 gap-2">
                    <div className="rounded-md border p-2">
                      <p className="font-semibold text-lg tabular-nums">{selectedSkill.calls}</p>
                      <p className="text-muted-foreground text-xs">{t("totalCalls")}</p>
                    </div>
                    <div className="rounded-md border p-2">
                      <p className="font-semibold text-lg tabular-nums">{selectedSkill.tasks}</p>
                      <p className="text-muted-foreground text-xs">{t("coveredTasks")}</p>
                    </div>
                    <div className="rounded-md border p-2">
                      <p
                        className="truncate font-medium text-sm"
                        title={fmtTime(selectedSkill.last_used, t("neverCalled"))}
                      >
                        {fmtTime(selectedSkill.last_used, t("neverCalled"))}
                      </p>
                      <p className="text-muted-foreground text-xs">{t("recent")}</p>
                    </div>
                  </div>
                  {selectedSkill.usage_agents.length > 0 && (
                    <div className="flex flex-wrap items-center gap-1">
                      <span className="text-muted-foreground text-xs">{t("callers")}</span>
                      {selectedSkill.usage_agents.map((k) => (
                        <Badge key={k} variant="secondary" className="font-normal text-xs">
                          {k}
                        </Badge>
                      ))}
                    </div>
                  )}
                  {usageLoading ? (
                    <p className="text-muted-foreground text-xs">{t("loadingCalls")}</p>
                  ) : usageCalls.length > 0 ? (
                    <div className="rounded-md border">
                      <div className="border-b px-2 py-1 text-muted-foreground text-xs">
                        {t("recentCallsCount", { count: usageCalls.length })}
                      </div>
                      <div className="max-h-56 overflow-y-auto">
                        {usageCalls.map((c, i) => (
                          <div
                            key={`${c.ts}-${i}`}
                            className="flex items-center gap-2 border-b px-2 py-1 text-xs last:border-b-0"
                          >
                            <span className="text-muted-foreground tabular-nums">
                              {fmtTime(c.ts, t("neverCalled"))}
                            </span>
                            <Badge variant="outline" className="font-normal">
                              {c.agent_key || "—"}
                            </Badge>
                            <span className="ml-auto text-muted-foreground">
                              {c.task_id > 0 ? t("taskRef", { id: c.task_id }) : c.session_id ? t("sessionRef") : "—"}
                            </span>
                          </div>
                        ))}
                      </div>
                    </div>
                  ) : (
                    <p className="text-muted-foreground text-xs">{t("noCallsYet")}</p>
                  )}
                </div>

                {/* ── 左侧：关联 MCP + 可见性 ── */}
                <div className="space-y-5 lg:min-w-0 lg:flex-1">
                  <div className="space-y-2">
                    <Label className="text-muted-foreground text-xs">
                      {t("linkedMcp")}
                      <span className="ml-1 font-normal">{t("linkedMcpHint")}</span>
                    </Label>
                    {mcpOptions.length === 0 ? (
                      <p className="text-muted-foreground text-xs">{t("noMcp")}</p>
                    ) : (
                      <div className="flex flex-wrap gap-x-4 gap-y-2">
                        {mcpOptions.map((m) => (
                          <label key={m.id} className="flex cursor-pointer items-center gap-2 text-sm">
                            <Checkbox
                              checked={detailMcps.includes(m.name)}
                              onCheckedChange={(on) => toggleSkillMcp(selectedSkill.name, m.name, !!on)}
                            />
                            {m.name}
                          </label>
                        ))}
                      </div>
                    )}
                  </div>

                  <div className="space-y-2">
                    <Label className="text-muted-foreground text-xs">{t("visibility")}</Label>
                    <div className="space-y-2">
                      {agents.map((a) => (
                        <label key={a.key} className="flex cursor-pointer items-center gap-2 text-sm">
                          <Checkbox
                            checked={(visibility[selectedSkill.name] ?? []).includes(a.id)}
                            onCheckedChange={() => toggleVisibility(selectedSkill.name, a.id, a.name)}
                          />
                          {a.name}
                        </label>
                      ))}
                      {agents.length === 0 && <span className="text-muted-foreground text-xs">{t("noAgents")}</span>}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}

          {selected && selected.path !== null && (
            <div className="flex h-full flex-col gap-2">
              <div className="flex items-center gap-2 text-muted-foreground text-xs">
                <span className="font-medium text-foreground">{selected.skill}</span>
                <span>/</span>
                <span className="font-mono">{selected.path}</span>
                <Button size="sm" className="ml-auto" onClick={saveFile} disabled={!dirty || saving}>
                  {saving ? t("saving") : t("save")}
                </Button>
              </div>
              {fileLoading ? (
                <p className="text-muted-foreground text-xs">{t("loading")}</p>
              ) : (
                <Textarea
                  className="flex-1 resize-none font-mono text-xs"
                  value={fileContent}
                  onChange={(e) => {
                    setFileContent(e.target.value);
                    setDirty(true);
                  }}
                />
              )}
            </div>
          )}
        </div>
      </div>

      {/* ── 技能包重命名 ── */}
      <AlertDialog
        open={!!renaming}
        onOpenChange={(o) => {
          if (!o && !renameBusy) setRenaming(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("renameTitle", { name: renaming?.skill ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>{t("renameDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <Input
            value={renaming?.value ?? ""}
            onChange={(e) => setRenaming((cur) => (cur ? { ...cur, value: e.target.value } : cur))}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !renameBusy) void commitSkillRename();
            }}
            placeholder={t("renamePlaceholder")}
            autoFocus
          />
          <AlertDialogFooter>
            <AlertDialogCancel disabled={renameBusy}>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={renameBusy}
              onClick={(e) => {
                e.preventDefault();
                void commitSkillRename();
              }}
            >
              {renameBusy ? t("renaming") : t("renameConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* ── 删除二次确认 ── */}
      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(o) => {
          if (!o) setPendingDelete(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingDelete?.kind === "skill" && t("deleteSkillQ", { name: pendingDelete.skill })}
              {pendingDelete?.kind === "dir" && t("deleteFolderQ", { path: pendingDelete.path })}
              {pendingDelete?.kind === "file" && t("deleteFileQ", { path: pendingDelete.path })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete?.kind === "skill"
                ? t("deleteSkillDesc")
                : pendingDelete?.kind === "dir"
                  ? t("deleteFolderDesc")
                  : t("irreversible")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>{t("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={deleting}
              onClick={(e) => {
                e.preventDefault();
                void runPendingDelete();
              }}
            >
              {deleting ? t("deleting") : t("delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* ── 新建 Skill 对话框 ── */}
      <Sheet open={newOpen} onOpenChange={setNewOpen}>
        <SheetContent side="right" className="flex w-full flex-col gap-0 data-[side=right]:sm:max-w-xl">
          <SheetHeader className="px-4 pt-4">
            <SheetTitle>{t("newSkill")}</SheetTitle>
          </SheetHeader>
          <Tabs defaultValue="basic" className="flex min-h-0 flex-1 flex-col">
            <TabsList className="mx-4 mt-3 shrink-0 justify-start">
              <TabsTrigger value="basic">{t("tabBasic")}</TabsTrigger>
              <TabsTrigger value="mcp">
                {t("linkedMcp")}
                {newMcps.length > 0 && (
                  <span className="ml-1.5 rounded-full bg-primary px-1.5 py-0.5 text-[10px] text-primary-foreground leading-none">
                    {newMcps.length}
                  </span>
                )}
              </TabsTrigger>
              <TabsTrigger value="visibility">
                {t("tabVisibility")}
                {newVisibility.length > 0 && (
                  <span className="ml-1.5 rounded-full bg-primary px-1.5 py-0.5 text-[10px] text-primary-foreground leading-none">
                    {newVisibility.length}
                  </span>
                )}
              </TabsTrigger>
            </TabsList>

            {/* 基本信息 */}
            <TabsContent
              value="basic"
              className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pt-4 pb-4 data-[state=inactive]:hidden"
            >
              <div className="grid gap-1.5">
                <Label htmlFor="sk-name">
                  {t("nameLabel")} <span className="text-destructive">*</span>
                </Label>
                <Input
                  id="sk-name"
                  placeholder="sqli-deepdive"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                />
                <p className="text-muted-foreground text-xs">{t("nameHint")}</p>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="sk-desc">
                  {t("descLabel")} <span className="text-destructive">*</span>
                </Label>
                <Textarea
                  id="sk-desc"
                  rows={2}
                  className="resize-none"
                  placeholder={t("descPlaceholder")}
                  value={newDesc}
                  onChange={(e) => setNewDesc(e.target.value)}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">{t("licenseLabel")}</Label>
                  <Input
                    placeholder="MIT / Proprietary"
                    value={newLicense}
                    onChange={(e) => setNewLicense(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">{t("compatibilityLabel")}</Label>
                  <Input
                    placeholder={t("compatPlaceholder")}
                    value={newCompat}
                    onChange={(e) => setNewCompat(e.target.value)}
                  />
                </div>
              </div>
              <div className="flex min-h-0 flex-1 flex-col gap-1.5">
                <Label htmlFor="sk-inst">
                  {t("bodyLabel")} <span className="font-normal text-muted-foreground text-xs">{t("bodyHint")}</span>
                </Label>
                <Textarea
                  id="sk-inst"
                  className="min-h-40 flex-1 resize-none font-mono text-sm leading-relaxed"
                  placeholder={t("bodyPlaceholder")}
                  value={newInst}
                  onChange={(e) => setNewInst(e.target.value)}
                />
              </div>
            </TabsContent>

            {/* 关联 MCP */}
            <TabsContent value="mcp" className="overflow-y-auto px-4 pt-4 pb-4 data-[state=inactive]:hidden">
              <p className="mb-3 text-muted-foreground text-xs">{t("mcpSheetHint")}</p>
              {mcpOptions.length === 0 ? (
                <p className="text-muted-foreground text-xs">{t("noMcp")}</p>
              ) : (
                <div className="flex flex-col gap-2">
                  {mcpOptions.map((m) => (
                    <label key={m.id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        checked={newMcps.includes(m.name)}
                        onCheckedChange={(on) =>
                          setNewMcps((cur) => (on ? [...cur, m.name] : cur.filter((n) => n !== m.name)))
                        }
                      />
                      {m.name}
                    </label>
                  ))}
                </div>
              )}
            </TabsContent>

            {/* 可见性 */}
            <TabsContent value="visibility" className="overflow-y-auto px-4 pt-4 pb-4 data-[state=inactive]:hidden">
              <p className="mb-3 text-muted-foreground text-xs">{t("visSheetHint")}</p>
              {agents.length === 0 ? (
                <p className="text-muted-foreground text-xs">{t("noAgents")}</p>
              ) : (
                <div className="flex flex-col gap-2">
                  {agents.map((a) => (
                    <label key={a.key} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        checked={newVisibility.includes(a.id)}
                        onCheckedChange={(on) =>
                          setNewVisibility((cur) => (on ? [...cur, a.id] : cur.filter((id) => id !== a.id)))
                        }
                      />
                      {a.name}
                    </label>
                  ))}
                </div>
              )}
            </TabsContent>
          </Tabs>

          <SheetFooter className="flex-row justify-end gap-2 border-t px-4 py-3">
            <Button variant="outline" onClick={() => setNewOpen(false)}>
              {t("cancel")}
            </Button>
            <Button onClick={createNewSkill} disabled={creatingSkill}>
              {creatingSkill ? t("creating") : t("create")}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* ── AI 自主沉淀：技能提议审阅与手动任务提炼 ── */}
      <Sheet open={proposalsOpen} onOpenChange={setProposalsOpen}>
        <SheetContent side="right" className="flex w-full flex-col gap-0 data-[side=right]:sm:max-w-4xl">
          <SheetHeader className="border-b px-4 py-3">
            <SheetTitle className="flex items-center gap-2 text-sm">
              <SparklesIcon className="size-4 text-primary" />
              {t("proposalsTitle")}
            </SheetTitle>
            <p className="text-muted-foreground text-xs">{t("proposalsDesc")}</p>
          </SheetHeader>

          {/* 手动从任务提炼 */}
          <div className="flex flex-wrap items-center gap-2 border-b bg-muted/30 px-4 py-2.5">
            <span className="font-medium text-xs">{t("distillSectionTitle")}</span>
            <Input
              className="h-7 w-40 font-mono text-xs"
              placeholder={t("distillTaskPlaceholder")}
              value={distillTaskId}
              onChange={(e) => setDistillTaskId(e.target.value)}
            />
            <label className="flex cursor-pointer items-center gap-1.5 text-muted-foreground text-xs">
              <Checkbox checked={distillApplyDirect} onCheckedChange={(on) => setDistillApplyDirect(!!on)} />
              {t("distillApplyDirect")}
            </label>
            <Button
              size="sm"
              variant="secondary"
              className="ml-auto h-7 text-xs"
              disabled={distilling}
              onClick={() => void handleDistillTask()}
            >
              <SparklesIcon className="size-3" />
              {distilling ? t("distilling") : t("distillAction")}
            </Button>
          </div>

          {/* 状态筛选与批量治理栏 */}
          <div className="flex flex-wrap items-center gap-1.5 border-b px-4 py-2">
            {(
              [
                { key: "pending", label: t("proposalFilterPending"), count: proposalCounts.pending ?? 0 },
                { key: "applied", label: t("proposalFilterApplied"), count: proposalCounts.applied ?? 0 },
                { key: "rejected", label: t("proposalFilterRejected"), count: proposalCounts.rejected ?? 0 },
                {
                  key: "all",
                  label: t("proposalFilterAll"),
                  count: (proposalCounts.pending ?? 0) + (proposalCounts.applied ?? 0) + (proposalCounts.rejected ?? 0),
                },
              ] as const
            ).map((tab) => (
              <Button
                key={tab.key}
                size="sm"
                variant={proposalFilter === tab.key ? "default" : "ghost"}
                className="h-7 px-2.5 text-xs"
                onClick={() => setProposalFilter(tab.key)}
              >
                {tab.label}
                <span className="ml-1 font-mono text-[11px] opacity-80 tabular-nums">({tab.count})</span>
              </Button>
            ))}

            {checkedProposalIds.length > 0 && (
              <div className="ml-auto flex items-center gap-1.5">
                <span className="text-muted-foreground text-xs">
                  {t("selectedCount", { count: checkedProposalIds.length })}
                </span>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 px-2 text-xs"
                  disabled={batchBusy}
                  onClick={() => void handleBatchReview("approve")}
                >
                  <CheckCircle2Icon className="size-3 text-emerald-500" />
                  {t("batchApproveBtn")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 px-2 text-xs"
                  disabled={batchBusy}
                  onClick={() => void handleBatchReview("reject")}
                >
                  <XCircleIcon className="size-3 text-destructive" />
                  {t("batchRejectBtn")}
                </Button>
                {checkedProposalIds.length >= 2 && (
                  <Button
                    size="sm"
                    variant="secondary"
                    className="h-7 px-2 text-xs"
                    onClick={() => setMergeOpen((v) => !v)}
                  >
                    <SparklesIcon className="size-3 text-primary" />
                    {t("mergeProposalsBtn")}
                  </Button>
                )}
              </div>
            )}
          </div>

          {/* 跨任务合并提炼面板 */}
          {mergeOpen && checkedProposalIds.length >= 2 && (
            <div className="space-y-2 border-b bg-primary/5 px-4 py-3 text-xs">
              <div className="flex items-center justify-between">
                <span className="font-semibold">{t("mergePanelTitle", { count: checkedProposalIds.length })}</span>
                <Button size="sm" variant="ghost" className="h-6 px-2 text-xs" onClick={() => setMergeOpen(false)}>
                  {t("cancel")}
                </Button>
              </div>
              <div className="grid gap-2 sm:grid-cols-2">
                <Input
                  className="h-7 font-mono text-xs"
                  placeholder="merged-web-tactics"
                  value={mergeName}
                  onChange={(e) => setMergeName(e.target.value)}
                />
                <Input
                  className="h-7 text-xs"
                  placeholder={t("mergeDescPlaceholder")}
                  value={mergeDesc}
                  onChange={(e) => setMergeDesc(e.target.value)}
                />
              </div>
              <div className="flex flex-wrap items-center gap-4 pt-0.5">
                <label className="flex cursor-pointer items-center gap-1.5 text-muted-foreground">
                  <Checkbox checked={mergeApplyDirect} onCheckedChange={(on) => setMergeApplyDirect(!!on)} />
                  {t("distillApplyDirect")}
                </label>
                <label className="flex cursor-pointer items-center gap-1.5 text-muted-foreground">
                  <Checkbox checked={mergeRejectSources} onCheckedChange={(on) => setMergeRejectSources(!!on)} />
                  {t("mergeRejectSourcesLabel")}
                </label>
                <Button
                  size="sm"
                  className="ml-auto h-7 text-xs"
                  disabled={merging}
                  onClick={() => void handleMergeProposals()}
                >
                  {merging ? t("distilling") : t("mergeConfirmBtn")}
                </Button>
              </div>
            </div>
          )}

          {/* 提议列表 + 详情分栏 */}
          <div className="flex min-h-0 flex-1 overflow-hidden">
            {/* 左侧提议列表 */}
            <div className="w-72 shrink-0 overflow-y-auto border-r p-2">
              {filteredProposals.length === 0 ? (
                <p className="p-4 text-center text-muted-foreground text-xs">{t("proposalEmpty")}</p>
              ) : (
                <div className="space-y-1.5">
                  {filteredProposals.map((p) => {
                    const active = selectedProposal?.id === p.id;
                    const checked = checkedProposalIds.includes(p.id);
                    return (
                      <div
                        key={p.id}
                        className={cn(
                          "flex w-full items-start gap-2 rounded-md border p-2.5 text-left transition-colors",
                          active ? "border-primary bg-accent" : "hover:bg-muted/60",
                        )}
                      >
                        <Checkbox
                          className="mt-0.5"
                          checked={checked}
                          onCheckedChange={(on) => toggleCheckedProposal(p.id, !!on)}
                        />
                        <button
                          type="button"
                          onClick={() => setSelectedProposalId(p.id)}
                          className="flex min-w-0 flex-1 flex-col gap-1 text-left"
                        >
                          <div className="flex items-center justify-between gap-2">
                            <span className="truncate font-mono font-semibold text-xs" title={p.skill}>
                              {p.skill}
                            </span>
                            <Badge
                              variant={
                                p.status === "applied" ? "secondary" : p.status === "rejected" ? "outline" : "default"
                              }
                              className="shrink-0 px-1.5 py-0 font-normal text-[10px]"
                            >
                              {p.status === "applied"
                                ? t("proposalStatusApplied")
                                : p.status === "rejected"
                                  ? t("proposalStatusRejected")
                                  : t("proposalStatusPending")}
                            </Badge>
                          </div>
                          <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
                            <span>
                              {p.origin === "curator" ? t("proposalOriginCurator") : t("proposalOriginAgent")}
                            </span>
                            <span className="tabular-nums">{fmtTime(p.created_at, "—")}</span>
                          </div>
                          {p.lint && (
                            <div className="flex items-center gap-1 text-[11px] text-amber-500">
                              <AlertTriangleIcon className="size-3 shrink-0" />
                              <span className="truncate">{p.lint}</span>
                            </div>
                          )}
                        </button>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>

            {/* 右侧详情与在线修订审阅 */}
            <div className="flex min-w-0 flex-1 flex-col overflow-y-auto p-4">
              {!selectedProposal ? (
                <div className="flex flex-1 items-center justify-center text-muted-foreground text-xs">
                  {t("proposalEmpty")}
                </div>
              ) : (
                <div className="flex flex-1 flex-col gap-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <h3 className="font-mono font-semibold text-sm">
                        {selectedProposal.skill}
                        {selectedProposal.file && selectedProposal.file !== "SKILL.md"
                          ? ` / ${selectedProposal.file}`
                          : " / SKILL.md"}
                      </h3>
                      <p className="text-muted-foreground text-xs">
                        ID: <code className="font-mono">{selectedProposal.id}</code> ·{" "}
                        {selectedProposal.origin === "curator" ? t("proposalOriginCurator") : t("proposalOriginAgent")}
                      </p>
                    </div>
                    {selectedProposal.status === "applied" && (
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-7 text-xs"
                        onClick={() => {
                          setSelected({ skill: selectedProposal.skill, path: null });
                          setProposalsOpen(false);
                        }}
                      >
                        {t("openInLibrary")}
                      </Button>
                    )}
                  </div>

                  {selectedProposal.evidence && (
                    <div className="rounded-md border bg-muted/30 px-3 py-2 text-xs">
                      <span className="font-medium text-muted-foreground">{t("proposalEvidenceLabel")}：</span>
                      <code className="break-all font-mono text-[11px]">{selectedProposal.evidence}</code>
                    </div>
                  )}

                  {selectedProposal.lint && (
                    <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-amber-600 text-xs dark:text-amber-400">
                      <AlertTriangleIcon className="mt-0.5 size-3.5 shrink-0" />
                      <span>{t("proposalLintWarning", { lint: selectedProposal.lint })}</span>
                    </div>
                  )}

                  <div className="flex min-h-0 flex-1 flex-col gap-1.5">
                    <Label className="text-muted-foreground text-xs">{t("proposalContentLabel")}</Label>
                    <Textarea
                      className="min-h-64 flex-1 resize-none font-mono text-xs leading-relaxed"
                      value={proposalContent}
                      readOnly={selectedProposal.status !== "pending"}
                      onChange={(e) => setProposalContent(e.target.value)}
                    />
                  </div>

                  <div className="grid gap-1.5">
                    <Label className="text-muted-foreground text-xs">{t("proposalNoteLabel")}</Label>
                    <Input
                      className="h-8 text-xs"
                      placeholder={t("proposalNotePlaceholder")}
                      value={reviewNote}
                      readOnly={selectedProposal.status !== "pending"}
                      onChange={(e) => setReviewNote(e.target.value)}
                    />
                  </div>

                  {selectedProposal.status === "pending" && (
                    <div className="flex items-center justify-end gap-2 pt-1">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={reviewingAction !== null}
                        onClick={() => void handleReviewProposal("reject")}
                      >
                        <XCircleIcon className="size-3.5 text-destructive" />
                        {reviewingAction === "reject" ? t("rejectingProposal") : t("rejectProposal")}
                      </Button>
                      <Button
                        size="sm"
                        disabled={reviewingAction !== null}
                        onClick={() => void handleReviewProposal("approve")}
                      >
                        <CheckCircle2Icon className="size-3.5" />
                        {reviewingAction === "approve" ? t("approvingProposal") : t("approveProposal")}
                      </Button>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        </SheetContent>
      </Sheet>

      {/* ── 外部报告（TXT / JSON / MD）导入沉淀为多文件技能包 ── */}
      <Sheet open={importReportOpen} onOpenChange={setImportReportOpen}>
        <SheetContent side="right" className="flex w-full flex-col gap-0 data-[side=right]:sm:max-w-2xl">
          <SheetHeader className="border-b px-4 py-3">
            <SheetTitle className="flex items-center gap-2 text-sm">
              <SparklesIcon className="size-4 text-primary" />
              {t("importReportTitle")}
            </SheetTitle>
            <p className="text-muted-foreground text-xs leading-relaxed">{t("importReportDesc")}</p>
          </SheetHeader>

          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
            {/* 1. 选择报告文件 */}
            <div className="space-y-2">
              <Label className="text-xs">{t("importReportFilesLabel")}</Label>
              <input
                ref={importFileRef}
                type="file"
                multiple
                accept=".md,.markdown,.txt,.json,.log,.csv"
                className="hidden"
                onChange={onImportFilesPick}
              />
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => importFileRef.current?.click()}
                  disabled={importingReport}
                >
                  <UploadIcon className="size-3.5" />
                  {t("importReportPickFiles")}
                </Button>
                {importFiles.length > 0 && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="h-7 text-muted-foreground text-xs"
                    onClick={() => setImportFiles([])}
                    disabled={importingReport}
                  >
                    {t("importReportClearFiles")}
                  </Button>
                )}
              </div>
              {importFiles.length > 0 && (
                <div className="flex flex-wrap gap-1.5 rounded-md border bg-muted/30 p-2.5">
                  {importFiles.map((f) => (
                    <Badge key={`${f.name}-${f.size}`} variant="secondary" className="gap-1.5 font-mono text-xs">
                      <span>{f.name}</span>
                      <span className="text-[10px] text-muted-foreground">({Math.ceil(f.size / 1024)} KB)</span>
                      <button
                        type="button"
                        className="ml-0.5 text-muted-foreground hover:text-foreground"
                        onClick={() => setImportFiles((cur) => cur.filter((x) => x !== f))}
                      >
                        ×
                      </button>
                    </Badge>
                  ))}
                </div>
              )}
            </div>

            {/* 2. 技能包名称与分析领域 */}
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label className="text-xs">{t("importReportNameLabel")}</Label>
                <Input
                  className="h-8 font-mono text-xs"
                  placeholder={t("importReportNamePlaceholder")}
                  value={importName}
                  onChange={(e) => setImportName(e.target.value)}
                  disabled={importingReport}
                />
              </div>
              <div className="grid gap-1.5">
                <Label className="text-xs">{t("importReportDomainLabel")}</Label>
                <div className="flex gap-1">
                  {(
                    [
                      { key: "auto", label: t("importReportDomainAuto").split("（")[0] },
                      { key: "web_security", label: "Web/云安全" },
                      { key: "software_re", label: "软件/WASM逆向" },
                    ] as const
                  ).map((opt) => (
                    <Button
                      key={opt.key}
                      type="button"
                      size="sm"
                      variant={importDomain === opt.key ? "default" : "outline"}
                      className="h-8 flex-1 px-2 text-xs"
                      onClick={() => setImportDomain(opt.key)}
                      disabled={importingReport}
                    >
                      {opt.label}
                    </Button>
                  ))}
                </div>
              </div>
            </div>

            {/* 3. 重点提炼方向 */}
            <div className="grid gap-1.5">
              <Label className="text-xs">{t("importReportFocusLabel")}</Label>
              <Input
                className="h-8 text-xs"
                placeholder={t("importReportFocusPlaceholder")}
                value={importFocus}
                onChange={(e) => setImportFocus(e.target.value)}
                disabled={importingReport}
              />
            </div>

            {/* 4. 直接粘贴报告内容 */}
            <div className="flex min-h-0 flex-1 flex-col gap-1.5">
              <Label className="text-xs">{t("importReportPasteLabel")}</Label>
              <Textarea
                className="min-h-40 flex-1 resize-none font-mono text-xs leading-relaxed"
                placeholder={t("importReportPastePlaceholder")}
                value={importPaste}
                onChange={(e) => setImportPaste(e.target.value)}
                disabled={importingReport}
              />
            </div>

            {/* 5. 提炼与落盘选项 */}
            <div className="space-y-2 rounded-md border bg-muted/20 p-3 text-xs">
              <label className="flex cursor-pointer items-center gap-2">
                <Checkbox
                  checked={importUseLLM}
                  onCheckedChange={(on) => setImportUseLLM(!!on)}
                  disabled={importingReport}
                />
                <span>{t("importReportUseLLM")}</span>
              </label>
              <label className="flex cursor-pointer items-center gap-2">
                <Checkbox
                  checked={importApplyDirect}
                  onCheckedChange={(on) => setImportApplyDirect(!!on)}
                  disabled={importingReport}
                />
                <span className="font-medium">{t("importReportApplyDirect")}</span>
              </label>
              {importApplyDirect && (
                <label className="flex cursor-pointer items-center gap-2 pl-6 text-muted-foreground">
                  <Checkbox
                    checked={importOverwrite}
                    onCheckedChange={(on) => setImportOverwrite(!!on)}
                    disabled={importingReport}
                  />
                  <span>{t("importReportOverwrite")}</span>
                </label>
              )}
            </div>
          </div>

          <SheetFooter className="flex-row justify-end gap-2 border-t px-4 py-3">
            <Button variant="outline" onClick={() => setImportReportOpen(false)} disabled={importingReport}>
              {t("cancel")}
            </Button>
            <Button onClick={() => void handleImportReport()} disabled={importingReport}>
              <SparklesIcon className="size-3.5" />
              {importingReport ? t("importReportSubmitting") : t("importReportSubmit")}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </div>
  );
}
