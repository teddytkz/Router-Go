import { Plus, Search, TriangleAlert } from "lucide-react";
import { useMemo, useState } from "react";
import { useCapsMap } from "../lib/hooks";
import { CapBadges } from "./CapBadges";
import { Badge, Button, cn, Input } from "./ui";

export type PickGroup = { key: string; label: string; models: string[] };

/**
 * Checklist of models grouped by provider. `selected` keeps the order in which models were chosen,
 * so what is already in a combo stays in place and new picks are appended.
 */
export function ModelPicker({ groups, selected, onChange, maxLen = 256, needs }: { groups: PickGroup[]; selected: string[]; onChange: (v: string[]) => void; maxLen?: number; needs?: { key: "vision" | "audioInput"; label: string } }) {
  const caps = useCapsMap(), [q, setQ] = useState(""), [draft, setDraft] = useState("");
  const needle = q.trim().toLowerCase();
  const sel = useMemo(() => new Set(selected.map((s) => s.toLowerCase())), [selected]);
  const known = useMemo(() => new Set(groups.flatMap((g) => g.models.map((m) => m.toLowerCase()))), [groups]);
  const extra = selected.filter((s) => !known.has(s.toLowerCase())); // members typed by hand or no longer available

  const toggle = (m: string) => onChange(sel.has(m.toLowerCase()) ? selected.filter((s) => s.toLowerCase() !== m.toLowerCase()) : [...selected, m]);
  const setGroup = (models: string[], on: boolean) => {
    const keep = selected.filter((s) => !models.some((m) => m.toLowerCase() === s.toLowerCase()));
    onChange(on ? [...keep, ...models] : keep);
  };
  const addDraft = () => {
    const v = draft.trim();
    if (v && v.length <= maxLen && !sel.has(v.toLowerCase())) onChange([...selected, v]);
    setDraft("");
  };

  // caps chips for a model; with `needs`, a warning on a picked model that lacks the capability
  const Extras = ({ m, on }: { m: string; on: boolean }) => {
    const cm = caps.get(m.toLowerCase());
    return (
      <>
        <CapBadges caps={cm?.effective} detected={cm?.detected} className="ml-auto" />
        {needs && on && cm && !cm.effective[needs.key] && <span className="inline-flex items-center gap-1 text-xs text-warn shrink-0" title={`This model cannot read ${needs.label}`}><TriangleAlert className="size-3.5" aria-hidden />no {needs.label}</span>}
      </>
    );
  };
  const shown = groups.map((g) => ({ ...g, visible: g.models.filter((m) => !needle || m.toLowerCase().includes(needle)) })).filter((g) => g.visible.length);
  return (
    <div className="space-y-4">
      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 size-4 text-muted" aria-hidden />
        <Input aria-label="Search models" className="pl-8" placeholder="Search models" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      {!groups.length ? <p className="rounded-lg border border-dashed border-border px-4 py-6 text-[13px] text-muted">No models are available yet. Add models to a provider first (Providers → a provider → Models), or type an ID below.</p>
        : !shown.length ? <p className="text-[13px] text-muted px-1">No models match.</p>
        : (
          <div className="space-y-4">
            {shown.map((g) => {
              const n = g.models.filter((m) => sel.has(m.toLowerCase())).length;
              return (
                <fieldset key={g.key} className="min-w-0">
                  <legend className="sr-only">{g.label}</legend>
                  <div className="flex items-center justify-between gap-2 mb-1.5">
                    <div className="flex items-center gap-2 text-[13px] font-semibold min-w-0"><span className="truncate">{g.label}</span>{n > 0 && <Badge tone="accent">{n} selected</Badge>}</div>
                    <button type="button" onClick={() => setGroup(g.visible, !g.visible.every((m) => sel.has(m.toLowerCase())))} className="text-xs text-accent hover:underline cursor-pointer rounded shrink-0">
                      {g.visible.every((m) => sel.has(m.toLowerCase())) ? "Clear" : "Select all"}
                    </button>
                  </div>
                  <ul className="rounded-lg border border-border divide-y divide-border">
                    {g.visible.map((m) => {
                      const on = sel.has(m.toLowerCase());
                      return (
                        <li key={m}>
                          <label className={cn("flex items-center gap-3 px-3 py-2 cursor-pointer hover:bg-surface-2/60", on && "bg-accent-soft/40")}>
                            <input type="checkbox" checked={on} onChange={() => toggle(m)} className="size-4 accent-[var(--accent)] shrink-0" />
                            <span className="font-mono text-[13px] truncate min-w-0" title={m}>{m}</span>
                            <Extras m={m} on={on} />
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                </fieldset>
              );
            })}
          </div>
        )}
      {extra.length > 0 && (
        <div>
          <div className="text-[13px] font-semibold mb-1.5">Other entries</div>
          <ul className="rounded-lg border border-border divide-y divide-border">
            {extra.map((m) => (
              <li key={m}><label className="flex items-center gap-3 px-3 py-2 cursor-pointer hover:bg-surface-2/60 bg-accent-soft/40">
                <input type="checkbox" checked onChange={() => toggle(m)} className="size-4 accent-[var(--accent)] shrink-0" />
                <span className="font-mono text-[13px] truncate min-w-0" title={m}>{m}</span>
                <Extras m={m} on />
              </label></li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex gap-2 border-t border-border pt-4">
        <Input aria-label="Add by ID" className="font-mono" placeholder="Or type an ID, alias or prefix/model" maxLength={maxLen} value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); addDraft(); } }} />
        <Button onClick={addDraft} disabled={!draft.trim()}><Plus className="size-4" />Add</Button>
      </div>
    </div>
  );
}
