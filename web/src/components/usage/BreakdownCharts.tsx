import { BarChart3 } from "lucide-react";
import { useState } from "react";
import { Card, EmptyState, SegmentedControl, Skeleton } from "../ui";
import type { UsageBreakdownRow } from "../../lib/api";
import { Dot, fmtFull, PALETTE } from "./shared";

type View = "tokens" | "requests";
type Item = { name: string; tokens: number; requests: number };
const OPTS: { value: View; label: string }[] = [{ value: "tokens", label: "Tokens" }, { value: "requests", label: "Requests" }];

function group(rows: UsageBreakdownRow[], key: (r: UsageBreakdownRow) => string, view: View, limit: number): Item[] {
  const m = new Map<string, Item>();
  for (const r of rows) {
    const k = key(r) || "unknown", it = m.get(k) ?? { name: k, tokens: 0, requests: 0 };
    it.tokens += r.promptTokens + r.completionTokens; it.requests += r.requests; m.set(k, it);
  }
  return [...m.values()].filter((i) => i[view] > 0).sort((a, b) => b[view] - a[view]).slice(0, limit);
}

const Header = ({ title, view, onChange }: { title: string; view: View; onChange: (v: View) => void }) => (
  <div className="flex items-center justify-between gap-2 mb-3">
    <h2 className="font-semibold">{title}</h2>
    <SegmentedControl small label={`${title} metric`} value={view} onChange={onChange} options={OPTS} />
  </div>
);

export function TopModels({ rows }: { rows: UsageBreakdownRow[] | undefined }) {
  const [view, setView] = useState<View>("tokens");
  const items = group(rows ?? [], (r) => r.model, view, 5);
  const top = items[0]?.[view] || 1;
  return (
    <Card className="p-4 min-w-0">
      <Header title="Top Models" view={view} onChange={setView} />
      {!items.length ? <EmptyState icon={<BarChart3 className="size-6" />} title="No model usage" />
        : (
          <ul className="space-y-3">
            {items.map((it, i) => (
              <li key={it.name} title={`${it.name}: ${fmtFull(it.tokens)} tokens, ${fmtFull(it.requests)} requests`}>
                <div className="flex justify-between gap-3 text-[13px]">
                  <span className="font-mono truncate min-w-0"><Dot color={PALETTE[i]} />{it.name}</span>
                  <span className="text-muted tabular-nums whitespace-nowrap">{fmtFull(it[view])}</span>
                </div>
                <div className="h-2 mt-1 rounded-full bg-surface-2" role="img" aria-label={`${it.name} ${fmtFull(it[view])} ${view}`}>
                  <div className="h-full rounded-full" style={{ width: `${(it[view] / top) * 100}%`, background: PALETTE[i] }} />
                </div>
              </li>
            ))}
          </ul>
        )}
    </Card>
  );
}

export function BreakdownCharts({ rows, loading }: { rows: UsageBreakdownRow[] | undefined; loading: boolean }) {
  return loading ? <Skeleton className="h-72" /> : <TopModels rows={rows} />;
}
