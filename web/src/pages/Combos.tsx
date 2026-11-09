import { ArrowRight, Layers, Pencil, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { CapacityAdapter } from "../components/CapacityAdapter";
import { CapBadges } from "../components/CapBadges";
import { ComboMembers } from "../components/ComboMembers";
import { ModelPicker, type PickGroup } from "../components/ModelPicker";
import { Badge, Button, Card, ConfirmModal, EmptyState, errText, Field, Input, Modal, PageHeader, Select, Skeleton, useToast } from "../components/ui";
import type { Combo, ComboStrategy } from "../lib/api";
import { useAliases, useAllModels, useCapsMap, useCombos, useDeleteCombo, useNodes, useSaveCombo } from "../lib/hooks";

const NAME_RE = /^[A-Za-z0-9_.-]{1,64}$/;
const STRATEGY: Record<ComboStrategy, { label: string; hint: string }> = {
  fallback: { label: "Fallback", hint: "Try members in order; move to the next when one fails or is cooling down." },
  "round-robin": { label: "Round-robin", hint: "Rotate the starting member on every request to spread load; fall back on failure." },
};

function ComboModal({ combo, onClose }: { combo?: Combo; onClose: () => void }) {
  const toast = useToast(), save = useSaveCombo();
  const nodes = useNodes(), models = useAllModels(nodes.data), aliases = useAliases();
  const [name, setName] = useState(combo?.name ?? ""), [strategy, setStrategy] = useState<ComboStrategy>(combo?.strategy ?? "fallback"), [members, setMembers] = useState(combo?.models ?? []);
  const [picking, setPicking] = useState(false), [draft, setDraft] = useState<string[]>([]);
  const nameErr = name && !NAME_RE.test(name) ? "Use 1-64 letters, digits, dot, dash or underscore" : undefined;

  // models grouped by provider (only models added to a provider can be used), plus aliases
  const groups = useMemo<PickGroup[]>(() => {
    const byPrefix = new Map<string, string[]>();
    for (const m of models.data ?? []) {
      const p = m.slice(0, m.indexOf("/"));
      byPrefix.set(p, [...(byPrefix.get(p) ?? []), m]);
    }
    const out: PickGroup[] = [...byPrefix].map(([prefix, ms]) => ({ key: prefix, label: nodes.data?.find((n) => n.prefix === prefix)?.name ?? prefix, models: ms }));
    const al = Object.keys(aliases.data ?? {});
    return al.length ? [...out, { key: "__aliases", label: "Aliases", models: al }] : out;
  }, [models.data, nodes.data, aliases.data]);
  const available = useMemo(() => new Set(groups.flatMap((g) => g.models.map((m) => m.toLowerCase()))), [groups]);

  const submit = () => save.mutate({ id: combo?.id, name, models: members, strategy }, {
    onSuccess: () => { toast("ok", combo ? "Combo updated" : "Combo created"); onClose(); }, onError: (e) => toast("err", errText(e)),
  });

  if (picking) {
    return (
      <Modal open onClose={() => setPicking(false)} title="Select models"
        footer={<><Button onClick={() => setPicking(false)}>Back</Button><Button variant="primary" onClick={() => { setMembers(draft); setPicking(false); }}>Apply · {draft.length} selected</Button></>}>
        <p className="text-[13px] text-muted -mt-1">Tick the models this combo may use. You set their priority on the previous screen.</p>
        <ModelPicker groups={groups} selected={draft} onChange={setDraft} />
      </Modal>
    );
  }
  return (
    <Modal open onClose={onClose} title={combo ? "Edit combo" : "New combo"}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={save.isPending} disabled={!NAME_RE.test(name) || !members.length} onClick={submit}>{combo ? "Save" : "Create combo"}</Button></>}>
      <Field label="Name" hint={name ? `Clients use model "${name}"` : "Used as the model name by clients"} error={nameErr}>
        {(id) => <Input id={id} className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="smart-coder" maxLength={64} />}
      </Field>
      <Field label="Strategy" hint={STRATEGY[strategy].hint}>
        {(id) => <Select id={id} value={strategy} onChange={(e) => setStrategy(e.target.value as ComboStrategy)}>{Object.entries(STRATEGY).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}</Select>}
      </Field>
      <ComboMembers value={members} onChange={setMembers} available={available} roundRobin={strategy === "round-robin"} onPick={() => { setDraft(members); setPicking(true); }} />
    </Modal>
  );
}

function ComboCard({ combo, onEdit }: { combo: Combo; onEdit: () => void }) {
  const toast = useToast(), del = useDeleteCombo(), caps = useCapsMap();
  const [confirm, setConfirm] = useState(false);
  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex items-center gap-2 flex-wrap min-w-0">
          <h2 className="font-semibold font-mono break-all">{combo.name}</h2>
          <Badge tone="accent">{STRATEGY[combo.strategy]?.label ?? combo.strategy}</Badge>
          <Badge>{combo.models.length} model{combo.models.length === 1 ? "" : "s"}</Badge>
        </div>
        <div className="flex items-center gap-1">
          <Button size="sm" variant="ghost" aria-label={`Edit ${combo.name}`} onClick={onEdit}><Pencil className="size-4" /></Button>
          <Button size="sm" variant="ghost" aria-label={`Delete ${combo.name}`} onClick={() => setConfirm(true)}><Trash2 className="size-4" /></Button>
        </div>
      </div>
      <ol className="flex flex-wrap items-center gap-1.5 mt-3">
        {combo.models.map((m, i) => {
          const cm = caps.get(m.toLowerCase());
          return (
          <li key={m + i} className="flex items-center gap-1.5">
            {i > 0 && <ArrowRight className="size-3.5 text-muted" aria-hidden />}
            <span className="inline-flex items-center gap-1.5 rounded-md bg-surface-2 px-2 py-1 font-mono text-xs"><span className="text-muted tabular-nums">{i + 1}</span>{m}<CapBadges caps={cm?.effective} detected={cm?.detected} /></span>
          </li>
          );
        })}
      </ol>
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete combo" body={`Delete "${combo.name}"? Clients requesting this model name will get an error.`}
        onConfirm={() => del.mutate(combo.id, { onSuccess: () => { setConfirm(false); toast("ok", "Combo deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </Card>
  );
}

export default function Combos() {
  const combos = useCombos();
  const [editing, setEditing] = useState<Combo | "new" | null>(null);
  const add = <Button variant="primary" onClick={() => setEditing("new")}><Plus className="size-4" />New combo</Button>;
  return (
    <>
      <PageHeader title="Combos" subtitle="Named model lists with fallback or round-robin routing" actions={add} />
      {combos.isLoading ? <div className="space-y-3"><Skeleton className="h-24" /><Skeleton className="h-24" /></div>
        : combos.data?.length ? <div className="space-y-4">{combos.data.map((c) => <ComboCard key={c.id} combo={c} onEdit={() => setEditing(c)} />)}</div>
        : <Card><EmptyState icon={<Layers className="size-6" />} title="No combos yet" body="A combo groups several models under one name. Clients request the combo and the gateway tries members in order or rotates between them." action={add} /></Card>}
      <CapacityAdapter />
      {editing && <ComboModal combo={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  );
}
