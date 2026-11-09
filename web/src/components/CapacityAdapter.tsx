import { AudioLines, Eye, type LucideIcon } from "lucide-react";
import { useMemo, useState } from "react";
import type { CapacityAdapter as Adapter, CapacityPool } from "../lib/api";
import { useCustomModels, useNodes, useSettings, useUpdateSettings } from "../lib/hooks";
import { ComboMembers } from "./ComboMembers";
import { ModelPicker, type PickGroup } from "./ModelPicker";
import { Button, Card, errText, Modal, Switch, useToast } from "./ui";

type Key = "vision" | "audioInput";
const MAX_POOL = 50;
const SECTIONS: { key: Key; title: string; label: string; what: string; Icon: LucideIcon }[] = [
  { key: "vision", title: "Vision", label: "images", what: "images (png, jpg, webp, …)", Icon: Eye },
  { key: "audioInput", title: "Audio", label: "audio", what: "audio input", Icon: AudioLines },
];
// on by default: an empty pool changes nothing, so turning it on only matters once models are added
const norm = (p?: CapacityPool): CapacityPool => ({ enabled: p?.enabled !== false, roundRobin: !!p?.roundRobin, models: p?.models ?? [] });

/** Fallback pools for requests that carry images/audio when no model of the requested combo can read them. */
export function CapacityAdapter() {
  const toast = useToast(), settings = useSettings(), save = useUpdateSettings();
  const custom = useCustomModels(), nodes = useNodes();
  const [pending, setPending] = useState<Adapter | null>(null); // shown until the server confirms, so toggles feel instant
  const [picking, setPicking] = useState<Key | null>(null), [draft, setDraft] = useState<string[]>([]), [showAll, setShowAll] = useState(false);

  const cur = pending ?? settings.data?.capacityAdapter;
  const pool = (k: Key) => norm(cur?.[k]);
  const update = (k: Key, patch: Partial<CapacityPool>) => {
    const next: Adapter = { vision: pool("vision"), audioInput: pool("audioInput"), [k]: { ...pool(k), ...patch } };
    setPending(next);
    save.mutate({ capacityAdapter: next }, { onError: (e) => toast("err", errText(e)), onSettled: () => setPending(null) });
  };

  const added = custom.data ?? [];
  const availableAll = useMemo(() => new Set(added.map((m) => `${m.providerAlias}/${m.id}`.toLowerCase())), [added]);
  const groups = useMemo<PickGroup[]>(() => {
    if (!picking) return [];
    const by = new Map<string, string[]>();
    for (const m of added) if (showAll || m.effective[picking]) by.set(m.providerAlias, [...(by.get(m.providerAlias) ?? []), `${m.providerAlias}/${m.id}`]);
    return [...by].map(([prefix, models]) => ({ key: prefix, label: nodes.data?.find((n) => n.prefix === prefix)?.name ?? prefix, models }));
  }, [added, nodes.data, picking, showAll]);
  const sec = SECTIONS.find((s) => s.key === picking);

  const open = (k: Key) => { setDraft(pool(k).models); setShowAll(false); setPicking(k); };
  const apply = () => { update(picking!, { models: draft }); setPicking(null); };

  return (
    <Card className="mt-8">
      <div className="px-4 py-3 border-b border-border">
        <h2 className="font-semibold">Capacity Adapter</h2>
        <p className="text-[13px] text-muted mt-0.5">Your model can't read images or audio? Requests that need it switch to a model in the pool below.</p>
      </div>
      <div className="p-4 space-y-4">
        {SECTIONS.map(({ key, title, what, Icon }) => {
          const p = pool(key);
          return (
            <section key={key} aria-label={`${title} pool`} className="rounded-lg border border-border p-3 space-y-3 min-w-0">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-center gap-2 min-w-0">
                  <span className="grid size-8 place-items-center rounded-lg bg-accent-soft text-accent shrink-0"><Icon className="size-4" aria-hidden /></span>
                  <div className="min-w-0"><h3 className="text-sm font-semibold">{title}</h3><p className="text-xs text-muted">Requests containing {what}</p></div>
                </div>
                <Switch checked={p.enabled} onChange={(v) => update(key, { enabled: v })} label={`Enable ${title} adapter`} />
              </div>
              <div className="flex items-center justify-between gap-3 text-[13px]">
                <span>Round-robin <span className="text-muted">· rotate the pool's starting model</span></span>
                <Switch checked={p.roundRobin} onChange={(v) => update(key, { roundRobin: v })} label={`Round-robin ${title} pool`} />
              </div>
              <ComboMembers pool value={p.models} onChange={(models) => update(key, { models })} available={availableAll} roundRobin={p.roundRobin} onPick={() => open(key)} />
              {p.enabled && !p.models.length && <p className="text-xs text-muted">No models in the pool: nothing to switch to</p>}
            </section>
          );
        })}
      </div>
      <p className="px-4 pb-4 text-xs text-muted">When no model of a request can read its images or audio, the pool is tried first, then the original models as a fallback.</p>
      {picking && sec && (
        <Modal open onClose={() => setPicking(null)} title={`Add ${sec.title.toLowerCase()} models`}
          footer={<><Button onClick={() => setPicking(null)}>Cancel</Button><Button variant="primary" disabled={draft.length > MAX_POOL} onClick={apply}>Apply · {draft.length} selected</Button></>}>
          <p className="text-[13px] text-muted -mt-1">Pick models that can read {sec.label}. Priority is set in the pool list.</p>
          <label className="flex items-center gap-2 text-[13px] text-muted cursor-pointer"><input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} className="size-4 accent-[var(--accent)]" />Show all models</label>
          {draft.length > MAX_POOL && <p role="alert" className="text-xs text-err">A pool holds at most {MAX_POOL} models.</p>}
          <ModelPicker groups={groups} selected={draft} onChange={setDraft} needs={{ key: picking, label: sec.label }} />
        </Modal>
      )}
    </Card>
  );
}
