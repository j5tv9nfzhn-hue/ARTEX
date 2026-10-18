"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const t = useTranslations("auth");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // 滚动到条款底部（含无需滚动即可完整展示的情况）方可点击「同意」。
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // 打开时重置，并处理内容本就不足一屏、无法触发滚动的场景。
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // 已登录直接进主界面（静态导出下无 middleware 代劳这层跳转）。
    const token = auth.getToken();
    if (token) {
      // localStorage 可能仍有凭据但 cookie 已丢失。先同步，再发起全新请求，
      // 避免服务端守卫或路由缓存把跳转送回仍处于 checking 状态的登录页。
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError(t("login.errorBackend")))
      .finally(() => setChecking(false));
  }, [router, t]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError(t("login.errorAgree"));
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX-REA", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError(t("login.errorCredential"));
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        {t("login.checking")}
      </div>
    );
  }

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
              {"ARTEX-REA // TACTICAL COMMAND"}
            </div>
            <p className="font-mono text-[10px] tracking-widest text-primary-foreground/75 uppercase">
              AUTONOMOUS OFFENSIVE &amp; REVERSE ENGINEERING ENGINE
            </p>
          </div>
        </div>
      </div>

      {/* Right login console panel */}
      <div className="relative flex w-full items-center justify-center bg-background p-6 sm:p-8 lg:w-3/5">
        <div className="relative w-full max-w-md overflow-hidden rounded-2xl border border-border/80 bg-card/95 p-8 shadow-lg ring-1 ring-border/50 backdrop-blur-xs before:pointer-events-none before:absolute before:inset-x-8 before:top-0 before:h-px before:bg-linear-to-r before:from-transparent before:via-primary/60 before:to-transparent sm:p-10">
          <div className="mb-8 space-y-3 text-center">
            <div className="mx-auto inline-flex size-11 items-center justify-center rounded-xl bg-primary/12 text-primary ring-1 ring-primary/25">
              <ShieldCheck className="size-5" />
            </div>
            <h2 className="font-display text-2xl font-semibold tracking-tight">{t("login.title")}</h2>
            <p className="mx-auto max-w-xl text-sm text-muted-foreground">{t("login.welcome")}</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">{t("login.username")}</Label>
              <Input id="username" value="ARTEX-REA" readOnly className="bg-muted/60 font-mono text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">{t("login.password")}</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t("login.passwordPlaceholder")}
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2 pt-1">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                {t.rich("login.agree", {
                  terms: (chunks) => (
                    <button
                      type="button"
                      onClick={() => setTermsOpen(true)}
                      className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                    >
                      {chunks}
                    </button>
                  ),
                })}
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" size="lg" className="mt-1 w-full" disabled={loading || !password || !agreed}>
              {loading ? t("login.submitting") : t("login.submit")}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">{t("terms.title")}</DialogTitle>
              <p className="text-xs text-muted-foreground">{t("terms.meta")}</p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              {t("terms.intro")}
              <span className="font-medium text-foreground"> {t("terms.introEmphasis")}</span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                {t("terms.a1Title")}
              </h4>
              <p className="pl-7">{t("terms.a1Body")}</p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                {t("terms.a2Title")}
              </h4>
              <p className="pl-7">{t("terms.a2Body")}</p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                {t("terms.a3Title")}
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>{t("terms.a3Item1")}</li>
                <li>{t("terms.a3Item2")}</li>
                <li>{t("terms.a3Item3")}</li>
                <li>{t("terms.a3Item4")}</li>
                <li>{t("terms.a3Item5")}</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                {t("terms.a4Title")}
              </h4>
              <p className="pl-7">{t("terms.a4Body")}</p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                {t("terms.a5Title")}
              </h4>
              <p className="pl-7">{t("terms.a5Body")}</p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                {t("terms.a6Title")}
              </h4>
              <p className="pl-7">
                {t("terms.a6Body")}
                <span className="font-medium text-foreground"> {t("terms.a6Emphasis")}</span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                {t("terms.a7Title")}
              </h4>
              <p className="pl-7">{t("terms.a7Body")}</p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                {t("terms.a8Title")}
              </h4>
              <p className="pl-7">{t("terms.a8Body")}</p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? t("terms.footerRead") : t("terms.footerNotRead")}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                {t("terms.agreeButton")}
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
