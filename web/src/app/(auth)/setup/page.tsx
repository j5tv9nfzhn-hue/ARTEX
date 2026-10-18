"use client";

import { useEffect, useState } from "react";

import { useRouter } from "next/navigation";

import { ShieldCheck } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function SetupPage() {
  const router = useRouter();
  const t = useTranslations("auth");
  const [setupToken, setSetupToken] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);

  useEffect(() => {
    api
      .authStatus()
      .then(({ initialized }) => {
        if (initialized) router.replace("/login");
      })
      .catch(() => setError(t("setup.errorBackend")))
      .finally(() => setChecking(false));
  }, [router, t]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!setupToken.trim()) {
      setError(t("setup.errorTokenEmpty"));
      return;
    }
    if (password !== confirm) {
      setError(t("setup.errorMismatch"));
      return;
    }
    if (password.length < 8) {
      setError(t("setup.errorTooShort"));
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.initPassword(password, setupToken.trim());
      auth.setToken(token);
      router.replace("/function/tasks");
    } catch (err) {
      setError(err instanceof Error ? err.message : t("setup.errorInit"));
    } finally {
      setLoading(false);
    }
  }

  if (checking) return null;

  return (
    <div className="flex h-dvh bg-background">
      {/* Left tactical command hero panel */}
      <div className="relative hidden flex-col items-center justify-center overflow-hidden bg-primary p-12 text-center text-primary-foreground lg:flex lg:w-2/5">
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_25%_20%,rgba(255,255,255,0.2),transparent_52%),radial-gradient(circle_at_80%_85%,rgba(0,0,0,0.36),transparent_65%)]"
        />
        <div className="relative flex flex-col items-center gap-8">
          <div className="relative flex items-center justify-center">
            <div className="absolute size-96 rounded-full border border-primary-foreground/8" />
            <div className="absolute size-72 rounded-full border border-dashed border-primary-foreground/15" />
            <div className="absolute size-52 rounded-full border border-primary-foreground/20" />
            <div className="absolute h-px w-80 bg-primary-foreground/12" />
            <div className="absolute h-80 w-px bg-primary-foreground/12" />
            <div className="relative flex size-36 items-center justify-center rounded-3xl border border-primary-foreground/25 bg-primary-foreground/10 shadow-2xl backdrop-blur-xs">
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img src="/logo.png" alt="ARTEX-REA" width={112} height={112} className="relative brightness-0 invert" />
            </div>
          </div>
          <div className="relative space-y-2">
            <div className="inline-flex items-center gap-2 rounded-full border border-primary-foreground/25 bg-primary-foreground/12 px-3.5 py-1 font-mono text-[11px] font-semibold tracking-widest uppercase">
              {"ARTEX-REA // INITIALIZATION"}
            </div>
            <p className="font-mono text-[10px] tracking-widest text-primary-foreground/75 uppercase">
              AUTONOMOUS OFFENSIVE &amp; REVERSE ENGINEERING ENGINE
            </p>
          </div>
        </div>
      </div>

      {/* Right setup console panel */}
      <div className="relative flex w-full items-center justify-center bg-background p-6 sm:p-8 lg:w-3/5">
        <div className="relative w-full max-w-md overflow-hidden rounded-2xl border border-border/80 bg-card/95 p-8 shadow-lg ring-1 ring-border/50 backdrop-blur-xs before:pointer-events-none before:absolute before:inset-x-8 before:top-0 before:h-px before:bg-linear-to-r before:from-transparent before:via-primary/60 before:to-transparent sm:p-10">
          <div className="mb-8 space-y-3 text-center">
            <div className="mx-auto inline-flex size-11 items-center justify-center rounded-xl bg-primary/12 text-primary ring-1 ring-primary/25">
              <ShieldCheck className="size-5" />
            </div>
            <h2 className="font-display text-2xl font-semibold tracking-tight">{t("setup.title")}</h2>
            <p className="mx-auto max-w-xl text-sm text-muted-foreground">{t("setup.desc")}</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="setup-token">{t("setup.setupToken")}</Label>
              <Input
                id="setup-token"
                type="text"
                value={setupToken}
                onChange={(e) => setSetupToken(e.target.value)}
                placeholder={t("setup.setupTokenPlaceholder")}
                autoFocus
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">{t("setup.setupTokenHelp")}</p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">{t("setup.newPassword")}</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t("setup.newPasswordPlaceholder")}
                autoComplete="new-password"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="confirm">{t("setup.confirmPassword")}</Label>
              <Input
                id="confirm"
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                placeholder={t("setup.confirmPlaceholder")}
                autoComplete="new-password"
              />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button
              type="submit"
              size="lg"
              className="mt-1 w-full"
              disabled={loading || !setupToken.trim() || !password || !confirm}
            >
              {loading ? t("setup.submitting") : t("setup.submit")}
            </Button>
          </form>
        </div>
      </div>
    </div>
  );
}
