"use client";

import * as React from "react";

import { KeyRoundIcon, RefreshCwIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { http } from "@/lib/api";

// 系统设置 → 登录密码
//
// 两块，对应两种真实处境：
//   改密码：记得旧密码，走 /api/auth/change-password（服务端会校验旧密码）。
//   重置  ：忘了旧密码但人还登录着，走 /api/password/reset（凭会话，不校验旧密码）。
//
// 两者会让 JWT 签名密钥（由密码哈希派生）一起变，所以手机端和别的浏览器都会掉线。
// 成功后直接送回登录页——不偷偷续一个 token，否则等于把「密码换了」这件事藏起来。

type Mode = "change" | "reset";

export function PasswordCard() {
  const t = useTranslations("settingsPage.password");
  const [mode, setMode] = React.useState<Mode>("change");
  const [oldPassword, setOldPassword] = React.useState("");
  const [newPassword, setNewPassword] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const reset = () => {
    setOldPassword("");
    setNewPassword("");
    setConfirm("");
    setError(null);
  };

  const submit = async () => {
    setError(null);
    if (!newPassword) {
      setError(t("empty"));
      return;
    }
    if (newPassword !== confirm) {
      setError(t("mismatch"));
      return;
    }
    if (mode === "change" && !oldPassword) {
      setError(t("oldRequired"));
      return;
    }
    setBusy(true);
    try {
      if (mode === "change") {
        const res = await http<{ ok: boolean; token?: string }>("/auth/change-password", {
          method: "POST",
          body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
        });
        // 改密码接口会顺手发一个新 token，这里用掉它，避免用户刚改完就被踢
        // （refresh 之后 token 仍与新哈希匹配）。
        if (res.token && typeof window !== "undefined") {
          localStorage.setItem("artex_token", res.token);
        }
        toast.success(t("changed"));
      } else {
        await http<{ ok: boolean }>("/password/reset", {
          method: "POST",
          body: JSON.stringify({ password: newPassword }),
        });
        toast.success(t("resetted"));
      }
      reset();
      // 手机端等其它会话已经失效，这里统一回登录页重新登一次。
      if (typeof window !== "undefined") {
        window.setTimeout(() => {
          window.location.href = "/login";
        }, 600);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <KeyRoundIcon />
          {t("title")}
        </CardTitle>
        <CardDescription>{t("desc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant={mode === "change" ? "default" : "outline"}
            onClick={() => {
              setMode("change");
              reset();
            }}
          >
            {t("modeChange")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant={mode === "reset" ? "default" : "outline"}
            onClick={() => {
              setMode("reset");
              reset();
            }}
          >
            {t("modeReset")}
          </Button>
        </div>

        <p className="text-muted-foreground text-xs">{mode === "change" ? t("changeHint") : t("resetHint")}</p>

        {mode === "change" ? (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="pw-old">{t("oldLabel")}</Label>
            <Input
              id="pw-old"
              type="password"
              autoComplete="current-password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
            />
          </div>
        ) : null}

        <div className="flex flex-col gap-1.5">
          <Label htmlFor="pw-new">{t("newLabel")}</Label>
          <Input
            id="pw-new"
            type="password"
            autoComplete="new-password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor="pw-confirm">{t("confirmLabel")}</Label>
          <Input
            id="pw-confirm"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </div>

        {error ? <p className="text-destructive text-sm">{error}</p> : null}

        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" size="sm" disabled={busy} onClick={() => void submit()}>
            {busy ? <RefreshCwIcon className="animate-spin" /> : null}
            {mode === "change" ? t("submitChange") : t("submitReset")}
          </Button>
          <span className="text-muted-foreground text-xs">{t("reloginNote")}</span>
        </div>

        <p className="text-muted-foreground text-xs">{t("cliHint")}</p>
      </CardContent>
    </Card>
  );
}
