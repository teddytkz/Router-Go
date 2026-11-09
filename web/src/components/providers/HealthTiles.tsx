import { Skeleton } from "../ui";
import { cn } from "../ui";
import { useRoutingHealth } from "../../lib/hooks";
import { fmtMs } from "./shared";

function Tile({ label, value, tone }: { label: string; value: string | number; tone?: "ok" | "err" | "warn" }) {
  return (
    <div className="rounded-lg border border-border bg-surface px-3 py-2.5">
      <div className="text-xs text-muted">{label}</div>
      <div className={cn("text-xl font-semibold tabular-nums", tone === "ok" && "text-ok", tone === "err" && "text-err", tone === "warn" && "text-warn")}>{value}</div>
    </div>
  );
}

/** Provider counts by circuit-breaker state, plus mean response time over the last 15 minutes. */
export function HealthTiles() {
  const q = useRoutingHealth();
  if (q.isLoading) return <div className="grid grid-cols-2 sm:grid-cols-5 gap-3 mb-6">{[0, 1, 2, 3, 4].map((i) => <Skeleton key={i} className="h-[66px]" />)}</div>;
  const all = q.data ?? [];
  if (!all.length) return null;
  const count = (c: string) => all.filter((r) => r.circuit === c).length;
  const lat = all.map((r) => r.avgLatencyMs).filter((x): x is number => x != null);
  return (
    <div className="grid grid-cols-2 sm:grid-cols-5 gap-3 mb-6" role="group" aria-label="Provider health">
      <Tile label="Total" value={all.length} />
      <Tile label="Healthy" value={count("CLOSED")} tone="ok" />
      <Tile label="Tripped" value={count("OPEN")} tone="err" />
      <Tile label="Recovering" value={count("HALF_OPEN")} tone="warn" />
      <Tile label="Avg response" value={lat.length ? fmtMs(lat.reduce((s, x) => s + x, 0) / lat.length) : "—"} />
    </div>
  );
}
