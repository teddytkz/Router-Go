// Add-key dialog shared by built-in and custom providers: Single (name, key + Check, priority, proxy pool) or Bulk (one key per line).
import { Zap } from "lucide-react";
import { useState } from "react";
import type { ValidateResult } from "../../lib/api";
import { useCreateConnection, useProxyPools } from "../../lib/hooks";
import { Button, cn, errText, Field, Input, Modal, Select, useToast } from "../ui";

const tab = (on: boolean) => cn("rounded-lg px-3.5 py-1.5 text-[13px] font-semibold cursor-pointer", on ? "bg-accent text-white" : "text-muted hover:text-text");

export function AddKeyDialog({ title, provider, count, keyOptional, hint, check, onClose }: {
  title: string; provider: string; count: number; keyOptional?: boolean; hint?: string;
  check?: (key: string) => Promise<ValidateResult>; // omitted: this provider has no key check, the key is verified when saved
  onClose: () => void;
}) {
  const toast = useToast(), create = useCreateConnection(), pools = useProxyPools();
  const [mode, setMode] = useState<"single" | "bulk">("single");
  const [name, setName] = useState(`Key ${count + 1}`), [key, setKey] = useState(""), [bulk, setBulk] = useState(""), [priority, setPriority] = useState(String(count + 1)), [pool, setPool] = useState("");
  const [res, setRes] = useState<{ ok: boolean; text: string } | null>(null), [checking, setChecking] = useState(false), [busy, setBusy] = useState(false);
  const lines = bulk.split("\n").map((l) => l.trim()).filter(Boolean);
  const extra = pool ? { proxyPoolId: pool } : {};

  const runCheck = async () => {
    setChecking(true);
    try { const r = await check!(key.trim()); setRes({ ok: r.valid, text: r.valid ? "Valid" : `Invalid — ${r.error ?? "check failed"}` }); }
    catch (e) { setRes({ ok: false, text: `Invalid — ${errText(e)}` }); } finally { setChecking(false); }
  };
  const saveSingle = () => create.mutate({ provider, name: name.trim(), apiKey: key.trim(), priority: Math.max(1, Math.round(Number(priority)) || count + 1), ...extra },
    { onSuccess: () => { toast("ok", "Key added"); onClose(); }, onError: (e) => toast("err", errText(e)) });
  const saveBulk = async () => { // one at a time: the provider checks each key as it is added
    setBusy(true);
    let ok = 0, last = "";
    for (const [i, k] of lines.entries()) {
      try { await create.mutateAsync({ provider, name: `Key ${count + i + 1}`, apiKey: k, ...extra }); ok++; } catch (e) { last = `line ${i + 1}: ${errText(e)}`; }
    }
    setBusy(false);
    toast(ok === lines.length ? "ok" : "err", ok === lines.length ? `Added ${ok} keys` : `Added ${ok} of ${lines.length} keys — ${last}`);
    if (ok) onClose();
  };
  const single = mode === "single", valid = single ? !!name.trim() && (keyOptional || !!key.trim()) : lines.length > 0;

  return (
    <Modal open onClose={onClose} title={title}
      footer={<><Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" loading={create.isPending || busy} disabled={!valid} onClick={single ? saveSingle : saveBulk}>{single ? "Save" : lines.length ? `Add ${lines.length} key${lines.length === 1 ? "" : "s"}` : "Add keys"}</Button></>}>
      <div role="tablist" className="flex gap-1">
        <button type="button" role="tab" aria-selected={single} className={tab(single)} onClick={() => setMode("single")}>Single</button>
        <button type="button" role="tab" aria-selected={!single} className={tab(!single)} onClick={() => setMode("bulk")}>Bulk Add</button>
      </div>
      <p className="text-muted text-[13px]">Keys are tried in priority order; if one is rate-limited or rejected the next takes over automatically.</p>
      {single ? (
        <>
          <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="Production Key" />}</Field>
          <Field label="API Key" hint={hint}>{(id) => (
            <div className="flex gap-2">
              <Input id={id} type="password" autoComplete="off" value={key} onChange={(e) => { setKey(e.target.value); setRes(null); }} />
              {check && <Button onClick={runCheck} loading={checking} disabled={!key.trim()}>{!checking && <Zap className="size-4" />}Check</Button>}
            </div>)}</Field>
          {res && <p role="status" className={cn("text-[13px] break-words", res.ok ? "text-ok" : "text-err")}>{res.text}</p>}
          <Field label="Priority">{(id) => <Input id={id} type="number" min={1} value={priority} onChange={(e) => setPriority(e.target.value)} />}</Field>
        </>
      ) : (
        <Field label="API keys" hint="One key per line. They are named Key N and added at the end of the priority order.">{(id) => (
          <textarea id={id} rows={6} autoComplete="off" spellCheck={false} value={bulk} onChange={(e) => setBulk(e.target.value)} placeholder={"sk-…\nsk-…"}
            className="w-full rounded-lg border border-border bg-surface px-3 py-2 font-mono text-[13px] placeholder:text-muted/70 focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/25" />)}</Field>
      )}
      <Field label="Proxy Pool" hint="Send this key's upstream traffic through a proxy or relay. Manage pools under Proxy Pools.">{(id) => (
        <Select id={id} value={pool} onChange={(e) => setPool(e.target.value)}>
          <option value="">None</option>
          {(pools.data ?? []).map((p) => <option key={p.id} value={p.id}>{p.name} · {p.type}{p.isActive ? "" : " (disabled)"}</option>)}
        </Select>)}</Field>
    </Modal>
  );
}
