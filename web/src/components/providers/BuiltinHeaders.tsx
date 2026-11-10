// Collapsible "Custom Headers" card of a built-in provider: edit the headers sent with every upstream request.
import { ChevronDown, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import type { BuiltinProvider } from "../../lib/api";
import { useResetHeaders, useSaveHeaders } from "../../lib/hooks";
import { Badge, Button, Card, ConfirmModal, errText, Input, cn, useToast } from "../ui";

type Row = { key: number; name: string; value: string };

function validate(rows: Row[]): string | null {
  const seen = new Set<string>();
  for (const r of rows) {
    const n = r.name.trim().toLowerCase();
    if (!n) return "Header names cannot be empty.";
    if (/[\s:]/.test(r.name.trim())) return `"${r.name.trim()}" is not a valid header name (no spaces or colons).`;
    if (seen.has(n)) return `"${r.name.trim()}" appears more than once.`;
    seen.add(n);
  }
  return null;
}

function Form({ p }: { p: BuiltinProvider }) {
  const toast = useToast(), save = useSaveHeaders(), reset = useResetHeaders();
  const [rows, setRows] = useState<Row[]>(() => Object.entries(p.headers.effective).map(([name, value], i) => ({ key: i, name, value })));
  const [next, setNext] = useState(rows.length), [confirm, setConfirm] = useState(false), [serverErr, setServerErr] = useState("");
  const err = validate(rows), defaults = p.headers.defaults;
  const edit = (key: number, patch: Partial<Row>) => { setServerErr(""); setRows((s) => s.map((r) => (r.key === key ? { ...r, ...patch } : r))); };
  const submit = () => save.mutate({ id: p.id, headers: Object.fromEntries(rows.map((r) => [r.name.trim(), r.value])) }, { onSuccess: () => toast("ok", "Headers saved"), onError: (e) => setServerErr(errText(e)) });
  return (
    <div className="p-4 space-y-3">
      <p className="text-[13px] text-muted">Sent with every request to this provider. Credentials are managed by the API keys (Authorization is not editable). Some upstreams only accept their own clients; headers are sent exactly as written.</p>
      {rows.length === 0 && <p className="rounded-lg border border-dashed border-border px-4 py-4 text-[13px] text-muted">No custom headers.</p>}
      <ul className="space-y-2">
        {rows.map((r) => {
          const custom = defaults[r.name.trim()] !== r.value;
          return (
            <li key={r.key} className="flex flex-wrap items-center gap-2">
              <Input aria-label="Header name" className="flex-1 basis-40 font-mono" placeholder="Header-Name" value={r.name} onChange={(e) => edit(r.key, { name: e.target.value })} />
              <Input aria-label={`Value of ${r.name || "header"}`} className="flex-[2] basis-48 font-mono" placeholder="value" value={r.value} onChange={(e) => edit(r.key, { value: e.target.value })} />
              {custom && <Badge tone="accent">customised</Badge>}
              <Button size="sm" variant="ghost" className="hover:text-err" aria-label={`Remove header ${r.name}`} onClick={() => { setServerErr(""); setRows((s) => s.filter((x) => x.key !== r.key)); }}><Trash2 className="size-4" /></Button>
            </li>
          );
        })}
      </ul>
      {(err && rows.length > 0 ? err : serverErr) && <p role="alert" className="text-xs text-err break-words">{(err && rows.length > 0 ? err : serverErr)}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => { setRows((s) => [...s, { key: next, name: "", value: "" }]); setNext(next + 1); }}><Plus className="size-4" />Add header</Button>
        <div className="ml-auto flex gap-2">
          <Button size="sm" onClick={() => setConfirm(true)} disabled={!p.headers.customized}>Reset</Button>
          <Button size="sm" variant="primary" loading={save.isPending} disabled={!!err} onClick={submit}>Save</Button>
        </div>
      </div>
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={reset.isPending} confirmLabel="Reset" title="Reset headers" body="Restore the default headers for this provider? Your custom headers will be lost."
        onConfirm={() => reset.mutate(p.id, { onSuccess: () => { setConfirm(false); toast("ok", "Headers reset to defaults"); }, onError: (e) => { setConfirm(false); toast("err", errText(e)); } })} />
    </div>
  );
}

export function BuiltinHeaders({ p }: { p: BuiltinProvider }) {
  const [open, setOpen] = useState(p.headers.customized || Object.keys(p.headers.effective).length > 0);
  return (
    <Card>
      <button type="button" aria-expanded={open} onClick={() => setOpen((o) => !o)} className="flex w-full items-center justify-between gap-2 px-4 py-3 text-left cursor-pointer rounded-xl">
        <span className="flex items-center gap-2"><h2 className="font-semibold">Custom Headers</h2>{p.headers.customized && <Badge tone="accent">customised</Badge>}</span>
        <ChevronDown className={cn("size-4 text-muted transition-transform", open && "rotate-180")} aria-hidden />
      </button>
      {open && <div className="border-t border-border"><Form key={JSON.stringify(p.headers.effective)} p={p} /></div>}
    </Card>
  );
}
