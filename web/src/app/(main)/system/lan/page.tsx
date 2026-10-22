"use client";

import * as React from "react";

import { RefreshCwIcon, ShieldAlertIcon, SmartphoneIcon, WifiIcon, WifiOffIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { CopyButton } from "@/components/copy-button";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { http } from "@/lib/api";

// 系统 → 局域网连接
//
// 这块页面回答两个问题：手机怎么连（二维码/地址）、没开局域网怎么开（按钮）。
//
// 二维码扫出来的是「配对码」而不是网址：手机 App 读到它就能同时拿到地址和登录凭据，
// 一步完成绑定（接口见 server/pair_api.go）。想让手机浏览器直接用，下面那行 http
// 地址照样可以复制。
//
// 前端不自己猜 IP：机器有多块网卡时，只有后端分得清哪一条能被手机访问。

type LanInfo = {
  version: string;
  listen_addr: string;
  port: string;
  loopback_only: boolean;
  urls: string[];
  primary_url: string;
  password_set: boolean;
  restart_cmd: string;
  firewall_cmd: string;
  mobile_hint: string;
  env_override: boolean;
};

type PairTicket = {
  code: string;
  host: string;
  payload: string;
  qr_svg: string;
  lan_open: boolean;
  expires_at: number;
};

type LanSwitchResult = {
  ok: boolean;
  listen: string;
  config: string;
  restart: boolean;
  firewall: string;
  env_override: boolean;
};

// svgToDataURL 把后端编好的 SVG 变成 <img> 能直接用的 data URL。
// 用 encodeURIComponent 而不是 btoa：SVG 里一旦出现非 ASCII 字符，btoa 会直接抛错。
function svgToDataURL(svg: string): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}

function InfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b py-1.5 last:border-b-0">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="font-medium text-sm tabular-nums">{children}</span>
    </div>
  );
}

export default function LanPage() {
  const t = useTranslations("lanPage");
  const [info, setInfo] = React.useState<LanInfo | null>(null);
  const [pair, setPair] = React.useState<PairTicket | null>(null);
  const [pairBusy, setPairBusy] = React.useState(false);
  const [left, setLeft] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);
  // 正在切监听地址：请求发出后进程会自己重启，这一屏要等它回来。
  const [switching, setSwitching] = React.useState(false);
  const [firewall, setFirewall] = React.useState<string>("");

  const load = React.useCallback(async () => {
    setError(null);
    try {
      setInfo(await http<LanInfo>("/lan"));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 每张配对码有有效期；过期就自动换一张，别让用户对着一张死图扫。
  const createPair = React.useCallback(async () => {
    setPairBusy(true);
    try {
      setPair(await http<PairTicket>("/pair/code", { method: "POST" }));
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setPairBusy(false);
    }
  }, []);

  const lanOpen = info !== null && !info.loopback_only;

  React.useEffect(() => {
    if (!lanOpen) {
      setPair(null);
      return;
    }
    void createPair();
  }, [lanOpen, createPair]);

  // 倒计时：每秒刷新剩余秒数，归零就自动换一张。
  React.useEffect(() => {
    if (!pair) return;
    const tick = () => {
      const rest = pair.expires_at - Math.floor(Date.now() / 1000);
      setLeft(rest > 0 ? rest : 0);
      if (rest <= 0) void createPair();
    };
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, [pair, createPair]);

  // 重启后自动回来：轮询 /api/health，通了就整页刷新。
  React.useEffect(() => {
    if (!switching) return;
    let alive = true;
    const tick = async () => {
      if (!alive) return;
      try {
        await http("/health");
        if (alive) window.location.reload();
        return;
      } catch {
        // 还没起来，继续等。
      }
      if (alive) window.setTimeout(tick, 1200);
    };
    const first = window.setTimeout(tick, 1500);
    return () => {
      alive = false;
      window.clearTimeout(first);
    };
  }, [switching]);

  const switchExposure = React.useCallback(
    async (lan: boolean) => {
      setError(null);
      setFirewall("");
      setSwitching(true);
      try {
        const res = await http<LanSwitchResult>("/lan/" + (lan ? "enable" : "disable"), {
          method: "POST",
          body: JSON.stringify({ lan }),
        });
        setFirewall(res.firewall);
        if (!res.restart) {
          // 环境变量压着监听地址时写配置不会生效，直接把原因说清楚，别假装成功。
          setSwitching(false);
          setError(res.env_override ? t("envOverride") : t("noRestart"));
        }
      } catch (e) {
        setSwitching(false);
        setError(e instanceof Error ? e.message : String(e));
      }
    },
    [t],
  );

  const urls = info?.urls ?? [];
  const primary = info?.primary_url ?? "";
  const qr = pair?.qr_svg ?? "";

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">{t("title")}</h1>
          <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("desc")}</p>
        </div>
        <Button type="button" size="sm" variant="outline" disabled={loading || switching} onClick={() => void load()}>
          <RefreshCwIcon />
          {t("refresh")}
        </Button>
      </div>

      {error ? (
        <Card>
          <CardContent className="p-4 text-destructive text-sm">{error}</CardContent>
        </Card>
      ) : null}

      {switching ? (
        <Card>
          <CardContent className="flex items-center gap-2 p-4 text-sm">
            <RefreshCwIcon className="animate-spin" />
            {t("restarting")}
          </CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>{t("statusTitle")}</CardTitle>
          <CardDescription>{t("statusDesc")}</CardDescription>
        </CardHeader>
        <CardContent>
          <InfoRow label={t("listenLabel")}>{info?.listen_addr ?? "-"}</InfoRow>
          <InfoRow label={t("lanLabel")}>
            {lanOpen ? (
              <Badge variant="secondary">{t("lanYes")}</Badge>
            ) : (
              <Badge variant="outline">{t("lanNo")}</Badge>
            )}
          </InfoRow>
          <InfoRow label={t("passwordLabel")}>
            {info?.password_set ? t("passwordSet") : t("passwordUnset")}
          </InfoRow>
        </CardContent>
      </Card>

      {lanOpen ? (
        <div className="grid gap-3 md:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle>{t("pairTitle")}</CardTitle>
              <CardDescription>{t("pairHint")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              {qr ? (
                <img
                  src={svgToDataURL(qr)}
                  alt={pair?.payload ?? ""}
                  style={{ width: 200, height: 200 }}
                  className="rounded bg-white p-1"
                />
              ) : null}
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground text-xs">{t("pairCodeLabel")}</span>
                <code className="rounded bg-muted px-2 py-1 font-mono font-semibold text-base tracking-[0.2em]">
                  {pair?.code ?? "--------"}
                </code>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={pairBusy}
                  onClick={() => void createPair()}
                >
                  <RefreshCwIcon />
                  {t("pairRefresh")}
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">{t("pairExpires", { sec: left })}</p>
              <p className="text-muted-foreground text-xs">{t("pairManual")}</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("webEntryTitle")}</CardTitle>
              <CardDescription>{t("webEntryHint")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                <code className="block select-all break-all rounded bg-muted p-2 text-xs">{primary}</code>
                <CopyButton text={primary} />
              </div>
              {urls.length > 1 ? (
                <div className="space-y-2">
                  <div className="text-muted-foreground text-xs">{t("urlsLabel")}</div>
                  {urls.map((u) => (
                    <div key={u} className="flex flex-wrap items-center gap-2">
                      <code className="block select-all break-all rounded bg-muted p-2 text-xs">{u}</code>
                      <CopyButton text={u} />
                    </div>
                  ))}
                </div>
              ) : null}
              <InfoRow label={t("appUser")}>{t("appUserValue")}</InfoRow>
              <InfoRow label={t("appPassword")}>{t("appPasswordValue")}</InfoRow>
              <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("appNote")}</p>
              {info?.password_set ? null : (
                <p className="text-sm font-normal leading-relaxed text-destructive">{t("passwordNeedInit")}</p>
              )}
              <div className="pt-2">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={switching}
                  onClick={() => void switchExposure(false)}
                >
                  <WifiOffIcon />
                  {t("disableButton")}
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">{t("disableNote")}</p>
            </CardContent>
          </Card>
        </div>
      ) : (
        <Card>
          <CardHeader>
            <CardTitle>{t("enableTitle")}</CardTitle>
            <CardDescription>{t("enableDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" disabled={switching || loading} onClick={() => void switchExposure(true)}>
                <WifiIcon />
                {t("enableButton")}
              </Button>
              <span className="text-muted-foreground text-xs">{t("enableButtonNote")}</span>
            </div>

            {firewall === "ok" ? <p className="text-muted-foreground text-xs">{t("firewallAuto")}</p> : null}
            {firewall === "manual" ? <p className="text-destructive text-xs">{t("firewallManual")}</p> : null}

            <div className="space-y-2">
              <div className="text-muted-foreground text-xs">{t("firewallHint")}</div>
              <div className="flex flex-wrap items-center gap-2">
                <code className="block select-all break-all rounded bg-muted p-2 text-xs">{info?.firewall_cmd ?? ""}</code>
                <CopyButton text={info?.firewall_cmd} />
              </div>
            </div>

            <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("enableNote")}</p>

            <div className="space-y-2">
              <div className="text-muted-foreground text-xs">{t("scriptHint")}</div>
              <div className="flex flex-wrap items-center gap-2">
                <code className="block select-all break-all rounded bg-muted p-2 text-xs">{info?.restart_cmd ?? ""}</code>
                <CopyButton text={info?.restart_cmd} />
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>
            <span className="flex items-center gap-2 font-medium text-foreground">
              <ShieldAlertIcon />
              {t("securityTitle")}
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("securityBody")}</p>
        </CardContent>
      </Card>

      <p className="flex items-center gap-2 text-muted-foreground text-xs">
        <SmartphoneIcon />
        {loading ? t("loading") : `${t("versionLabel")} ${info?.version ?? "-"}`}
      </p>
    </div>
  );
}
