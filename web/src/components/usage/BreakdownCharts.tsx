import { BarChart3 } from "lucide-react";
import { useState } from "react";
import { Card, EmptyState, SegmentedControl, Skeleton } from "../ui";
import type { UsageBreakdownRow } from "../../lib/api";
import { Dot, fmtAxis, fmtFull, niceMax, PALETTE, Tip, useTip } from "./shared";

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

const W = 480, H = 220, L = 46, R = 8, T = 10, B = 32;

export function ProviderBars({ rows }: { rows: UsageBreakdownRow[] | undefined }) {
  const [view, setView] = useState<View>("tokens");
  const { ref, tip, at, clear } = useTip<number>();
  const items = group(rows ?? [], (r) => r.providerName || r.provider, view, 8);
  const max = niceMax(Math.max(0, ...items.map((i) => i[view])));
  const pw = W - L - R, ph = H - T - B, slot = pw / Math.max(items.length, 1), bw = Math.min(48, slot * 0.6);
  const y = (v: number) => T + (1 - v / max) * ph;
  const hi = tip?.v !== undefined ? items[tip.v] : undefined;
  return (
    <Card className="p-4 min-w-0">
      <Header title="By Provider" view={view} onChange={setView} />
      {!items.length ? <EmptyState icon={<BarChart3 className="size-6" />} title="No provider usage" />
        : (
          <div ref={ref} className="relative" onPointerLeave={clear}>
            <svg viewBox={`0 0 ${W} ${H}`} className="w-full h-auto" role="img" aria-label={`${view} by provider`}>
              {[0, 1, 2, 3, 4].map((k) => (
                <g key={k}>
                  <line x1={L} x2={W - R} y1={y((max * k) / 4)} y2={y((max * k) / 4)} stroke="var(--border)" strokeDasharray={k ? "3 4" : undefined} />
                  <text x={L - 6} y={y((max * k) / 4) + 4} textAnchor="end" fontSize="11" fill="var(--muted)">{fmtAxis((max * k) / 4)}</text>
                </g>
              ))}
              {items.map((it, i) => {
                const cx = L + slot * i + slot / 2, h = Math.max(2, (it[view] / max) * ph);
                return (
                  <g key={it.name} onPointerMove={(e) => at(e, i)}>
                    <rect x={cx - bw / 2} y={T + ph - h} width={bw} height={h} rx="3" fill={PALETTE[i % PALETTE.length]} opacity={tip && tip.v !== i ? 0.55 : 1} />
                    <rect x={cx - slot / 2} y={T} width={slot} height={ph + B} fill="transparent" />
                    <text x={cx} y={H - 10} textAnchor="middle" fontSize="11" fill="var(--muted)">{it.name.length > 10 ? it.name.slice(0, 9) + "…" : it.name}</text>
                  </g>
                );
              })}
            </svg>
            <Tip tip={tip} boxRef={ref}>
              {hi && <><div className="font-medium mb-1">{hi.name}</div><div className="tabular-nums">Tokens: {fmtFull(hi.tokens)}</div><div className="tabular-nums">Requests: {fmtFull(hi.requests)}</div></>}
            </Tip>
          </div>
        )}
    </Card>
  );
}

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
  return loading ? <div className="grid lg:grid-cols-2 gap-3"><Skeleton className="h-72" /><Skeleton className="h-72" /></div>
    : <div className="grid lg:grid-cols-2 gap-3"><ProviderBars rows={rows} /><TopModels rows={rows} /></div>;
}
