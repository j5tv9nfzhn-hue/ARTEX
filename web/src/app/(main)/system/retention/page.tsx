"use client";

import * as React from "react";

import { AlertTriangleIcon, DatabaseIcon, EyeIcon, RefreshCwIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { RETENTION_CONFIRMATION, type RetentionField } from "@/lib/retention";

import { RetentionAudit } from "./_components/retention-audit";
import { RetentionCounts } from "./_components/retention-counts";
import { type RetentionError, useRetention } from "./_components/use-retention";

const numericFields: RetentionField[] = ["usage_days", "log_days", "notification_days", "max_rows_per_table"];

export default function RetentionPage() {
  const t = useTranslations("retentionPage");
  const model = useRetention();
  const [reloadOpen, setReloadOpen] = React.useState(false);
  const selected = model.lease?.counts.reduce((n, row) => n + row.selected, 0) ?? 0;
  const seconds = Math.ceil(model.remaining / 1000);
  const countdown = `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  const pending = model.busy !== null;
  const locked = pending || !model.envelope;
  const errorText = (error: RetentionError) =>
    error.kind === "http" ? t("error.http", { status: error.status ?? 0 }) : t(`error.${error.kind}`);
  const refreshPolicy = () => {
    if (model.dirty) setReloadOpen(true);
    else void model.reloadPolicy();
  };
  const close = () => model.closeConfirmation();
  const status = model.busy ? t(`busy.${model.busy}`) : model.notice && t(`notice.${model.notice}`);
  let policyStatus = t("policy.noDefaults");
  if (model.envelope) policyStatus = model.dirty ? t("policy.unsaved") : t("policy.saved");

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6" aria-busy={pending}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="font-semibold text-xl tracking-tight">{t("title")}</h1>
            <Badge variant="secondary">{t(model.envelope ? "automaticOff" : "manualOnly")}</Badge>
          </div>
          <p className="mt-1 max-w-3xl text-muted-foreground text-sm">{t("subtitle")}</p>
        </div>
        <Button variant="outline" disabled={pending} onClick={refreshPolicy}>
          <RefreshCwIcon className="size-4" />
          {t("reload")}
        </Button>
      </div>

      <Alert>
        <ShieldCheckIcon />
        <AlertTitle>{t("boundaryTitle")}</AlertTitle>
        <AlertDescription>
          <p>{t("boundary")}</p>
          <p>{t("scopeLimit")}</p>
        </AlertDescription>
      </Alert>
      {model.error && (
        <Alert variant="destructive" role="alert">
          <AlertTriangleIcon />
          <AlertTitle>{t("error.title")}</AlertTitle>
          <AlertDescription>
            <p>{errorText(model.error)}</p>
            <p>{t("error.recovery")}</p>
          </AlertDescription>
        </Alert>
      )}
      <div role="status" aria-live="polite" className="text-muted-foreground text-sm">
        {status}
      </div>

      {model.uncertain && (
        <Alert variant="destructive" role="alert">
          <AlertTriangleIcon />
          <AlertTitle>{t("uncertain.title")}</AlertTitle>
          <AlertDescription className="space-y-3">
            <p>{t("uncertain.description")}</p>
            <p>{t("uncertain.limit")}</p>
            <div className="flex flex-wrap items-center gap-3">
              <Button
                size="sm"
                variant="outline"
                disabled={pending || model.auditBusy}
                onClick={() => void model.reloadAudit()}
              >
                {t("audit.refresh")}
              </Button>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={model.reviewed}
                  disabled={pending || model.auditBusy || !model.auditFresh}
                  onChange={(e) => model.setReviewed(e.target.checked)}
                />
                {t("uncertain.reviewed")}
              </label>
              <Button
                size="sm"
                variant="outline"
                disabled={!model.reviewed || !model.auditFresh || model.auditBusy || pending}
                onClick={model.acknowledgeUncertainty}
              >
                {t("uncertain.acknowledge")}
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      )}

      <div className="grid items-start gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <DatabaseIcon className="size-4" />
              {t("policy.title")}
            </CardTitle>
            <CardDescription>{t("policy.description")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-5">
            <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
              <div>
                <Label htmlFor="retention-enabled">{t("policy.enabled")}</Label>
                <p id="retention-enabled-help" className="mt-1 text-muted-foreground text-xs">
                  {t("policy.enabledHelp")}
                </p>
              </div>
              <Switch
                id="retention-enabled"
                checked={model.draft?.enabled ?? false}
                disabled={locked || !model.draft}
                aria-describedby="retention-enabled-help"
                onCheckedChange={(value) => {
                  if (model.draft) model.updateDraft({ ...model.draft, enabled: value });
                }}
              />
            </div>
            {!model.envelope && (
              <p className="text-muted-foreground text-sm">
                {model.busy === "load" ? t("policy.loading") : t("policy.unavailable")}
              </p>
            )}
            <div className="grid gap-4 sm:grid-cols-2">
              {numericFields.map((field) => (
                <div className="space-y-2" key={field}>
                  <Label htmlFor={`retention-${field}`}>{t(`field.${field}.label`)}</Label>
                  <Input
                    id={`retention-${field}`}
                    type="text"
                    inputMode="numeric"
                    autoComplete="off"
                    maxLength={4}
                    disabled={locked || !model.draft}
                    value={model.draft?.[field] ?? ""}
                    aria-invalid={!!model.errors[field]}
                    aria-describedby={`retention-${field}-help${model.errors[field] ? ` retention-${field}-error` : ""}`}
                    onChange={(e) => {
                      if (model.draft) model.updateDraft({ ...model.draft, [field]: e.target.value });
                    }}
                  />
                  <p id={`retention-${field}-help`} className="text-muted-foreground text-xs">
                    {t(`field.${field}.help`)}
                  </p>
                  {model.errors[field] && (
                    <p id={`retention-${field}-error`} className="text-destructive text-xs">
                      {t(`field.${field}.error`)}
                    </p>
                  )}
                </div>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button disabled={!model.canSave} onClick={() => void model.save()}>
                {t("policy.save")}
              </Button>
              <p className="text-muted-foreground text-xs">{policyStatus}</p>
            </div>
            <p className="border-t pt-3 text-muted-foreground text-xs">{t("policy.invalidate")}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <EyeIcon className="size-4" />
              {t("preview.title")}
            </CardTitle>
            <CardDescription>{t("preview.description")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Button variant="outline" disabled={!model.canPreview} onClick={() => void model.preview()}>
              <EyeIcon className="size-4" />
              {t("preview.create")}
            </Button>
            {model.dirty && <p className="text-amber-700 text-sm dark:text-amber-400">{t("preview.saveFirst")}</p>}
            {model.envelope && !model.envelope.policy.enabled && (
              <p className="text-muted-foreground text-sm">{t("preview.disabled")}</p>
            )}
            {model.lease ? (
              <>
                <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-muted/50 p-3">
                  <span className="text-sm">{t("preview.selected", { count: selected })}</span>
                  <span className="font-mono text-xs" role="timer" aria-label={t("preview.remaining")}>
                    {countdown}
                  </span>
                </div>
                <RetentionCounts rows={model.lease.counts} />
                <p className="text-muted-foreground text-xs">{t("preview.bounded")}</p>
                {model.lease.counts.some((row) => row.more_candidates) && (
                  <p className="text-amber-700 text-xs dark:text-amber-400">{t("preview.more")}</p>
                )}
                {model.block && <p className="text-muted-foreground text-sm">{t(`block.${model.block}`)}</p>}
                <Button variant="destructive" disabled={!model.canExecute} onClick={model.openConfirmation}>
                  <Trash2Icon className="size-4" />
                  {t("preview.review")}
                </Button>
              </>
            ) : (
              <div className="rounded-lg border border-dashed p-5 text-muted-foreground text-sm">
                {t("preview.none")}
              </div>
            )}
            <p className="border-t pt-3 text-muted-foreground text-xs">{t("preview.expiry")}</p>
            {model.envelope && <p className="text-muted-foreground text-xs">{model.envelope.scope}</p>}
          </CardContent>
        </Card>
      </div>

      {model.result && (
        <Card>
          <CardHeader>
            <CardTitle>{t("result.title")}</CardTitle>
            <CardDescription>{t("result.description")}</CardDescription>
          </CardHeader>
          <CardContent>
            <RetentionCounts rows={model.result.counts} result />
            <p className="mt-3 text-muted-foreground text-xs">{t("result.space")}</p>
          </CardContent>
        </Card>
      )}

      <RetentionAudit
        entries={model.entries}
        busy={model.auditBusy}
        loaded={model.auditLoaded}
        error={model.auditError ? errorText(model.auditError) : null}
        disabled={pending}
        refresh={() => void model.reloadAudit()}
      />

      <AlertDialog
        open={model.confirmOpen}
        onOpenChange={(open) => {
          if (!open) close();
        }}
      >
        <AlertDialogContent className="max-h-[90dvh] overflow-y-auto data-[size=default]:max-w-lg sm:data-[size=default]:max-w-lg">
          <AlertDialogHeader>
            <AlertDialogTitle>{t("confirm.title")}</AlertDialogTitle>
            <AlertDialogDescription>{t("confirm.description")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-4">
            <p className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm">
              {t("confirm.summary", { count: selected, limit: model.lease?.policy.max_rows_per_table ?? 0 })}
            </p>
            {model.lease && <RetentionCounts rows={model.lease.counts} />}
            <div className="space-y-2">
              <Label htmlFor="retention-confirm">{t("confirm.type")}</Label>
              <code id="retention-confirm-phrase" className="block select-all break-all rounded bg-muted p-2 text-xs">
                {RETENTION_CONFIRMATION}
              </code>
              <Input
                id="retention-confirm"
                autoComplete="off"
                spellCheck={false}
                value={model.confirmation}
                disabled={pending}
                aria-describedby="retention-confirm-phrase retention-confirm-safety"
                onChange={(e) => model.setConfirmation(e.target.value)}
              />
            </div>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-1"
                checked={model.acknowledgement}
                disabled={pending}
                onChange={(e) => model.setAcknowledgement(e.target.checked)}
              />
              {t("confirm.acknowledge")}
            </label>
            <p id="retention-confirm-safety" className="text-muted-foreground text-xs">
              {t("confirm.safety")}
            </p>
          </div>
          <AlertDialogFooter>
            <Button variant="outline" disabled={pending} onClick={close}>
              {t("confirm.cancel")}
            </Button>
            <Button
              variant="destructive"
              disabled={!model.canExecute || model.confirmation !== RETENTION_CONFIRMATION || !model.acknowledgement}
              onClick={() => void model.execute()}
            >
              {t("confirm.execute")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={reloadOpen} onOpenChange={setReloadOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("reloadTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("reloadDescription")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="outline" onClick={() => setReloadOpen(false)}>
              {t("confirm.cancel")}
            </Button>
            <Button
              onClick={() => {
                setReloadOpen(false);
                void model.reloadPolicy();
              }}
            >
              {t("reload")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
