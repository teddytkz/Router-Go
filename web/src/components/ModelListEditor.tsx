import { ArrowDown, ArrowUp, Plus, X } from "lucide-react";
import { useId, useState } from "react";
import { Button, Input } from "./ui";

/** Editable list of model identifiers: pick from `options` (datalist) or type free text. Order is user-controlled. */
export function ModelListEditor({ value, onChange, options, max = 200, maxLen = 256, label = "Add model" }: {
  value: string[]; onChange: (v: string[]) => void; options: string[]; max?: number; maxLen?: number; label?: string;
}) {
  const [draft, setDraft] = useState("");
  const listId = useId();
  const add = () => {
    const v = draft.trim();
    if (!v || v.length > maxLen || value.length >= max) return;
    if (!value.some((x) => x.toLowerCase() === v.toLowerCase())) onChange([...value, v]);
    setDraft("");
  };
  const move = (i: number, d: number) => { const n = [...value]; [n[i], n[i + d]] = [n[i + d], n[i]]; onChange(n); };
  const left = options.filter((o) => !value.includes(o));
  return (
    <div className="space-y-2">
      <div className="flex gap-2">
        <Input aria-label={label} list={listId} className="font-mono" value={draft} maxLength={maxLen} placeholder="Pick or type prefix/model, alias or combo"
          onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
        <datalist id={listId}>{left.map((o) => <option key={o} value={o} />)}</datalist>
        <Button onClick={add} disabled={!draft.trim() || value.length >= max}><Plus className="size-4" />Add</Button>
      </div>
      {value.length ? (
        <ol className="rounded-lg border border-border divide-y divide-border max-h-64 overflow-y-auto">
          {value.map((m, i) => (
            <li key={m} className="flex items-center gap-1 pl-3 pr-1 h-9">
              <span className="text-xs text-muted tabular-nums w-5">{i + 1}</span>
              <span className="font-mono text-[13px] flex-1 truncate" title={m}>{m}</span>
              <Button size="sm" variant="ghost" aria-label={`Move ${m} up`} disabled={i === 0} onClick={() => move(i, -1)}><ArrowUp className="size-4" /></Button>
              <Button size="sm" variant="ghost" aria-label={`Move ${m} down`} disabled={i === value.length - 1} onClick={() => move(i, 1)}><ArrowDown className="size-4" /></Button>
              <Button size="sm" variant="ghost" aria-label={`Remove ${m}`} onClick={() => onChange(value.filter((_, j) => j !== i))}><X className="size-4" /></Button>
            </li>
          ))}
        </ol>
      ) : <p className="text-xs text-muted">Nothing added yet.</p>}
    </div>
  );
}
