import { ArrowDown, ArrowUp, KeyRound, Pencil, Plus, Trash2, Zap } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import type { Connection, ProviderNode, TestBatch } from "../../lib/api";
import { useDeleteConnection, usePatchConnection, useProxyPools, useTestBatch, useTestConnection, useValidateNode } from "../../lib/hooks";
import { Badge, Button, Card, ConfirmModal, EmptyState, errText, Field, Input, Modal, Select, Switch, useToast } from "../ui";
import { connState, ProxyBadge, type ConnState } from "./shared";
import { AddKeyDialog } from "./AddKeyDialog";
import { ApplyProxy } from "./ApplyProxy";
import { TestResultsModal } from "./TestResultsModal";

type Run = { state: "queued" | "testing" | "success" | "failed"; error?: string };

export function CooldownTimer({ until }: { until: string }) {
  const [left, setLeft] = useState(() => new Date(until).getTime() - Date.now());
  useEffect(() => {
    const tick = () => setLeft(new Date(until).getTime() - Date.now());
    tick(); const t = setInterval(tick, 1000); return () => clearInterval(t);
  }, [until]);
  if (left <= 0) return null;
  const s = Math.floor(left / 1000), p = (n: number) => String(n).padStart(2, "0");
  return <span className="text-xs text-warn font-mono tabular-nums" title="Cooldown remaining">⏱ {s >= 3600 ? `${Math.floor(s / 3600)}h ${p(Math.floor((s % 3600) / 60))}m` : `${p(Math.floor(s / 60))}:${p(s % 60)}`}</span>;
}

// ---------- add / edit ----------
export function AddKeyModal({ node, count, onClose }: { node: ProviderNode; count: number; onClose: () => void }) {
  const validate = useValidateNode();
  return <AddKeyDialog title={`Add API key to ${node.name}`} provider={node.id} count={count} keyOptional hint="Leave empty for upstreams without auth (e.g. local Ollama)" onClose={onClose}
    check={(apiKey) => validate.mutateAsync({ type: node.type, baseUrl: node.baseUrl, ...(node.type === "anthropic-compatible" ? {} : { apiType: node.apiType }), apiKey })} />;
}

export function EditKeyModal({ c, onClose }: { c: Connection; onClose: () => void }) {
  const toast = useToast(), patch = usePatchConnection();
  const pools = useProxyPools();
  const [name, setName] = useState(c.name), [key, setKey] = useState(""), [pool, setPool] = useState(c.providerSpecificData?.proxyPoolId ?? "");
  const was = c.providerSpecificData?.proxyPoolId ?? "";
  return (
    <Modal open onClose={onClose} title="Edit API key"
      footer={<><Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" loading={patch.isPending} disabled={!name.trim()}
          onClick={() => patch.mutate({ id: c.id, name: name.trim(), ...(key.trim() ? { apiKey: key.trim() } : {}), ...(pool !== was ? { proxyPoolId: pool } : {}) }, { onSuccess: () => { toast("ok", "Key updated"); onClose(); }, onError: (e) => toast("err", errText(e)) })}>Save</Button></>}>
      <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      <Field label="API key" hint={`Current: ${c.apiKeyMasked || "none"}. Leave empty to keep it.`}>{(id) => <Input id={id} type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder="Paste a new key to replace" />}</Field>
      <Field label="Proxy pool" hint="Send this key's upstream traffic through a proxy or relay. Manage pools under Proxy Pools.">{(id) => (
        <Select id={id} value={pool} onChange={(e) => setPool(e.target.value)}>
          <option value="">Direct (no proxy)</option>
          {(pools.data ?? []).map((p) => <option key={p.id} value={p.id}>{p.name} · {p.type}{p.isActive ? "" : " (disabled)"}</option>)}
        </Select>)}</Field>
    </Modal>
  );
}

// ---------- row ----------
export const STATE_BADGE: Record<ConnState, { tone: "ok" | "err" | "warn" | "neutral"; label: string }> = {
  active: { tone: "ok", label: "active" }, error: { tone: "err", label: "error" }, cooling: { tone: "warn", label: "cooling" }, disabled: { tone: "neutral", label: "disabled" }, unknown: { tone: "neutral", label: "untested" },
};
export const dotCls = { ok: "bg-ok", err: "bg-err", warn: "bg-warn", neutral: "bg-muted" };

function ConnectionRow({ c, first, last, busy, run, onUp, onDown, onTest }: { c: Connection; first: boolean; last: boolean; busy: boolean; run?: Run; onUp: () => void; onDown: () => void; onTest: () => void }) {
  const toast = useToast(), patch = usePatchConnection(), del = useDeleteConnection();
  const [edit, setEdit] = useState(false), [confirm, setConfirm] = useState(false);
  const st = connState(c), b = STATE_BADGE[st], testing = run?.state === "testing";
  return (
    <li className={`flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between ${c.isActive ? "" : "opacity-60"}`}>
      <div className="flex min-w-0 flex-1 items-start gap-3 sm:items-center">
        <div className="flex shrink-0 flex-col">
          <button type="button" aria-label={`Move ${c.name} up`} disabled={first || busy} onClick={onUp} className="rounded p-0.5 text-muted hover:text-accent disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"><ArrowUp className="size-4" /></button>
          <button type="button" aria-label={`Move ${c.name} down`} disabled={last || busy} onClick={onDown} className="rounded p-0.5 text-muted hover:text-accent disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"><ArrowDown className="size-4" /></button>
        </div>
        <KeyRound className="size-4 shrink-0 text-muted mt-1 sm:mt-0" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="font-medium truncate">{c.name}</p>
          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1.5">
            <Badge tone={b.tone}><i aria-hidden className={`size-1.5 rounded-full ${dotCls[b.tone]}`} />{b.label}</Badge>
            <Badge>API Key</Badge>
            <ProxyBadge c={c} />
            {st === "cooling" && c.rateLimitedUntil && <CooldownTimer until={c.rateLimitedUntil} />}
            {c.isActive && c.lastError && <span className="max-w-full truncate text-xs text-err sm:max-w-[300px]" title={c.lastError}>{c.lastError}</span>}
            <span className="text-xs text-muted tabular-nums" title="Priority">#{c.priority}</span>
            {run && run.state !== "testing" && <Badge tone={run.state === "success" ? "ok" : run.state === "failed" ? "err" : "neutral"}><span title={run.error}>{run.state === "failed" && run.error ? `failed: ${run.error.slice(0, 40)}` : run.state}</span></Badge>}
            {testing && <Badge tone="accent">testing</Badge>}
          </div>
          <p className="mt-1 font-mono text-xs text-muted truncate">{c.apiKeyMasked || "no key"}</p>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1 self-end sm:self-auto">
        <Button size="sm" variant="ghost" loading={testing} onClick={onTest} aria-label={`Test ${c.name}`}>{!testing && <Zap className="size-4" />}Test</Button>
        <Button size="sm" variant="ghost" onClick={() => setEdit(true)} aria-label={`Edit ${c.name}`}><Pencil className="size-4" />Edit</Button>
        <Button size="sm" variant="ghost" className="text-err hover:text-err" onClick={() => setConfirm(true)} aria-label={`Delete ${c.name}`}><Trash2 className="size-4" />Delete</Button>
        <Switch checked={c.isActive} label={`${c.name} enabled`} disabled={patch.isPending} onChange={(v) => patch.mutate({ id: c.id, isActive: v }, { onError: (e) => toast("err", errText(e)) })} />
      </div>
      {edit && <EditKeyModal c={c} onClose={() => setEdit(false)} />}
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete API key" body={`Remove "${c.name}"? Requests will stop using this key.`}
        onConfirm={() => del.mutate(c.id, { onSuccess: () => { setConfirm(false); toast("ok", "Key deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </li>
  );
}

// ---------- card ----------
export function ConnectionsCard({ providerId, conns, onAdd, emptyAction, emptyBody }: { providerId: string; conns: Connection[]; onAdd: () => void; emptyAction?: ReactNode; emptyBody?: string }) {
  const toast = useToast(), test = useTestConnection(), batch = useTestBatch(), patch = usePatchConnection();
  const [runs, setRuns] = useState<Record<string, Run>>({}), [moving, setMoving] = useState(false), [log, setLog] = useState<TestBatch | null>(null);
  const sorted = [...conns].sort((a, b) => a.priority - b.priority);

  const single = async (c: Connection) => {
    setRuns((s) => ({ ...s, [c.id]: { state: "testing" } }));
    try {
      const r = await test.mutateAsync({ id: c.id });
      setRuns((s) => ({ ...s, [c.id]: r.valid ? { state: "success" } : { state: "failed", error: r.error } }));
      toast(r.valid ? "ok" : "err", r.valid ? `${c.name}: connection OK` : `${c.name}: ${r.error ?? "test failed"}`);
    } catch (e) { setRuns((s) => ({ ...s, [c.id]: { state: "failed", error: errText(e) } })); toast("err", errText(e)); }
  };
  const testAll = () => batch.mutate([providerId], {
    onSuccess: (r) => {
      setRuns(Object.fromEntries(r.results.map((x) => [x.connectionId, x.ok ? { state: "success" } : { state: "failed", error: x.error }])) as Record<string, Run>);
      setLog(r);
    },
    onError: (e) => toast("err", errText(e)),
  });
  const move = async (i: number, d: -1 | 1) => { // swap with neighbour; priorities are 1..N
    const a = sorted[i], b = sorted[i + d];
    setMoving(true);
    try { await Promise.all([patch.mutateAsync({ id: a.id, priority: i + d + 1 }), patch.mutateAsync({ id: b.id, priority: i + 1 })]); }
    catch (e) { toast("err", errText(e)); } finally { setMoving(false); }
  };
  const addBtn = <Button variant="primary" size="sm" onClick={onAdd}><Plus className="size-4" />Add API Key</Button>;

  return (
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 border-b border-border">
        <h2 className="font-semibold">Connections</h2>
        <div className="flex flex-wrap items-center gap-2">
          <ApplyProxy conns={sorted} />
          {sorted.length > 0 && <Button size="sm" loading={batch.isPending} onClick={testAll}>{!batch.isPending && <Zap className="size-4" />}Test all</Button>}
        </div>
      </div>
      {sorted.length ? (
        <ul className="divide-y divide-border">
          {sorted.map((c, i) => <ConnectionRow key={c.id} c={c} first={i === 0} last={i === sorted.length - 1} busy={moving} run={runs[c.id]} onUp={() => move(i, -1)} onDown={() => move(i, 1)} onTest={() => single(c)} />)}
        </ul>
      ) : <EmptyState icon={<KeyRound className="size-6" />} title="No connections yet" body={emptyBody ?? "Add an API key so requests can be routed to this provider."} action={emptyAction === undefined ? addBtn : emptyAction} />}
      {log && <TestResultsModal data={log} onClose={() => setLog(null)} />}
    </Card>
  );
}
