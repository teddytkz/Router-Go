import { useId, useState } from "react";
import { Card, EmptyState, SegmentedControl } from "../ui";
import type { UsageSummary } from "../../lib/api";
import { Dot, fmtAxis, fmtFull, niceMax, Tip, useTip } from "./shared";
import { LineChart } from "lucide-react";

type View = "tokens" | "requests";
const W = 720, H = 230, L = 46, R = 10, T = 10, B = 26;

const short = (t: string, bucket: "hour" | "day") => bucket === "day"
  ? new Date(t).toLocaleDateString([], { month: "short", day: "numeric" })
  : new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const full = (t: string, bucket: "hour" | "day") => new Date(t).toLocaleString([], bucket === "day" ? { dateStyle: "medium" } : { dateStyle: "medium", timeStyle: "short" });

export function UsageChart({ data }: { data: UsageSummary | undefined }) {
  const [view, setView] = useState<View>("tokens");
  const gid = useId().replace(/:/g, "");
  const { ref, tip, at, clear } = useTip<number>();
  const series = data?.series ?? [];
  const lines = view === "tokens"
    ? [{ key: "in", label: "Input", color: "var(--accent)", v: series.map((d) => d.promptTokens) }, { key: "out", label: "Output", color: "var(--ok)", v: series.map((d) => d.completionTokens) }]
    : [{ key: "req", label: "Requests", color: "var(--accent)", v: series.map((d) => d.requests) }];
  const max = niceMax(Math.max(0, ...lines.flatMap((l) => l.v)));
  const n = series.length - 1, pw = W - L - R, ph = H - T - B;
  const x = (i: number) => L + (n > 0 ? (i / n) * pw : pw / 2);
  const y = (v: number) => T + (1 - v / max) * ph;
  const path = (v: number[]) => v.map((p, i) => `${i ? "L" : "M"}${x(i)},${y(p)}`).join(" ");
  const labelIdx = [...new Set([0, 1, 2, 3, 4].map((k) => Math.round((k * n) / 4)))];
  const hi = tip?.v;
  const d = hi !== undefined ? series[hi] : undefined;

  return (
    <Card className="p-4 h-full">
      <div className="flex flex-wrap items-center justify-between gap-2 mb-3">
        <h2 className="font-semibold">Usage over time</h2>
        <SegmentedControl small label="Chart metric" value={view} onChange={setView} options={[{ value: "tokens", label: "Tokens" }, { value: "requests", label: "Requests" }]} />
      </div>
      {!series.length ? <EmptyState icon={<LineChart className="size-6" />} title="No data for this period" />
        : (
          <div ref={ref} className="relative" onPointerLeave={clear}>
            <svg viewBox={`0 0 ${W} ${H}`} className="w-full h-auto touch-pan-y" role="img" aria-label={`${view === "tokens" ? "Tokens" : "Requests"} over time`}
              onPointerMove={(e) => {
                const r = e.currentTarget.getBoundingClientRect();
                const vx = ((e.clientX - r.left) / r.width) * W;
                at(e, n > 0 ? Math.min(n, Math.max(0, Math.round(((vx - L) / pw) * n))) : 0);
              }}>
              <defs>
                {lines.map((l) => (
                  <linearGradient key={l.key} id={`${gid}${l.key}`} x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor={l.color} stopOpacity="0.35" /><stop offset="100%" stopColor={l.color} stopOpacity="0" />
                  </linearGradient>
                ))}
              </defs>
              {[0, 1, 2, 3, 4].map((k) => (
                <g key={k}>
                  <line x1={L} x2={W - R} y1={y((max * k) / 4)} y2={y((max * k) / 4)} stroke="var(--border)" strokeDasharray={k ? "3 4" : undefined} />
                  <text x={L - 6} y={y((max * k) / 4) + 4} textAnchor="end" fontSize="11" fill="var(--muted)">{fmtAxis((max * k) / 4)}</text>
                </g>
              ))}
              {lines.map((l) => (
                <g key={l.key}>
                  {n > 0 && <path d={`${path(l.v)} L${x(n)},${y(0)} L${x(0)},${y(0)} Z`} fill={`url(#${gid}${l.key})`} />}
                  {n > 0 ? <path d={path(l.v)} fill="none" stroke={l.color} strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" /> : <circle cx={x(0)} cy={y(l.v[0])} r="3" fill={l.color} />}
                </g>
              ))}
              {labelIdx.map((i, k) => (
                <text key={i} x={x(i)} y={H - 6} fontSize="11" fill="var(--muted)" textAnchor={k === 0 && n > 0 ? "start" : k === labelIdx.length - 1 && n > 0 ? "end" : "middle"}>{short(series[i].t, data!.bucket)}</text>
              ))}
              {hi !== undefined && (
                <g>
                  <line x1={x(hi)} x2={x(hi)} y1={T} y2={T + ph} stroke="var(--muted)" strokeDasharray="3 3" />
                  {lines.map((l) => <circle key={l.key} cx={x(hi)} cy={y(l.v[hi])} r="4" fill="var(--surface)" stroke={l.color} strokeWidth="2" />)}
                </g>
              )}
            </svg>
            <Tip tip={tip} boxRef={ref}>
              {d && <>
                <div className="font-medium mb-1">{full(d.t, data!.bucket)}</div>
                {lines.map((l) => <div key={l.key} className="tabular-nums"><Dot color={l.color} />{l.label}: {fmtFull(l.v[hi!])}</div>)}
                {view === "requests" && d.errors > 0 && <div className="tabular-nums text-err">Failed: {fmtFull(d.errors)}</div>}
              </>}
            </Tip>
          </div>
        )}
      {view === "tokens" && series.length > 0 && (
        <div className="flex gap-4 mt-2 text-xs text-muted">{lines.map((l) => <span key={l.key}><Dot color={l.color} />{l.label}</span>)}</div>
      )}
    </Card>
  );
}
