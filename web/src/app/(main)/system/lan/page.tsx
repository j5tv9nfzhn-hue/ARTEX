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
// 这个页面只回答一个问题：手机现在该填什么、扫什么。所以信息全部来自
// GET /api/lan（server/lan_api.go）——监听地址、可用 URL、二维码。
// 前端不自己猜 IP：机器有多块网卡时只有后端才拿得准哪一条能用。
//
// 「还没开局域网」这一块是按钮，不是让用户去敲命令：
// POST /api/lan/enable 把 listen 写进 config.json 并让进程自己重启一次
// （走的就是「更新后重启」那条已有路径），重启回来就是局域网可连状态。
// 防火墙也会先自动尝试放行，只有系统拦下（没给管理员权限）时才把命令摆出来。

type LanInfo = {
  version: string;
  listen_addr: string;
  port: string;
  loopback_only: boolean;
  urls: string[];
  primary_url: string;
  password_set: boolean;
  qr_svg: string;
  restart_cmd: string;
  firewall_cmd: string;
  mobile_hint: string;
  env_override: boolean;
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

  // 重启后自动回来：轮询 /api/health，通了就整页刷新。
  // 不设上限次数——本地重启通常三秒内回来，偶尔被防火墙拦一下也不该让用户手动按 F5。
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
  const qr = info?.qr_svg ?? "";
  const lanOpen = info !== null && !info.loopback_only;

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
              <CardTitle>{t("qrTitle")}</CardTitle>
              <CardDescription>{t("qrHint")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              {qr ? (
                <img
                  src={svgToDataURL(qr)}
                  alt={primary}
                  style={{ width: 200, height: 200 }}
                  className="rounded bg-white p-1"
                />
              ) : null}
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
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>
                <span className="flex flex-wrap items-center gap-2">
                  {t("appTitle")}
                  <Badge variant="outline">{t("appExperimentalTag")}</Badge>
                </span>
              </CardTitle>
              <CardDescription>{t("appDesc")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-2">
              <InfoRow label={t("appServer")}>{info?.mobile_hint ?? "-"}</InfoRow>
              <InfoRow label={t("appUser")}>{t("appUserValue")}</InfoRow>
              <InfoRow label={t("appPassword")}>{t("appPasswordValue")}</InfoRow>
              <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("appNote")}</p>
              <p className="text-muted-foreground text-xs">{t("appExperimental")}</p>
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
