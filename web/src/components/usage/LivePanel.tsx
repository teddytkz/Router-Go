import { Card, cn, EmptyState } from "../ui";
import type { UsageSummary } from "../../lib/api";
import { ago, fmtFull, useNow } from "./shared";
import { Inbox } from "lucide-react";

const lat = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`);

export function LivePanel({ data }: { data: UsageSummary | undefined }) {
  const now = useNow(30_000);
  const l = data?.live;
  const pills = [
    { label: "Live Requests · 5m", value: l ? fmtFull(l.requests) : "—" },
    { label: "Models Active · 5m", value: l ? fmtFull(l.modelsActive) : "—" },
    { label: "RPM", value: l ? (Math.round(l.rpm * 10) / 10).toString() : "—" },
    { label: "Avg Response", value: l ? lat(l.avgLatencyMs) : "—" },
  ];
  return (
    <Card className="p-4 h-full flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-2">
        {pills.map((p) => (
          <div key={p.label} className="rounded-lg bg-surface-2 px-3 py-2">
            <div className="text-[11px] text-muted truncate">{p.label}</div>
            <div className="text-lg font-semibold tabular-nums">{p.value}</div>
          </div>
        ))}
      </div>
      <h2 className="font-semibold">Recent Requests</h2>
      {!data?.recent.length ? <EmptyState icon={<Inbox className="size-5" />} title="No recent requests" />
        : (
          <ul className="max-h-64 lg:max-h-none lg:flex-1 lg:min-h-40 overflow-y-auto -mx-1 divide-y divide-border">
            {data.recent.slice(0, 6).map((r) => (
              <li key={r.id} className="flex items-center gap-2 px-1 py-2 text-[13px]">
                <span className={cn("size-2 rounded-full shrink-0", r.status === "ok" ? "bg-ok" : r.status === "canceled" ? "bg-warn" : "bg-err")} aria-label={r.status} />
                <span className="font-mono truncate flex-1 min-w-0" title={r.model}>{r.model}</span>
                <span className="tabular-nums whitespace-nowrap text-xs"><span className="text-accent">{fmtFull(r.promptTokens)}↑</span> <span className="text-ok">{fmtFull(r.completionTokens)}↓</span></span>
                <span className="text-xs text-muted whitespace-nowrap w-16 text-right">{ago(r.timestamp, now)}</span>
              </li>
            ))}
          </ul>
        )}
    </Card>
  );
}
