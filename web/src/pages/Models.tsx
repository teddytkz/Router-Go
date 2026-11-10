import { Boxes, Pencil, Plus, Tag, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { Badge, Button, Card, ConfirmModal, EmptyState, errText, Field, Input, Modal, PageHeader, Select, Skeleton, useToast } from "../components/ui";
import type { CustomModel } from "../lib/api";
import { useAddCustomModel, useAliases, useAllModels, useCustomModels, useDeleteAlias, useDeleteCustomModel, useNodes, useAllNodes, useSetAlias } from "../lib/hooks";

const th = "text-left text-xs font-medium text-muted px-4 py-2";

// ---------- aliases ----------
function AliasModal({ alias, model, onClose }: { alias?: string; model?: string; onClose: () => void }) {
  const toast = useToast(), set = useSetAlias(), nodes = useAllNodes(), models = useAllModels(nodes.data), listId = useId();
  const [a, setA] = useState(alias ?? ""), [m, setM] = useState(model ?? "");
  const submit = () => set.mutate({ alias: a.trim(), model: m.trim() }, { onSuccess: () => { toast("ok", "Alias saved"); onClose(); }, onError: (e) => toast("err", errText(e)) });
  return (
    <Modal open onClose={onClose} title={alias ? "Edit alias" : "New alias"}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={set.isPending} disabled={!a.trim() || !m.trim()} onClick={submit}>Save</Button></>}>
      <Field label="Alias" hint="The short name clients will send as the model">{(id) => <Input id={id} className="font-mono" value={a} onChange={(e) => setA(e.target.value)} disabled={!!alias} placeholder="fast" />}</Field>
      <Field label="Target model" hint="prefix/model — pick a known model or type one">
        {(id) => <><Input id={id} list={listId} className="font-mono" value={m} onChange={(e) => setM(e.target.value)} placeholder="openai/gpt-4o-mini" />
          <datalist id={listId}>{models.data?.map((x) => <option key={x} value={x} />)}</datalist></>}
      </Field>
    </Modal>
  );
}

function Aliases() {
  const toast = useToast(), q = useAliases(), del = useDeleteAlias();
  const [edit, setEdit] = useState<{ alias?: string; model?: string } | null>(null), [rm, setRm] = useState<string | null>(null);
  const rows = Object.entries(q.data ?? {}).sort(([x], [y]) => x.localeCompare(y));
  const add = <Button variant="primary" size="sm" onClick={() => setEdit({})}><Plus className="size-4" />Add alias</Button>;
  return (
    <Card className="mb-6">
      <div className="px-4 h-12 flex items-center justify-between border-b border-border"><h2 className="font-semibold">Aliases</h2>{add}</div>
      {q.isLoading ? <Skeleton className="h-20 m-4" /> : rows.length ? (
        <div className="overflow-x-auto"><table className="w-full">
          <thead><tr className="border-b border-border"><th className={th}>Alias</th><th className={th}>Target model</th><th className="w-24" /></tr></thead>
          <tbody className="divide-y divide-border">{rows.map(([a, m]) => (
            <tr key={a}>
              <td className="px-4 py-2 font-mono text-[13px]">{a}</td>
              <td className="px-4 py-2 font-mono text-[13px] text-muted break-all">{m}</td>
              <td className="px-2 py-1 text-right whitespace-nowrap">
                <Button size="sm" variant="ghost" aria-label={`Edit ${a}`} onClick={() => setEdit({ alias: a, model: m })}><Pencil className="size-4" /></Button>
                <Button size="sm" variant="ghost" aria-label={`Delete ${a}`} onClick={() => setRm(a)}><Trash2 className="size-4" /></Button>
              </td>
            </tr>))}</tbody>
        </table></div>
      ) : <EmptyState icon={<Tag className="size-6" />} title="No aliases" body="An alias is a short name that points at a full prefix/model, so clients can keep using a stable name while you switch the target." action={add} />}
      {edit && <AliasModal {...edit} onClose={() => setEdit(null)} />}
      <ConfirmModal open={!!rm} onClose={() => setRm(null)} loading={del.isPending} title="Delete alias" body={`Remove alias "${rm}"? Clients using it will get an error.`}
        onConfirm={() => del.mutate(rm!, { onSuccess: () => { setRm(null); toast("ok", "Alias deleted"); }, onError: (e) => toast("err", errText(e)) })} />
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
              <td className="px-2 py-1 text-right"><Button size="sm" variant="ghost" aria-label={`Delete ${m.providerAlias}/${m.id}`} onClick={() => setRm(m)}><Trash2 className="size-4" /></Button></td>
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
      <PageHeader title="Models" subtitle="Aliases and hand-registered models" />
      <Aliases />
      <CustomModels />
    </>
  );
}
