"use client";

import * as React from "react";

import { CheckIcon, CopyIcon, KeyRoundIcon, RefreshCwIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { MCPExposure } from "@/lib/types";

// 「对外接入」卡片：把 ARTEX 当成一台 MCP 服务器，给 Codex / OpenCode / Cursor /
// Claude Code / VS Code / Gemini CLI / DSH 这些外部 harness 挂上。
// 与下面的服务器列表方向相反：那里是 ARTEX 去连别人，这里是别人来连 ARTEX。

type ClientKey = "codex" | "opencode" | "cursor" | "claudeCode" | "vscode" | "gemini" | "dsh";

const EXE_FALLBACK = "C:\\ARTEX程序\\artex-rea.exe";

function buildSnippets(exe: string, endpoint: string): Record<ClientKey, string> {
  return {
    codex: `# ~/.codex/config.toml（表名必须是 mcp_servers，写成 JSON 会被静默忽略）
[mcp_servers.artex]
command = "${exe}"
args = ["mcp", "serve", "--stdio"]
startup_timeout_sec = 20
tool_timeout_sec = 300
enabled = true

# 远程形态（二选一）：
# [mcp_servers.artex]
# url = "${endpoint}"
# bearer_token_env_var = "ARTEX_MCP_TOKEN"`,
    opencode: `// ~/.config/opencode/opencode.json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "artex": {
      "type": "local",
      "command": ["${exe}", "mcp", "serve", "--stdio"],
      "enabled": true,
      "timeout": 15000
    }
  }
}

// 远程形态：type 改 "remote" + url + oauth:false
// { "mcp": { "artex": { "type": "remote", "url": "${endpoint}",
//     "headers": { "Authorization": "Bearer {env:ARTEX_MCP_TOKEN}" }, "oauth": false } } }`,
    cursor: `// %USERPROFILE%\\.cursor\\mcp.json
{
  "mcpServers": {
    "artex": {
      "url": "${endpoint}",
      "headers": { "Authorization": "Bearer \${env:ARTEX_MCP_TOKEN}" }
    }
  }
}
// 注意：cursor-agent CLI 不认 "streamable-http"，写了会让整份配置被静默丢弃；
// Cursor 对总工具数有上限（约 40–80），长尾工具请在控制台按档位收紧。`,
    claudeCode: `claude mcp add --transport http artex ${endpoint} \\
  --header "Authorization: Bearer $ARTEX_MCP_TOKEN" --scope user

# 或本地 stdio：
claude mcp add artex -- "${exe}" mcp serve --stdio
# 工具在 Claude Code 里呈现为 mcp__artex__pe_info`,
    vscode: `// .vscode/mcp.json —— 根键是 servers（唯一不同的一家）
{
  "inputs": [
    { "id": "artexToken", "type": "promptString", "description": "ARTEX MCP token", "password": true }
  ],
  "servers": {
    "artex": {
      "type": "http",
      "url": "${endpoint}",
      "headers": { "Authorization": "Bearer \${input:artexToken}" }
    }
  }
}`,
    gemini: `// ~/.gemini/settings.json —— httpUrl 才是 Streamable HTTP，url 是旧 SSE
{
  "mcpServers": {
    "artex": {
      "httpUrl": "${endpoint}",
      "headers": { "Authorization": "Bearer $ARTEX_MCP_TOKEN" },
      "timeout": 30000
    }
  }
}`,
    dsh: `# 原生插件（推荐）：
dsh plugin --profile web add artex-dsh

# 若坚持用官方 mcp-client 桥接，$DSH_HOME/cordis.patch.yml 加一行：
plugins:
  artex:
    transport: streamable-http
    url: ${endpoint}
    headers:
      Authorization: "Bearer \${ARTEX_MCP_TOKEN}"`,
  };
}

export function MCPExposureCard() {
  const t = useTranslations("mcpExposure");
  const [status, setStatus] = React.useState<MCPExposure | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [revealed, setRevealed] = React.useState(false);
  const [copied, setCopied] = React.useState<string | null>(null);

  const load = React.useCallback(() => {
    api
      .mcpExposure()
      .then(setStatus)
      .catch((e) => toast.error(String(e)));
  }, []);

  React.useEffect(load, [load]);

  const endpoint = React.useMemo(() => {
    if (status?.endpoint && status.endpoint.startsWith("http")) return status.endpoint;
    const origin = typeof window === "undefined" ? "http://127.0.0.1:8787" : window.location.origin;
    return `${origin}/mcp`;
  }, [status?.endpoint]);

  const exe = status?.exe || EXE_FALLBACK;
  const snippets = React.useMemo(() => buildSnippets(exe, endpoint), [exe, endpoint]);

  const copy = async (key: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(key);
      window.setTimeout(() => setCopied((cur) => (cur === key ? null : cur)), 1500);
    } catch {
      toast.error(t("copyFailed"));
    }
  };

  const toggleEnabled = async (next: boolean) => {
    if (!status) return;
    setBusy(true);
    try {
      setStatus(await api.saveMcpExposure({ enabled: next }));
    } catch (e) {
      toast.error(String(e));
    } finally {
      setBusy(false);
    }
  };

  const toggleTier = async (id: string) => {
    if (!status || id === "L3") return;
    const has = status.tiers.includes(id);
    const next = has ? status.tiers.filter((x) => x !== id) : [...status.tiers, id];
    if (next.length === 0) return;
    setBusy(true);
    try {
      setStatus(await api.saveMcpExposure({ tiers: next }));
    } catch (e) {
      toast.error(String(e));
    } finally {
      setBusy(false);
    }
  };

  const rotate = async () => {
    setBusy(true);
    try {
      setStatus(await api.rotateMcpExposureToken());
      toast.success(t("rotated"));
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
            <KeyRoundIcon className="size-4 shrink-0 text-primary/80" />
            <CardTitle className="text-base">{t("title")}</CardTitle>
            {status ? (
              <Badge variant={status.enabled ? "default" : "secondary"}>{status.enabled ? t("on") : t("off")}</Badge>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" onClick={load} disabled={busy}>
              <RefreshCwIcon className="size-4" />
              {t("refresh")}
            </Button>
            <Switch
              checked={Boolean(status?.enabled)}
              onCheckedChange={toggleEnabled}
              disabled={busy || !status}
              aria-label={t("title")}
            />
          </div>
        </div>
        <p className="text-muted-foreground text-sm">{t("subtitle")}</p>
      </CardHeader>

      <CardContent className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <div className="rounded-lg border border-border/70 p-3">
            <div className="mb-1 text-muted-foreground text-xs">{t("endpointLabel")}</div>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded bg-muted px-2 py-1 font-mono text-xs">{endpoint}</code>
              <Button variant="ghost" size="icon" onClick={() => copy("endpoint", endpoint)} aria-label={t("copy")}>
                {copied === "endpoint" ? <CheckIcon className="size-4" /> : <CopyIcon className="size-4" />}
              </Button>
            </div>
            <div className="mt-2 text-muted-foreground text-xs">{t("stdioHint")}</div>
            <code className="mt-1 block truncate rounded bg-muted px-2 py-1 font-mono text-xs">
              "{exe}" mcp serve --stdio
            </code>
          </div>

          <div className="rounded-lg border border-border/70 p-3">
            <div className="mb-1 flex items-center justify-between gap-2">
              <span className="text-muted-foreground text-xs">{t("tokenLabel")}</span>
              <div className="flex items-center gap-1">
                <Button variant="ghost" size="sm" onClick={() => setRevealed((v) => !v)}>
                  {revealed ? t("hide") : t("show")}
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => copy("token", status?.token ?? "")}
                  aria-label={t("copy")}
                >
                  {copied === "token" ? <CheckIcon className="size-4" /> : <CopyIcon className="size-4" />}
                </Button>
                <Button variant="outline" size="sm" onClick={rotate} disabled={busy}>
                  {t("rotate")}
                </Button>
              </div>
            </div>
            <code className="block truncate rounded bg-muted px-2 py-1 font-mono text-xs">
              {revealed ? (status?.token ?? "") : (status?.token_mask ?? "")}
            </code>
            <div className="mt-2 text-muted-foreground text-xs">{t("tokenHint")}</div>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <span className="text-muted-foreground text-xs">{t("tiersLabel")}</span>
          {(status?.all_tiers ?? []).map((tier) => {
            const disabled = tier.id === "L3";
            const active = status?.tiers.includes(tier.id);
            return (
              <button
                key={tier.id}
                type="button"
                onClick={() => toggleTier(tier.id)}
                disabled={busy || disabled}
                title={disabled ? t("l3Hint") : tier.label}
                className={
                  active
                    ? "rounded-full border border-primary/60 bg-primary/10 px-3 py-1 font-medium text-primary text-xs"
                    : "rounded-full border border-border/80 px-3 py-1 text-muted-foreground text-xs hover:border-primary/40 disabled:opacity-50"
                }
              >
                {tier.id} · {tier.label}
                {disabled ? ` · ${t("l3Short")}` : ""}
              </button>
            );
          })}
          <span className="text-muted-foreground text-xs">{t("toolCount", { count: status?.tool_count ?? 0 })}</span>
        </div>

        {status && status.tiers.includes("L2") ? (
          <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-amber-700 text-xs dark:text-amber-400">
            {t("l2Warning")}
          </div>
        ) : null}

        <div>
          <div className="mb-2 font-medium text-sm">{t("clientsTitle")}</div>
          <Tabs defaultValue="codex">
            <TabsList className="flex-wrap">
              <TabsTrigger value="codex">Codex</TabsTrigger>
              <TabsTrigger value="opencode">OpenCode</TabsTrigger>
              <TabsTrigger value="cursor">Cursor</TabsTrigger>
              <TabsTrigger value="claudeCode">Claude Code</TabsTrigger>
              <TabsTrigger value="vscode">VS Code</TabsTrigger>
              <TabsTrigger value="gemini">Gemini CLI</TabsTrigger>
              <TabsTrigger value="dsh">DSH</TabsTrigger>
            </TabsList>
            {(Object.keys(snippets) as ClientKey[]).map((key) => (
              <TabsContent key={key} value={key} className="mt-2">
                <div className="relative">
                  <pre className="max-h-[320px] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-relaxed">
                    {snippets[key]}
                  </pre>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="absolute top-2 right-2"
                    onClick={() => copy(key, snippets[key])}
                  >
                    {copied === key ? <CheckIcon className="size-4" /> : <CopyIcon className="size-4" />}
                    {copied === key ? t("copied") : t("copy")}
                  </Button>
                </div>
              </TabsContent>
            ))}
          </Tabs>
          <p className="mt-2 text-muted-foreground text-xs">{t("clientsHint")}</p>
        </div>
      </CardContent>
    </Card>
  );
}
