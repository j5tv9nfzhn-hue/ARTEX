"use client";

import * as React from "react";

import {
  draftRetentionPolicy,
  leaseRetentionPreview,
  RETENTION_CONFIRMATION,
  type RetentionAuditEntry,
  type RetentionDraft,
  type RetentionExecution,
  type RetentionLease,
  type RetentionPolicyEnvelope,
  readRetentionDraft,
  retentionExecutionBlock,
  retentionRemaining,
  sameRetentionPolicy,
} from "@/lib/retention";
import { type RetentionFailure, RetentionRequestError, retentionApi } from "@/lib/retention-api";

type Operation = "load" | "save" | "preview" | "execute";
type Job = { id: number; generation: number; operation: Operation };
export type RetentionNotice = "saved" | "changed" | "expired" | "invalidated" | "submitted" | "acknowledged" | null;
export interface RetentionError {
  kind: RetentionFailure;
  status?: number;
}
const UNCERTAIN_KEY = "artex_retention_execution_uncertain_v1";
function persistedUncertainty(): boolean {
  try {
    return sessionStorage.getItem(UNCERTAIN_KEY) === "1";
  } catch {
    return false;
  }
}
function persistUncertainty(value: boolean) {
  // Only this boolean is persisted; never a token, policy, candidate or request body.
  // Storage may be blocked. The in-memory gate still works for the current mount.
  try {
    if (value) sessionStorage.setItem(UNCERTAIN_KEY, "1");
    else sessionStorage.removeItem(UNCERTAIN_KEY);
  } catch {
    /* no storage in private/restricted contexts */
  }
}
function publicError(error: unknown): RetentionError {
  return error instanceof RetentionRequestError ? { kind: error.kind, status: error.status } : { kind: "network" };
}

export function useRetention() {
  const [envelope, setEnvelope] = React.useState<RetentionPolicyEnvelope | null>(null);
  const [draft, setDraft] = React.useState<RetentionDraft | null>(null);
  const [lease, setLease] = React.useState<RetentionLease | null>(null);
  const [remaining, setRemaining] = React.useState(0);
  const [busy, setBusy] = React.useState<Operation | null>(null);
  const [error, setError] = React.useState<RetentionError | null>(null);
  const [notice, setNotice] = React.useState<RetentionNotice>(null);
  const [result, setResult] = React.useState<RetentionExecution | null>(null);
  const [entries, setEntries] = React.useState<RetentionAuditEntry[]>([]);
  const [auditBusy, setAuditBusy] = React.useState(false);
  const [auditError, setAuditError] = React.useState<RetentionError | null>(null);
  const [auditLoaded, setAuditLoaded] = React.useState(false);
  const [auditFresh, setAuditFresh] = React.useState(false);
  const [uncertain, setUncertain] = React.useState(false);
  const [reviewed, setReviewed] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  const [confirmation, setConfirmation] = React.useState("");
  const [acknowledgement, setAcknowledgement] = React.useState(false);
  const mounted = React.useRef(false),
    generation = React.useRef(0),
    sequence = React.useRef(0);
  const activeJob = React.useRef<Job | null>(null),
    auditSequence = React.useRef(0),
    auditing = React.useRef(false);
  const controllers = React.useRef(new Set<AbortController>());
  const previewRevision = React.useRef(0);
  const envelopeRef = React.useRef<RetentionPolicyEnvelope | null>(null),
    draftRef = React.useRef<RetentionDraft | null>(null);
  const leaseRef = React.useRef<RetentionLease | null>(null),
    token = React.useRef<string | null>(null);
  const uncertainRef = React.useRef(false),
    uncertaintyAt = React.useRef(0);

  const forgetPreview = React.useCallback((why: RetentionNotice = null) => {
    previewRevision.current++;
    token.current = null;
    leaseRef.current = null;
    setLease(null);
    setRemaining(0);
    setConfirmOpen(false);
    setConfirmation("");
    setAcknowledgement(false);
    if (why) setNotice(why);
  }, []);
  const markUncertain = React.useCallback((value: boolean) => {
    uncertainRef.current = value;
    setUncertain(value);
    persistUncertainty(value);
    setReviewed(false);
    setAuditFresh(false);
    if (value) uncertaintyAt.current = performance.now();
  }, []);
  const validJob = React.useCallback(
    (job: Job) => mounted.current && generation.current === job.generation && activeJob.current?.id === job.id,
    [],
  );
  const begin = React.useCallback((operation: Operation): Job | null => {
    // Ref gates rapid double-clicks before React has committed disabled controls.
    if (!mounted.current || activeJob.current) return null;
    const job = { operation, id: ++sequence.current, generation: generation.current };
    activeJob.current = job;
    setBusy(operation);
    setError(null);
    setNotice(null);
    return job;
  }, []);
  const finish = React.useCallback(
    (job: Job) => {
      if (validJob(job)) {
        activeJob.current = null;
        setBusy(null);
      }
    },
    [validJob],
  );
  const perform = React.useCallback(async <T>(operation: (signal: AbortSignal) => Promise<T>): Promise<T> => {
    const controller = new AbortController();
    controllers.current.add(controller);
    const timer = setTimeout(() => controller.abort(), 20000);
    try {
      return await operation(controller.signal);
    } finally {
      clearTimeout(timer);
      controllers.current.delete(controller);
    }
  }, []);

  const reloadAudit = React.useCallback(async () => {
    if (!mounted.current || auditing.current) return;
    const seq = ++auditSequence.current,
      gen = generation.current,
      started = performance.now();
    auditing.current = true;
    setAuditBusy(true);
    setAuditError(null);
    try {
      const next = await perform(retentionApi.audit);
      if (!mounted.current || gen !== generation.current || seq !== auditSequence.current) return;
      setEntries(next);
      setAuditLoaded(true);
      setAuditFresh(started >= uncertaintyAt.current);
    } catch (err) {
      if (!mounted.current || gen !== generation.current || seq !== auditSequence.current) return;
      setAuditError(publicError(err));
      setAuditFresh(false);
    } finally {
      if (mounted.current && gen === generation.current && seq === auditSequence.current) {
        auditing.current = false;
        setAuditBusy(false);
      }
    }
  }, [perform]);
  const reloadPolicy = React.useCallback(async () => {
    const job = begin("load");
    if (!job) return;
    forgetPreview();
    try {
      const next = await perform(retentionApi.policy);
      if (!validJob(job)) return;
      envelopeRef.current = next;
      draftRef.current = draftRetentionPolicy(next.policy);
      setEnvelope(next);
      setDraft(draftRef.current);
    } catch (err) {
      if (!validJob(job)) return;
      // No fallback-to-default policy after an unavailable/old backend response.
      envelopeRef.current = null;
      setEnvelope(null);
      setError(publicError(err));
    } finally {
      finish(job);
    }
  }, [begin, finish, forgetPreview, perform, validJob]);

  React.useEffect(() => {
    mounted.current = true;
    generation.current++;
    if (persistedUncertainty()) markUncertain(true);
    void reloadPolicy();
    void reloadAudit();
    const invalidate = () => {
      // A pending preview has no lease yet. Still invalidate its future response.
      previewRevision.current++;
      if (leaseRef.current) forgetPreview("invalidated");
    };
    const visibility = () => {
      if (document.hidden) invalidate();
    };
    const unload = (event: BeforeUnloadEvent) => {
      if (activeJob.current?.operation === "execute") {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("focus", invalidate);
    document.addEventListener("visibilitychange", visibility);
    window.addEventListener("beforeunload", unload);
    return () => {
      mounted.current = false;
      generation.current++;
      auditSequence.current++;
      token.current = null;
      leaseRef.current = null;
      activeJob.current = null;
      auditing.current = false;
      for (const controller of controllers.current) controller.abort();
      controllers.current.clear();
      window.removeEventListener("focus", invalidate);
      document.removeEventListener("visibilitychange", visibility);
      window.removeEventListener("beforeunload", unload);
    };
  }, [forgetPreview, markUncertain, reloadAudit, reloadPolicy]);

  React.useEffect(() => {
    if (!lease) return;
    const tick = () => {
      const left = retentionRemaining(lease, Date.now(), performance.now());
      setRemaining(left);
      if (left <= 0) forgetPreview("expired");
    };
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [forgetPreview, lease]);

  const updateDraft = (next: RetentionDraft) => {
    if (activeJob.current || !envelopeRef.current) return;
    forgetPreview(leaseRef.current ? "invalidated" : null);
    draftRef.current = next;
    setDraft(next);
    setError(null);
  };
  const save = async () => {
    const current = envelopeRef.current,
      input = draftRef.current;
    if (!current || !input) return;
    const parsed = readRetentionDraft(input);
    const policyToSave = parsed.policy;
    if (!policyToSave) return;
    const job = begin("save");
    if (!job) return;
    forgetPreview();
    try {
      const policy = await perform((signal) => retentionApi.save(policyToSave, signal));
      if (!validJob(job)) return;
      const next = { ...current, policy };
      envelopeRef.current = next;
      draftRef.current = draftRetentionPolicy(policy);
      setEnvelope(next);
      setDraft(draftRef.current);
      setNotice("saved");
    } catch (err) {
      if (!validJob(job)) return;
      // A lost PUT response can still have saved a policy. Require an explicit GET.
      envelopeRef.current = null;
      setEnvelope(null);
      setError(publicError(err));
    } finally {
      finish(job);
      if (mounted.current && generation.current === job.generation) void reloadAudit();
    }
  };
  const preview = async () => {
    const current = envelopeRef.current,
      input = draftRef.current;
    if (!current || !input || uncertainRef.current || auditing.current) return;
    const parsed = readRetentionDraft(input);
    if (!parsed.policy || !sameRetentionPolicy(parsed.policy, current.policy)) return;
    const job = begin("preview");
    if (!job) return;
    forgetPreview();
    setResult(null);
    const started = performance.now(),
      revision = previewRevision.current;
    try {
      const next = await perform(retentionApi.preview);
      if (!validJob(job)) return;
      if (document.hidden || revision !== previewRevision.current) {
        setNotice("invalidated");
        return;
      }
      if (!sameRetentionPolicy(next.policy, current.policy)) {
        const refreshed = { ...current, policy: next.policy };
        envelopeRef.current = refreshed;
        draftRef.current = draftRetentionPolicy(next.policy);
        setEnvelope(refreshed);
        setDraft(draftRef.current);
        setNotice("changed");
        return; // Never silently adopt a token for a policy the operator has not seen.
      }
      const leased = leaseRetentionPreview(next, started, Date.now(), performance.now(), current.preview_ttl_seconds);
      if (retentionRemaining(leased.lease, Date.now(), performance.now()) <= 0) {
        setNotice("expired");
        return;
      }
      token.current = leased.token;
      leaseRef.current = leased.lease;
      setLease(leased.lease);
      setRemaining(retentionRemaining(leased.lease, Date.now(), performance.now()));
    } catch (err) {
      if (validJob(job)) setError(publicError(err));
    } finally {
      finish(job);
      if (mounted.current && generation.current === job.generation) void reloadAudit();
    }
  };
  const liveBlock = () => {
    const current = envelopeRef.current,
      input = draftRef.current;
    const parsed = input ? readRetentionDraft(input).policy : null;
    return retentionExecutionBlock(
      leaseRef.current,
      current?.policy ?? null,
      !parsed || !current || !sameRetentionPolicy(parsed, current.policy),
      Date.now(),
      performance.now(),
    );
  };
  const openConfirmation = () => {
    if (activeJob.current || auditing.current || uncertainRef.current || !token.current || liveBlock()) return;
    setConfirmation("");
    setAcknowledgement(false);
    setConfirmOpen(true);
  };
  const closeConfirmation = () => {
    if (activeJob.current?.operation === "execute") return;
    setConfirmOpen(false);
    setConfirmation("");
    setAcknowledgement(false);
  };
  const execute = async () => {
    // Repeat every guard at the actual event boundary; disabled JSX is not a lock.
    if (
      !confirmOpen ||
      confirmation !== RETENTION_CONFIRMATION ||
      !acknowledgement ||
      uncertainRef.current ||
      auditing.current ||
      liveBlock()
    )
      return;
    const snapshot = leaseRef.current,
      secret = token.current;
    if (!snapshot || !secret) return;
    const job = begin("execute");
    if (!job) return;
    markUncertain(true);
    forgetPreview(); // Consume local authority BEFORE any await.
    try {
      const next = await perform((signal) => retentionApi.execute(secret, snapshot, signal));
      if (!validJob(job)) return;
      setResult(next);
      markUncertain(false);
      setNotice("submitted");
    } catch (err) {
      if (validJob(job)) setError(publicError(err));
      // Remains uncertain even for a conflict: the plan may have been used before.
      // Never retry, restore the old token, or present a missing response as rollback.
    } finally {
      finish(job);
      if (mounted.current && generation.current === job.generation) void reloadAudit();
    }
  };
  const acknowledgeUncertainty = () => {
    if (!reviewed || !auditFresh || auditBusy || activeJob.current || !uncertainRef.current) return;
    forgetPreview();
    markUncertain(false);
    setNotice("acknowledged");
  };
  const parsed = draft ? readRetentionDraft(draft) : null;
  const dirty = !!envelope && (!parsed?.policy || !sameRetentionPolicy(parsed.policy, envelope.policy));
  const block = retentionExecutionBlock(
    lease,
    envelope?.policy ?? null,
    dirty,
    Date.now(),
    typeof performance === "undefined" ? 0 : performance.now(),
  );
  return {
    envelope,
    draft,
    lease,
    remaining,
    busy,
    error,
    notice,
    result,
    entries,
    auditBusy,
    auditError,
    auditLoaded,
    auditFresh,
    uncertain,
    reviewed,
    setReviewed,
    confirmOpen,
    confirmation,
    setConfirmation,
    acknowledgement,
    setAcknowledgement,
    errors: parsed?.errors ?? {},
    dirty,
    block,
    canSave: !!envelope && !!parsed?.policy && dirty && !busy,
    canPreview: !!envelope && !dirty && !busy && !auditBusy && !uncertain,
    canExecute: !!lease && !block && !busy && !auditBusy && !uncertain,
    updateDraft,
    reloadPolicy,
    reloadAudit,
    save,
    preview,
    openConfirmation,
    closeConfirmation,
    execute,
    acknowledgeUncertainty,
  };
}
