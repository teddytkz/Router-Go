import { ChevronRight } from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { Badge, Card, cn, EmptyState, Select } from "../ui";
import type { UsageBreakdownRow } from "../../lib/api";
import { ago, fmtFull } from "./shared";
import { Table2 } from "lucide-react";

type View = "model" | "account" | "apiKey" | "endpoint";
type Row = { model: string; provider: string; account: string; keyName: string; endpoint: string; requests: number; prompt: number; completion: number; cache: number; lastUsed: string };
type Col = { field: keyof Row | "input" | "cached" | "output" | "total"; label: string; right?: boolean };

const VIEWS: Record<View, { label: string; groupBy: keyof Row; cols: Col[]; empty: string }> = {
  model: { label: "Usage by Model", groupBy: "model", empty: "No usage recorded yet.", cols: [{ field: "model", label: "Model" }, { field: "provider", label: "Provider" }] },
  account: { label: "Usage by Account", groupBy: "account", empty: "No account-specific usage recorded yet.", cols: [{ field: "account", label: "Account" }, { field: "model", label: "Model" }, { field: "provider", label: "Provider" }] },
  apiKey: { label: "Usage by API Key", groupBy: "keyName", empty: "No API key usage recorded yet.", cols: [{ field: "keyName", label: "API Key" }, { field: "model", label: "Model" }, { field: "provider", label: "Provider" }] },
  endpoint: { label: "Usage by Endpoint", groupBy: "endpoint", empty: "No endpoint usage recorded yet.", cols: [{ field: "endpoint", label: "Endpoint" }, { field: "model", label: "Model" }, { field: "provider", label: "Provider" }] },
};
const NUM_COLS: Col[] = [
  { field: "requests", label: "Requests", right: true }, { field: "lastUsed", label: "Last Used", right: true },
  { field: "input", label: "Input", right: true }, { field: "cached", label: "Cached", right: true }, { field: "output", label: "Output", right: true }, { field: "total", label: "Total", right: true },
];
const STRINGS = new Set(["model", "provider", "account", "keyName", "endpoint", "lastUsed"]);

const input = (r: Row) => Math.max(0, r.prompt - r.cache);
const val = (r: Row, f: Col["field"]): string | number =>
  f === "input" ? input(r) : f === "cached" ? r.cache : f === "output" ? r.completion : f === "total" ? r.prompt + r.completion : r[f];

/** Regroup the server breakdown into (group key -> detail rows by model+provider, summed). */
function build(rows: UsageBreakdownRow[], view: View) {
  const gb = VIEWS[view].groupBy;
  const detail = new Map<string, Row>();
  for (const b of rows) {
    const r: Row = {
      model: b.model, provider: b.providerName || b.provider, account: b.account || (b.connectionId ? `Account ${b.connectionId.slice(0, 8)}…` : "Unknown"),
      keyName: b.keyName || (b.apiKey ? b.apiKey : "No key"), endpoint: b.endpoint || "unknown",
      requests: b.requests, prompt: b.promptTokens, completion: b.completionTokens, cache: b.cacheTokens, lastUsed: b.lastUsed,
    };
    const k = `${r[gb]}\u0000${r.model}\u0000${r.provider}`, cur = detail.get(k);
    if (!cur) detail.set(k, r);
    else { cur.requests += r.requests; cur.prompt += r.prompt; cur.completion += r.completion; cur.cache += r.cache; if (r.lastUsed > cur.lastUsed) cur.lastUsed = r.lastUsed; }
  }
  const groups = new Map<string, { key: string; sum: Row; items: Row[] }>();
  for (const r of detail.values()) {
    const key = String(r[gb]);
    const g = groups.get(key) ?? { key, sum: { model: "", provider: "", account: "", keyName: "", endpoint: "", requests: 0, prompt: 0, completion: 0, cache: 0, lastUsed: "" }, items: [] };
    g.items.push(r); g.sum.requests += r.requests; g.sum.prompt += r.prompt; g.sum.completion += r.completion; g.sum.cache += r.cache;
    if (r.lastUsed > g.sum.lastUsed) g.sum.lastUsed = r.lastUsed;
    groups.set(key, g);
  }
  return [...groups.values()];
}

const storeKey = (v: View) => `usage-table:expanded:${v}`;
function loadExpanded(v: View): Set<string> {
  try { return new Set(JSON.parse(localStorage.getItem(storeKey(v)) ?? "[]")); } catch { return new Set(); }
}

function TableBody({ view, rows }: { view: View; rows: UsageBreakdownRow[] }) {
  const cfg = VIEWS[view], cols = [...cfg.cols, ...NUM_COLS];
  const [expanded, setExpanded] = useState(() => loadExpanded(view));
  const [sort, setSort] = useState<{ f: Col["field"]; asc: boolean }>({ f: "total", asc: false });
  const toggle = (k: string) => setExpanded((p) => {
    const n = new Set(p); n.has(k) ? n.delete(k) : n.add(k);
    try { localStorage.setItem(storeKey(view), JSON.stringify([...n])); } catch { /* storage unavailable */ }
    return n;
  });
  const cmp = (a: string | number, b: string | number) => (typeof a === "number" && typeof b === "number" ? a - b : String(a).localeCompare(String(b)));
  const groups = useMemo(() => {
    const dir = sort.asc ? 1 : -1;
    const gs = build(rows, view);
    for (const g of gs) g.items.sort((a, b) => dir * cmp(val(a, sort.f), val(b, sort.f)));
    // Group-level string columns (provider, model...) are blank on the summary: fall back to the first sorted item.
    const gv = (g: (typeof gs)[number]) => { const v = val(g.sum, sort.f); return v === "" ? val(g.items[0], sort.f) : v; };
    return gs.sort((a, b) => dir * cmp(gv(a), gv(b)));
  }, [rows, view, sort]);

  const lead = cfg.cols.length;
  const nums = (r: Row) => <>
    <td className="px-4 py-2.5 text-right tabular-nums">{fmtFull(r.requests)}</td>
    <td className="px-4 py-2.5 text-right text-muted whitespace-nowrap">{ago(r.lastUsed)}</td>
    <td className="px-4 py-2.5 text-right tabular-nums">{fmtFull(input(r))}</td>
    <td className={cn("px-4 py-2.5 text-right tabular-nums", !r.cache && "text-muted")}>{r.cache ? fmtFull(r.cache) : "—"}</td>
    <td className="px-4 py-2.5 text-right tabular-nums">{fmtFull(r.completion)}</td>
    <td className="px-4 py-2.5 text-right tabular-nums font-medium">{fmtFull(r.prompt + r.completion)}</td>
  </>;

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px] whitespace-nowrap">
        <thead>
          <tr className="text-left text-muted border-b border-border">
            {cols.map((c) => (
              <th key={c.field} scope="col" className={cn("px-4 h-10 font-medium whitespace-nowrap", c.right && "text-right")}
                aria-sort={sort.f === c.field ? (sort.asc ? "ascending" : "descending") : "none"}>
                <button type="button" className="cursor-pointer hover:text-text inline-flex items-center gap-1"
                  onClick={() => setSort((s) => ({ f: c.field, asc: s.f === c.field ? !s.asc : STRINGS.has(c.field) }))}>
                  {c.label}<span aria-hidden className={sort.f === c.field ? "" : "opacity-30"}>{sort.f === c.field ? (sort.asc ? "↑" : "↓") : "↕"}</span>
                </button>
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {groups.map((g) => {
            const open = expanded.has(g.key);
            return (
              <Fragment key={g.key}>
                <tr className="cursor-pointer hover:bg-surface-2/60" onClick={() => toggle(g.key)}>
                  <td className="px-4 py-2.5">
                    <button type="button" aria-expanded={open} aria-label={`${open ? "Collapse" : "Expand"} ${g.key}`} className="flex items-center gap-2 font-medium cursor-pointer text-left"
                      onClick={(e) => { e.stopPropagation(); toggle(g.key); }}>
                      <ChevronRight className={cn("size-4 text-muted transition-transform shrink-0", open && "rotate-90")} aria-hidden />
                      <span className={view !== "account" && view !== "apiKey" ? "font-mono" : ""}>{g.key}</span>
                      <Badge>{g.items.length}</Badge>
                    </button>
                  </td>
                  {cfg.cols.slice(1).map((c) => <td key={c.field} className="px-4 py-2.5 text-muted">—</td>)}
                  {nums(g.sum)}
                </tr>
                {open && g.items.map((r) => (
                  <tr key={`${r.model}|${r.provider}`} className="bg-surface-2/30 hover:bg-surface-2/60">
                    {cfg.cols.map((c, i) => (
                      <td key={c.field} className={cn("px-4 py-2.5", i === 0 && "pl-10 text-muted", c.field === "model" && "font-mono")}>
                        {c.field === "provider" ? <Badge>{r.provider}</Badge> : String(r[c.field as keyof Row])}
                      </td>
                    ))}
                    {nums(r)}
                  </tr>
                ))}
              </Fragment>
            );
          })}
          {!groups.length && <tr><td colSpan={lead + NUM_COLS.length}><EmptyState icon={<Table2 className="size-6" />} title={cfg.empty} /></td></tr>}
        </tbody>
      </table>
    </div>
  );
}

export function UsageTable({ rows }: { rows: UsageBreakdownRow[] }) {
  const [view, setView] = useState<View>("model");
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between gap-3 p-4 border-b border-border">
        <h2 className="font-semibold whitespace-nowrap shrink-0">Usage breakdown</h2>
        <Select aria-label="Group usage by" className="w-52 min-w-0" value={view} onChange={(e) => setView(e.target.value as View)}>
          {Object.entries(VIEWS).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
        </Select>
      </div>
      <TableBody key={view} view={view} rows={rows} />
    </Card>
  );
}
