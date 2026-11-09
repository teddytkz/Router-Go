// "Available Models" card of a built-in provider: enabled models as a card grid, disabled ones as click-to-enable chips.
import { Check, Loader2, Plus, SlidersHorizontal, Trash2, TriangleAlert, X, Zap } from "lucide-react";
import { useState } from "react";
import type { BuiltinModel, BuiltinProvider } from "../../lib/api";
import { useAddCustomModel, useBulkModels, useCustomModels, useDeleteCustomModel, useTestModel } from "../../lib/hooks";
import { CapBadges } from "../CapBadges";
import { Badge, Button, Card, ConfirmModal, CopyButton, errText, Field, Input, Modal, cn, useToast } from "../ui";
import { CapsEditor } from "./CapsEditor";
import { fmtMs } from "./shared";

type Result = { state: "testing" } | { state: "ok" | "fail"; msg: string };
const act = "grid size-7 place-items-center rounded-md text-muted hover:bg-surface-2 hover:text-text disabled:opacity-60 cursor-pointer";

function AddModelModal({ p, onClose }: { p: BuiltinProvider; onClose: () => void }) {
  const toast = useToast(), add = useAddCustomModel(), [id, setId] = useState("");
  const submit = () => id.trim() && add.mutate({ providerAlias: p.alias, id: id.trim() }, { onSuccess: () => { toast("ok", `${id.trim()} enabled`); onClose(); }, onError: (e) => toast("err", errText(e)) });
  return (
    <Modal open onClose={onClose} title={`Add model to ${p.name}`}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={add.isPending} disabled={!id.trim()} onClick={submit}>Add model</Button></>}>
      <Field label="Model ID" hint={p.passthrough ? "This provider passes any model id through to the upstream, so any id works." : `Clients will request it as ${p.alias}/<model-id>.`}>
        {(fid) => <Input id={fid} className="font-mono" placeholder="e.g. gpt-4o" value={id} onChange={(e) => setId(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); submit(); } }} />}
      </Field>
    </Modal>
  );
}

export function BuiltinModels({ p }: { p: BuiltinProvider }) {
  const toast = useToast(), custom = useCustomModels(), add = useAddCustomModel(), del = useDeleteCustomModel(), bulk = useBulkModels(), testModel = useTestModel();
  const [results, setResults] = useState<Record<string, Result>>({}), [editing, setEditing] = useState<string | null>(null), [adding, setAdding] = useState(false), [confirm, setConfirm] = useState(false), [busy, setBusy] = useState<string | null>(null);
  const enabled = p.models.filter((m) => m.enabled || m.custom), disabled = p.models.filter((m) => !m.enabled && !m.custom), code = (id: string) => `${p.alias}/${id}`;
  const errors = Object.entries(results).filter((e): e is [string, { state: "fail"; msg: string }] => e[1].state === "fail");
  const editModel = editing ? (custom.data ?? []).find((m) => m.providerAlias === p.alias && m.id === editing) : undefined;

  const test = async (id: string) => {
    setResults((s) => ({ ...s, [id]: { state: "testing" } }));
    let res: Result;
    try { const r = await testModel.mutateAsync(code(id)); res = r.ok ? { state: "ok", msg: `Works${r.latencyMs != null ? ` · ${fmtMs(r.latencyMs)}` : ""}` } : { state: "fail", msg: r.error ?? "Test failed" }; }
    catch (e) { res = { state: "fail", msg: errText(e) }; }
    setResults((s) => ({ ...s, [id]: res }));
  };
  const enable = (m: BuiltinModel) => { setBusy(m.id); add.mutate({ providerAlias: p.alias, id: m.id, name: m.name }, { onError: (e) => toast("err", errText(e)), onSettled: () => setBusy(null) }); };
  const disable = (id: string) => { setBusy(id); del.mutate({ providerAlias: p.alias, id }, { onError: (e) => toast("err", errText(e)), onSettled: () => setBusy(null) }); };
  const bulkDo = (action: "enable-all" | "disable-all") => bulk.mutate({ id: p.id, action }, { onSuccess: () => { setConfirm(false); toast("ok", action === "enable-all" ? "All models enabled" : "All models disabled"); }, onError: (e) => { setConfirm(false); toast("err", errText(e)); } });

  return (
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 border-b border-border">
        <div className="flex items-center gap-2"><h2 className="font-semibold">Available Models</h2><Badge tone={enabled.length ? "ok" : "warn"}>{enabled.length} enabled</Badge></div>
        <div className="flex gap-2">
          <Button size="sm" onClick={() => bulkDo("enable-all")} loading={bulk.isPending && !confirm} disabled={!disabled.length}>Enable All</Button>
          <Button size="sm" onClick={() => setConfirm(true)} disabled={!enabled.length}>Disable All</Button>
        </div>
      </div>
      <div className="p-4 space-y-4">
        {!enabled.length && (
          <div role="alert" className="flex items-start gap-3 rounded-lg border border-warn/40 bg-warn-soft px-3 py-3 text-[13px]">
            <TriangleAlert className="size-4 shrink-0 text-warn mt-0.5" aria-hidden />
            <p>No models enabled yet — enable from the list below or Enable All.</p>
          </div>
        )}
        {errors.length > 0 && (
          <div role="alert" className="rounded-lg border border-err/40 bg-err-soft px-3 py-2.5 text-[13px]">
            <div className="flex items-center justify-between gap-2">
              <span className="inline-flex items-center gap-2 font-medium text-err"><TriangleAlert className="size-4" aria-hidden />Test failed for {errors.length} model{errors.length === 1 ? "" : "s"}</span>
              <button type="button" onClick={() => setResults((s) => Object.fromEntries(Object.entries(s).filter(([, r]) => r.state !== "fail")))} className="text-xs text-muted hover:text-text cursor-pointer rounded">Dismiss all</button>
            </div>
            <ul className="mt-1.5 space-y-1">
              {errors.map(([id, r]) => (
                <li key={id} className="flex items-start gap-2">
                  <span className="font-mono text-xs shrink-0">{id}</span><span className="text-err break-words min-w-0 flex-1">{r.msg}</span>
                  <button type="button" aria-label={`Dismiss ${id}`} onClick={() => setResults((s) => { const n = { ...s }; delete n[id]; return n; })} className="shrink-0 text-muted hover:text-text cursor-pointer rounded"><X className="size-3.5" /></button>
                </li>
              ))}
            </ul>
          </div>
        )}
        <ul className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3" aria-label="Enabled models">
          {enabled.map((m) => {
            const r = results[m.id], testing = r?.state === "testing";
            return (
              <li key={m.id} className={cn("rounded-lg border bg-surface p-3 min-w-0", r?.state === "ok" ? "border-ok/60" : r?.state === "fail" ? "border-err/60" : "border-border")}>
                <div className="flex items-center gap-2">
                  <span className="inline-flex min-w-0 flex-1 items-center rounded-md bg-surface-2 pl-2 font-mono text-xs text-muted" title={m.name && m.name !== m.id ? m.name : undefined}>
                    <span className="truncate" title={code(m.id)}>{code(m.id)}</span><CopyButton value={code(m.id)} label={`Copy ${code(m.id)}`} />
                  </span>
                  <div className="flex shrink-0 items-center gap-0.5">
                    <button type="button" className={act} disabled={testing} onClick={() => test(m.id)} aria-label={`Test ${code(m.id)}`} title="Test">
                      {testing ? <Loader2 className="size-4 animate-spin" /> : r?.state === "ok" ? <Check className="size-4 text-ok" /> : r?.state === "fail" ? <X className="size-4 text-err" /> : <Zap className="size-4" />}
                    </button>
                    <button type="button" className={act} disabled={!custom.data?.some((c) => c.providerAlias === p.alias && c.id === m.id)} onClick={() => setEditing(m.id)} aria-haspopup="dialog" aria-label={`Edit capabilities of ${m.id}`} title="Edit capabilities"><SlidersHorizontal className="size-4" /></button>
                    <button type="button" className={cn(act, "hover:text-err")} disabled={busy === m.id} onClick={() => disable(m.id)} aria-label={`${m.custom ? "Remove" : "Disable"} ${m.id}`} title={m.custom ? "Remove" : "Disable"}>{m.custom ? <Trash2 className="size-4" /> : <X className="size-4" />}</button>
                  </div>
                </div>
                <div className="mt-2 flex min-h-6 items-center gap-2">
                  <CapBadges caps={m.effective} detected={m.detected} size="xs" />
                  {m.custom && <Badge>custom</Badge>}
                </div>
                {r?.state === "ok" && <p role="status" className="mt-1 text-xs text-ok">{r.msg}</p>}
              </li>
            );
          })}
          <li>
            <button type="button" onClick={() => setAdding(true)} aria-haspopup="dialog" className="flex h-full min-h-24 w-full flex-col items-center justify-center gap-1 rounded-lg border border-dashed border-border p-3 text-[13px] text-muted hover:border-accent/60 hover:text-text cursor-pointer transition-colors">
              <Plus className="size-5" aria-hidden />Add Model
            </button>
          </li>
        </ul>
        {disabled.length > 0 && (
          <section aria-labelledby="dis-h" className="space-y-2">
            <h3 id="dis-h" className="text-[13px] font-medium text-muted">Disabled models ({disabled.length}):</h3>
            <ul className="flex flex-wrap gap-2 max-h-56 overflow-y-auto p-0.5">
              {disabled.map((m) => (
                <li key={m.id}>
                  <button type="button" disabled={busy === m.id} onClick={() => enable(m)} title={`Enable ${code(m.id)}`}
                    className="inline-flex items-center gap-1.5 rounded-lg border border-dashed border-border px-2.5 py-1.5 text-[13px] text-muted hover:border-accent/60 hover:text-text disabled:opacity-60 cursor-pointer transition-colors">
                    {busy === m.id ? <Loader2 className="size-3.5 animate-spin" aria-hidden /> : <Plus className="size-3.5" aria-hidden />}{m.id}
                  </button>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
      {editModel && <CapsEditor model={editModel} onClose={() => setEditing(null)} />}
      {adding && <AddModelModal p={p} onClose={() => setAdding(false)} />}
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={bulk.isPending} confirmLabel="Disable All" title="Disable all models" body={`Disable every model of ${p.name}? Clients can no longer use them until you enable them again.`} onConfirm={() => bulkDo("disable-all")} />
    </Card>
  );
}
