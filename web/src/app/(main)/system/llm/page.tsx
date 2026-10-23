"use client";

import * as React from "react";

import {
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
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
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { LLMPoolMember, LLMPoolStatus, LLMProfile, LLMProvider, LLMRetryOverride } from "@/lib/types";
import { cn } from "@/lib/utils";

import { ProfileRetryFields, RetryPolicyPanel, ZERO_OVERRIDE } from "./_components/retry";

// 思考开关(thinking.type)与思考强度(reasoning_effort)是两个【互相独立】的字段，
// 各自单独设置——有些接口没有 thinking 字段、只靠强度参数就能激活思考，故需解耦。
// 存库空字符串 = 该字段【不发送】；Radix Select 不接受空 value，故 UI 用 "none"
// 哨兵表示不发送，存取时与 "" 互转（NONE / fromStore / toStore）。
const NONE = "none";
const fromStore = (v?: string) => (v ? v : NONE);
const toStore = (v: string) => (v === NONE ? "" : v);

// 凭据选择器的「内联 Key」哨兵：Radix Select 不接受空字符串 value，与 NONE 同理；
// 存取时与 ""（= 未绑定，用这家供应商自己的 api_key）互转。

type LLMFormat = "anthropic" | "openai" | "openai-responses";

// 输入模态复选框与存储串互转。存储格式是逗号分隔的 text/image/audio/pdf（后端
// 保存时归一）；"" = 未声明，后端按模型名保守推断。text 由后端始终隐含，界面上
// 固定勾选且禁用，因此这里只处理三种「扩展模态」。
const EXTRA_MODALITIES = ["image", "audio", "pdf"] as const;
type ExtraModality = (typeof EXTRA_MODALITIES)[number];
const extraModalities = (v?: string): ExtraModality[] =>
  (v ?? "")
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter((m): m is ExtraModality => (EXTRA_MODALITIES as readonly string[]).includes(m));

function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

// 一条模型实际会发出去的输出上限。真正调用时后端还会再按「窗口 − 输入 − margin」
// 钳一次（只降不升、溢出即放弃钳制交给溢出恢复），这里只推演「配置 vs 模型上限」
// 这一层——它正是用户在设置页能直接改坏的那一层：填超了会被静默压到上限，
// 界面上必须看得见压成了多少，否则「我明明填了 64K」会变成长期的口径纠纷。
function effectiveMaxOutput(p: LLMProfile): { value: number; cap: number; configured: number; capped: boolean } {
  const configured = Math.max(0, p.max_tokens ?? 0);
  const cap = Math.max(0, p.caps?.max_output_tokens ?? 0);
  if (configured <= 0) return { value: cap, cap, configured, capped: false };
  if (cap <= 0) return { value: configured, cap, configured, capped: false };
  return { value: Math.min(configured, cap), cap, configured, capped: configured > cap };
}

// 一个模型在卡片上显示的「是否正常」。没填 Key 的模型根本发不出请求，比熔断更该
// 先说；其余状态来自轮询的熔断记录（轮询关着时不会产生新记录，此时「正常」=
// 没有已知故障）。文案走 i18n（llmPage.health.*），所以把 t 传进来由调用方注入。
type Health = { label: string; cls: string; hint?: string };

// ─────────────────────────────────────────────────────────────────────────────
// 轮询配置抽屉
// ─────────────────────────────────────────────────────────────────────────────

function PoolSheet({
  open,
  onOpenChange,
  pool,
  onReload,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  pool: LLMPoolStatus | null;
  onReload: () => Promise<void>;
}) {
  const t = useTranslations("llmPage");
  const [busy, setBusy] = React.useState(false);

  // 冷却倒计时是后端算出的剩余秒数——抽屉开着且有配置不正常时才定时拉，让它走起来。
  // 条件不满足时 usePolling 直接停表，语义与原 effect 的提前 return 一致。
  const poolNeedsTick = open && !!pool?.enabled && !!pool.chain.some((m) => m.state !== "ok");
  usePolling(() => onReload(), { intervalMs: 10_000, enabled: poolNeedsTick });

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await onReload();
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? t("pool.toast.enabled") : t("pool.toast.disabled"));
      } else {
        toast.success(t("pool.toast.fallbackUpdated"));
      }
    } catch (e) {
      toast.error(t("pool.toast.setFailed", { msg: (e as Error).message }));
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? t("pool.toast.recoveredOne") : t("pool.toast.recoveredAll"));
    } catch (e) {
      toast.error(t("pool.toast.recoverFailed", { msg: (e as Error).message }));
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // 参与轮询的成员（排除被标记「不参与轮询」的），顺序即后端实际的尝试顺序。
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> {t("pool.title")}
          </SheetTitle>
          <SheetDescription>{t.rich("pool.desc", { b: (c) => <b>{c}</b> })}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-6">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">{t("pool.enable.label")}</Label>
              <p className="text-muted-foreground text-xs">{t("pool.enable.hint")}</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label={t("pool.enable.aria")}
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">{t("pool.bindFallback.label")}</Label>
                  <p className="text-muted-foreground text-xs">{t("pool.bindFallback.hint")}</p>
                </div>
                <Switch
                  checked={pool?.bind_fallback ?? false}
                  disabled={busy}
                  onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
                  aria-label={t("pool.bindFallback.aria")}
                />
              </div>

              <Separator />

              <div className="grid gap-2">
                <div className="flex items-center justify-between">
                  <Label className="text-sm">{t("pool.order.label")}</Label>
                  {tripped.length > 0 && (
                    <Button size="sm" variant="ghost" onClick={() => void recover()}>
                      <RotateCcwIcon /> {t("pool.recoverAll")}
                    </Button>
                  )}
                </div>
                {inChain.length < 2 && (
                  <p className="text-muted-foreground text-xs">{t("pool.order.tooFew", { n: inChain.length })}</p>
                )}
                {chain.map((m) => {
                  const excluded = m.excluded && !m.active;
                  const order = excluded ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                  return (
                    <div
                      key={m.profile_id}
                      className={cn(
                        "grid gap-1 rounded-lg border p-2.5 text-sm",
                        excluded && "opacity-55",
                        m.state === "tripped" && "border-destructive/40",
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs">
                          {order ?? "—"}
                        </span>
                        <span className="font-medium">{m.name}</span>
                        {m.active && (
                          <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                            {t("pool.badge.active")}
                          </Badge>
                        )}
                        {excluded && <Badge variant="outline">{t("pool.badge.excluded")}</Badge>}
                        <div className="ml-auto flex items-center gap-2">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="text-muted-foreground text-xs">
                              {t("pool.cooldown", { cooldown: cooldownText(m.cooldown_secs) })}
                            </span>
                          )}
                          {m.state === "degraded" && (
                            <span className="text-muted-foreground text-xs">
                              {t("pool.consecutiveFails", { fails: m.fails })}
                            </span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              aria-label={t("pool.recoverNow.aria")}
                              title={t("pool.recoverNow.title")}
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        {/* 供应商名：轮询链按模型排序，同一家可能连着出现多条，
                            标出归属才知道「换的到底是端点还是只是模型」。 */}
                        {m.provider_name && <span>{m.provider_name}</span>}
                        {!m.active && <span>{t("pool.priority", { priority: m.priority })}</span>}
                      </div>
                      {m.last_error && (
                        <p className="truncate pl-7 font-mono text-muted-foreground text-xs" title={m.last_error}>
                          {m.last_error}
                        </p>
                      )}
                    </div>
                  );
                })}
                {chain.length === 0 && (
                  <div className="rounded-lg border border-dashed">
                    <EmptyState compact illustration="nodes" title={t("pool.empty")} />
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                {t("pool.note")}
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 供应商抽屉（新建 / 编辑）
//
// 这一层只放「一家端点共享的字段」：格式、Base URL、代理、Key/凭据、端点级限速、
// 会话头。模型自己的参数全在模型抽屉里——代理与 Key 在这里只维护一次，就是这次
// 重构的目的本身。
// ─────────────────────────────────────────────────────────────────────────────

function ProviderSheet({
  provider,
  open,
  onOpenChange,
  onSaved,
}: {
  provider: LLMProvider | null; // null = 新建
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const t = useTranslations("llmPage");
  const isNew = !provider;
  const [name, setName] = React.useState("");
  const [format, setFormat] = React.useState<LLMFormat>("anthropic");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [sessionHeaderKey, setSessionHeaderKey] = React.useState("");
  // 连接测试要一个模型名。供应商本身不带模型，默认拿名下第一条去测；
  // 还没有模型时让用户手填，反正只是探端点通不通。
  const [testModel, setTestModel] = React.useState("");
  const [testing, setTesting] = React.useState(false);
  const [testElapsed, setTestElapsed] = React.useState(0);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setName(provider?.name ?? "");
    setFormat(
      provider?.format === "openai" || provider?.format === "openai-responses" ? provider.format : "anthropic",
    );
    setBaseUrl(provider?.base_url ?? "");
    setProxy(provider?.proxy ?? "");
    setApiKey("");
    setKeyHint(provider?.api_key_hint ?? "");
    setRps(String(provider?.rate_per_second ?? 0));
    setRpm(String(provider?.rate_per_minute ?? 0));
    setSessionHeaderKey(provider?.session_header_key ?? "");
    setTestModel(provider?.models?.[0]?.model ?? "");
  }, [open, provider]);

  const providerId = provider ? Number(provider.id) : undefined;
  // 名下第一条模型：测试时把它的 id 递过去，后端据此取到「这条供应商的 Key」
  // （读路径本来就是 COALESCE(供应商, 模型)）。
  const sampleProfileId = provider?.models?.length ? Number(provider.models[0].id) : undefined;

  async function testConnection() {
    if (testing) return;
    const model = testModel.trim();
    if (!model) {
      toast.error(t("provider.testNeedModel"));
      return;
    }
    setTesting(true);
    setTestElapsed(0);
    const startedAt = Date.now();
    const ticker = setInterval(() => setTestElapsed(Math.floor((Date.now() - startedAt) / 1000)), 1000);
    try {
      const r = await api.testLLM(
        format,
        model,
        baseUrl,
        apiKey,
        proxy,
        "",
        "",
        sampleProfileId,
        sessionHeaderKey.trim(),
      );
      const keyLine = r.key_source ? t("profile.toast.testKey", { source: r.key_source, hint: r.key_hint ?? "" }) : "";
      if (r.ok)
        toast.success(t("profile.toast.testOk", { latency: r.latency_ms ?? "?", model: r.model ?? model }), {
          description:
            [r.reply ? t("profile.toast.testReply", { reply: r.reply }) : "", keyLine].filter(Boolean).join(" · ") ||
            undefined,
        });
      else
        toast.error(t("profile.toast.testFailed", { error: r.error ?? t("profile.toast.unknown") }), {
          description: keyLine || undefined,
        });
    } catch (e) {
      toast.error(t("profile.toast.testError", { msg: (e as Error).message }));
    } finally {
      clearInterval(ticker);
      setTesting(false);
    }
  }

  async function save() {
    if (!name.trim()) {
      toast.error(t("provider.toast.nameRequired"));
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProvider({
        ...(providerId ? { id: providerId } : {}),
        name: name.trim(),
        format,
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        // trim 后再提交：粘贴 Key 时常带首尾空白/换行，原样入库会让鉴权静默失败。
        api_key: apiKey.trim(),
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        session_header_key: sessionHeaderKey.trim(),
      });
      toast.success(isNew ? t("provider.toast.created", { name: name.trim() }) : t("provider.toast.saved"));
      onSaved(id);
      onOpenChange(false);
    } catch (e) {
      toast.error(t("provider.toast.saveFailed", { msg: (e as Error).message }));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <PlugZapIcon className="size-4" />
            {isNew ? t("provider.newTitle") : t("provider.editTitle", { name: provider?.name ?? "" })}
          </SheetTitle>
          <SheetDescription>{isNew ? t("provider.descNew") : t("provider.descEdit")}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="v-name">{t("provider.name.label")}</Label>
              <Input
                id="v-name"
                placeholder={t("provider.name.placeholder")}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>{t("profile.format.label")}</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as LLMFormat)}>
                <SelectTrigger>
                  <SelectValue placeholder={t("profile.format.placeholder")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI (Chat Completions)</SelectItem>
                  <SelectItem value="openai-responses">OpenAI (Responses API)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="v-base-url">{t("profile.baseUrl.label")}</Label>
            <Input
              id="v-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="v-proxy">{t("profile.proxy.label")}</Label>
            <Input
              id="v-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">{t("profile.proxy.hint")}</p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="v-session-header">{t("profile.sessionHeader.label")}</Label>
            <Input
              id="v-session-header"
              className="font-mono"
              placeholder={t("profile.sessionHeader.placeholder")}
              value={sessionHeaderKey}
              onChange={(e) => setSessionHeaderKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              {t.rich("profile.sessionHeader.hint", { b: (c) => <b>{c}</b> })}
            </p>
          </div>

          {/* Key 就住在这家供应商身上——没有「从别处挑一把」的第二条来源，
              所以这里恒显示输入框。空着不填 = 保持原 Key 不变（后端只在给了
              新 Key 时才覆盖）。 */}
          <div className="grid gap-2">
            <Label htmlFor="v-api-key">API Key</Label>
            <Input
              id="v-api-key"
              // 阻止浏览器密码管理器把已保存的登录密码自动填进来（同模型表单）。
              name={`llm-provider-key-${provider?.id ?? "new"}`}
              autoComplete="new-password"
              type="password"
              placeholder={keyHint ? t("profile.apiKey.placeholderSet", { hint: keyHint }) : "sk-…"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">{t("profile.apiKey.hint")}</p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="v-rps">{t("profile.rps")}</Label>
              <Input id="v-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="v-rpm">{t("profile.rpm")}</Label>
              <Input id="v-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
          </div>
          <p className="-mt-2 text-muted-foreground text-xs">{t("profile.rateHint")}</p>

          <div className="grid gap-2">
            <Label htmlFor="v-test-model">{t("provider.testModel")}</Label>
            <Input
              id="v-test-model"
              className="font-mono"
              placeholder="claude-opus-4-8"
              value={testModel}
              onChange={(e) => setTestModel(e.target.value)}
            />
          </div>
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={testConnection} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {testing ? t("profile.testingElapsed", { s: testElapsed }) : t("profile.testConnection")}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? t("profile.create") : t("profile.save")}
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 模型抽屉（新建 / 编辑共用同一套表单）
//
// 只提交「模型自己的参数」。格式 / Base URL / 代理 / Key / 限速 / 会话头全部属于
// 供应商：这里既不显示可编辑的输入框、也不往请求里塞这些字段（后端收到 nil 就
// 不动供应商行）。「改到这里等于把模型挪到另一家供应商下」是唯一的例外——那是
// 换归属，不是改端点。
// ─────────────────────────────────────────────────────────────────────────────

function ModelSheet({
  profile,
  providerId,
  providers,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = 新建
  providerId: string; // 新建时的默认归属；编辑时由 profile.provider_id 覆盖
  providers: LLMProvider[];
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const t = useTranslations("llmPage");
  const isNew = !profile;
  const [name, setName] = React.useState("");
  const [pid, setPid] = React.useState("");
  const [model, setModel] = React.useState("");
  const [cw, setCw] = React.useState("0"); // 上下文窗口(K tokens);0=默认 200K
  const [thinkingType, setThinkingType] = React.useState(NONE);
  const [effort, setEffort] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // 轮询顺位;越大越先
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [streaming, setStreaming] = React.useState(true); // true=流式(默认);false=非流式
  const [maxTokens, setMaxTokens] = React.useState("0"); // 单次回复输出上限;0=不发送
  const [maxTokensField, setMaxTokensField] = React.useState(NONE); // 上限用哪个字段名;NONE=max_tokens
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // 本模型的重试覆盖;全 0=跟随全局
  // 声明的扩展输入模态(image/audio/pdf);空数组 = 未声明,后端按模型名保守推断。
  const [modalities, setModalities] = React.useState<ExtraModality[]>([]);
  const [probing, setProbing] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  // 思考开关 / 上限字段名 / 思考强度三组选项的标签走 i18n，故在组件内按当前语言构造。
  const THINKING_TYPES: { value: string; label: string }[] = [
    { value: NONE, label: t("profile.thinkingType.opt.none") },
    { value: "disabled", label: t("profile.thinkingType.opt.disabled") },
    { value: "enabled", label: t("profile.thinkingType.opt.enabled") },
  ];
  const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
    { value: NONE, label: t("profile.maxTokensField.opt.default") },
    { value: "max_completion_tokens", label: "max_completion_tokens" },
  ];
  const EFFORT_LEVELS: { value: string; label: string }[] = [
    { value: NONE, label: t("profile.effort.opt.none") },
    { value: "low", label: "low" },
    { value: "medium", label: "medium" },
    { value: "high", label: "high" },
    { value: "xhigh", label: "xhigh" },
    { value: "max", label: "max" },
  ];

  // 每次打开时从传入的 profile 灌一遍表单（新建则重置为默认值）。抽屉关掉再打开
  // 就是一次干净的开始，不会留下上一条模型的残影。
  React.useEffect(() => {
    if (!open) return;
    setName(profile?.name ?? "");
    setPid(profile?.provider_id ?? providerId);
    setModel(profile?.model ?? "");
    setCw(String(profile?.context_window_k ?? 0));
    setThinkingType(fromStore(profile?.thinking_type));
    setEffort(fromStore(profile?.reasoning_effort));
    setPriority(String(profile?.priority ?? 0));
    setPoolExclude(profile?.pool_exclude ?? false);
    setStreaming(profile?.streaming ?? true);
    setMaxTokens(String(profile?.max_tokens ?? 0));
    setMaxTokensField(fromStore(profile?.max_tokens_field));
    setRetry(profile?.retry ?? ZERO_OVERRIDE);
    setModalities(extraModalities(profile?.input_modalities));
    setModels([]);
    setModelsOpen(false);
  }, [open, profile, providerId]);

  const endpoint = providers.find((v) => v.id === pid);
  const format = endpoint?.format ?? "anthropic";
  const profileId = profile ? Number(profile.id) : undefined;
  // 生效上限跟随输入框实时推演（而不是等保存后回读），否则用户改了数字却看不到
  // 「其实会被压到多少」，这条提示就白写了。cap 来自能力快照，没探测过时为 0。
  const cap = Math.max(0, profile?.caps?.max_output_tokens ?? 0);
  const asked = Math.max(0, Number(maxTokens) || 0);
  const effValue = asked > 0 ? (cap > 0 ? Math.min(asked, cap) : asked) : cap;
  const capped = asked > 0 && cap > 0 && asked > cap;

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      // 端点参数取自供应商（只读），Key 留空 + 传 profile id：后端据 profile 的
      // 读路径（COALESCE 供应商 Key）取到真正会用的那把。
      const r = await api.fetchLLMModels(
        format,
        endpoint?.base_url ?? "",
        "",
        endpoint?.proxy ?? "",
        profileId,
      );
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(t("profile.toast.modelsLoaded", { n: r.models.length }));
      } else {
        toast.error(t("profile.toast.loadModelsFailed", { error: r.error ?? t("profile.toast.noModels") }));
      }
    } catch (e) {
      toast.error(t("profile.toast.loadModelsError", { msg: (e as Error).message }));
    } finally {
      setLoadingModels(false);
    }
  }

  // 手动重新探测：窗口 / 最大输出 / 模态 / 推理档位。成功后重新拉列表，
  // 让「生效上限」与压缩口径立刻按新能力显示。
  async function reprobe() {
    if (!profile || probing) return;
    setProbing(true);
    try {
      const r = await api.probeLLMProfile(profile.id);
      if (r.ok) {
        toast.success(t("model.reprobeOk", { k: Math.round(r.effective_context_window_k ?? 0) }));
        onSaved(profile.id);
      } else {
        toast.error(t("model.reprobeFailed", { msg: r.error ?? t("profile.toast.unknown") }));
      }
    } catch (e) {
      toast.error(t("model.reprobeFailed", { msg: (e as Error).message }));
    } finally {
      setProbing(false);
    }
  }

  async function save() {
    if (!name.trim() || !model.trim()) {
      toast.error(t("profile.toast.nameModelRequired"));
      return;
    }
    // 没有归属就没法保存：端点（于是格式）只存在于供应商上，后端拿不到 format
    // 就只能报「不支持的格式」。这里先说明真正该做的事。
    if (!pid) {
      toast.error(t("model.providerRequired"));
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProfile({
        ...(profile ? { id: Number(profile.id) } : {}),
        name: name.trim(),
        model: model.trim(),
        // 归属：新界面永远显式表态，后端据此决定这条模型用哪套端点与 Key。
        ...(pid ? { provider_id: Number(pid) } : {}),
        context_window_k: Number(cw) || 0,
        thinking_type: toStore(thinkingType),
        reasoning_effort: toStore(effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
        streaming,
        max_tokens: Math.max(0, Number(maxTokens) || 0),
        // 字段名开关只对 openai(Chat Completions) 有意义，其它格式一律回落到默认；
        // 后端也会再做一次同样的归一化，这里只是别让 UI 送出自相矛盾的值。
        max_tokens_field: format === "openai" ? toStore(maxTokensField) : "",
        retry,
        // 三种扩展模态全不勾 = 未声明（"" 交给后端按模型名保守推断）；勾选即显式
        // 声明。text 始终隐含，由后端归一补上，这里一并写出规范顺序。
        input_modalities: modalities.length
          ? ["text", ...EXTRA_MODALITIES.filter((m) => modalities.includes(m))].join(",")
          : "",
        // 注意：这里【不】发 format / base_url / proxy / rate_* / session_header_key /
        // api_key。它们属于供应商，混进来就会让「只改模型」变成「顺手把端点覆盖
        // 成表单里的旧值」。后端按 nil = 不改处理。
      });
      if (isNew) toast.success(t("profile.toast.created", { name: name.trim() }));
      else toast.success(profile?.is_default ? t("profile.toast.savedActive") : t("profile.toast.saved"));
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(t("profile.toast.saveFailed", { msg: (e as Error).message }));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? t("profile.titleNew") : t("profile.titleEdit", { name: profile?.name ?? "" })}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                {t("profile.activeBadge")}
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>{isNew ? t("profile.descNew") : t("profile.descEdit")}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">{t("profile.name.label")}</Label>
              <Input
                id="p-name"
                placeholder={t("profile.name.placeholder")}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>{t("model.providerLabel")}</Label>
              <Select value={pid} onValueChange={setPid}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {providers.map((v) => (
                    <SelectItem key={v.id} value={v.id}>
                      {v.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {/* 只读的端点摘要：让「这条模型跑在哪个端点、用谁的 Key」一眼可见，
              同时明确告知改这些要去供应商那边改。 */}
          <div className="grid gap-1.5 rounded-lg border border-dashed p-3 text-xs">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline" className="uppercase">
                {format}
              </Badge>
              <code className="truncate font-mono text-muted-foreground">{endpoint?.base_url || "—"}</code>
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground">
              {endpoint?.api_key_hint ? (
                <span>{endpoint.api_key_hint}</span>
              ) : (
                <span className="text-destructive">{t("health.noKey.label")}</span>
              )}
              <span>
                {endpoint?.rate_per_second ?? 0}/s · {endpoint?.rate_per_minute ?? 0}/min
              </span>
              {endpoint?.proxy && <span className="truncate">{t("card.proxy", { proxy: endpoint.proxy })}</span>}
              {endpoint?.session_header_key && <span className="font-mono">{endpoint.session_header_key}</span>}
            </div>
            <p className="text-muted-foreground leading-relaxed">{t("model.providerHint")}</p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-model">{t("profile.model.label")}</Label>
            <div className="flex gap-2">
              <Input
                id="p-model"
                className="font-mono"
                placeholder="claude-opus-4-8"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
              {/* modal: 这个 Popover 的内容被 portal 到 <body>，在 Sheet 的滚动锁之外，
                  不加 modal 时列表能渲染却滚不动。modal 让它自己持有最上层滚动锁。 */}
              <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    className="shrink-0"
                    disabled={loadingModels}
                    onClick={loadModels}
                    title={t("profile.model.loadTitle")}
                  >
                    {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                  </Button>
                </PopoverTrigger>
                {models.length > 0 && (
                  <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                    {models.map((m) => (
                      <button
                        key={m}
                        type="button"
                        className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                        onClick={() => {
                          setModel(m);
                          setModelsOpen(false);
                        }}
                      >
                        {m}
                      </button>
                    ))}
                  </PopoverContent>
                )}
              </Popover>
            </div>
          </div>

          <div className="grid gap-2">
            <Label>{t("profile.modalities.label")}</Label>
            <div className="grid gap-2 rounded-lg border p-3">
              {/* text 由后端始终隐含（任何能读消息的模型都能读文本），固定勾选、
                  禁用，不参与编辑，避免出现「不支持文本」这种无意义的声明。 */}
              <div className="flex items-center gap-2">
                <Checkbox id="p-mod-text" checked disabled />
                <Label htmlFor="p-mod-text" className="text-sm">
                  {t("profile.modalities.opt.text")}
                </Label>
                <span className="text-muted-foreground text-xs">{t("profile.modalities.textAlways")}</span>
              </div>
              {EXTRA_MODALITIES.map((m) => (
                <div key={m} className="flex items-center gap-2">
                  <Checkbox
                    id={`p-mod-${m}`}
                    checked={modalities.includes(m)}
                    onCheckedChange={(v) =>
                      setModalities((prev) => (v === true ? [...prev, m] : prev.filter((x) => x !== m)))
                    }
                  />
                  <Label htmlFor={`p-mod-${m}`} className="text-sm">
                    {t(`profile.modalities.opt.${m}`)}
                  </Label>
                </div>
              ))}
            </div>
            <p className="text-muted-foreground text-xs">{t("profile.modalities.hint")}</p>
          </div>

          <div className="grid gap-2 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-cw" className="text-sm">
                  {t("profile.cw")}
                </Label>
                <p className="text-muted-foreground text-xs">{t("profile.rateHint")}</p>
              </div>
              <Input
                id="p-cw"
                type="number"
                min={0}
                max={1000}
                className="w-24 shrink-0"
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>

            {/* 能力探测结果 + 重新探测按钮。快照存在 settings 里而不是列上，
                这里的值就是压缩与上限实际会用的那一份。 */}
            <div className="grid gap-1 border-t pt-3 text-xs">
              {profile?.caps?.context_tokens ? (
                <p className="text-muted-foreground leading-relaxed">
                  {t("profile.caps.detected")}：
                  {Math.round((profile.caps.context_tokens ?? 0) / 1000)}K
                  {profile.caps.max_output_tokens ? ` · ${t("profile.caps.output")} ${profile.caps.max_output_tokens}` : ""}
                  {profile.caps.input_modalities?.length
                    ? ` · ${t("profile.caps.modalities")} ${profile.caps.input_modalities.join(" / ")}`
                    : ""}
                  {profile.caps.reasoning
                    ? ` · ${t("profile.caps.reasoning")}${
                        profile.caps.reasoning_efforts?.length ? " " + profile.caps.reasoning_efforts.join(" / ") : ""
                      }`
                    : ""}
                  {profile.caps.source ? ` · ${profile.caps.source}` : ""}
                  {profile.effective_context_window_k
                    ? ` · ${t("profile.caps.effective")} ${profile.effective_context_window_k}K`
                    : ""}
                </p>
              ) : (
                <p className="text-muted-foreground leading-relaxed">{t("model.noCaps")}</p>
              )}
              <div className="flex items-center justify-between gap-2">
                <span className="text-muted-foreground">{profile?.caps?.probed_at ?? ""}</span>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7"
                  disabled={!profile || probing}
                  onClick={() => void reprobe()}
                >
                  {probing ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                  {t("model.reprobe")}
                </Button>
              </div>
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-priority" className="text-sm">
                  {t("profile.priority.label")}
                </Label>
                <p className="text-muted-foreground text-xs">{t("profile.priority.hint")}</p>
              </div>
              <Input
                id="p-priority"
                type="number"
                className="w-24 shrink-0"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">{t("profile.exclude.label")}</Label>
                <p className="text-muted-foreground text-xs">{t("profile.exclude.hint")}</p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label={t("profile.exclude.aria")} />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">{t("profile.streaming.label")}</Label>
                <p className="text-muted-foreground text-xs">{t("profile.streaming.hint")}</p>
              </div>
              <Switch checked={streaming} onCheckedChange={setStreaming} aria-label={t("profile.streaming.aria")} />
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-max-tokens" className="text-sm">
                  {t("profile.maxTokens.label")}
                </Label>
                <p className="text-muted-foreground text-xs">{t("profile.maxTokens.hint")}</p>
              </div>
              <Input
                id="p-max-tokens"
                type="number"
                min={0}
                className="w-28 shrink-0"
                value={maxTokens}
                onChange={(e) => setMaxTokens(e.target.value)}
                placeholder="0"
              />
            </div>
            {/* 生效上限：填超了会被模型上限压回去，界面上必须看得见压成了多少。 */}
            <p className="text-muted-foreground text-xs leading-relaxed">
              {capped
                ? t("model.effectiveMaxCapped", { configured: asked, cap, n: effValue })
                : effValue > 0
                  ? t("model.effectiveMax", { n: effValue, configured: asked > 0 ? asked : cap, cap })
                  : t("model.effectiveMaxUnknown")}
            </p>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">{t("profile.maxTokensField.label")}</Label>
                <p className="text-muted-foreground text-xs">{t(`profile.maxTokensFieldHint.${format}`)}</p>
              </div>
              <Select
                value={format === "openai" ? maxTokensField : NONE}
                onValueChange={setMaxTokensField}
                disabled={format !== "openai"}
              >
                <SelectTrigger className="w-56 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {MAX_TOKENS_FIELDS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label className="text-sm">{t("profile.thinkingType.label")}</Label>
                <p className="text-muted-foreground text-xs">{t("profile.thinkingType.hint")}</p>
              </div>
              <Select value={thinkingType} onValueChange={setThinkingType}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_TYPES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">{t("profile.effort.label")}</Label>
                <p className="text-muted-foreground text-xs">{t("profile.effort.hint")}</p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EFFORT_LEVELS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <ProfileRetryFields value={retry} onChange={setRetry} />
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? t("profile.create") : t("profile.save")}
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 供应商卡片：端点信息 + 名下模型行
//
// provider === null 时渲染「未归属供应商」——历史库里可能存在 provider_id 为空
// 的模型（例如回填被跳过、或外部脚本直接写库），它们必须仍然可见、可编辑，
// 否则设置页会静默丢掉这些配置。
// ─────────────────────────────────────────────────────────────────────────────

function ProviderCard({
  provider,
  models,
  health,
  poolOn,
  probingId,
  onEditProvider,
  onDeleteProvider,
  onAddModel,
  onEditModel,
  onDeleteModel,
  onActivate,
  onProbe,
}: {
  provider: LLMProvider | null;
  models: LLMProfile[];
  health: Map<string, LLMPoolMember>;
  poolOn: boolean;
  probingId: string | null;
  onEditProvider: (v: LLMProvider) => void;
  onDeleteProvider: (v: LLMProvider) => void;
  onAddModel: (v: LLMProvider | null) => void;
  onEditModel: (p: LLMProfile) => void;
  onDeleteModel: (p: LLMProfile) => void;
  onActivate: (p: LLMProfile) => void;
  onProbe: (p: LLMProfile) => void;
}) {
  const t = useTranslations("llmPage");

  // 一条模型在卡片上的「是否正常」。没 Key 的模型根本发不出请求，比熔断更该先说。
  const healthOf = (p: LLMProfile): Health => {
    const m = health.get(p.id);
    if (!p.api_key_hint) {
      return {
        label: t("health.noKey.label"),
        cls: "border-muted-foreground/40 text-muted-foreground",
        hint: t("health.noKey.hint"),
      };
    }
    if (m?.state === "tripped") {
      return {
        label:
          m.cooldown_secs > 0 ? t("health.trippedCooldown", { cooldown: cooldownText(m.cooldown_secs) }) : t("health.tripped"),
        cls: "border-destructive/50 text-destructive",
        hint: m.last_error,
      };
    }
    if (m?.state === "degraded") {
      return {
        label: t("health.degraded", { fails: m.fails }),
        cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
        hint: m.last_error,
      };
    }
    return { label: t("health.ok"), cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" };
  };

  return (
    <Card className="gap-0 overflow-hidden py-0">
      <div className="flex items-start gap-2 border-b bg-muted/30 px-4 py-3">
        <PlugZapIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate font-medium text-sm">{provider?.name ?? t("page.orphanTitle")}</span>
            {provider && (
              <Badge variant="outline" className="uppercase">
                {provider.format}
              </Badge>
            )}
            <Badge variant="outline" className="text-muted-foreground">
              {t("card.providerModels", { n: models.length })}
            </Badge>
          </div>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground text-xs">
            {provider?.base_url && <code className="truncate font-mono">{provider.base_url}</code>}
            {provider?.key_error ? (
              // 读不出 Key 与「没配 Key」是两回事：前者要用户重填或找回主密钥，
              // 后者才是去填一把新的。混成一句会让人反复填正确的 Key。
              <span className="text-destructive" title={provider.key_error}>
                {t("health.keyUnreadable")}
              </span>
            ) : provider?.api_key_hint ? (
              <span>{provider.api_key_hint}</span>
            ) : provider ? (
              <span className="text-destructive">{t("health.noKey.label")}</span>
            ) : null}
            {provider && (
              <span>
                {provider.rate_per_second}/s · {provider.rate_per_minute}/min
              </span>
            )}
            {provider?.proxy && <span className="truncate">{t("card.proxy", { proxy: provider.proxy })}</span>}
            {provider?.session_header_key && <span className="font-mono">{provider.session_header_key}</span>}
          </div>
        </div>
        {provider && (
          <div className="flex shrink-0 gap-1">
            <Button
              size="icon"
              variant="ghost"
              className="size-7"
              aria-label={t("provider.edit")}
              title={t("provider.edit")}
              onClick={() => onEditProvider(provider)}
            >
              <SaveIcon className="size-3.5" />
            </Button>
            <Button
              size="icon"
              variant="ghost"
              className="size-7"
              aria-label={t("provider.deleteAria")}
              title={t("provider.deleteAria")}
              onClick={() => onDeleteProvider(provider)}
            >
              <Trash2Icon className="size-3.5 text-destructive" />
            </Button>
          </div>
        )}
      </div>

      <div className="divide-y">
        {models.map((p) => {
          const h = healthOf(p);
          const eff = effectiveMaxOutput(p);
          return (
            // biome-ignore lint/a11y/useSemanticElements: 行内含自己的操作按钮，用原生 <button> 会造成按钮嵌套（非法 HTML）
            <div
              key={p.id}
              role="button"
              tabIndex={0}
              onClick={() => onEditModel(p)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onEditModel(p);
                }
              }}
              className={cn(
                "flex cursor-pointer items-start gap-2 px-4 py-2.5 outline-none transition-colors hover:bg-accent/40",
                p.is_default && "bg-amber-400/5",
              )}
            >
              <StarIcon
                className={cn(
                  "mt-0.5 size-4 shrink-0",
                  p.is_default ? "fill-amber-400 text-amber-400" : "text-muted-foreground",
                )}
              />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate font-medium text-xs">{p.name}</span>
                  <Badge variant="outline" className={cn(h.cls)} title={h.hint}>
                    {h.label}
                  </Badge>
                </div>
                <code className="mt-0.5 block truncate font-mono text-muted-foreground text-xs">{p.model}</code>
                <div className="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground text-xs">
                  {(p.effective_context_window_k || p.context_window_k) && (
                    <span>{t("card.windowK", { k: p.effective_context_window_k ?? p.context_window_k ?? 0 })}</span>
                  )}
                  {/* 输出上限显示「生效值」而不是原样的配置值：两者不一致时用户
                      看到的就是真正发出去的那个数。 */}
                  {eff.value > 0 && <span>{t("card.maxTokens", { n: eff.value })}</span>}
                  {p.reasoning_effort && (
                    <span>
                      {t("card.thinking", {
                        effort: p.reasoning_effort === "off" ? t("card.thinkingOff") : p.reasoning_effort,
                      })}
                    </span>
                  )}
                  {/* 实际生效的扩展输入模态（声明优先、否则按模型名推断）；text
                      始终隐含，不占版面。空 = 纯文本模型。 */}
                  {extraModalities(p.effective_input_modalities).length > 0 && (
                    <span>
                      {t("card.modalities", {
                        modalities: extraModalities(p.effective_input_modalities)
                          .map((m) => t(`profile.modalities.opt.${m}`))
                          .join("·"),
                      })}
                    </span>
                  )}
                  {/* 轮询相关的两个字段只在轮询开着时才有意义，关着时不占版面 */}
                  {poolOn &&
                    !p.is_default &&
                    (p.pool_exclude ? (
                      <span>{t("card.excluded")}</span>
                    ) : (
                      <span>{t("card.priority", { priority: p.priority ?? 0 })}</span>
                    ))}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-7"
                  aria-label={t("model.reprobe")}
                  title={t("model.reprobe")}
                  disabled={probingId === p.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    onProbe(p);
                  }}
                >
                  {probingId === p.id ? (
                    <Loader2Icon className="size-3.5 animate-spin" />
                  ) : (
                    <RefreshCwIcon className="size-3.5" />
                  )}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7"
                  disabled={p.is_default}
                  onClick={(e) => {
                    e.stopPropagation();
                    onActivate(p);
                  }}
                >
                  {p.is_default ? t("card.active") : t("card.activate")}
                </Button>
                {/* 激活中的、以及供应商下最后一条模型都能删：删完激活位自动
                    交接，一条不剩就留个空供应商等以后再加。 */}
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-7"
                  aria-label={t("model.deleteAria")}
                  title={t("model.deleteAria")}
                  onClick={(e) => {
                    e.stopPropagation();
                    onDeleteModel(p);
                  }}
                >
                  <Trash2Icon className="size-3.5 text-destructive" />
                </Button>
              </div>
            </div>
          );
        })}
        {models.length === 0 && (
          <p className="px-4 py-3 text-muted-foreground text-xs">{t("provider.noModels")}</p>
        )}
      </div>

      {/* 未归属卡片不提供「添加模型」：新模型必须落在某家供应商下（见 ModelSheet
          的 providerRequired 守卫）。 */}
      {provider && (
        <div className="border-t px-3 py-2">
          <Button size="sm" variant="ghost" onClick={() => onAddModel(provider)}>
            <PlusIcon /> {t("provider.addModel")}
          </Button>
        </div>
      )}
    </Card>
  );
}

// ─────────────────────────────────────────────────────────────────────────────

export default function LLMPage() {
  const t = useTranslations("llmPage");
  const [providers, setProviders] = React.useState<LLMProvider[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  const [probingId, setProbingId] = React.useState<string | null>(null);
  // 待确认删除的供应商。非空 = 确认框打开；删除逻辑本身不依赖这个 state。
  const [deletingProvider, setDeletingProvider] = React.useState<LLMProvider | null>(null);

  // 抽屉的开关和内容分开存：关闭时 editing 保持不变，否则关闭动画期间标题会从
  // 「编辑 X」闪成「新建」。editing = null 表示新建。
  const [providerOpen, setProviderOpen] = React.useState(false);
  const [editingProvider, setEditingProvider] = React.useState<LLMProvider | null>(null);
  const [modelOpen, setModelOpen] = React.useState(false);
  const [editingModel, setEditingModel] = React.useState<LLMProfile | null>(null);
  const [modelProviderId, setModelProviderId] = React.useState("");

  const openProvider = React.useCallback((v: LLMProvider | null) => {
    setEditingProvider(v);
    setProviderOpen(true);
  }, []);
  const openModel = React.useCallback((p: LLMProfile | null, pid: string) => {
    setEditingModel(p);
    setModelProviderId(p?.provider_id ?? pid);
    setModelOpen(true);
  }, []);

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);

  // 供应商树（含 models[]）与扁平列表各拉一份：树负责分组渲染，扁平列表负责
  // 「未归属」兜底与轮询健康映射——两者都来自同一批行，读的是同一份 DTO。
  const load = React.useCallback(async () => {
    try {
      const [vs, ps] = await Promise.all([api.llmProviders(), api.llmProfiles()]);
      setProviders(vs);
      setProfiles(ps);
    } catch {
      /* ignore */
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 按 provider_id 分组；找不到归属的模型进「未归属」桶，绝不静默丢弃。
  const grouped = React.useMemo(() => {
    const byProv = new Map<string, LLMProfile[]>();
    for (const v of providers) byProv.set(v.id, []);
    const orphans: LLMProfile[] = [];
    for (const p of profiles) {
      const bucket = p.provider_id ? byProv.get(p.provider_id) : undefined;
      if (bucket) bucket.push(p);
      else orphans.push(p);
    }
    // 卡片内排序：激活的永远在最前，其余按轮询优先级降序、再按名称。
    const order = (a: LLMProfile, b: LLMProfile) =>
      Number(b.is_default) - Number(a.is_default) ||
      (b.priority ?? 0) - (a.priority ?? 0) ||
      a.name.localeCompare(b.name);
    for (const list of byProv.values()) list.sort(order);
    orphans.sort(order);
    return { byProv, orphans };
  }, [providers, profiles]);

  // 行上的健康徽章按 profile id 取轮询状态。
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(p: LLMProfile) {
    try {
      await api.activateLLMProfile(p.id);
      toast.success(t("page.toast.activated", { name: p.name }));
      await load();
    } catch (e) {
      toast.error(t("page.toast.activateFailed", { msg: (e as Error).message }));
    }
  }

  async function removeModel(p: LLMProfile) {
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(t("page.toast.deleted", { name: p.name }));
      await load();
    } catch (e) {
      toast.error(t("page.toast.deleteFailed", { msg: (e as Error).message }));
    }
  }

  // 删供应商是一次性动作：名下的模型、配置、任务链引用全部随之消失，所以只在
  // 这里弹一个确认框说清「会连带删掉几个模型」，确认后一次请求删干净。
  function removeProvider(v: LLMProvider) {
    setDeletingProvider(v);
  }

  async function confirmRemoveProvider() {
    const v = deletingProvider;
    if (!v) return;
    setDeletingProvider(null);
    try {
      // 后端在一个事务里删模型 → 删供应商 → 交接激活位；返回被带走的模型数。
      const r = await api.deleteLLMProvider(v.id);
      toast.success(t("provider.toast.cascadeDeleted", { name: v.name, n: r.models_deleted ?? 0 }));
      await load();
    } catch (e) {
      toast.error(t("provider.toast.deleteFailed", { msg: (e as Error).message }));
    }
  }

  async function probe(p: LLMProfile) {
    if (probingId) return;
    setProbingId(p.id);
    try {
      const r = await api.probeLLMProfile(p.id);
      if (r.ok) {
        toast.success(t("model.reprobeOk", { k: Math.round(r.effective_context_window_k ?? 0) }));
        await load();
      } else {
        toast.error(t("model.reprobeFailed", { msg: r.error ?? t("profile.toast.unknown") }));
      }
    } catch (e) {
      toast.error(t("model.reprobeFailed", { msg: (e as Error).message }));
    } finally {
      setProbingId(null);
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
          <p className="text-muted-foreground text-sm">{t("page.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon /> {t("page.poolConfig")}
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                {t("page.enabled")}
              </Badge>
            )}
          </Button>
          <Button size="sm" variant="outline" onClick={() => openProvider(null)}>
            <PlusIcon /> {t("page.newProvider")}
          </Button>
        </div>
      </div>

      <Tabs defaultValue="providers" className="flex-1">
        <TabsList>
          <TabsTrigger value="providers">{t("page.tab.providers")}</TabsTrigger>
          <TabsTrigger value="retry">{t("page.tab.retry")}</TabsTrigger>
        </TabsList>

        <TabsContent value="providers" className="mt-4">
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {providers.map((v) => (
              <ProviderCard
                key={v.id}
                provider={v}
                models={grouped.byProv.get(v.id) ?? []}
                health={health}
                poolOn={poolOn}
                probingId={probingId}
                onEditProvider={openProvider}
                onDeleteProvider={(x) => void removeProvider(x)}
                onAddModel={(x) => openModel(null, x?.id ?? "")}
                onEditModel={(p) => openModel(p, p.provider_id ?? "")}
                onDeleteModel={(p) => void removeModel(p)}
                onActivate={(p) => void activate(p)}
                onProbe={(p) => void probe(p)}
              />
            ))}
            {grouped.orphans.length > 0 && (
              <ProviderCard
                provider={null}
                models={grouped.orphans}
                health={health}
                poolOn={poolOn}
                probingId={probingId}
                onEditProvider={openProvider}
                onDeleteProvider={() => undefined}
                onAddModel={(x) => openModel(null, x?.id ?? "")}
                onEditModel={(p) => openModel(p, "")}
                onDeleteModel={(p) => void removeModel(p)}
                onActivate={(p) => void activate(p)}
                onProbe={(p) => void probe(p)}
              />
            )}
            {providers.length === 0 && grouped.orphans.length === 0 && (
              <div className="col-span-full rounded-lg border border-dashed">
                <EmptyState illustration="nodes" title={t("page.empty")} />
              </div>
            )}
          </div>
        </TabsContent>

        <TabsContent value="retry" className="mt-4">
          <RetryPolicyPanel />
        </TabsContent>
      </Tabs>

      <ProviderSheet
        provider={editingProvider}
        open={providerOpen}
        onOpenChange={setProviderOpen}
        onSaved={() => void load()}
      />
      <ModelSheet
        profile={editingModel}
        providerId={modelProviderId}
        providers={providers}
        open={modelOpen}
        onOpenChange={setModelOpen}
        onSaved={() => void load()}
      />
      <PoolSheet open={poolOpen} onOpenChange={setPoolOpen} pool={pool} onReload={loadPool} />

      {/* 唯一的防误触闸门：一个确认框，说明会连带删掉什么。 */}
      <AlertDialog
        open={deletingProvider !== null}
        onOpenChange={(o) => {
          if (!o) setDeletingProvider(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("provider.confirmDelete.title", { name: deletingProvider?.name ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>
              {deletingProvider?.model_count
                ? t("provider.confirmDelete.withModels", { n: deletingProvider.model_count })
                : t("provider.confirmDelete.empty")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("provider.confirmDelete.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => void confirmRemoveProvider()}
            >
              {t("provider.confirmDelete.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
