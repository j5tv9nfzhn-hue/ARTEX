"use client";

import * as React from "react";

import { CheckCircle2Icon, FolderOpenIcon, TerminalIcon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api } from "@/lib/api";
import type { GitBashBrowse, GitBashDetect, ShellInfo } from "@/lib/types";

/** 工具调用层当前用的 shell。family 决定命令按哪套语法解析。 */
const FAMILY_LABEL: Record<string, string> = {
  bash: "Git Bash / POSIX bash",
  busybox: "BusyBox ash",
  pwsh: "PowerShell 7",
  powershell: "Windows PowerShell 5.1",
};

type ShellState = { variant: "destructive" | "secondary" | "outline"; text: string };

/** 状态徽标文案。后端 family 是字符串，未知值由调用处兜底，不能在这里假设枚举闭合。 */
const SHELL_STATE: Record<string, ShellState> = {
  // busybox 的 ash 是 POSIX 子集，个别 GNU 写法不支持，所以单独标出来而不是混进"就绪"。
  bash: { variant: "secondary", text: "POSIX shell 就绪" },
  busybox: { variant: "outline", text: "BusyBox ash（子集，个别 GNU 写法不支持）" },
  pwsh: { variant: "outline", text: "降级为 PowerShell 7（无 POSIX 工具）" },
  powershell: { variant: "destructive", text: "降级为 PowerShell 5.1（POSIX 命令会失败）" },
};

/** source 说明"这个结果是怎么来的"——判断配置有没有真的生效就看它。 */
function sourceLabel(source: string): string {
  switch (source) {
    case "explicit":
      return "使用你指定的路径";
    case "platform":
      return "当前平台固定使用系统 bash";
    case "fallback":
      return "自动降级";
    case "detect:path":
      return "自动探测（PATH）";
    case "detect:known":
      return "自动探测（常见安装位置）";
    case "detect:git":
      return "自动探测（由 git 位置推导）";
    case "detect:busybox":
      return "自动探测（BusyBox）";
    default:
      return source;
  }
}

type ProbeState = { ok: boolean; title: string; detail?: string; error?: string };

export function GitBashCard() {
  // 卡片自持状态：初始值从 /api/settings 的 git_bash 回显取，之后由本卡片自己的
  // 检测/保存动作刷新——与同页 UpdateCard 的做法一致，避免为一个卡片把整页的
  // apply() 链路牵进来。
  const [status, setStatus] = React.useState<ShellInfo | undefined>(undefined);
  const [path, setPath] = React.useState("");
  const [busy, setBusy] = React.useState<"" | "detect" | "test" | "save" | "browse">("");
  const [probe, setProbe] = React.useState<ProbeState | null>(null);
  const [browse, setBrowse] = React.useState<GitBashBrowse | null>(null);
  const [browseOpen, setBrowseOpen] = React.useState(false);

  React.useEffect(() => {
    api
      .settings()
      .then((s) => {
        setStatus(s.git_bash);
        setPath(s.git_bash?.configured ?? "");
      })
      .catch(() => undefined);
  }, []);

  const applicable = status?.applicable ?? false;
  const configuredPath = status?.configured ?? "";

  const onSaved = React.useCallback((s: ShellInfo) => {
    setStatus(s);
    setPath(s.configured);
  }, []);

  const applyResult = (r: GitBashDetect) => {
    if (r.effective) onSaved(r.effective);
  };

  const detect = async () => {
    setBusy("detect");
    setProbe(null);
    try {
      const r = await api.detectGitBash();
      applyResult(r);
      const found = r.ok && r.path ? r.path : "";
      setProbe({
        ok: !!r.ok,
        title: r.ok ? `检测通过：${found || "当前 shell 可用"}` : "检测未通过",
        detail: [r.version, r.detail].filter(Boolean).join(" · "),
        error: r.error,
      });
      // 自动探测命中且尚未配置时，顺手把路径填进输入框，等用户确认保存。
      if (found && applicable && !path.trim()) setPath(found);
      if (!r.ok) toast.error(r.detail || "未检测到可用的 Git Bash");
    } catch (e) {
      setProbe({ ok: false, title: "检测请求失败", error: (e as Error).message });
      toast.error("检测失败：" + (e as Error).message);
    } finally {
      setBusy("");
    }
  };

  const testPath = async () => {
    const p = path.trim();
    if (!p) {
      toast.error("请先填写或选择 bash.exe 的路径");
      return;
    }
    setBusy("test");
    setProbe(null);
    try {
      const r = await api.detectGitBash(p);
      applyResult(r);
      setProbe({
        ok: !!r.ok,
        title: r.ok ? "该路径可用" : "该路径不可用",
        detail: [r.shell?.path, r.version].filter(Boolean).join(" · "),
        error: r.error,
      });
      if (r.ok) toast.success("该 shell 可用，点「保存」即对下一条工具调用生效");
      else toast.error(r.error || r.detail || "该路径不可用");
    } catch (e) {
      setProbe({ ok: false, title: "测试请求失败", error: (e as Error).message });
      toast.error("测试失败：" + (e as Error).message);
    } finally {
      setBusy("");
    }
  };

  const save = async (value?: string) => {
    const p = (value ?? path).trim();
    setBusy("save");
    try {
      const s = await api.setSettings({ git_bash_path: p });
      if (s.git_bash) onSaved(s.git_bash);
      setProbe({
        ok: true,
        title: "已保存",
        detail: `当前生效：${s.git_bash?.path || "自动"}（${sourceLabel(s.git_bash?.source || "")}）`,
      });
      toast.success(p ? "已保存 Git Bash 路径，下一条工具调用即生效" : "已清空，回落到自动探测");
    } catch (e) {
      setProbe({ ok: false, title: "保存失败", error: (e as Error).message });
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setBusy("");
    }
  };

  const openBrowse = async (dir?: string) => {
    setBusy("browse");
    try {
      const r = await api.browseGitBash(dir);
      setBrowse(r);
      setBrowseOpen(true);
    } catch (e) {
      toast.error("浏览目录失败：" + (e as Error).message);
    } finally {
      setBusy("");
    }
  };

  // 状态徽标：见模块级 SHELL_STATE（含未知值兜底）。
  const st = !applicable
    ? { variant: "secondary" as const, text: "本平台无需配置" }
    : (SHELL_STATE[status?.family ?? ""] ?? { variant: "outline" as const, text: "状态未知" });
  const busyAny = busy !== "";

  return (
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <TerminalIcon className="size-4" />
          工具调用层 · Git Bash
        </CardTitle>
        <CardDescription>
          Windows 裸机上给 agent 的 Bash 工具一个真正的 POSIX shell（Git for Windows 的 bash.exe）。 没有它时命令会落到
          PowerShell，而模型写的是 <code>&amp;&amp;</code>、管道、heredoc、grep/sed 这类 POSIX
          语法，会持续报错。保存即生效，无需重启；路径失效时自动降级为 PowerShell 而不是报错卡死。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* 当前生效的 shell：这是判断"配置到底有没有生效"的唯一可信依据。 */}
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={st.variant}>{st.text}</Badge>
          <span className="font-mono text-sm">{status?.path || "—"}</span>
          <span className="text-muted-foreground text-xs">
            {FAMILY_LABEL[status?.family ?? ""] || status?.family || "未知"} · {sourceLabel(status?.source || "")}
          </span>
        </div>
        {!applicable && (
          <p className="text-muted-foreground text-xs">
            当前平台（{status?.platform || "?"}）固定使用系统 bash，本设置不参与解析；此处填写的路径仅作记录。
          </p>
        )}
        {applicable && status?.reason && <p className="text-muted-foreground text-xs">{status.reason}</p>}

        {/* 一键检测：真的起一个进程跑 `echo MARK && ...`，文件存在不等于语法可用。 */}
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={detect} disabled={busyAny}>
            {busy === "detect" ? "检测中…" : "一键检测 Git Bash"}
          </Button>
          <span className="text-muted-foreground text-xs">
            执行一条命令验证可用性；自动探测失败时改用下面的手动指定。
          </span>
        </div>

        {/* 手动输入/粘贴路径，或浏览文件系统选择（仅 Windows）。 */}
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <Input
              className="font-mono text-sm"
              placeholder={`C:\\Program Files\\Git\\bin\\bash.exe（留空=自动探测）`}
              value={path}
              disabled={busyAny}
              onChange={(e) => setPath(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void testPath();
              }}
            />
            <Button variant="outline" onClick={testPath} disabled={busyAny} className="shrink-0">
              {busy === "test" ? "测试中…" : "测试该路径"}
            </Button>
            {applicable && (
              <Button variant="outline" onClick={() => openBrowse()} disabled={busyAny} className="shrink-0">
                <FolderOpenIcon className="size-4" />
                浏览…
              </Button>
            )}
            <Button onClick={() => save()} disabled={busyAny} className="shrink-0">
              {busy === "save" ? "保存中…" : "保存"}
            </Button>
          </div>
          {configuredPath && (
            <div className="flex items-center gap-2">
              <Button
                variant="ghost"
                size="sm"
                className="text-muted-foreground h-6 px-2 text-xs"
                disabled={busyAny}
                onClick={() => save("")}
              >
                清空并回落自动探测
              </Button>
            </div>
          )}
        </div>

        {probe && (
          <div
            className={`flex items-start gap-2 rounded-md border px-3 py-2 text-xs ${
              probe.ok ? "border-emerald-500/40" : "border-destructive/40"
            }`}
          >
            {probe.ok ? (
              <CheckCircle2Icon className="text-emerald-600 mt-0.5 size-3.5 shrink-0" />
            ) : (
              <TriangleAlertIcon className="text-destructive mt-0.5 size-3.5 shrink-0" />
            )}
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">{probe.title}</span>
              {probe.detail && <span className="text-muted-foreground">{probe.detail}</span>}
              {probe.error && <span className="text-destructive">{probe.error}</span>}
            </div>
          </div>
        )}

        <Dialog open={browseOpen} onOpenChange={setBrowseOpen}>
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle className="text-sm">选择 bash.exe</DialogTitle>
            </DialogHeader>
            <div className="flex items-center gap-2 text-xs">
              <Button
                variant="outline"
                size="sm"
                disabled={!browse?.parent || busyAny}
                onClick={() => openBrowse(browse?.parent || undefined)}
              >
                返回上级
              </Button>
              <span className="text-muted-foreground truncate font-mono">{browse?.dir || "（选择盘符）"}</span>
            </div>
            <ScrollArea className="border-border h-72 rounded-md border p-2">
              <div className="flex flex-col gap-1">
                {(browse?.roots ?? []).map((r) => (
                  <Button
                    key={r}
                    variant="ghost"
                    size="sm"
                    className="justify-start font-mono text-xs"
                    onClick={() => openBrowse(r)}
                  >
                    {r}
                  </Button>
                ))}
                {(browse?.dirs ?? []).map((d) => (
                  <Button
                    key={d}
                    variant="ghost"
                    size="sm"
                    className="justify-start text-xs"
                    onClick={() => openBrowse(`${browse?.dir}\\${d}`)}
                  >
                    📁 {d}
                  </Button>
                ))}
                {(browse?.shells ?? []).map((f) => (
                  <Button
                    key={f.path}
                    variant="secondary"
                    size="sm"
                    className="justify-start font-mono text-xs"
                    onClick={() => {
                      setPath(f.path);
                      setBrowseOpen(false);
                      toast.success("已填入路径，建议先「测试该路径」再保存");
                    }}
                  >
                    ⚙ {f.name}
                  </Button>
                ))}
                {(browse?.dirs?.length ?? 0) + (browse?.shells?.length ?? 0) + (browse?.roots?.length ?? 0) === 0 && (
                  <p className="text-muted-foreground p-2 text-xs">该目录没有可选项。</p>
                )}
              </div>
            </ScrollArea>
            <p className="text-muted-foreground text-xs">
              只显示目录与常见 shell 可执行文件；不支持符号链接指向的目录。
            </p>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
