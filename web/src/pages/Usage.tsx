import { Activity, ArrowDownToLine, ArrowUpFromLine, DatabaseZap, Trash2 } from "lucide-react";
import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { BreakdownCharts } from "../components/usage/BreakdownCharts";
import { ProviderActivity } from "../components/usage/ProviderActivity";
import { LivePanel } from "../components/usage/LivePanel";
import { RequestLogs } from "../components/usage/RequestLogs";
import { UsageChart } from "../components/usage/UsageChart";
import { UsageTable } from "../components/usage/UsageTable";
import { fmtFull } from "../components/usage/shared";
import { Button, Card, cn, EmptyState, errText, Input, Modal, PageHeader, SegmentedControl, Skeleton, Tabs, useToast } from "../components/ui";
import type { UsagePeriod, UsageSummary } from "../lib/api";
import { useResetUsage, useUsageSummary } from "../lib/hooks";

const PERIODS: { value: UsagePeriod; label: string }[] = [
  { value: "month", label: "This Month" }, { value: "today", label: "Today" }, { value: "24h", label: "24h" }, { value: "7d", label: "7D" },
  { value: "30d", label: "30D" }, { value: "60d", label: "60D" }, { value: "all", label: "All" },
];
type Tab = "overview" | "logs";
const TABS: { value: Tab; label: string }[] = [{ value: "overview", label: "Overview" }, { value: "logs", label: "Request Logs" }];

function ResetModal({ onClose }: { onClose: () => void }) {
  const toast = useToast(), reset = useResetUsage();
  const [text, setText] = useState("");
  const ok = text.trim().toLowerCase() === "reset";
  const submit = () => ok && reset.mutate(undefined, {
    onSuccess: (r) => { toast("ok", `Deleted ${fmtFull(r.deleted)} usage record${r.deleted === 1 ? "" : "s"}`); onClose(); },
    onError: (e) => toast("err", errText(e)),
  });
  return (
    <Modal open onClose={onClose} title="Reset usage"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="danger" loading={reset.isPending} disabled={!ok} onClick={submit}>Reset usage</Button></>}>
      <p className="text-muted">This permanently deletes all recorded usage and request logs. It cannot be undone.</p>
      <label className="block space-y-1.5">
        <span className="text-[13px] font-medium">Type <code className="font-mono text-err">reset</code> to confirm</span>
        <Input value={text} onChange={(e) => setText(e.target.value)} onKeyDown={(e) => e.key === "Enter" && submit()} autoComplete="off" />
      </label>
    </Modal>
  );
}

function StatCard({ icon, label, value, color }: { icon: React.ReactNode; label: string; value: string; color: string }) {
  return (
    <Card className="p-4 text-center">
      <div className="flex items-center justify-center gap-2 text-muted text-[13px]"><span style={{ color }}>{icon}</span>{label}</div>
      <div className="mt-2 text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
    </Card>
  );
}

function Overview({ data, stale }: { data: UsageSummary | undefined; stale: boolean }) {
  return (
    <div className={cn("space-y-3 transition-opacity", stale && "opacity-60")} aria-busy={stale}>
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        {!data ? Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-[92px]" />) : <>
          <StatCard icon={<Activity className="size-4" />} color="var(--accent)" label="Total Requests" value={fmtFull(data.requests)} />
          <StatCard icon={<ArrowDownToLine className="size-4" />} color="var(--accent)" label="Total Input Tokens" value={fmtFull(data.promptTokens)} />
          <StatCard icon={<DatabaseZap className="size-4" />} color="var(--warn)" label="Cached Tokens" value={fmtFull(data.cacheTokens)} />
          <StatCard icon={<ArrowUpFromLine className="size-4" />} color="var(--ok)" label="Output Tokens" value={fmtFull(data.completionTokens)} />
        </>}
      </div>
      <div className="grid lg:grid-cols-3 gap-3">
        <div className="lg:col-span-2 min-w-0"><ProviderActivity data={data} /></div>
        <div className="min-w-0"><LivePanel data={data} /></div>
      </div>
      {data ? <UsageChart data={data} /> : <Skeleton className="h-80" />}
      <BreakdownCharts rows={data?.breakdown} loading={!data} />
      {data ? <UsageTable rows={data.breakdown} /> : <Skeleton className="h-64" />}
    </div>
  );
}

export default function Usage() {
  const [sp, setSp] = useSearchParams();
  const period = PERIODS.find((p) => p.value === sp.get("period"))?.value ?? "today";
  const tab = TABS.find((t) => t.value === sp.get("tab"))?.value ?? "overview";
  const set = (k: string, v: string) => setSp((prev) => { const n = new URLSearchParams(prev); n.set(k, v); return n; }, { replace: true });
  const q = useUsageSummary(period);
  const [resetting, setResetting] = useState(false);
  return (
    <>
      <PageHeader title="Usage" subtitle="Requests, tokens and activity through the gateway" />
      <Card className="p-3 mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3 min-w-0 max-w-full">
          <span className="text-[13px] font-medium text-muted">Time period</span>
          <SegmentedControl label="Time period" value={period} onChange={(v) => set("period", v)} options={PERIODS} />
        </div>
        <Button variant="danger" onClick={() => setResetting(true)}><Trash2 className="size-4" aria-hidden />Reset usage</Button>
      </Card>
      <Tabs label="Usage sections" value={tab} onChange={(v) => set("tab", v)} tabs={TABS} />
      <div className="mt-4">
        {tab === "logs" ? <RequestLogs />
          : q.isError && !q.data ? <Card><EmptyState icon={<Activity className="size-6" />} title="Could not load usage" body={errText(q.error)} action={<Button onClick={() => q.refetch()}>Retry</Button>} /></Card>
          : <Overview data={q.data} stale={q.isPlaceholderData} />}
      </div>
      {resetting && <ResetModal onClose={() => setResetting(false)} />}
    </>
  );
}
