"use client";

import * as React from "react";

import {
  ArrowDownToLineIcon,
  CheckCircle2Icon,
  CloudDownloadIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { UpdateCheck, UpdateJournal, UpdateProgress } from "@/lib/types";

/** 字节数 → 人类可读大小；发布包通常在几十 MB。 */
function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** ISO 时间 → 本地 YYYY-MM-DD HH:mm；解析不了就原样返回。 */
function humanTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

const PHASE_LABEL: Record<UpdateProgress["phase"], string> = {
  idle: "准备中",
  downloading: "下载中",
  verifying: "校验 SHA256",
  extracting: "解包中",
  staged: "已就绪",
  failed: "失败",
};

/**
 * 版本与更新。
 *
 * 三件事必须让用户看得懂：现在跑的是哪一版、有没有新版、点下去之后到底发生了什么。
 * 下载在后端做（服务器可能在内网，浏览器能连 GitHub 不代表服务器能连），所以进度
 * 只能走后端 SSE；页面刷新后重新订阅也能补到当前状态。
 */
export function UpdateCard() {
  const [check, setCheck] = React.useState<UpdateCheck | null>(null);
  const [loading, setLoading] = React.useState(true);
  // applying 只控制"是否订阅 SSE"：下载本身在后端 goroutine 上跑。
  const [applying, setApplying] = React.useState(false);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  const [restarting, setRestarting] = React.useState(false);
  // 看门狗拿到的失败台账：非空即代表"这次升级没成"，界面必须说清楚。
  const [journalIssue, setJournalIssue] = React.useState<UpdateJournal | null>(null);
  const [autoCheck, setAutoCheck] = React.useState(true);
  const [autoApply, setAutoApply] = React.useState(false);
  const [savingPref, setSavingPref] = React.useState(false);
  const currentRef = React.useRef("");

  const load = React.useCallback((force: boolean) => {
    setLoading(true);
    api
      .checkUpdate(force)
      .then((c) => {
        setCheck(c);
        currentRef.current = c.current;
        // 后端已经把它下好放在磁盘上了（自动更新做的）：直接进入"重启生效"态。
        if (c.staged_version && c.staged_version !== c.current) setRestarting(false);
      })
      .catch((e) => toast.error(`检查更新失败：${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    // 首次进来读缓存（30 分钟内不重复问 GitHub），点按钮才强制回源。
    load(false);
    api
      .settings()
      .then((s) => {
        setAutoCheck(s.update_auto_check !== false);
        setAutoApply(!!s.update_auto_apply);
      })
      .catch(() => undefined);
  }, [load]);

  // 只在用户点了「立即更新」后订阅：平时不占一条长连接。
  React.useEffect(() => {
    if (!applying) return;
    const es = new EventSource(api.updateStreamUrl());
    es.onmessage = (ev) => {
      let p: UpdateProgress;
      try {
        p = JSON.parse(ev.data) as UpdateProgress;
      } catch {
        return; // 心跳/非 JSON 帧
      }
      setProgress(p);
      if (p.phase === "failed") {
        es.close();
        setApplying(false);
        toast.error(p.error || p.message || "更新失败");
      } else if (p.phase === "staged") {
        es.close();
        setApplying(false);
        setRestarting(true);
      }
    };
    // 进程换装时会主动断开这条流：交给下面的健康轮询判断新版本是否已经起来。
    return () => es.close();
  }, [applying]);

  // 换装看门狗：健康轮询负责"版本变了就刷新"，落盘台账负责"到底成没成"。
  //
  // 旧实现只有 2s 无限轮询：只要换装静默失败（暂存件被误启动、文件被锁、
  // 健康检查不过），页面就会永远停在"正在重启"，用户拿不到任何原因。
  // 这里 60s 后读一次 /api/update/journal，失败即显式收尾。
  React.useEffect(() => {
    if (!restarting) return;
    let alive = true;
    const startedAt = Date.now();
    const timer = window.setInterval(() => {
      api
        .health()
        .then((h) => {
          const v = (h.version ?? "").replace(/^v(?=\d)/, "");
          if (v && v !== currentRef.current) window.location.reload();
        })
        .catch(() => undefined);

      const elapsed = Date.now() - startedAt;
      if (elapsed < 60000) return;
      api
        .updateJournal()
        .then((j) => {
          if (!alive) return;
          const bad = j.state === "failed" || j.state === "rolled-back";
          // 台账还停在 staged/applying/swapped，说明换装压根没走完。
          // 等够 3 分钟仍无结论就显式收尾：无限转圈正是上一版的表现。
          const stuck =
            elapsed > 180000 && j.state !== "confirmed" && j.state !== "idle";
          if (bad || stuck) {
            setJournalIssue(j);
            setRestarting(false);
            window.clearInterval(timer);
          }
        })
        .catch(() => undefined);
    }, 3000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [restarting]);

  const apply = () => {
    setJournalIssue(null);
    setProgress({ phase: "idle", percent: -1, message: "正在准备…" });
    setApplying(true);
    api.applyUpdate().catch((e) => {
      setApplying(false);
      setProgress(null);
      toast.error(`更新失败：${(e as Error).message}`);
    });
  };

  const rollback = () => {
    if (!window.confirm("回滚会换回上一版本并重启服务，确定继续？")) return;
    setRestarting(true);
    api
      .rollbackUpdate()
      .then(() => toast.info("正在回滚，服务即将重启…"))
      .catch((e) => {
        setRestarting(false);
        toast.error(`回滚失败：${(e as Error).message}`);
      });
  };

  const savePref = (key: "update_auto_check" | "update_auto_apply", value: boolean) => {
    const prevCheck = autoCheck;
    const prevApply = autoApply;
    if (key === "update_auto_check") setAutoCheck(value);
    else setAutoApply(value);
    setSavingPref(true);
    api
      .setSettings({ [key]: value })
      .then((s) => {
        setAutoCheck(s.update_auto_check !== false);
        setAutoApply(!!s.update_auto_apply);
        toast.success("已保存");
        // 打开检测后后端会立刻查一次，这里顺手把结果拉回来。
        if (key === "update_auto_check" && value) load(false);
      })
      .catch((e) => {
        setAutoCheck(prevCheck);
        setAutoApply(prevApply);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingPref(false));
  };

  const docker = check?.mode === "docker";
  // canRestart：后端回答的是"当前环境能否自动重启"（以前问的是"有没有 start.sh /
  // start.bat 监护"）。进程现在会自己拉起新进程重开，脚本只是可选兜底。
  const canRestart = check?.supervised !== false;
  const staged = check?.staged_version && check.staged_version !== check.current ? check.staged_version : "";
  const canApply =
    !!check?.has_update &&
    !applying &&
    !restarting &&
    !docker &&
    check.comparable !== false &&
    check.asset_available !== false;
  const pct = progress && progress.percent >= 0 ? progress.percent : 0;

  return (
    // 设置页是多列瀑布流布局，卡片自己负责行间距并禁止跨列断开（见 page.tsx 的注释）。
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <CloudDownloadIcon className="size-4" />
          版本与更新
        </CardTitle>
        <CardDescription>
          更新源固定为本仓库的 GitHub Release，只接受带 SHA256 校验的正式版；检测走
          <span className="font-mono"> latest.json </span>
          清单，不消耗 GitHub API 配额。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge variant="secondary" className="font-mono">
            v{(check?.current ?? "…").replace(/^v(?=\d)/, "")}
          </Badge>
          {check?.repo && (
            <Badge variant="outline" className="font-mono">
              {check.repo}
            </Badge>
          )}
          {check?.os && (
            <Badge variant="outline" className="font-mono">
              {check.os}/{check.arch}
            </Badge>
          )}
          {docker && <Badge variant="outline">容器</Badge>}
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto"
            onClick={() => load(true)}
            disabled={loading || applying}
          >
            <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} />
            {loading ? "检查中…" : "检查更新"}
          </Button>
        </div>

        {/* 上次启动时的自更新结论：换装失败、已自动回滚等，必须让用户看见。 */}
        {check?.boot_notice && (
          <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2.5 text-xs">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0 text-amber-500" />
            <div>
              <div className="font-medium text-foreground">
                {check.rolled_back ? "上次升级已自动回滚" : "上次升级提示"}
              </div>
              <div className="text-muted-foreground">{check.boot_notice}</div>
            </div>
          </div>
        )}

        {restarting && (
          <div className="rounded-md border bg-muted/30 p-3 text-sm">
            <div className="font-medium">正在重启，切换到新版本…</div>
            <p className="mt-1 text-muted-foreground text-xs">
              服务会短暂中断，页面在检测到新版本后自动刷新。若长时间没有反应，请手动重启服务。
            </p>
          </div>
        )}
        {/* 升级失败：原因来自落盘台账，而不是一句"正在重启"。 */}
        {journalIssue && (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 p-3 text-sm">
            <div className="font-medium">升级未完成（{journalIssue.state}）</div>
            <p className="mt-1 text-muted-foreground text-xs">
              {journalIssue.last_error || journalIssue.detail || "未记录具体原因"}
            </p>
            <p className="mt-1 text-muted-foreground text-xs">
              详见程序目录下的 <span className="font-mono">logs/update.log</span> 与{" "}
              <span className="font-mono">updates/journal.json</span>。
              {journalIssue.pointer?.previous ? " 可先回滚到上一版本恢复服务。" : ""}
            </p>
          </div>
        )}
        {!restarting && staged && (
          <div className="rounded-md border border-emerald-500/40 bg-emerald-500/10 p-3 text-sm">
            <div className="font-medium">v{staged.replace(/^v(?=\d)/, "")} 已下载并校验完成</div>
            <p className="mt-1 text-muted-foreground text-xs">
              {canRestart
                ? "装好后进程会自己重启并切到新版本，不需要手动重启，也不需要 start.bat。"
                : "当前环境不能自动重启（例如容器内），更新无法在这里一键生效。"}
            </p>
          </div>
        )}

        {/* 进度条：只在真的在下载/校验时出现，避免和"已是最新"抢注意力。 */}
        {applying && progress && (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between text-xs">
              <span className="font-medium">
                {PHASE_LABEL[progress.phase]}
                {progress.version ? ` v${progress.version.replace(/^v(?=\d)/, "")}` : ""}
              </span>
              <span className="text-muted-foreground tabular-nums">
                {progress.phase === "downloading" && progress.percent >= 0 ? `${progress.percent}%` : ""}
              </span>
            </div>
            <Progress value={progress.phase === "downloading" ? pct : 100} className="h-1.5" />
            {progress.message && <p className="text-muted-foreground text-xs">{progress.message}</p>}
          </div>
        )}

        {check?.error && (
          <div className="flex items-start gap-2 rounded-md border p-2.5 text-xs">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0 text-amber-500" />
            <div>
              <div className="font-medium text-foreground">暂时查不到新版本</div>
              <div className="text-muted-foreground">{check?.error}</div>
            </div>
          </div>
        )}
        {!check?.error && check?.has_update && (
          <div className="space-y-2">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="font-medium">
                发现新版本 <span className="font-mono">v{(check?.latest ?? "").replace(/^v(?=\d)/, "")}</span>
              </span>
              {check?.published_at && (
                <span className="text-muted-foreground text-xs">发布于 {humanTime(check?.published_at)}</span>
              )}
              {humanSize(check?.size) && (
                <span className="text-muted-foreground text-xs">{humanSize(check?.size)}</span>
              )}
            </div>
            {check?.notes && (
              <details className="rounded-md border bg-muted/20 p-2.5 text-xs">
                <summary className="cursor-pointer select-none font-medium">更新说明</summary>
                <pre className="mt-2 max-h-56 overflow-auto whitespace-pre-wrap font-sans text-muted-foreground">
                  {check?.notes}
                </pre>
              </details>
            )}
            {!check?.comparable && check?.reason && <p className="text-amber-500 text-xs">{check?.reason}</p>}
            {check?.comparable !== false && check?.asset_available === false && (
              <p className="text-amber-500 text-xs">
                该版本没有提供 {check?.os}/{check?.arch} 的安装包（{check?.asset}），请等待重新发布。
              </p>
            )}
            {docker && (
              <p className="text-muted-foreground text-xs">
                容器内不支持一键换装（只改到容器可写层，重建容器即回退），请拉取新镜像后重建容器。
              </p>
            )}
            <div className="flex flex-wrap gap-2">
              <Button onClick={apply} disabled={!canApply}>
                <ArrowDownToLineIcon className="size-4" />
                {staged ? "重启并生效" : "立即更新"}
              </Button>
              {check?.html_url && (
                <Button asChild variant="outline">
                  <a href={check?.html_url} target="_blank" rel="noreferrer">
                    查看发布页
                  </a>
                </Button>
              )}
            </div>
          </div>
        )}
        {!check?.error && check?.has_update === false && (
          <div className="flex items-center gap-2 text-muted-foreground text-sm">
            <CheckCircle2Icon className="size-4 text-emerald-500" />
            已是最新版本
          </div>
        )}

        <div className="space-y-3 border-t pt-3">
          <div className="flex items-center justify-between gap-4">
            <div>
              <div className="text-sm">自动检测新版本</div>
              <p className="text-muted-foreground text-xs">
                每 6 小时查一次（只读版本清单，不下载、不改动任何东西）。关闭后后台不再访问 GitHub。
              </p>
            </div>
            <Switch
              checked={autoCheck}
              disabled={savingPref || loading}
              onCheckedChange={(v) => savePref("update_auto_check", v)}
            />
          </div>
          <div className="flex items-center justify-between gap-4">
            <div>
              <div className="text-sm">自动下载新版本</div>
              <p className="text-muted-foreground text-xs">
                {canRestart
                  ? "检测到新版本会自动下载并校验；装好后进程自己重启生效，不必守着控制台。"
                  : "当前环境不能自动重启（例如容器内），自动下载后无法自动生效。"}
              </p>
            </div>
            <Switch
              checked={autoApply}
              disabled={savingPref || loading || !canRestart || docker}
              onCheckedChange={(v) => savePref("update_auto_apply", v)}
            />
          </div>
          {check?.has_backup && (
            <Button variant="outline" size="sm" onClick={rollback} disabled={applying || restarting}>
              <RotateCcwIcon className="size-4" />
              回滚到上一版本
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
