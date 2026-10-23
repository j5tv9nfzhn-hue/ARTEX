"use client";

import * as React from "react";

import Link from "next/link";

import {
  CircleDollarSignIcon,
  CoinsIcon,
  CpuIcon,
  FlaskConicalIcon,
  KeyboardIcon,
  LanguagesIcon,
  RadioTowerIcon,
  SearchIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  SparklesIcon,
  TerminalIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { LOCALE_LABELS } from "@/i18n/config";
import { api } from "@/lib/api";
import { type ChatSendMode, setChatSendMode, useChatSendMode, useChatSendModeOptions } from "@/lib/chat-send-mode";
import type { Settings, ShellSettings, VerificationCapabilities } from "@/lib/types";

import { AboutCard } from "./_components/about-card";
import { PasswordCard } from "./_components/password-card";
import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const t = useTranslations("settingsPage");
  const richTags = {
    b: (chunks: React.ReactNode) => <b>{chunks}</b>,
    br: () => <br />,
    code: (chunks: React.ReactNode) => <span className="font-mono">{chunks}</span>,
  };
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  // 工具执行器（Bash 用哪个 Shell）。没有 auto：只列本机实测可用的明确选项，
  // 选项列表要真跑探测，所以单独走 /api/settings/shell 按需拉取。
  const [shellInfo, setShellInfo] = React.useState<ShellSettings | null>(null);
  const [shellMode, setShellMode] = React.useState("powershell");
  const [shellBin, setShellBin] = React.useState("");
  const [savingShell, setSavingShell] = React.useState(false);
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 新建任务的默认 token 预算；输入单位=千 token(k)，存库为原始 token 数。
  const [tokenBudgetK, setTokenBudgetK] = React.useState("0");
  const [savingTokenBudget, setSavingTokenBudget] = React.useState(false);
  // 模型价目 JSON(USD/百万 token)；读取接口按它读时现算费用，空=不算钱。
  const [priceTable, setPriceTable] = React.useState("");
  const [savingPriceTable, setSavingPriceTable] = React.useState(false);
  // 操作约束注入范围(默认都开)。
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 实验功能:noa 上下文压缩(默认关)。
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 实验功能:完成闸门(未收口分支不许判完成)。off=关 / shadow=只记录 / enforce=真拦，切换即时生效。
  const [branchGateMode, setBranchGateMode] = React.useState("shadow");
  // 复核成本闸门：等级达到阈值自动复核，低于阈值按比例抽查（见 server/verify_policy.go）。
  const [verifyThreshold, setVerifyThreshold] = React.useState("high");
  const [verifySamplePercent, setVerifySamplePercent] = React.useState("25");
  const [savingVerifyGate, setSavingVerifyGate] = React.useState(false);
  // AI 自我进化沉淀：任务完成/删除时自动生成技能；审阅门控默认开启。
  const [skillReviewGate, setSkillReviewGate] = React.useState(true);
  const [skillAutoDistill, setSkillAutoDistill] = React.useState(true);
  const [skillAutoDistillApply, setSkillAutoDistillApply] = React.useState(false);
  // 人机验证工具链只读状态快照
  const [verificationCaps, setVerificationCaps] = React.useState<VerificationCapabilities | null>(null);
  // 纯前端偏好：不走 /api/settings，直接读写 localStorage。
  const sendMode = useChatSendMode();
  const sendModeOptions = useChatSendModeOptions();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    // 原始 token 数 → 千 token;非整千的外部改动四舍五入到 k。
    setTokenBudgetK(String(Math.round((s.default_task_token_budget ?? 0) / 1000)));
    setPriceTable(s.price_table ?? "");
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
    setBranchGateMode(s.branch_gate_mode || "shadow");
    setSkillReviewGate(s.skill_review_gate !== false);
    setSkillAutoDistill(s.skill_auto_distill !== false);
    setSkillAutoDistillApply(!!s.skill_auto_distill_apply);
    setVerifyThreshold(s.verify_auto_threshold || "high");
    setVerifySamplePercent(String(s.verify_sample_percent ?? 25));
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error(t("toast.workersInvalid"));
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success(t("toast.workersSaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingWorkers(false));
  };

  const saveTokenBudget = () => {
    // 单位=千 token；0=不限。必须为非负整数，避免出现 0.5k 这类模糊语义。
    const k = Number(tokenBudgetK);
    if (!Number.isInteger(k) || k < 0) {
      toast.error(t("toast.budgetInvalid"));
      return;
    }
    setSavingTokenBudget(true);
    api
      .setSettings({ default_task_token_budget: k * 1000 })
      .then((s) => {
        apply(s);
        toast.success(t("toast.budgetSaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingTokenBudget(false));
  };

  // 复核闸门：阈值走下拉框、采样比例是整数百分比。校验只做本地能确定的
  // （整数、0–100）；阈值枚举的合法性由后端再校验一次，非法值返回中文 400。
  const saveVerifyGate = () => {
    const percent = Number(verifySamplePercent);
    if (!Number.isInteger(percent) || percent < 0 || percent > 100) {
      toast.error(t("toast.verifyGateInvalid"));
      return;
    }
    setSavingVerifyGate(true);
    api
      .setSettings({ verify_auto_threshold: verifyThreshold, verify_sample_percent: percent })
      .then((s) => {
        apply(s);
        toast.success(t("toast.verifyGateSaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingVerifyGate(false));
  };

  const savePriceTable = () => {
    // JSON 合法性/负价格由后端校验并返回中文 400 错误；这里只裁掉首尾空白,
    // 空串=清除价格表(回到"不算钱")。
    setSavingPriceTable(true);
    api
      .setSettings({ price_table: priceTable.trim() })
      .then((s) => {
        apply(s);
        toast.success(t("toast.priceTableSaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingPriceTable(false));
  };

  // 执行器：解析结果要真跑一次探针，独立加载；失败不阻塞页面其它部分。
  const loadShell = React.useCallback(() => {
    api
      .shellSettings()
      .then((r) => {
        setShellInfo(r);
        setShellMode(r.runtime.mode || r.shell_mode || "powershell");
        setShellBin(r.shell_bin ?? "");
      })
      .catch(() => undefined);
  }, []);

  // 保存执行器：后端先真跑一次探针，跑不起来返回 400 且不写库——所以这里
  // 失败时不清空用户输入，直接把它打到 toast 里。
  const saveShell = () => {
    setSavingShell(true);
    api
      .setSettings({ shell_mode: shellMode, shell_bin: shellBin.trim() })
      .then(() => {
        toast.success(t("shell.saved"));
        loadShell();
      })
      .catch((e) => toast.error(t("shell.toastInvalid") + "：" + (e as Error).message))
      .finally(() => setSavingShell(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success(t("toast.pythonSaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };
  // 本仓库只提供简体中文一种语言，界面语言与模型输出语言固定为中文，
  // 因此不再提供语言切换器，也不必同步服务器 output_language。

  React.useEffect(() => {
    loadShell();
  }, [loadShell]);

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
    api
      .verificationCapabilities()
      .then(setVerificationCaps)
      .catch(() => undefined);
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? t("toast.bindingOn") : t("toast.bindingOff"));
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? t("toast.noaOn") : t("toast.noaOff"));
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      });
  };

  const changeBranchGateMode = (v: string) => {
    const prev = branchGateMode;
    setBranchGateMode(v); // optimistic
    api
      .setSettings({ branch_gate_mode: v })
      .then((s) => {
        apply(s);
        toast.success(t("experimental.branchGateSaved"));
      })
      .catch((e) => {
        setBranchGateMode(prev); // revert on failure
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      });
  };

  const toggleSkillReviewGate = (v: boolean) => {
    setSkillReviewGate(v); // optimistic
    api
      .setSettings({ skill_review_gate: v })
      .then(apply)
      .catch((e) => {
        setSkillReviewGate(!v);
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      });
  };

  const toggleSkillAutoDistill = (v: boolean) => {
    setSkillAutoDistill(v); // optimistic
    api
      .setSettings({ skill_auto_distill: v })
      .then(apply)
      .catch((e) => {
        setSkillAutoDistill(!v);
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      });
  };

  const toggleSkillAutoDistillApply = (v: boolean) => {
    setSkillAutoDistillApply(v); // optimistic
    api
      .setSettings({ skill_auto_distill_apply: v })
      .then(apply)
      .catch((e) => {
        setSkillAutoDistillApply(!v);
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success(t("toast.webSearchSaved"));
      })
      .catch((e) => {
        toast.error(t("toast.saveFailed", { error: (e as Error).message }));
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success(t("toast.braveKeySaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success(t("toast.tavilyKeySaved"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? t("toast.proxySaved") : t("toast.proxyCleared"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? t("toast.globalProxySaved") : t("toast.globalProxyCleared"));
      })
      .catch((e) => toast.error(t("toast.saveFailed", { error: (e as Error).message })))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(t("toast.testSuccess", { backend: r.backend ?? "", count: r.count ?? 0 }));
        else toast.error(t("toast.testFailed", { error: r.error || t("toast.testUnknownError") }));
      })
      .catch((e) => toast.error(t("toast.testFailed", { error: (e as Error).message })))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{t("title")}</h1>
        <p className="text-muted-foreground text-sm">{t("subtitle")}</p>
      </div>

      {/* 多列而非 grid：网络搜索卡片比其余高数倍，且高度随所选后端变化（brave/tavily
          的 key 输入是条件渲染）。grid 会按最高的一张撑满整行、在旁边留下大片空白，
          多列则自动按内容高度平衡填充。卡片间距靠 mb 而非 gap——多列布局下
          column-gap 只管列间距，行间距要由子元素自己给。 */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />
        <PasswordCard />
        <AboutCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {t("trafficCapture.title")}
            </CardTitle>
            <CardDescription>{t.rich("trafficCapture.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? t("trafficCapture.on") : t("trafficCapture.off")}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {t("agentBinding.title")}
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              {t.rich("agentBinding.desc", richTags)}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? t("agentBinding.on") : t("agentBinding.off")}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldCheckIcon className="size-4" />
              {t("verifyGate.title")}
            </CardTitle>
            <CardDescription id="verify-gate-description">
              {t.rich("verifyGate.desc", richTags)}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <Label
                htmlFor="verify-gate-threshold"
                className="text-sm font-normal text-muted-foreground"
              >
                {t("verifyGate.thresholdLabel")}
              </Label>
              <Select
                value={verifyThreshold}
                disabled={!loaded || savingVerifyGate}
                onValueChange={setVerifyThreshold}
              >
                <SelectTrigger
                  id="verify-gate-threshold"
                  aria-describedby="verify-gate-description"
                  className="w-full"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="off">{t("verifyGate.threshold.off")}</SelectItem>
                  <SelectItem value="critical">{t("verifyGate.threshold.critical")}</SelectItem>
                  <SelectItem value="high">{t("verifyGate.threshold.high")}</SelectItem>
                  <SelectItem value="medium">{t("verifyGate.threshold.medium")}</SelectItem>
                  <SelectItem value="low">{t("verifyGate.threshold.low")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Label
                htmlFor="verify-gate-sample"
                className="text-sm font-normal text-muted-foreground"
              >
                {t("verifyGate.sampleLabel")}
              </Label>
              <Input
                id="verify-gate-sample"
                type="number"
                min={0}
                max={100}
                step={1}
                className="w-24 font-mono text-sm"
                value={verifySamplePercent}
                disabled={!loaded || savingVerifyGate || verifyThreshold === "off"}
                onChange={(e) => setVerifySamplePercent(e.target.value)}
              />
              <span className="text-sm text-muted-foreground">%</span>
            </div>
            <p className="text-muted-foreground text-xs">{t("verifyGate.sampleHint")}</p>
            <div className="flex items-center justify-end">
              <Button onClick={saveVerifyGate} disabled={!loaded || savingVerifyGate}>
                {t("save")}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {t("globalProxy.title")}
            </CardTitle>
            <CardDescription>{t.rich("globalProxy.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              {t("globalProxy.addrLabel")}
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder={t("globalProxy.placeholder")}
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                {t("save")}
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? t("globalProxy.configured") : t("globalProxy.notConfigured")}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              {t("constraints.title")}
            </CardTitle>
            <CardDescription>{t.rich("constraints.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                {t("constraints.injectPlanner")}
                {injectPlanner ? t("onSuffix") : t("offSuffix")}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                {t("constraints.injectWorker")}
                {injectWorker ? t("onSuffix") : t("offSuffix")}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              {t("experimental.title")}
            </CardTitle>
            <CardDescription>{t.rich("experimental.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
                {t("experimental.noaLabel")}
                {noaCompaction ? t("onSuffix") : t("offSuffix")}
              </Label>
              <Switch
                id="noa-compaction"
                checked={noaCompaction}
                disabled={!loaded}
                onCheckedChange={toggleNoaCompaction}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="branch-gate" className="text-sm font-normal text-muted-foreground">
                {t("experimental.branchGateLabel")}
              </Label>
              <Select value={branchGateMode} onValueChange={changeBranchGateMode} disabled={!loaded}>
                <SelectTrigger id="branch-gate" className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="off">{t("experimental.branchGateOff")}</SelectItem>
                  <SelectItem value="shadow">{t("experimental.branchGateShadow")}</SelectItem>
                  <SelectItem value="enforce">{t("experimental.branchGateEnforce")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <p className="text-xs text-muted-foreground">{t("experimental.branchGateHint")}</p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SparklesIcon className="size-4" />
              {t("skillEvolution.title")}
            </CardTitle>
            <CardDescription>{t.rich("skillEvolution.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="skill-auto-distill" className="text-sm font-normal text-muted-foreground">
                {t("skillEvolution.autoDistill")}
                {skillAutoDistill ? t("onSuffix") : t("offSuffix")}
              </Label>
              <Switch
                id="skill-auto-distill"
                checked={skillAutoDistill}
                disabled={!loaded}
                onCheckedChange={toggleSkillAutoDistill}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="skill-review-gate" className="text-sm font-normal text-muted-foreground">
                {t("skillEvolution.reviewGate")}
                {skillReviewGate ? t("onSuffix") : t("offSuffix")}
              </Label>
              <Switch
                id="skill-review-gate"
                checked={skillReviewGate}
                disabled={!loaded}
                onCheckedChange={toggleSkillReviewGate}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <div>
                <Label htmlFor="skill-auto-distill-apply" className="text-sm font-normal text-muted-foreground">
                  {t("skillEvolution.autoApply")}
                  {skillReviewGate && !skillAutoDistillApply ? t("offSuffix") : t("onSuffix")}
                </Label>
                {!skillReviewGate && (
                  <p className="mt-1 text-muted-foreground text-xs">{t("skillEvolution.autoApplyGateOff")}</p>
                )}
              </div>
              <Switch
                id="skill-auto-distill-apply"
                checked={!skillReviewGate || skillAutoDistillApply}
                disabled={!loaded || !skillReviewGate}
                onCheckedChange={toggleSkillAutoDistillApply}
              />
            </div>
            <div className="flex justify-end pt-1">
              <Button asChild size="sm" variant="outline" className="h-7 text-xs">
                <Link href="/system/skills?openProposals=1">
                  <SparklesIcon className="size-3" />
                  {t("skillEvolution.openProposalsLink")}
                </Link>
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldCheckIcon className="size-4" />
              {t("verificationStatus.title")}
            </CardTitle>
            <CardDescription>{t("verificationStatus.desc")}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-xs">
            <div className="space-y-1.5">
              <Label className="text-muted-foreground text-xs">{t("verificationStatus.nativeProtocols")}</Label>
              <div className="flex flex-wrap gap-1.5">
                {(
                  verificationCaps?.native_protocols ?? [
                    "altcha_v1_sha256_sha512",
                    "altcha_v2_sha2_pbkdf2",
                    "anubis_legacy_fast_slow_sha256",
                  ]
                ).map((proto) => (
                  <Badge key={proto} variant="secondary" className="font-mono font-normal text-[11px]">
                    {proto}
                  </Badge>
                ))}
              </div>
            </div>

            <div className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label className="text-muted-foreground text-xs">{t("verificationStatus.helperLabel")}</Label>
                <Badge
                  variant={
                    verificationCaps?.fingerprint_helper.state === "detected_not_handshaken"
                      ? "default"
                      : verificationCaps?.fingerprint_helper.state === "configuration_invalid_or_missing"
                        ? "destructive"
                        : "outline"
                  }
                  className="font-normal text-[11px]"
                >
                  {verificationCaps?.fingerprint_helper.state === "detected_not_handshaken"
                    ? t("verificationStatus.stateDetected")
                    : verificationCaps?.fingerprint_helper.state === "configuration_invalid_or_missing"
                      ? t("verificationStatus.stateInvalid")
                      : t("verificationStatus.stateNotInstalled")}
                </Badge>
              </div>
              <p className="text-muted-foreground text-[11px] leading-relaxed">
                {t("verificationStatus.helperMeta", {
                  binary: verificationCaps?.fingerprint_helper.binary_name ?? "artex-verification-http",
                  provider: verificationCaps?.fingerprint_helper.provider ?? "req/v3@v3.59.0",
                  profiles: (verificationCaps?.fingerprint_helper.profiles ?? ["chrome", "firefox"]).join(", "),
                })}
              </p>
            </div>

            {verificationCaps?.limits && (
              <div className="grid grid-cols-2 gap-2 rounded-md border bg-muted/30 p-2 text-[11px]">
                <div>
                  <span className="text-muted-foreground">{t("verificationStatus.limitTimeout")}：</span>
                  <span className="font-mono tabular-nums">{verificationCaps.limits.flow_timeout_ms ?? 15000}ms</span>
                </div>
                <div>
                  <span className="text-muted-foreground">{t("verificationStatus.limitRoundtrips")}：</span>
                  <span className="font-mono tabular-nums">{verificationCaps.limits.flow_roundtrips ?? 6}</span>
                </div>
                <div>
                  <span className="text-muted-foreground">{t("verificationStatus.limitProcesses")}：</span>
                  <span className="font-mono tabular-nums">{verificationCaps.limits.helper_processes ?? 2}</span>
                </div>
                <div>
                  <span className="text-muted-foreground">{t("verificationStatus.limitMemory")}：</span>
                  <span className="font-mono tabular-nums">
                    {verificationCaps.limits.helper_go_memory_soft_mib ?? 64} MiB
                  </span>
                </div>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              {t("webSearch.title")}
            </CardTitle>
            <CardDescription>{t.rich("webSearch.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? t("webSearch.on") : t("webSearch.off")}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">{t("webSearch.sourceLabel")}</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder={t("webSearch.sourcePlaceholder")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">{t("webSearch.sourceDdgs")}</SelectItem>
                    <SelectItem value="brave-free">{t("webSearch.sourceBrave")}</SelectItem>
                    <SelectItem value="tavily">{t("webSearch.sourceTavily")}</SelectItem>
                    <SelectItem value="deepseek">{t("webSearch.sourceDeepseek")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">{t("webSearch.deepseekTitle")}</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  {t.rich("webSearch.deepseekDesc1", richTags)}
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  {t.rich("webSearch.deepseekDesc2", richTags)}
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">{t("webSearch.deepseekDesc3")}</p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  {t("webSearch.braveKeyLabel")}
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">{t("configured")}</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={
                      braveKeySet ? t("webSearch.braveKeyPlaceholderSet") : t("webSearch.braveKeyPlaceholderUnset")
                    }
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    {t("save")}
                  </Button>
                </div>
                {braveNeedsKey && <p className="text-xs text-amber-500">{t("webSearch.braveNeedsKey")}</p>}
                <p className="text-muted-foreground text-xs">{t("webSearch.braveQuota")}</p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  {t("webSearch.tavilyKeyLabel")}
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">{t("configured")}</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={
                      tavilyKeySet ? t("webSearch.tavilyKeyPlaceholderSet") : t("webSearch.tavilyKeyPlaceholderUnset")
                    }
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    {t("save")}
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">{t("webSearch.tavilyNeedsKey")}</p>
                )}
                <p className="text-muted-foreground text-xs">{t("webSearch.tavilyGetKey")}</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  {t("webSearch.proxyLabel")}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder={t("webSearch.proxyPlaceholder")}
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    {t("save")}
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">{t("webSearch.proxyHint")}</p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">{t("webSearch.testHint")}</p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? t("webSearch.testRunning") : t("webSearch.testButton")}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              {t("python.title")}
            </CardTitle>
            <CardDescription>{t.rich("python.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder={t("python.placeholder")}
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                {t("python.detect")}
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                {t("save")}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <TerminalIcon className="size-4" />
              {t("shell.title")}
            </CardTitle>
            <CardDescription>{t("shell.desc")}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="shell-mode">{t("shell.mode")}</Label>
              <div className="flex items-center gap-2">
                <Select value={shellMode} onValueChange={setShellMode} disabled={!loaded || savingShell}>
                  <SelectTrigger id="shell-mode" className="w-64">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {/* 只渲染本机实测可用的选项：Git Bash 没装就不出现，
                        也不给「自动」——选了哪个就跑哪个，不会悄悄换掉。 */}
                    {(shellInfo?.options ?? [])
                      .filter((o) => o.available)
                      .map((o) => (
                        <SelectItem key={o.mode} value={o.mode}>
                          {o.label}
                          {o.version ? ` ${o.version}` : ""}
                        </SelectItem>
                      ))}
                  </SelectContent>
                </Select>
                <Button onClick={saveShell} disabled={!loaded || savingShell}>
                  {t("shell.save")}
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">{t("shell.onlyDetected")}</p>
              <p className="text-muted-foreground text-xs">{t("shell.noAuto")}</p>
              <p className="text-muted-foreground text-xs">{t("shell.hint5")}</p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="shell-bin">{t("shell.bin")}</Label>
              <Input
                id="shell-bin"
                className="font-mono text-sm"
                placeholder={t("shell.binPlaceholder")}
                value={shellBin}
                disabled={!loaded || savingShell}
                onChange={(e) => setShellBin(e.target.value)}
              />
            </div>
            {shellInfo && (
              <div className="text-muted-foreground flex flex-col gap-1 border-t pt-3 text-xs">
                <span>
                  {t("shell.current")}：
                  {shellInfo.runtime.resolved
                    ? `${shellInfo.runtime.mode} ${shellInfo.runtime.version || ""} — ${shellInfo.runtime.bin}`
                    : `${t("shell.unavailable")}${shellInfo.runtime.error ? `：${shellInfo.runtime.error}` : ""}`}
                </span>
                <span>
                  {t("shell.pool")}：{t("shell.poolIdle", {
                    idle: shellInfo.runtime.pool_idle,
                    target: shellInfo.runtime.pool_target,
                  })}
                </span>
                <span>{t("shell.latency", { ms: shellInfo.runtime.last_latency_ms })}</span>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              {t("workers.title")}
            </CardTitle>
            <CardDescription>{t.rich("workers.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                {t("save")}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CoinsIcon className="size-4" />
              {t("budget.title")}
            </CardTitle>
            <CardDescription>{t.rich("budget.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={0}
                step={1}
                className="w-32 font-mono text-sm"
                placeholder="0"
                value={tokenBudgetK}
                disabled={!loaded || savingTokenBudget}
                onChange={(e) => setTokenBudgetK(e.target.value)}
              />
              <span className="text-sm text-muted-foreground">k</span>
              <Button onClick={saveTokenBudget} disabled={!loaded || savingTokenBudget}>
                {t("save")}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CircleDollarSignIcon className="size-4" />
              {t("pricing.title")}
            </CardTitle>
            <CardDescription>{t.rich("pricing.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {/* 等宽多行文本框；示例 JSON 用 t.raw 取原文，避免 ICU 把大括号当占位符解析。 */}
            <Textarea
              className="min-h-28 font-mono text-xs"
              placeholder={t.raw("pricing.placeholder")}
              value={priceTable}
              spellCheck={false}
              disabled={!loaded || savingPriceTable}
              onChange={(e) => setPriceTable(e.target.value)}
            />
            <div className="flex items-center gap-2">
              <Button onClick={savePriceTable} disabled={!loaded || savingPriceTable}>
                {t("save")}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <LanguagesIcon className="size-4" />
              {t("language.title")}
            </CardTitle>
            <CardDescription>{t("language.desc")}</CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">{LOCALE_LABELS.zh}</p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              {t("sendKey.title")}
            </CardTitle>
            <CardDescription>{t.rich("sendKey.desc", richTags)}</CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              {t("sendKey.label")}
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sendModeOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
