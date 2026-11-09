import { Check, RefreshCw, ScrollText, X } from "lucide-react";
import { useState } from "react";
import { Badge, Button, Card, cn, EmptyState, fmtTime, Input, Select, Skeleton, Switch } from "../ui";
import { useConnections, useHistory, useNodes } from "../../lib/hooks";
import { fmtFull } from "./shared";

const LIMIT = 200;
const STATUS = { ok: ["OK", "text-ok"], error: ["FAILED", "text-err"], canceled: ["CANCELED", "text-warn"] } as const;

export function RequestLogs() {
  const [auto, setAuto] = useState(true), [q, setQ] = useState(""), [status, setStatus] = useState("");
  const hist = useHistory({ limit: LIMIT, offset: 0, provider: "", model: "", status }, auto ? 3000 : false);
  const nodes = useNodes(), conns = useConnections();
  const nodeName = (id: string) => nodes.data?.find((n) => n.id === id)?.name ?? id;
  const connName = (id?: string) => (id ? conns.data?.find((c) => c.id === id)?.name ?? id.slice(0, 8) : "—");
  const all = hist.data?.rows ?? [];
  const needle = q.trim().toLowerCase();
  const rows = needle ? all.filter((r) => [r.model, nodeName(r.provider), connName(r.connectionId)].some((s) => s.toLowerCase().includes(needle))) : all;
  const okN = all.filter((r) => r.status === "ok").length, failN = all.filter((r) => r.status === "error").length;

  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-3 p-4 border-b border-border">
        <div className="flex items-center gap-2 flex-wrap">
          <h2 className="font-semibold">Request Logs</h2>
          <Badge>{fmtFull(hist.data?.total ?? 0)} total</Badge>
          <Badge tone="ok"><Check className="size-3" aria-hidden />{okN} ok</Badge>
          <Badge tone="err"><X className="size-3" aria-hidden />{failN} failed</Badge>
        </div>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-2 text-[13px] text-muted">Auto-refresh (3s)<Switch checked={auto} onChange={setAuto} label="Auto-refresh every 3 seconds" /></label>
          <Button size="sm" onClick={() => hist.refetch()} disabled={hist.isFetching}><RefreshCw className={cn("size-4", hist.isFetching && "animate-spin")} aria-hidden />Refresh</Button>
        </div>
      </div>
      <div className="flex flex-wrap gap-2 p-4 border-b border-border">
        <Input aria-label="Search logs" className="sm:w-72" placeholder="Search model, provider or account" value={q} onChange={(e) => setQ(e.target.value)} />
        <Select aria-label="Status" className="w-40" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">All</option><option value="ok">Success</option><option value="error">Failed</option><option value="canceled">Canceled</option>
        </Select>
      </div>
      {hist.isLoading ? <div className="p-4 space-y-2">{Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="h-8" />)}</div>
        : !rows.length ? <EmptyState icon={<ScrollText className="size-6" />} title="No requests found" body={all.length ? "No log matches your search." : "Requests routed through /v1 appear here."} />
        : (
          <div className="max-h-[600px] overflow-auto">
            <table className="w-full text-xs font-mono">
              <thead className="sticky top-0 z-10 bg-surface shadow-[0_1px_0_var(--border)]">
                <tr className="text-left text-muted">
                  {["DateTime", "Model", "Provider", "Account", "Input", "Output", "Latency", "Status"].map((h, i) => (
                    <th key={h} scope="col" className={cn("px-3 h-9 font-medium whitespace-nowrap", i >= 4 && i <= 6 && "text-right")}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {rows.map((r) => {
                  const [label, tone] = STATUS[r.status as keyof typeof STATUS] ?? [r.status.toUpperCase(), "text-muted"];
                  return (
                    <tr key={r.id} className="hover:bg-surface-2/60">
                      <td className="px-3 py-1.5 whitespace-nowrap text-muted">{fmtTime(r.timestamp)}</td>
                      <td className="px-3 py-1.5 max-w-[220px] truncate" title={r.model}>{r.model}</td>
                      <td className="px-3 py-1.5"><Badge>{nodeName(r.provider)}</Badge></td>
                      <td className="px-3 py-1.5 whitespace-nowrap text-muted">{connName(r.connectionId)}</td>
                      <td className="px-3 py-1.5 text-right tabular-nums">{fmtFull(r.promptTokens)}</td>
                      <td className="px-3 py-1.5 text-right tabular-nums">{fmtFull(r.completionTokens)}</td>
                      <td className="px-3 py-1.5 text-right tabular-nums whitespace-nowrap">{r.latencyMs} ms</td>
                      <td className={cn("px-3 py-1.5 font-semibold", tone)}>{label}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      <div className="px-4 h-11 flex items-center border-t border-border text-[13px] text-muted">Showing {rows.length} of {fmtFull(hist.data?.total ?? 0)} records</div>
    </Card>
  );
}
