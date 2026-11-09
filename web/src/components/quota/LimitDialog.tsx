// Create / edit dialog for a quota limit.
import { useMemo, useState } from "react";
import type { QuotaBody, QuotaLimit, QuotaScope } from "../../lib/api";
import { useBuiltinProviders, useConnections, useNodes, useSaveQuota } from "../../lib/hooks";
import { parseTokens } from "../CapBadges";
import { Button, errText, Field, Input, Modal, SegmentedControl, Select, Switch, useToast } from "../ui";

const WINDOWS = ["hour", "day", "week", "month"] as const;
const MODE_HINT = { calendar: "Resets on the boundary: top of the hour, midnight, Monday, or the 1st (server local time).", rolling: "Counts the last hour / day / 7 days / 30 days; usage frees up gradually." };

export function LimitDialog({ edit, preset, onClose }: { edit?: QuotaLimit; preset?: { scope: QuotaScope; target: string }; onClose: () => void }) {
  const toast = useToast(), save = useSaveQuota(), nodes = useNodes(), builtin = useBuiltinProviders(), conns = useConnections();
  const [scope, setScope] = useState<QuotaScope>(edit?.scope ?? preset?.scope ?? "provider"), [target, setTarget] = useState(edit?.target ?? preset?.target ?? "");
  const [metric, setMetric] = useState(edit?.metric ?? "requests"), [limit, setLimit] = useState(edit ? String(edit.limit) : "");
  const [win, setWin] = useState(edit?.window ?? "day"), [mode, setMode] = useState(edit?.mode ?? "calendar"), [enforce, setEnforce] = useState(edit?.enforce ?? true), [name, setName] = useState(edit?.name ?? "");
  const [err, setErr] = useState("");

  const provName = useMemo(() => {
    const m = new Map<string, string>();
    (builtin.data ?? []).forEach((p) => m.set(p.id, p.name)); (nodes.data ?? []).forEach((n) => m.set(n.id, n.name));
    return (id: string) => m.get(id) ?? id;
  }, [builtin.data, nodes.data]);
  // provider scope: custom nodes + built-ins that have keys; key scope: every connection, grouped by provider
  const providers = useMemo(() => {
    const withKeys = new Set((conns.data ?? []).map((c) => c.provider));
    return [...(nodes.data ?? []).map((n) => ({ id: n.id, name: n.name })), ...(builtin.data ?? []).filter((p) => withKeys.has(p.id)).map((p) => ({ id: p.id, name: p.name }))];
  }, [nodes.data, builtin.data, conns.data]);
  const groups = useMemo(() => {
    const g = new Map<string, { id: string; label: string }[]>();
    (conns.data ?? []).forEach((c) => g.set(c.provider, [...(g.get(c.provider) ?? []), { id: c.id, label: `${provName(c.provider)} / ${c.name}` }]));
    return [...g.entries()];
  }, [conns.data, provName]);
  const known = scope === "provider" ? providers.some((p) => p.id === target) : groups.some(([, l]) => l.some((c) => c.id === target));

  const n = parseTokens(limit), limitErr = limit.trim() && (n === null || n < 1) ? "Enter a positive number such as 500, 10k or 1m" : undefined;
  const ok = !!target && !!n && n >= 1 && !limitErr;
  const submit = () => {
    if (!ok || !n) return;
    const body: QuotaBody = { ...(name.trim() ? { name: name.trim() } : {}), scope, target, metric, window: win, mode, limit: n, enforce };
    save.mutate({ id: edit?.id, body }, { onSuccess: () => { toast("ok", edit ? "Limit updated" : "Limit created"); onClose(); }, onError: (e) => setErr(errText(e)) });
  };

  return (
    <Modal open onClose={onClose} title={edit ? "Edit limit" : "New limit"}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={save.isPending} disabled={!ok} onClick={submit}>{edit ? "Save" : "Create limit"}</Button></>}>
      <div className="space-y-1.5">
        <span className="block text-[13px] font-medium">Applies to</span>
        <SegmentedControl label="Scope" value={scope} onChange={(v) => { setScope(v); setTarget(""); }} options={[{ value: "provider", label: "Provider" }, { value: "connection", label: "API key" }]} />
      </div>
      <Field label={scope === "provider" ? "Provider" : "API key"} hint={scope === "provider" ? undefined : "Only that key is stopped; the next key of the provider takes over."}>
        {(id) => (
          <Select id={id} value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="" disabled>Select…</option>
            {edit && target && !known && <option value={target}>{edit.targetName || target}</option>}
            {scope === "provider" ? providers.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)
              : groups.map(([pid, list]) => <optgroup key={pid} label={provName(pid)}>{list.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}</optgroup>)}
          </Select>
        )}
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-1.5">
          <span className="block text-[13px] font-medium">Count</span>
          <SegmentedControl label="Metric" value={metric} onChange={setMetric} options={[{ value: "requests", label: "Requests" }, { value: "tokens", label: "Tokens" }]} />
        </div>
        <Field label="Per" >{(id) => <Select id={id} value={win} onChange={(e) => setWin(e.target.value as typeof win)}>{WINDOWS.map((w) => <option key={w} value={w}>{w[0].toUpperCase() + w.slice(1)}</option>)}</Select>}</Field>
      </div>
      <Field label={`Limit (${metric})`} error={limitErr} hint="Accepts 500, 10k or 1m.">
        {(id) => <Input id={id} className="font-mono" inputMode="text" placeholder={metric === "tokens" ? "1m" : "1000"} value={limit} onChange={(e) => setLimit(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); submit(); } }} />}
      </Field>
      <div className="space-y-1.5">
        <span className="block text-[13px] font-medium">Window</span>
        <SegmentedControl label="Window mode" value={mode} onChange={setMode} options={[{ value: "calendar", label: "Calendar" }, { value: "rolling", label: "Rolling" }]} />
        <p className="text-xs text-muted">{MODE_HINT[mode]}</p>
      </div>
      <div className="flex items-start justify-between gap-3 rounded-lg border border-border px-3 py-2.5">
        <div className="min-w-0"><div className="text-[13px] font-medium">Block at limit</div><p className="text-xs text-muted">On: the gateway stops routing to this {scope === "provider" ? "provider" : "key"} while its budget is used up (a combo member or the next key takes over). Off: tracking only.</p></div>
        <Switch checked={enforce} onChange={setEnforce} label="Block at limit" />
      </div>
      <Field label="Name (optional)">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Daily budget" />}</Field>
      {err && <p role="alert" className="text-xs text-err break-words">{err}</p>}
    </Modal>
  );
}
