import { Activity, ArrowDownToLine, ArrowUpFromLine, Cpu, DatabaseZap, MemoryStick } from "lucide-react";
import { useState } from "react";
import { Card, cn, fmtNum, PageHeader, Skeleton } from "../components/ui";
import { Dot, fmtFull, Tip, useTip } from "../components/usage/shared";
import type { Stats, SysSnapshot, UsagePeriod } from "../lib/api";
import { useStats, useSystem } from "../lib/hooks";

const RANGES = [
  { label: "Today", period: "today", sub: "Today" },
  { label: "24h", period: "24h", sub: "Last 24 hours" },
  { label: "7d", period: "7d", sub: "Last 7 days" },
  { label: "30d", period: "30d", sub: "Last 30 days" },
] as const;
const COLORS = { input: "var(--accent)", output: "var(--ok)", cache: "var(--warn)" };

function StatCard({ icon, label, value, sub, color }: { icon: React.ReactNode; label: string; value: string; sub?: string; color: string }) {
  return (
    <Card className="p-4">
      <div className="flex items-center gap-2 text-muted text-[13px]"><span style={{ color }}>{icon}</span>{label}</div>
      <div className="mt-2 text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
      {sub && <div className="text-xs text-muted mt-0.5">{sub}</div>}
    </Card>
  );
}

const W = 720, H = 170;

function xLabels(series: Stats["series"], bucket: Stats["bucket"]) {
  const fmt = (t: string) => bucket === "day"
    ? new Date(t).toLocaleDateString([], { month: "short", day: "numeric" })
    : new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return [0, Math.floor(series.length / 2), series.length - 1].map((i, n) => series[i] && (
    <text key={n} x={n === 0 ? 0 : n === 1 ? W / 2 : W} textAnchor={n === 0 ? "start" : n === 1 ? "middle" : "end"} y={H + 16} fontSize="11" fill="var(--muted)">{fmt(series[i].t)}</text>
  ));
}

const when = (t: string, bucket: Stats["bucket"]) =>
  new Date(t).toLocaleString([], bucket === "day" ? { month: "short", day: "numeric" } : { hour: "2-digit", minute: "2-digit", month: "short", day: "numeric" });

/** One bar per bucket; the failed share is overlaid in the error colour. Hover (or touch) a column for the exact numbers. */
function RequestsChart({ s }: { s: Stats }) {
  const { ref, tip, at, clear } = useTip<number>();
  const max = Math.max(1, ...s.series.map((d) => d.requests));
  const gap = 3, bw = (W - gap * (s.series.length - 1)) / s.series.length;
  const hi = tip?.v, d = hi !== undefined ? s.series[hi] : undefined;
  return (
    <div ref={ref} className="relative" onPointerLeave={clear}>
      <svg viewBox={`0 0 ${W} ${H + 22}`} className="w-full h-auto touch-pan-y" role="img" aria-label="Requests over time">
        {[0.5, 1].map((f) => <line key={f} x1="0" x2={W} y1={H - H * f} y2={H - H * f} stroke="var(--border)" strokeDasharray="3 4" />)}
        {s.series.map((d, i) => {
          const h = (d.requests / max) * H, eh = (d.errors / max) * H, x = i * (bw + gap);
          return (
            <g key={d.t}>
              <rect x={x} y={H - h} width={bw} height={Math.max(h, d.requests ? 2 : 0)} rx="2" fill="var(--accent)" opacity={hi === undefined || hi === i ? 0.85 : 0.4} />
              {eh > 0 && <rect x={x} y={H - eh} width={bw} height={eh} rx="2" fill="var(--err)" />}
              {/* full-height column, so thin or empty bars can still be hovered */}
              <rect x={x - gap / 2} y="0" width={bw + gap} height={H} fill="transparent" onPointerMove={(e) => at(e, i)} onPointerDown={(e) => at(e, i)} />
            </g>
          );
        })}
        {xLabels(s.series, s.bucket)}
      </svg>
      <Tip tip={tip} boxRef={ref}>
        {d && <>
          <div className="font-medium mb-1">{when(d.t, s.bucket)}</div>
          <div className="tabular-nums"><Dot color="var(--accent)" />Requests: {fmtFull(d.requests)}</div>
          {d.errors > 0 && <div className="tabular-nums text-err">Failed: {fmtFull(d.errors)}</div>}
        </>}
      </Tip>
    </div>
  );
}

/** Input, output and cache tokens as three lines on a shared scale. */
function TokensChart({ s }: { s: Stats }) {
  const { ref, tip, at, clear } = useTip<number>();
  const lines = [
    { key: "input", label: "Input", color: COLORS.input, v: s.series.map((d) => d.promptTokens) },
    { key: "output", label: "Output", color: COLORS.output, v: s.series.map((d) => d.completionTokens) },
    { key: "cache", label: "Cache", color: COLORS.cache, v: s.series.map((d) => d.cacheTokens) },
  ];
  const max = Math.max(1, ...lines.flatMap((l) => l.v));
  const n = Math.max(1, s.series.length - 1);
  const pts = (v: number[]) => v.map((y, i) => `${(i / n) * W},${H - (y / max) * H}`).join(" ");
  const hi = tip?.v, d = hi !== undefined ? s.series[hi] : undefined;
  return (
    <div ref={ref} className="relative" onPointerLeave={clear}>
      <div className="flex gap-4 mb-2 text-[13px] text-muted">
        {lines.map((l) => <span key={l.key} className="inline-flex items-center gap-1.5"><i className="size-2 rounded-full" style={{ background: l.color }} />{l.label}</span>)}
      </div>
      <svg viewBox={`0 0 ${W} ${H + 22}`} className="w-full h-auto touch-pan-y" role="img" aria-label="Tokens over time">
        {[0.5, 1].map((f) => <line key={f} x1="0" x2={W} y1={H - H * f} y2={H - H * f} stroke="var(--border)" strokeDasharray="3 4" />)}
        <text x="0" y="11" fontSize="11" fill="var(--muted)">{fmtNum(max)}</text>
        {lines.map((l) => <polyline key={l.key} points={pts(l.v)} fill="none" stroke={l.color} strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />)}
        {hi !== undefined && <>
          <line x1={(hi / n) * W} x2={(hi / n) * W} y1="0" y2={H} stroke="var(--muted)" strokeDasharray="3 3" />
          {lines.map((l) => <circle key={l.key} cx={(hi / n) * W} cy={H - (l.v[hi] / max) * H} r="4" fill={l.color} stroke="var(--surface)" strokeWidth="2" />)}
        </>}
        {s.series.map((d, i) => (
          <rect key={d.t} x={(i / n) * W - W / n / 2} y="0" width={W / n} height={H} fill="transparent" onPointerMove={(e) => at(e, i)} onPointerDown={(e) => at(e, i)} />
        ))}
        {xLabels(s.series, s.bucket)}
      </svg>
      <Tip tip={tip} boxRef={ref}>
        {d && <>
          <div className="font-medium mb-1">{when(d.t, s.bucket)}</div>
          {lines.map((l) => <div key={l.key} className="tabular-nums"><Dot color={l.color} />{l.label}: {fmtFull(l.v[hi!])}</div>)}
        </>}
      </Tip>
    </div>
  );
}

function ModelsChart({ models }: { models: Stats["topModels"] }) {
  if (!models.length) return <p className="text-muted">No traffic yet.</p>;
  return (
    <ul className="space-y-3">
      {models.map((m) => (
        <li key={m.model}>
          <div className="flex justify-between gap-3 text-[13px]">
            <span className="font-mono truncate" title={m.model}>{m.model}</span>
            <span className="text-muted tabular-nums whitespace-nowrap">{fmtNum(m.requests)} req · {fmtNum(m.tokens)} tok</span>
          </div>
          <div className="h-2 mt-1 rounded-full bg-surface-2"><div className="h-full rounded-full bg-accent" style={{ width: `${(m.requests / models[0].requests) * 100}%` }} /></div>
        </li>
      ))}
    </ul>
  );
}

const MB = 1024 ** 2;
const bytes = (n: number) => (n >= 1024 * MB ? `${(n / 1024 / MB).toFixed(2)} GB` : `${(n / MB).toFixed(1)} MB`);

/** Two-minute sparkline of one series, scaled to `max`. Hover (or touch) for the value and time of a sample. */
function Spark({ values, times, max, label, fmt }: { values: number[]; times: string[]; max: number; label: string; fmt: (v: number) => string }) {
  const { ref, tip, at, clear } = useTip<number>();
  const W = 240, H = 44, n = Math.max(1, values.length - 1);
  const pts = values.map((y, i) => `${(i / n) * W},${H - (Math.min(y, max) / max) * H}`).join(" ");
  const hi = tip?.v;
  return (
    <div ref={ref} className="relative mt-3" onPointerLeave={clear}>
      <svg viewBox={`0 0 ${W} ${H}`} className="block w-full h-11 touch-pan-y" role="img" aria-label={label} preserveAspectRatio="none">
        <polyline points={pts} fill="none" stroke="var(--accent)" strokeWidth="2" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
        {/* a line, not a dot: the SVG is stretched, so a circle would turn into an ellipse */}
        {hi !== undefined && <line x1={(hi / n) * W} x2={(hi / n) * W} y1="0" y2={H} stroke="var(--muted)" strokeWidth="1" vectorEffect="non-scaling-stroke" />}
        {values.map((_, i) => <rect key={i} x={values.length < 2 ? 0 : (i / n) * W - W / n / 2} y="0" width={values.length < 2 ? W : W / n} height={H} fill="transparent" onPointerMove={(e) => at(e, i)} onPointerDown={(e) => at(e, i)} />)}
      </svg>
      <Tip tip={tip} boxRef={ref}>
        {hi !== undefined && <>
          <div className="font-medium tabular-nums">{fmt(values[hi])}</div>
          <div className="text-muted tabular-nums">{new Date(times[hi]).toLocaleTimeString()}</div>
        </>}
      </Tip>
    </div>
  );
}

/** CPU and memory of the Router-Go process only: nothing about the rest of the machine. */
function SystemCards({ s }: { s: SysSnapshot | undefined }) {
  if (!s) return <div className="grid lg:grid-cols-2 gap-3 mb-3"><Skeleton className="h-36" /><Skeleton className="h-36" /></div>;
  const c = s.current, rss = s.history.map((x) => x.rss);
  return (
    <div className="grid lg:grid-cols-2 gap-3 mb-3">
      <Card className="p-4">
        <div className="flex items-center justify-between gap-2 text-muted text-[13px]"><span className="inline-flex items-center gap-2"><Cpu className="size-4 text-accent" aria-hidden />CPU usage</span><span>Router-Go</span></div>
        <div className="mt-2 flex items-baseline gap-2"><span className="text-2xl font-semibold tabular-nums">{c.cpu.toFixed(1)}%</span><span className="text-xs text-muted">of {s.cpus} cores</span></div>
        <div className="text-xs text-muted mt-1.5">{s.goroutines} goroutines</div>
        <Spark values={s.history.map((x) => x.cpu)} times={s.history.map((x) => x.t)} fmt={(v) => `${v.toFixed(1)}%`} max={Math.max(5, ...s.history.map((x) => x.cpu))} label="Router-Go CPU usage, last two minutes" />
      </Card>
      <Card className="p-4">
        <div className="flex items-center justify-between gap-2 text-muted text-[13px]"><span className="inline-flex items-center gap-2"><MemoryStick className="size-4 text-accent" aria-hidden />RAM usage</span><span>Router-Go</span></div>
        <div className="mt-2 flex items-baseline gap-2"><span className="text-2xl font-semibold tabular-nums">{bytes(c.rss)}</span><span className="text-xs text-muted">{s.totalRam ? `/ ${bytes(s.totalRam)} total RAM` : "resident"}</span></div>
        <div className="text-xs text-muted mt-1.5">Go heap {bytes(s.goHeap)}</div>
        <Spark values={rss} times={s.history.map((x) => x.t)} fmt={bytes} max={Math.max(1, ...rss)} label="Router-Go memory, last two minutes" />
      </Card>
    </div>
  );
}

export default function Dashboard() {
  const system = useSystem();
  const [period, setPeriod] = useState<UsagePeriod>("today");
  const stats = useStats(period);
  const s = stats.data;
  const range = (
    <div role="group" aria-label="Time range" className="inline-flex rounded-lg border border-border bg-surface p-0.5">
      {RANGES.map((r) => (
        <button key={r.period} type="button" aria-pressed={period === r.period} onClick={() => setPeriod(r.period)}
          className={cn("h-8 px-3 rounded-md text-[13px] font-medium cursor-pointer transition-colors", period === r.period ? "bg-accent-soft text-accent" : "text-muted hover:text-text")}>
          {r.label}
        </button>
      ))}
    </div>
  );
  return (
    <>
      <PageHeader title="Dashboard" subtitle={RANGES.find((r) => r.period === period)?.sub} actions={range} />
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-3">
        {!s ? Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-[104px]" />) : <>
          <StatCard icon={<Activity className="size-4" />} color={COLORS.input} label="Total requests" value={fmtFull(s.requests)} sub={s.errors ? `${s.errors} failed` : "no failures"} />
          <StatCard icon={<ArrowDownToLine className="size-4" />} color={COLORS.input} label="Total input" value={fmtFull(s.promptTokens)} sub="tokens" />
          <StatCard icon={<DatabaseZap className="size-4" />} color={COLORS.cache} label="Total cache" value={fmtFull(s.cacheTokens)} sub="cached tokens" />
          <StatCard icon={<ArrowUpFromLine className="size-4" />} color={COLORS.output} label="Total output" value={fmtFull(s.completionTokens)} sub="tokens" />
        </>}
      </div>
      <SystemCards s={system.data} />
      <div className="grid lg:grid-cols-2 gap-3 mb-3">
        <Card className="p-4">
          <h2 className="font-semibold mb-3">Requests</h2>
          {s ? <RequestsChart s={s} /> : <Skeleton className="h-48" />}
        </Card>
        <Card className="p-4">
          <h2 className="font-semibold mb-3">Tokens</h2>
          {s ? <TokensChart s={s} /> : <Skeleton className="h-48" />}
        </Card>
      </div>
      <Card className="p-4">
        <h2 className="font-semibold mb-3">Most used models</h2>
        {s ? <ModelsChart models={s.topModels} /> : <Skeleton className="h-32" />}
      </Card>
    </>
  );
}
