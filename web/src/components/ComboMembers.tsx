import { ArrowDown, ArrowUp, GripVertical, ListChecks, TriangleAlert, X } from "lucide-react";
import { useState } from "react";
import { useCapsMap } from "../lib/hooks";
import { CapBadges } from "./CapBadges";
import { Badge, Button, cn } from "./ui";

/** The combo's members (or, with `pool`, a Capacity Adapter pool) in priority order: drag the handle or use the arrows; #1 is tried first. */
export function ComboMembers({ value, onChange, available, onPick, roundRobin, pool }: {
  value: string[]; onChange: (v: string[]) => void; available: Set<string>; onPick: () => void; roundRobin: boolean; pool?: boolean;
}) {
  const caps = useCapsMap(), [drag, setDrag] = useState<number | null>(null), [over, setOver] = useState<number | null>(null);
  const move = (from: number, to: number) => {
    if (from === to || to < 0 || to >= value.length) return;
    const n = [...value];
    n.splice(to, 0, n.splice(from, 1)[0]);
    onChange(n);
  };
  const missing = value.filter((m) => !available.has(m.toLowerCase())).length;

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-[13px] font-medium">Models <span className="text-muted font-normal">· {value.length} selected</span></div>
        <Button size="sm" onClick={onPick}><ListChecks className="size-4" />{pool ? "Add models" : "Select models"}</Button>
      </div>
      {value.length ? (
        <>
          <ol className="rounded-lg border border-border divide-y divide-border max-h-72 overflow-y-auto" onDragLeave={() => setOver(null)}>
            {value.map((m, i) => {
              const ok = available.has(m.toLowerCase()), cm = caps.get(m.toLowerCase());
              return (
                <li key={m} draggable onDragStart={(e) => { setDrag(i); e.dataTransfer.effectAllowed = "move"; }} onDragEnd={() => { setDrag(null); setOver(null); }}
                  onDragOver={(e) => { if (drag !== null) { e.preventDefault(); setOver(i); } }}
                  onDrop={(e) => { e.preventDefault(); if (drag !== null) move(drag, i); setDrag(null); setOver(null); }}
                  className={cn("flex items-center gap-1.5 pl-1.5 pr-1 min-h-11 bg-surface", drag === i && "opacity-40", over === i && drag !== null && drag !== i && "shadow-[inset_0_2px_0_var(--accent)]")}>
                  <span className="cursor-grab text-muted p-1 touch-none" aria-hidden title="Drag to reorder"><GripVertical className="size-4" /></span>
                  <Badge tone={i === 0 ? "accent" : "neutral"}><span title={roundRobin ? "Position in the rotation" : i === 0 ? "Tried first" : `Tried ${i + 1}${i === 1 ? "nd" : i === 2 ? "rd" : "th"}`}>#{i + 1}</span></Badge>
                  <span className="font-mono text-[13px] flex-1 truncate min-w-0" title={m}>{m}</span>
                  <CapBadges caps={cm?.effective} detected={cm?.detected} size="xs" className="sm:hidden" />
                  <CapBadges caps={cm?.effective} detected={cm?.detected} className="hidden sm:inline-flex" />
                  {!ok && <span className="inline-flex items-center gap-1 text-xs text-warn shrink-0" title="Not added to its provider: this member is skipped until it is added"><TriangleAlert className="size-3.5" aria-hidden />not added</span>}
                  <Button size="sm" variant="ghost" aria-label={`Move ${m} up`} disabled={i === 0} onClick={() => move(i, i - 1)}><ArrowUp className="size-4" /></Button>
                  <Button size="sm" variant="ghost" aria-label={`Move ${m} down`} disabled={i === value.length - 1} onClick={() => move(i, i + 1)}><ArrowDown className="size-4" /></Button>
                  <Button size="sm" variant="ghost" aria-label={`Remove ${m}`} onClick={() => onChange(value.filter((_, j) => j !== i))}><X className="size-4" /></Button>
                </li>
              );
            })}
          </ol>
          <p className="text-xs text-muted">{roundRobin ? "Requests rotate through this list; a failing member falls through to the next." : "Priority #1 is tried first; the next member is used when one fails or is cooling down."}</p>
          {!pool && <p className="text-xs text-muted">Models that can read images or reason are tried first when a request needs it.</p>}
          {missing > 0 && <p className="text-xs text-warn">{missing} member{missing === 1 ? " is" : "s are"} not added to a provider yet and will be skipped.</p>}
        </>
      ) : (
        <button type="button" onClick={onPick} className="w-full rounded-lg border border-dashed border-border px-4 py-6 text-[13px] text-muted hover:border-accent/60 hover:text-text cursor-pointer">
          {pool ? "No models yet — click to add models to this pool" : "No models yet — click to choose which models this combo uses"}
        </button>
      )}
    </div>
  );
}
