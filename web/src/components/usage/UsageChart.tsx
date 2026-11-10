import { useEffect, useState } from "react";
import { Card, EmptyState, SegmentedControl } from "../ui";
import type { UsageSummary } from "../../lib/api";
import { Dot, fmtAxis, fmtFull, niceMax, Tip, useTip } from "./shared";
import { LineChart } from "lucide-react";

type View = "tokens" | "requests";
const H = 180, L = 40, R = 4, T = 8, B = 22;

const short = (t: string, bucket: "hour" | "day") => bucket === "day"
  ? new Date(t).toLocaleDateString([], { month: "short", day: "numeric" })
  : new Date(t).toLocaleTimeString([], { hour: "numeric" });
const full = (t: string, bucket: "hour" | "day") => new Date(t).toLocaleString([], bucket === "day" ? { dateStyle: "medium" } : { dateStyle: "medium", timeStyle: "short" });

/** One bar per bucket (hour for the 24h view). The SVG is drawn at the container's real pixel width, so type and bars keep their size on wide screens. */
export function UsageChart({ data }: { data: UsageSummary | undefined }) {
  const [view, setView] = useState<View>("tokens");
  const [w, setW] = useState(720);
  const { ref, tip, at, clear } = useTip<number>();
  const series = data?.series ?? [];
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(Math.max(240, el.clientWidth)));
    ro.observe(el); setW(Math.max(240, el.clientWidth));
    return () => ro.disconnect();
  }, [ref, series.length]);
  const lines = view === "tokens"
    ? [{ key: "in", label: "Input", color: "var(--accent)", v: series.map((d) => d.promptTokens) }, { key: "out", label: "Output", color: "var(--ok)", v: series.map((d) => d.completionTokens) }]
    : [{ key: "req", label: "Requests", color: "var(--accent)", v: series.map((d) => d.requests) }];
  const max = niceMax(Math.max(0, ...series.map((_, i) => lines.reduce((s, l) => s + l.v[i], 0))));
  const pw = w - L - R, ph = H - T - B, slot = pw / Math.max(series.length, 1), bw = Math.min(20, Math.max(3, slot * 0.62));
  const y = (v: number) => T + (1 - v / max) * ph;
  const step = Math.max(1, Math.ceil(series.length / Math.max(2, Math.floor(pw / 64))));
  const hi = tip?.v;
  const d = hi !== undefined ? series[hi] : undefined;

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center justify-between gap-2 mb-2">
        <h2 className="font-semibold">Usage over time <span className="ml-1 text-xs font-normal text-muted">last 24 hours, per hour</span></h2>
        <div className="flex items-center gap-4">
          {view === "tokens" && series.length > 0 && <div className="hidden sm:flex gap-3 text-xs text-muted">{lines.map((l) => <span key={l.key}><Dot color={l.color} />{l.label}</span>)}</div>}
          <SegmentedControl small label="Chart metric" value={view} onChange={setView} options={[{ value: "tokens", label: "Tokens" }, { value: "requests", label: "Requests" }]} />
        </div>
      </div>
      {!series.length ? <EmptyState icon={<LineChart className="size-6" />} title="No data for this period" />
        : (
          <div ref={ref} className="relative" onPointerLeave={clear}>
            <svg width={w} height={H} className="block touch-pan-y" role="img" aria-label={`${view === "tokens" ? "Tokens" : "Requests"} per hour, last 24 hours`}>
              {[0, 1, 2].map((k) => (
                <g key={k}>
                  <line x1={L} x2={w - R} y1={y((max * k) / 2)} y2={y((max * k) / 2)} stroke="var(--border)" strokeDasharray={k ? "3 4" : undefined} />
                  <text x={L - 6} y={y((max * k) / 2) + 4} textAnchor="end" fontSize="11" fill="var(--muted)">{fmtAxis((max * k) / 2)}</text>
                </g>
              ))}
              {series.map((pt, i) => {
                const cx = L + slot * i + slot / 2;
                let base = 0;
                return (
                  <g key={pt.t} onPointerMove={(e) => at(e, i)}>
                    <rect x={L + slot * i} y={T} width={slot} height={ph} fill="transparent" />
                    {lines.map((l) => {
                      const h = (l.v[i] / max) * ph, top = T + ph - base - h;
                      base += h;
                      return h > 0 ? <rect key={l.key} x={cx - bw / 2} y={top} width={bw} height={Math.max(h, 2)} rx="2" fill={l.color} opacity={hi !== undefined && hi !== i ? 0.45 : 1} /> : null;
                    })}
                    {i % step === 0 && <text x={cx} y={H - 6} fontSize="11" fill="var(--muted)" textAnchor="middle">{short(pt.t, data!.bucket)}</text>}
                  </g>
                );
              })}
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
    </Card>
  );
}
