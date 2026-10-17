"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslations } from "next-intl";

import { api } from "@/lib/api";
import type {
  REDataPage,
  REEntity,
  REEvidence,
  REMCPCandidate,
  REOverview,
  REProviderConfig,
  RESample,
} from "@/lib/types";

const panel = "rounded-xl border bg-card p-4 space-y-3 min-w-0";
const field = "rounded-md border bg-background px-3 py-2 text-sm min-w-0 disabled:opacity-50";
const button = `${field} hover:bg-muted disabled:cursor-not-allowed`;
const initialConfig: REProviderConfig = { mode: "open", reviewed_static: false, operations: {} };
const panes = ["functions", "callgraph", "xrefs", "types", "evidence"] as const;
type Pane = (typeof panes)[number];
const queries = ["status", "identity", "functions", "decompile", "xrefs", "callgraph", "types"] as const;
const emptyPage: REDataPage<REEntity | REEvidence> = { items: [], has_more: false, next_before: 0 };
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

function CallMap({
  rows,
  onAddress,
  caption,
}: {
  rows: REEntity[];
  onAddress: (address: string) => void;
  caption: string;
}) {
  const points = new Map<string, { label: string; address: string; x: number; y: number }>();
  const edges: { source: string; target: string; id: number }[] = [];
  for (const row of rows) {
    const source = String(row.payload.source ?? "");
    const target = String(row.payload.target ?? "");
    if (!source || !target || edges.length >= 40) continue;
    const a = JSON.stringify([row.module, row.address_kind, source]);
    const b = JSON.stringify([row.module, row.address_kind, target]);
    if ((!points.has(a) && points.size >= 24) || (!points.has(b) && points.size + Number(!points.has(a)) >= 24))
      continue;
    for (const [key, address] of [
      [a, source],
      [b, target],
    ]) {
      if (!points.has(key))
        points.set(key, { label: `${row.module} / ${row.address_kind}:${address}`, address, x: 0, y: 0 });
    }
    edges.push({ source: a, target: b, id: row.id });
  }
  const entries = [...points.entries()];
  for (let i = 0; i < entries.length; i++) {
    const point = entries[i][1];
    const angle = (2 * Math.PI * i) / Math.max(entries.length, 1);
    point.x = 400 + Math.cos(angle) * 300;
    point.y = 230 + Math.sin(angle) * 180;
  }
  if (!entries.length) return null;
  return (
    <figure className="rounded-lg bg-muted/30 p-2">
      <svg viewBox="0 0 800 460" className="max-h-[26rem] w-full" role="img" aria-label={caption}>
        {edges.map((edge) => {
          const a = points.get(edge.source);
          const b = points.get(edge.target);
          if (!a || !b) return null;
          return (
            <g key={edge.id}>
              <line x1={a.x} y1={a.y} x2={b.x} y2={b.y} stroke="currentColor" opacity="0.25" />
              <circle cx={a.x + (b.x - a.x) * 0.78} cy={a.y + (b.y - a.y) * 0.78} r="3" fill="currentColor">
                <title>{`${a.label} → ${b.label}`}</title>
              </circle>
            </g>
          );
        })}
        {entries.map(([key, point]) => (
          <g key={key}>
            <circle cx={point.x} cy={point.y} r="7" fill="currentColor" opacity="0.65" />
            <foreignObject x={point.x - 50} y={point.y + 10} width="100" height="40">
              <button
                type="button"
                className="w-full truncate rounded border bg-background px-1 py-1 text-xs"
                title={point.label}
                onClick={() => onAddress(point.address)}
              >
                {point.address}
              </button>
            </foreignObject>
          </g>
        ))}
      </svg>
      <figcaption className="text-muted-foreground text-xs">{caption}</figcaption>
    </figure>
  );
}

export function SoftwareRETab({ taskId }: { taskId: string }) {
  const t = useTranslations("softwareRE");
  const [overview, setOverview] = useState<REOverview | null>(null);
  const [library, setLibrary] = useState<RESample[]>([]);
  const [mcps, setMCPs] = useState<REMCPCandidate[]>([]);
  const [sample, setSample] = useState("");
  const [attachSHA, setAttachSHA] = useState("");
  const [providerID, setProviderID] = useState(0);
  const [sessionID, setSessionID] = useState(0);
  const [pane, setPane] = useState<Pane>("functions");
  const [before, setBefore] = useState(0);
  const [page, setPage] = useState(emptyPage);
  const [evidence, setEvidence] = useState<REEvidence | null>(null);
  const [observation, setObservation] = useState("");
  const [address, setAddress] = useState("");
  const [query, setQuery] = useState("");
  const [operation, setOperation] = useState("functions");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const sessionRef = useRef(0);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [editID, setEditID] = useState(0);
  const [mcpID, setMCPID] = useState(0);
  const [name, setName] = useState("");
  const [kind, setKind] = useState("rea");
  const [config, setConfig] = useState(JSON.stringify(initialConfig, null, 2));
  const [reviewed, setReviewed] = useState(false);
  const alive = useRef(true);
  const pending = useRef(false);
  const evidenceRequest = useRef(0);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      evidenceRequest.current++;
    };
  }, []);

  const refresh = useCallback(async () => {
    const value = await api.reOverview(taskId);
    if (alive.current) setOverview(value);
  }, [taskId]);
  useEffect(() => {
    let active = true;
    setOverview(null);
    setSessionID(0);
    setSample("");
    setEvidence(null);
    setPage(emptyPage);
    Promise.all([api.reOverview(taskId), api.reLibrary(), api.reMCPCandidates()])
      .then(([value, all, candidates]) => {
        if (!active) return;
        setOverview(value);
        setLibrary(all.samples);
        setMCPs(candidates.servers);
      })
      .catch((e) => {
        if (active) setError(messageOf(e));
      });
    return () => {
      active = false;
      evidenceRequest.current++;
    };
  }, [taskId]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: revision invalidates persisted result pages after explicit refresh or mutation.
  useEffect(() => {
    let active = true;
    setPage(emptyPage);
    if (sessionID)
      api
        .reData(taskId, sessionID, pane, before)
        .then((value) => {
          if (active) setPage(value);
        })
        .catch((e) => {
          if (active) setError(messageOf(e));
        });
    return () => {
      active = false;
    };
  }, [taskId, sessionID, pane, before, revision]);

  async function run(action: () => Promise<void>) {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await action();
      if (alive.current) {
        await refresh();
        setRevision((v) => v + 1);
      }
    } catch (e) {
      if (alive.current) {
        setError(messageOf(e));
        try {
          await refresh();
        } catch {
          /* Keep the original failure visible. */
        }
      }
    } finally {
      pending.current = false;
      if (alive.current) setBusy(false);
    }
  }
  async function showEvidence(id: number) {
    const token = ++evidenceRequest.current;
    setEvidence(null);
    setObservation("");
    try {
      const value = await api.reEvidence(taskId, id);
      if (alive.current && token === evidenceRequest.current) setEvidence(value);
    } catch (e) {
      if (alive.current && token === evidenceRequest.current) setError(messageOf(e));
    }
  }
  function changeSession(id: number) {
    sessionRef.current = id;
    setSessionID(id);
    setBefore(0);
    setEvidence(null);
    setObservation("");
    setConfirmed(false);
    evidenceRequest.current++;
  }
  function editProvider(id: number) {
    setEditID(id);
    const value = overview?.providers.find((p) => p.id === id);
    setMCPID(value?.mcp_id ?? 0);
    setName(value?.name ?? "");
    setKind(value?.kind ?? "rea");
    setConfig(JSON.stringify(value?.config ?? initialConfig, null, 2));
    setReviewed(value?.config.reviewed_static ?? false);
  }
  function downloadEvidence() {
    if (!evidence) return;
    const url = URL.createObjectURL(new Blob([JSON.stringify(evidence, null, 2)], { type: "application/json" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `re-evidence-${evidence.id}.json`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  const session = overview?.sessions.find((s) => s.id === sessionID);
  const selectedProvider = overview?.providers.find((p) => p.id === providerID);
  const editingProvider = overview?.providers.find((p) => p.id === editID);
  const operations = queries.filter((q) => q !== "identity" && session?.snapshot.config.operations[q]);
  const mappedOperation = operations.includes(operation as (typeof queries)[number])
    ? operation
    : (operations[0] ?? "");
  const entities = page.items.filter((row): row is REEntity => "payload" in row);
  const records = page.items.filter((row): row is REEvidence => "output_sha" in row);
  const attached = overview?.samples.find((s) => s.sha256 === sample);
  const canControl = Boolean(session && session.state !== "closed" && session.state !== "failed");

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="font-semibold">{t("title")}</h2>
          <p className="text-muted-foreground text-sm">{t("subtitle")}</p>
        </div>
        <button
          className={button}
          disabled={refreshing}
          onClick={async () => {
            setRefreshing(true);
            try {
              const [value, all, candidates] = await Promise.all([
                api.reOverview(taskId),
                api.reLibrary(),
                api.reMCPCandidates(),
              ]);
              if (alive.current) {
                setOverview(value);
                setLibrary(all.samples);
                setMCPs(candidates.servers);
                setRevision((v) => v + 1);
              }
            } catch (e) {
              if (alive.current) setError(messageOf(e));
            } finally {
              if (alive.current) setRefreshing(false);
            }
          }}
          type="button"
        >
          {refreshing ? t("loading") : t("refresh")}
        </button>
      </header>
      <p className="text-muted-foreground text-xs">{t("scope")}</p>
      {error && (
        <p
          role="alert"
          className="break-words rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-destructive text-sm"
        >
          {t("error", { message: error })}
        </p>
      )}
      {status && (
        <p role="status" className="rounded-lg border p-3 text-sm">
          {status}
        </p>
      )}
      <div className="grid gap-4 xl:grid-cols-2">
        <section className={panel}>
          <h3 className="font-medium">{t("samples")}</h3>
          <p className="text-muted-foreground text-xs">{t("sampleHint")}</p>
          <label className="flex flex-wrap items-center gap-3 text-sm">
            {t("upload")}
            <input
              type="file"
              disabled={busy}
              className="max-w-full text-xs"
              onChange={(event) => {
                const file = event.target.files?.[0];
                event.target.value = "";
                if (file)
                  void run(async () => {
                    const value = await api.reUpload(taskId, file);
                    setSample(value.sha256);
                    const all = await api.reLibrary();
                    setLibrary(all.samples);
                  });
              }}
            />
          </label>
          <select
            className={`${field} w-full`}
            aria-label={t("chooseSample")}
            value={sample}
            onChange={(event) => setSample(event.target.value)}
            disabled={busy}
          >
            <option value="">{t("chooseSample")}</option>
            {overview?.samples.map((item) => (
              <option key={item.sha256} value={item.sha256}>
                {item.name} · {item.sha256.slice(0, 12)}
              </option>
            ))}
          </select>
          {!overview?.samples.length && <p className="text-muted-foreground text-xs">{t("emptySamples")}</p>}
          {attached && (
            <details className="text-xs">
              <summary className="cursor-pointer break-all">
                {t("copyHash")}: {attached.sha256}
              </summary>
              <p>{t("fileSize", { size: attached.size })}</p>
              <pre className="max-h-52 overflow-auto whitespace-pre-wrap break-all">
                {JSON.stringify(attached.metadata, null, 2)}
              </pre>
            </details>
          )}
          <div className="flex gap-2">
            <select
              className={`${field} flex-1`}
              value={attachSHA}
              onChange={(event) => setAttachSHA(event.target.value)}
              aria-label={t("attach")}
              disabled={busy}
            >
              <option value="">{t("attach")}</option>
              {library
                .filter((item) => !overview?.samples.some((v) => v.sha256 === item.sha256))
                .map((item) => (
                  <option key={item.sha256} value={item.sha256}>
                    {item.name} · {item.sha256.slice(0, 12)}
                  </option>
                ))}
            </select>
            <button
              type="button"
              className={button}
              disabled={busy || !attachSHA}
              onClick={() =>
                run(async () => {
                  await api.reAttach(taskId, attachSHA);
                  setSample(attachSHA);
                  setAttachSHA("");
                })
              }
            >
              {t("attach")}
            </button>
          </div>
        </section>
        <section className={panel}>
          <h3 className="font-medium">{t("sessions")}</h3>
          <p className="text-muted-foreground text-xs">{t("sessionHint")}</p>
          <select
            className={`${field} w-full`}
            value={providerID}
            onChange={(event) => setProviderID(Number(event.target.value))}
            aria-label={t("chooseProvider")}
            disabled={busy}
          >
            <option value={0}>{t("chooseProvider")}</option>
            {overview?.providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} · {p.kind} · {p.discovery}
              </option>
            ))}
          </select>
          {!overview?.providers.length && <p className="text-muted-foreground text-xs">{t("noProvider")}</p>}
          <button
            className={button}
            type="button"
            disabled={
              busy ||
              !sample ||
              !selectedProvider?.config.reviewed_static ||
              selectedProvider.discovery !== "catalog_verified"
            }
            onClick={() =>
              run(async () => {
                const value = await api.reOpen(taskId, sample, providerID);
                changeSession(value.id);
              })
            }
          >
            {t("open")}
          </button>
          <select
            className={`${field} w-full`}
            value={sessionID}
            onChange={(event) => changeSession(Number(event.target.value))}
            aria-label={t("chooseSession")}
          >
            <option value={0}>{t("chooseSession")}</option>
            {overview?.sessions.map((s) => (
              <option key={s.id} value={s.id}>
                #{s.id} · {s.sample_sha.slice(0, 12)} · {s.snapshot.name} · {s.state}
              </option>
            ))}
          </select>
          {!overview?.sessions.length && <p className="text-muted-foreground text-xs">{t("noSession")}</p>}
          {session && (
            <p className="break-words text-xs">
              {session.state} · {session.message}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              className={button}
              disabled={busy || session?.state !== "ready"}
              onClick={() =>
                run(async () => {
                  await api.reControl(taskId, sessionID, "close");
                })
              }
            >
              {t("close")}
            </button>
            <button
              type="button"
              className={button}
              disabled={!canControl}
              onClick={async () => {
                try {
                  await api.reControl(taskId, sessionID, "cancel");
                  await refresh();
                } catch (e) {
                  setError(messageOf(e));
                }
              }}
            >
              {t("cancel")}
            </button>
          </div>
          <p className="text-muted-foreground text-xs">{t("cancelHint")}</p>
          <label className="flex items-start gap-2 text-xs">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(e) => setConfirmed(e.target.checked)}
              disabled={busy}
            />
            {t("releaseConfirm")}
          </label>
          <button
            type="button"
            className={button}
            disabled={busy || !canControl || !confirmed}
            onClick={() =>
              run(async () => {
                await api.reControl(taskId, sessionID, "release", true);
                setConfirmed(false);
              })
            }
          >
            {t("release")}
          </button>
        </section>
      </div>
      <section className={panel}>
        <div className="flex flex-wrap items-center gap-2">
          <strong className="text-sm">{t("staticOnly")}</strong>
          <select
            className={field}
            value={mappedOperation}
            onChange={(event) => setOperation(event.target.value)}
            disabled={busy || !operations.length}
            aria-label={t("operation")}
          >
            <option value="" disabled>
              {t("operation")}
            </option>
            {operations.map((value) => (
              <option key={value} value={value}>
                {value === "status" ? t("queryStatus") : t(value)}
              </option>
            ))}
          </select>
          <input
            className={`${field} flex-1`}
            placeholder={t("address")}
            aria-label={t("address")}
            value={address}
            maxLength={256}
            onChange={(event) => setAddress(event.target.value)}
            disabled={busy}
          />
          <input
            className={`${field} flex-1`}
            placeholder={t("query")}
            aria-label={t("query")}
            value={query}
            maxLength={1000}
            onChange={(event) => setQuery(event.target.value)}
            disabled={busy}
          />
          <button
            type="button"
            className={button}
            disabled={busy || session?.state !== "ready" || !mappedOperation}
            onClick={() =>
              run(async () => {
                const result = await api.reQuery(taskId, sessionID, {
                  operation: mappedOperation,
                  address,
                  query,
                  limit: 100,
                });
                setStatus(
                  result.is_error
                    ? t("queryError", { id: result.evidence_id })
                    : t("queried", { id: result.evidence_id, count: result.normalized_count, state: result.state }),
                );
                setBefore(0);
                if (sessionRef.current === sessionID) await showEvidence(result.evidence_id);
              })
            }
          >
            {t("run")}
          </button>
        </div>
        {!sessionID ? (
          <p className="text-muted-foreground text-sm">{t("waiting")}</p>
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              {panes.map((value) => (
                <button
                  type="button"
                  key={value}
                  className={`${button} ${pane === value ? "bg-muted font-medium" : ""}`}
                  aria-pressed={pane === value}
                  onClick={() => {
                    setPane(value);
                    setBefore(0);
                  }}
                >
                  {t(value)}
                </button>
              ))}
            </div>
            {(pane === "callgraph" || pane === "xrefs") && (
              <CallMap rows={entities} caption={t("partialGraph")} onAddress={setAddress} />
            )}
            <div className="overflow-auto">
              <table className="w-full text-left text-xs">
                <thead className="text-muted-foreground">
                  <tr>
                    <th className="p-2">{t("name")}</th>
                    <th className="p-2">{t("location")}</th>
                    <th className="p-2">{t("sourceEvidence")}</th>
                  </tr>
                </thead>
                <tbody>
                  {entities.map((row) => (
                    <tr key={row.id} className="border-t">
                      <td className="max-w-64 break-words p-2">
                        {row.name ||
                          `${String(row.payload.source ?? row.address)}${row.payload.target ? ` → ${String(row.payload.target)}` : ""}`}
                      </td>
                      <td className="max-w-72 break-all p-2">
                        <button
                          type="button"
                          className="text-left hover:underline"
                          onClick={() => setAddress(row.address || String(row.payload.source ?? ""))}
                        >
                          {row.module} / {row.address_kind} {row.address}
                        </button>
                      </td>
                      <td className="p-2">
                        <button type="button" className="underline" onClick={() => showEvidence(row.evidence_id)}>
                          #{row.evidence_id}
                        </button>
                      </td>
                    </tr>
                  ))}
                  {records.map((row) => (
                    <tr key={row.id} className="border-t">
                      <td className="p-2">
                        {row.operation} / {row.tool}
                      </td>
                      <td className="max-w-64 break-all p-2">
                        {row.created_at}
                        <br />
                        {row.output_sha}
                        {row.is_error ? " · error" : ""}
                      </td>
                      <td className="p-2">
                        <button type="button" className="underline" onClick={() => showEvidence(row.id)}>
                          #{row.id}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!page.items.length && <p className="text-muted-foreground text-xs">{t("emptyRows")}</p>}
            <div className="flex gap-2">
              <button type="button" className={button} disabled={!before} onClick={() => setBefore(0)}>
                {t("first")}
              </button>
              <button
                type="button"
                className={button}
                disabled={!page.has_more}
                onClick={() => setBefore(page.next_before)}
              >
                {t("more")}
              </button>
            </div>
          </>
        )}
      </section>
      <section className={panel}>
        <h3 className="font-medium">
          {t("raw")}
          {evidence && ` #${evidence.id} · ${evidence.operation}`}
        </h3>
        {!evidence ? (
          <p className="text-muted-foreground text-sm">{t("noEvidence")}</p>
        ) : (
          <>
            <p className="break-all text-xs">
              sample SHA-256: {evidence.sample_sha} · session #{evidence.session_id} · {evidence.tool} ·{" "}
              {evidence.created_at}
            </p>
            <p className="break-all text-xs">
              {t("originalHash")}: {evidence.output_sha}
              <br />
              {t("retainedHash")}: {evidence.retained_sha}
            </p>
            {evidence.diagnostic && <p className="break-words text-destructive text-sm">{evidence.diagnostic}</p>}
            {evidence.truncated && <p className="text-destructive text-sm">{t("outputTruncated")}</p>}
            <details>
              <summary className="cursor-pointer text-xs">
                {t("input")} · is_error={String(evidence.is_error)}
              </summary>
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs">
                {JSON.stringify(evidence.input, null, 2)}
              </pre>
            </details>
            <pre className="max-h-[26rem] overflow-auto whitespace-pre-wrap break-all rounded-lg bg-muted/40 p-3 text-xs">
              {evidence.output?.slice(0, 20000)}
            </pre>
            {(evidence.output?.length ?? 0) > 20000 && (
              <p className="text-muted-foreground text-xs">{t("displayTruncated")}</p>
            )}
            <button type="button" className={button} onClick={downloadEvidence}>
              {t("download")}
            </button>
            <label className="block text-sm">
              {t("interpretation")}
              <textarea
                className={`${field} mt-2 min-h-24 w-full`}
                value={observation}
                maxLength={900}
                onChange={(event) => setObservation(event.target.value)}
              />
            </label>
            <p className="text-muted-foreground text-xs">{t("observationHint")}</p>
            <button
              type="button"
              className={button}
              disabled={busy || !observation.trim()}
              onClick={() =>
                run(async () => {
                  await api.reObservation(taskId, evidence.id, observation);
                  setObservation("");
                  setStatus(t("saved"));
                })
              }
            >
              {t("saveObservation")}
            </button>
          </>
        )}
      </section>
      <details className={panel}>
        <summary className="cursor-pointer font-medium">{t("providerSettings")}</summary>
        <p className="text-muted-foreground text-xs">{t("providerNotice")}</p>
        <select
          className={`${field} w-full`}
          value={editID}
          onChange={(event) => editProvider(Number(event.target.value))}
          aria-label={t("editProvider")}
          disabled={busy}
        >
          <option value={0}>{t("newProvider")}</option>
          {overview?.providers.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name} · {p.kind}
            </option>
          ))}
        </select>
        <div className="grid gap-2 md:grid-cols-3">
          <select
            className={field}
            value={mcpID}
            onChange={(event) => setMCPID(Number(event.target.value))}
            disabled={busy || Boolean(editID)}
            aria-label={t("mcp")}
          >
            <option value={0}>{t("mcp")}</option>
            {mcps.map((m) => (
              <option key={m.id} value={m.id} disabled={!m.enabled}>
                {m.name} · {m.transport}
                {!m.enabled ? " · disabled" : ""}
              </option>
            ))}
          </select>
          <input
            className={field}
            placeholder={t("providerName")}
            aria-label={t("providerName")}
            value={name}
            maxLength={100}
            onChange={(event) => setName(event.target.value)}
            disabled={busy}
          />
          <select
            className={field}
            value={kind}
            aria-label={t("providerKind")}
            onChange={(event) => setKind(event.target.value)}
            disabled={busy}
          >
            <option value="rea">REA</option>
            <option value="ghidra">Ghidra MCP</option>
            <option value="ida">IDA MCP</option>
          </select>
        </div>
        <label className="block text-sm">
          {t("config")}
          <textarea
            className={`${field} mt-2 min-h-64 w-full font-mono`}
            value={config}
            maxLength={50000}
            onChange={(event) => {
              setConfig(event.target.value);
              setReviewed(false);
            }}
            disabled={busy}
            spellCheck={false}
          />
        </label>
        <p className="text-muted-foreground text-xs">{t("configHint")}</p>
        <label className="flex items-start gap-2 text-xs">
          <input
            type="checkbox"
            checked={reviewed}
            onChange={(event) => setReviewed(event.target.checked)}
            disabled={busy}
          />
          {t("reviewed")}
        </label>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            className={button}
            disabled={busy || !mcpID || !name.trim()}
            onClick={() =>
              run(async () => {
                const parsed = JSON.parse(config) as REProviderConfig;
                if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(t("config"));
                const result = await api.reSaveProvider({
                  id: editID,
                  mcp_id: mcpID,
                  name,
                  kind,
                  config: { ...parsed, reviewed_static: reviewed },
                });
                setEditID(result.id);
                setStatus(t("saved"));
              })
            }
          >
            {t("saveProvider")}
          </button>
          <button
            type="button"
            className={button}
            disabled={busy || !editID}
            onClick={() =>
              run(async () => {
                await api.reDiscover(editID);
              })
            }
          >
            {t("discover")}
          </button>
        </div>
        {editingProvider && (
          <p className="break-words text-xs">
            {t("providerState")}: {editingProvider.discovery} · {editingProvider.message}
          </p>
        )}
        <details>
          <summary className="cursor-pointer text-sm">{t("catalog")}</summary>
          {editingProvider?.catalog.length ? (
            <pre className="max-h-[32rem] overflow-auto whitespace-pre-wrap break-all bg-muted/30 p-3 text-xs">
              {JSON.stringify(editingProvider.catalog, null, 2)}
            </pre>
          ) : (
            <p className="text-muted-foreground text-xs">{t("catalogUnknown")}</p>
          )}
        </details>
      </details>
    </div>
  );
}
