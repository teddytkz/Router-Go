// Connections card of a built-in provider's detail page: batch test, round-robin/sticky, selection, priority, per-key actions.
import { ArrowDown, ArrowUp, KeyRound, LogIn, Pencil, Plus, Trash2, Zap } from "lucide-react";
import { useEffect, useState } from "react";
import type { BuiltinProvider, Connection } from "../../lib/api";
import { useBuiltinConfig, useCreateConnection, useDeleteConnection, usePatchConnection, useTestConnection } from "../../lib/hooks";
import { Badge, Button, Card, ConfirmModal, EmptyState, errText, Switch, cn, useToast } from "../ui";
import { CooldownTimer, EditKeyModal, STATE_BADGE, dotCls } from "./ConnectionsCard";
import { ApplyProxy } from "./ApplyProxy";
import { OAuthModal } from "./OAuthModal";
import { connState, ProxyBadge } from "./shared";

type Run = { state: "queued" | "testing" | "success" | "failed"; error?: string };
const iconBtn = "flex flex-col items-center gap-0.5 rounded-md px-1.5 py-1 text-[10px] leading-none text-muted hover:bg-surface-2 hover:text-text cursor-pointer";

function Row({ p, c, first, last, busy, run, checked, onCheck, onUp, onDown }: { p: BuiltinProvider; c: Connection; first: boolean; last: boolean; busy: boolean; run?: Run; checked: boolean; onCheck: (v: boolean) => void; onUp: () => void; onDown: () => void }) {
  const toast = useToast(), patch = usePatchConnection(), del = useDeleteConnection();
  const [edit, setEdit] = useState(false), [confirm, setConfirm] = useState(false);
  const st = connState(c), b = STATE_BADGE[st];
  return (
    <li className={cn("flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:justify-between", !c.isActive && "opacity-60")}>
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <input type="checkbox" checked={checked} onChange={(e) => onCheck(e.target.checked)} aria-label={`Select ${c.name}`} className="size-4 shrink-0 accent-[var(--accent)] cursor-pointer" />
        <div className="flex shrink-0 flex-col">
          <button type="button" aria-label={`Move ${c.name} up`} disabled={first || busy} onClick={onUp} className="rounded p-0.5 text-muted hover:text-accent disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"><ArrowUp className="size-4" /></button>
          <button type="button" aria-label={`Move ${c.name} down`} disabled={last || busy} onClick={onDown} className="rounded p-0.5 text-muted hover:text-accent disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"><ArrowDown className="size-4" /></button>
        </div>
        <KeyRound className="size-4 shrink-0 text-muted" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="font-medium truncate" title={c.name}>{c.name}</p>
          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1.5">
            <Badge tone={b.tone}><i aria-hidden className={`size-1.5 rounded-full ${dotCls[b.tone]}`} />{b.label}</Badge>
            <Badge>{c.authType === "oauth" ? "OAuth" : p.noAuth ? "Free" : "API Key"}</Badge>
            <ProxyBadge c={c} />
            <span className="text-xs text-muted tabular-nums" title="Priority">#{c.priority}</span>
            {st === "cooling" && c.rateLimitedUntil && <CooldownTimer until={c.rateLimitedUntil} />}
            {run && <Badge tone={run.state === "success" ? "ok" : run.state === "failed" ? "err" : run.state === "testing" ? "accent" : "neutral"}><span title={run.error}>{run.state}</span></Badge>}
          </div>
          {(c.apiKeyMasked || c.email || c.displayName) && <p className="mt-1 font-mono text-xs text-muted truncate" title="Key / account">{c.apiKeyMasked || c.email || c.displayName}</p>}
          {c.isActive && c.lastError && <p className="mt-1 max-w-full truncate text-xs text-err" title={c.lastError}>{c.lastError}</p>}
          {run?.state === "failed" && run.error && <p className="mt-1 max-w-full truncate text-xs text-err" title={run.error}>{run.error}</p>}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1 self-end sm:self-auto">
        <button type="button" className={iconBtn} onClick={() => setEdit(true)} aria-label={`Edit ${c.name}`}><Pencil className="size-4" />Edit</button>
        <button type="button" className={cn(iconBtn, "hover:text-err")} onClick={() => setConfirm(true)} aria-label={`Delete ${c.name}`}><Trash2 className="size-4" />Delete</button>
        <Switch checked={c.isActive} label={`${c.name} enabled`} disabled={patch.isPending} onChange={(v) => patch.mutate({ id: c.id, isActive: v }, { onError: (e) => toast("err", errText(e)) })} />
      </div>
      {edit && <EditKeyModal c={c} onClose={() => setEdit(false)} />}
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete API key" body={`Remove "${c.name}"? Requests will stop using this key.`}
        onConfirm={() => del.mutate(c.id, { onSuccess: () => { setConfirm(false); toast("ok", "Key deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </li>
  );
}

export function BuiltinConnections({ p, conns, onAdd }: { p: BuiltinProvider; conns: Connection[]; onAdd: () => void }) {
  const toast = useToast(), test = useTestConnection(), patch = usePatchConnection(), del = useDeleteConnection(), cfg = useBuiltinConfig(), create = useCreateConnection();
  const [runs, setRuns] = useState<Record<string, Run>>({}), [batch, setBatch] = useState<string[]>([]), [running, setRunning] = useState(false), [moving, setMoving] = useState(false);
  const [oauth, setOauth] = useState(false), [sel, setSel] = useState<Set<string>>(new Set()), [bulkDel, setBulkDel] = useState(false), [stickyText, setStickyText] = useState(String(p.config.sticky));
  useEffect(() => setStickyText(String(p.config.sticky)), [p.config.sticky]);
  const sorted = [...conns].sort((a, b) => a.priority - b.priority), selected = sorted.filter((c) => sel.has(c.id)), unsupported = p.status === "unsupported";

  const run = async (list: Connection[]) => {
    setRunning(true); setBatch(list.map((c) => c.id));
    setRuns(Object.fromEntries(list.map((c) => [c.id, { state: "queued" }])));
    for (const c of list) { // one at a time: upstreams rate-limit parallel validation
      setRuns((s) => ({ ...s, [c.id]: { state: "testing" } }));
      try { const r = await test.mutateAsync({ id: c.id }); setRuns((s) => ({ ...s, [c.id]: r.valid ? { state: "success" } : { state: "failed", error: r.error } })); }
      catch (e) { setRuns((s) => ({ ...s, [c.id]: { state: "failed", error: errText(e) } })); }
    }
    setRunning(false);
  };
  const done = batch.map((id) => runs[id]).filter((r) => r && (r.state === "success" || r.state === "failed"));
  const passed = done.filter((r) => r.state === "success").length;

  const move = async (i: number, d: -1 | 1) => { // swap with neighbour; priorities are 1..N
    const a = sorted[i], b = sorted[i + d];
    setMoving(true);
    try { await Promise.all([patch.mutateAsync({ id: a.id, priority: i + d + 1 }), patch.mutateAsync({ id: b.id, priority: i + 1 })]); }
    catch (e) { toast("err", errText(e)); } finally { setMoving(false); }
  };
  const saveCfg = (roundRobin: boolean, sticky: number) => cfg.mutate({ providerId: p.id, roundRobin, sticky }, { onError: (e) => { setStickyText(String(p.config.sticky)); toast("err", errText(e)); } });
  const commitSticky = () => {
    const n = Math.min(1000, Math.max(1, Math.round(Number(stickyText) || p.config.sticky || 1)));
    setStickyText(String(n));
    if (n !== p.config.sticky) saveCfg(p.config.roundRobin, n);
  };
  const deleteSelected = async () => {
    try { for (const c of selected) await del.mutateAsync(c.id); toast("ok", `Deleted ${selected.length} key${selected.length === 1 ? "" : "s"}`); setSel(new Set()); }
    catch (e) { toast("err", errText(e)); } finally { setBulkDel(false); }
  };
  const enable = () => create.mutate({ provider: p.id, name: "Default", apiKey: "" }, { onSuccess: () => toast("ok", `${p.name} enabled`), onError: (e) => toast("err", errText(e)) });
  const allOn = sorted.length > 0 && selected.length === sorted.length;

  return (
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 border-b border-border">
        <h2 className="font-semibold">Connections</h2>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          {!p.local && ( // a provider driven through a local program never goes through a proxy
            <ApplyProxy conns={sorted} prepare={p.noAuth ? async () => [(await create.mutateAsync({ provider: p.id, name: "Default", apiKey: "" })).connection] : undefined} />
          )}
          {sorted.length > 0 && <Button size="sm" disabled={running} loading={running} onClick={() => run(sorted)}>{!running && <Zap className="size-4" />}Test Connection One-by-One</Button>}
          <div className="flex items-center gap-2 text-[13px]">
            <span>Round Robin</span>
            <Switch checked={p.config.roundRobin} label="Round Robin" disabled={cfg.isPending} onChange={(v) => saveCfg(v, p.config.sticky)} />
            <label htmlFor="sticky" className="text-muted">Sticky:</label>
            <input id="sticky" type="number" min={1} max={1000} value={stickyText} onChange={(e) => setStickyText(e.target.value)} onBlur={commitSticky} onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
              className="h-8 w-16 rounded-lg border border-border bg-surface px-2 text-[13px] tabular-nums focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/25" />
          </div>
        </div>
      </div>
      {batch.length > 0 && (
        <p role="status" className="flex flex-wrap gap-x-4 gap-y-1 px-4 py-2 border-b border-border text-[13px]">
          <span>Total <b className="tabular-nums">{batch.length}</b></span><span>Completed <b className="tabular-nums">{done.length}</b></span>
          <span className="text-ok">Passed <b className="tabular-nums">{passed}</b></span><span className={done.length - passed ? "text-err" : "text-muted"}>Failed <b className="tabular-nums">{done.length - passed}</b></span>
        </p>
      )}
      {sorted.length > 0 && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2 border-b border-border text-[13px]">
          <label className="flex items-center gap-2 cursor-pointer">
            <input type="checkbox" checked={allOn} ref={(el) => { if (el) el.indeterminate = selected.length > 0 && !allOn; }} onChange={(e) => setSel(e.target.checked ? new Set(sorted.map((c) => c.id)) : new Set())} className="size-4 accent-[var(--accent)]" />Select All
          </label>
          {selected.length > 0 && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted">
              <span className="font-medium text-text">{selected.length} selected</span>·
              <button type="button" className="hover:text-text cursor-pointer rounded disabled:opacity-50" disabled={running} onClick={() => run(selected)}>Test selected</button>·
              <button type="button" className="text-err hover:underline cursor-pointer rounded" onClick={() => setBulkDel(true)}>Delete selected</button>·
              <button type="button" className="hover:text-text cursor-pointer rounded" onClick={() => setSel(new Set())}>Clear</button>
            </div>
          )}
        </div>
      )}
      {sorted.length ? (
        <ul className="divide-y divide-border">
          {sorted.map((c, i) => <Row key={c.id} p={p} c={c} first={i === 0} last={i === sorted.length - 1} busy={moving} run={runs[c.id]} checked={sel.has(c.id)}
            onCheck={(v) => setSel((s) => { const n = new Set(s); if (v) n.add(c.id); else n.delete(c.id); return n; })} onUp={() => move(i, -1)} onDown={() => move(i, 1)} />)}
        </ul>
      ) : <EmptyState icon={<KeyRound className="size-6" />} title="No connections yet"
        body={unsupported ? p.note || "This provider cannot be used yet." : p.noAuth ? "No key is needed: it is set up automatically on the first request. Enable it now to see it here." : "Add an API key so requests can be routed to this provider."} />}
      {!unsupported && (p.oauth || !p.noAuth || sorted.length === 0) && (
        <div className="flex flex-wrap gap-2 p-3 border-t border-border">
          {p.oauth && <Button className="flex-1 basis-40" onClick={() => setOauth(true)}><LogIn className="size-4" />OAuth</Button>}
          {p.noAuth ? (
            sorted.length === 0 && <Button variant="primary" className="flex-1 basis-40" loading={create.isPending} onClick={enable}>Enable</Button>
          ) : (
            <Button variant="primary" className="flex-1 basis-40" onClick={onAdd}><Plus className="size-4" />API Key</Button>
          )}
        </div>
      )}
      {oauth && <OAuthModal p={p} onClose={() => setOauth(false)} />}
      <ConfirmModal open={bulkDel} onClose={() => setBulkDel(false)} loading={del.isPending} title="Delete API keys" body={`Remove ${selected.length} selected key${selected.length === 1 ? "" : "s"}? Requests will stop using them.`} onConfirm={deleteSelected} />
    </Card>
  );
}
