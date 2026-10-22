"use client";

import * as React from "react";

import { RefreshCwIcon, ShieldAlertIcon, SmartphoneIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { CopyButton } from "@/components/copy-button";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { http } from "@/lib/api";

// 系统 → 局域网连接
//
// 这个页面只回答一个问题：手机现在该填什么、扫什么。所以信息全部来自
// GET /api/lan（server/lan_api.go）——监听地址、可用 URL、二维码、以及
// 「还没开局域网」时要让用户复制的那两条命令。前端不自己猜 IP：
// 机器有多块网卡时只有后端才拿得准哪一条能用。
//
// 这里刻意没有「一键切换到局域网」按钮：换监听地址必须重启进程，
// 静默重启一个正在跑任务的 ARTEX 比让用户复制一条命令危险得多。

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
        <Button type="button" size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
          <RefreshCwIcon />
          {t("refresh")}
        </Button>
      </div>

      {error ? (
        <Card>
          <CardContent className="p-6 text-muted-foreground text-sm">{t("loadFailed")}</CardContent>
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
              <CardTitle>{t("appTitle")}</CardTitle>
              <CardDescription>{t("appDesc")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-2">
              <InfoRow label={t("appServer")}>{info?.mobile_hint ?? "-"}</InfoRow>
              <InfoRow label={t("appUser")}>{t("appUserValue")}</InfoRow>
              <InfoRow label={t("appPassword")}>{t("appPasswordValue")}</InfoRow>
              <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("appNote")}</p>
              {info?.password_set ? null : (
                <p className="text-sm font-normal leading-relaxed text-destructive">{t("passwordNeedInit")}</p>
              )}
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
            <div className="space-y-2">
              <div className="text-muted-foreground text-xs">{t("stepRestart")}</div>
              <div className="flex flex-wrap items-center gap-2">
                <code className="block select-all break-all rounded bg-muted p-2 text-xs">{info?.restart_cmd ?? ""}</code>
                <CopyButton text={info?.restart_cmd} />
              </div>
            </div>
            {info?.firewall_cmd ? (
              <div className="space-y-2">
                <div className="text-muted-foreground text-xs">{t("stepFirewall")}</div>
                <div className="flex flex-wrap items-center gap-2">
                  <code className="block select-all break-all rounded bg-muted p-2 text-xs">{info.firewall_cmd}</code>
                  <CopyButton text={info.firewall_cmd} />
                </div>
              </div>
            ) : null}
            <p className="text-sm font-normal leading-relaxed text-muted-foreground">{t("enableNote")}</p>
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
