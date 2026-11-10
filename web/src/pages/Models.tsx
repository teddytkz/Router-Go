import { ArrowRight, Boxes, ListChecks, Pencil, Plus, Tag, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { ModelPicker, type PickGroup } from "../components/ModelPicker";
import { Badge, Button, Card, ConfirmModal, EmptyState, errText, Field, Input, Modal, PageHeader, Select, Skeleton, useToast } from "../components/ui";
import type { CustomModel } from "../lib/api";
import { useAddCustomModel, useAliases, useAllModels, useCombos, useCustomModels, useDeleteAlias, useDeleteCustomModel, useNodes, useAllNodes, useSetAlias } from "../lib/hooks";

const act = "grid size-7 place-items-center rounded-md cursor-pointer";
const th = "text-left text-xs font-medium text-muted px-4 py-2";

// ---------- masks (stored as aliases) ----------
function AliasModal({ alias, model, onClose }: { alias?: string; model?: string; onClose: () => void }) {
  const toast = useToast(), set = useSetAlias(), nodes = useAllNodes(), models = useAllModels(nodes.data), combos = useCombos();
  const [a, setA] = useState(alias ?? ""), [m, setM] = useState(model ?? ""), [picking, setPicking] = useState(false), [draft, setDraft] = useState<string[]>([]);
  const submit = () => set.mutate({ alias: a.trim(), model: m.trim() }, { onSuccess: () => { toast("ok", "Mask saved"); onClose(); }, onError: (e) => toast("err", errText(e)) });
  // models grouped by provider, plus combos: either can sit behind a mask
  const groups = useMemo<PickGroup[]>(() => {
    const by = new Map<string, string[]>();
    for (const x of models.data ?? []) { const p = x.slice(0, x.indexOf("/")); by.set(p, [...(by.get(p) ?? []), x]); }
    const out: PickGroup[] = [...by].map(([prefix, ms]) => ({ key: prefix, label: nodes.data?.find((n) => n.prefix === prefix)?.name ?? prefix, models: ms }));
    const cs = (combos.data ?? []).map((c) => c.name);
    return cs.length ? [{ key: "__combos", label: "Combos", models: cs }, ...out] : out;
  }, [models.data, nodes.data, combos.data]);

  if (picking) {
    return (
      <Modal open onClose={() => setPicking(false)} title="Select real model or combo"
        footer={<><Button onClick={() => setPicking(false)}>Back</Button><Button variant="primary" disabled={!draft.length} onClick={() => { setM(draft[0]); setPicking(false); }}>Use {draft[0] ?? "…"}</Button></>}>
        <p className="text-[13px] text-muted -mt-1">Pick one model or combo. Choosing another replaces the current pick.</p>
        <ModelPicker groups={groups} selected={draft} onChange={(v) => setDraft(v.slice(-1))} />
      </Modal>
    );
  }
  return (
    <Modal open onClose={onClose} title={alias ? "Edit mask" : "New mask"}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={set.isPending} disabled={!a.trim() || !m.trim()} onClick={submit}>Save</Button></>}>
      <Field label="Masked name" hint="The name clients will send as the model">{(id) => <Input id={id} className="font-mono" value={a} onChange={(e) => setA(e.target.value)} disabled={!!alias} placeholder="fast" />}</Field>
      <Field label="Real model or combo" hint="prefix/model or a combo name — choose from the list or type it">
        {(id) => <div className="flex gap-2"><Input id={id} className="font-mono" value={m} onChange={(e) => setM(e.target.value)} placeholder="openai/gpt-4o-mini" />
          <Button onClick={() => { setDraft(m.trim() ? [m.trim()] : []); setPicking(true); }}><ListChecks className="size-4" />Choose</Button></div>}
      </Field>
    </Modal>
  );
}

function Aliases() {
  const toast = useToast(), q = useAliases(), del = useDeleteAlias();
  const [edit, setEdit] = useState<{ alias?: string; model?: string } | null>(null), [rm, setRm] = useState<string | null>(null);
  const rows = Object.entries(q.data ?? {}).sort(([x], [y]) => x.localeCompare(y));
  const add = <Button variant="primary" size="sm" onClick={() => setEdit({})}><Plus className="size-4" />Add mask</Button>;
  return (
    <Card className="mb-6">
      <div className="px-4 h-12 flex items-center justify-between border-b border-border"><h2 className="font-semibold">Masked names</h2>{add}</div>
      {q.isLoading ? <Skeleton className="h-20 m-4" /> : rows.length ? (
        <ul className="divide-y divide-border">{rows.map(([a, m]) => (
          <li key={a} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5">
            <span className="rounded-md bg-accent-soft px-2 py-0.5 font-mono text-[13px] text-accent break-all">{a}</span>
            <ArrowRight className="size-4 shrink-0 text-muted" aria-hidden />
            <span className="min-w-0 flex-1 font-mono text-[13px] text-muted break-all">{m}</span>
            <div className="flex shrink-0 items-center gap-0.5">
              <button type="button" className={`${act} text-accent hover:bg-accent-soft`} aria-label={`Edit ${a}`} title="Edit" onClick={() => setEdit({ alias: a, model: m })}><Pencil className="size-4" /></button>
              <button type="button" className={`${act} text-err hover:bg-err-soft`} aria-label={`Delete ${a}`} title="Delete" onClick={() => setRm(a)}><Trash2 className="size-4" /></button>
            </div>
          </li>))}</ul>
      ) : <EmptyState icon={<Tag className="size-6" />} title="No masked names" body="A mask is a name clients send instead of the real model or combo, so they keep a stable name while you switch what is behind it." action={add} />}
      {edit && <AliasModal {...edit} onClose={() => setEdit(null)} />}
      <ConfirmModal open={!!rm} onClose={() => setRm(null)} loading={del.isPending} title="Delete mask" body={`Remove the masked name "${rm}"? Clients using it will get an error.`}
        onConfirm={() => del.mutate(rm!, { onSuccess: () => { setRm(null); toast("ok", "Mask deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </Card>
  );
}

// ---------- custom models ----------
function CustomModal({ prefixes, initial, onClose }: { prefixes: string[]; initial: string; onClose: () => void }) {
  const toast = useToast(), add = useAddCustomModel();
  const [p, setP] = useState(initial || prefixes[0] || ""), [id, setId] = useState(""), [name, setName] = useState("");
  const submit = () => add.mutate({ providerAlias: p, id: id.trim(), name: name.trim() || undefined }, { onSuccess: () => { toast("ok", "Custom model added"); onClose(); }, onError: (e) => toast("err", errText(e)) });
  return (
    <Modal open onClose={onClose} title="Add custom model"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={add.isPending} disabled={!p || !id.trim()} onClick={submit}>Add model</Button></>}>
      <p className="text-muted text-[13px]">For upstreams that do not list their models. The model becomes available as <span className="font-mono">{p || "prefix"}/{id.trim() || "id"}</span>.</p>
      <Field label="Provider prefix">{(fid) => <Select id={fid} className="font-mono" value={p} onChange={(e) => setP(e.target.value)}>{prefixes.map((x) => <option key={x}>{x}</option>)}</Select>}</Field>
      <Field label="Model ID">{(fid) => <Input id={fid} className="font-mono" value={id} onChange={(e) => setId(e.target.value)} placeholder="gpt-4o-mini" />}</Field>
      <Field label="Display name (optional)">{(fid) => <Input id={fid} value={name} onChange={(e) => setName(e.target.value)} placeholder="GPT-4o mini" />}</Field>
    </Modal>
  );
}

function CustomModels() {
  const toast = useToast(), q = useCustomModels(), nodes = useNodes(), del = useDeleteCustomModel();
  const [filter, setFilter] = useState(""), [adding, setAdding] = useState(false), [rm, setRm] = useState<CustomModel | null>(null);
  const prefixes = nodes.data?.map((n) => n.prefix) ?? [];
  const all = q.data ?? [];
  const filterOpts = [...new Set([...prefixes, ...all.map((m) => m.providerAlias)])].sort();
  const rows = all.filter((m) => !filter || m.providerAlias === filter).sort((a, b) => a.providerAlias.localeCompare(b.providerAlias) || a.id.localeCompare(b.id));
  const add = <Button variant="primary" size="sm" disabled={!prefixes.length} title={prefixes.length ? undefined : "Add a provider first"} onClick={() => setAdding(true)}><Plus className="size-4" />Add model</Button>;
  return (
    <Card>
      <div className="px-4 min-h-12 py-2 flex flex-wrap items-center justify-between gap-2 border-b border-border">
        <h2 className="font-semibold">Custom models</h2>
        <div className="flex items-center gap-2">
          {filterOpts.length > 1 && <Select aria-label="Filter by provider prefix" className="w-44 h-8 font-mono text-[13px]" value={filter} onChange={(e) => setFilter(e.target.value)}><option value="">All providers</option>{filterOpts.map((x) => <option key={x}>{x}</option>)}</Select>}
          {add}
        </div>
      </div>
      {q.isLoading ? <Skeleton className="h-20 m-4" /> : rows.length ? (
        <div className="overflow-x-auto"><table className="w-full">
          <thead><tr className="border-b border-border"><th className={th}>Provider</th><th className={th}>Model ID</th><th className={th}>Name</th><th className="w-14" /></tr></thead>
          <tbody className="divide-y divide-border">{rows.map((m) => (
            <tr key={m.providerAlias + "|" + m.id}>
              <td className="px-4 py-2"><Badge tone="accent" className="font-mono">{m.providerAlias}/</Badge></td>
              <td className="px-4 py-2 font-mono text-[13px] break-all">{m.id}</td>
              <td className="px-4 py-2 text-muted">{m.name && m.name !== m.id ? m.name : "—"}</td>
              <td className="px-2 py-1 text-right"><button type="button" className={`${act} text-err hover:bg-err-soft`} aria-label={`Delete ${m.providerAlias}/${m.id}`} title="Delete" onClick={() => setRm(m)}><Trash2 className="size-4" /></button></td>
            </tr>))}</tbody>
        </table></div>
      ) : <EmptyState icon={<Boxes className="size-6" />} title={filter ? "No custom models for this provider" : "No custom models"} body="Register model IDs by hand for providers whose /models endpoint is missing or incomplete." action={add} />}
      {adding && <CustomModal prefixes={prefixes} initial={filter} onClose={() => setAdding(false)} />}
      <ConfirmModal open={!!rm} onClose={() => setRm(null)} loading={del.isPending} title="Delete custom model" body={`Remove "${rm?.providerAlias}/${rm?.id}" from the model list?`}
        onConfirm={() => del.mutate({ providerAlias: rm!.providerAlias, id: rm!.id }, { onSuccess: () => { setRm(null); toast("ok", "Custom model deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </Card>
  );
}

export default function Models() {
  return (
    <>
      <PageHeader title="Model Masking" subtitle="Masked names for models and combos, and hand-registered models" />
      <Aliases />
      <CustomModels />
    </>
  );
}
