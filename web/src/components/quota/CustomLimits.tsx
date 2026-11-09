import { ChevronDown, Hourglass, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { LimitDialog } from "./LimitDialog";
import { ago, fmtAxis, fmtFull, useNow } from "../usage/shared";
import { Badge, Button, Card, cn, ConfirmModal, EmptyState, errText, Select, Skeleton, Switch, useToast } from "../ui";
import type { QuotaLimit, TrackedConnection } from "../../lib/api";
import { Link } from "react-router-dom";
import { useDeleteQuota, useQuota } from "../../lib/hooks";

function dur(ms: number) {
  const m = Math.max(0, Math.ceil(ms / 60000)), d = Math.floor(m / 1440), h = Math.floor((m % 1440) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m % 60}m` : `${m}m`;
}

function LimitCard({ l, now, onEdit, onDelete }: { l: QuotaLimit; now: number; onEdit: () => void; onDelete: () => void }) {
  const pct = l.limit > 0 ? (l.used / l.limit) * 100 : 0, tone = l.exhausted || pct >= 90 ? "err" : pct >= 70 ? "warn" : "ok";
  const left = new Date(l.resetAt).getTime() - now, title = l.name || `${l.targetName}`;
  return (
    <Card className="p-4 space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-semibold truncate" title={title}>{title}</h3>
            <Badge tone="accent">{l.scope === "provider" ? "Provider" : "API key"}</Badge>
            <Badge tone={l.enforce ? "warn" : "neutral"}>{l.enforce ? "Blocks at limit" : "Tracking only"}</Badge>
            {l.exhausted && <Badge tone="err">Exhausted</Badge>}
          </div>
          <p className="mt-0.5 text-[13px] text-muted break-words">{l.targetName} · {fmtFull(l.limit)} {l.metric} per {l.window} · {l.mode}</p>
        </div>
        <div className="flex items-center gap-1">
          <Button size="sm" variant="ghost" onClick={onEdit} aria-label={`Edit ${title}`}><Pencil className="size-4" />Edit</Button>
          <Button size="sm" variant="ghost" className="text-err hover:text-err" onClick={onDelete} aria-label={`Delete ${title}`}><Trash2 className="size-4" />Delete</Button>
        </div>
      </div>
      <div>
        <div role="progressbar" aria-label={`${title} usage`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, Math.round(pct))} className="h-2 overflow-hidden rounded-full bg-surface-2">
          <div className={`h-full rounded-full transition-[width] ${tone === "err" ? "bg-err" : tone === "warn" ? "bg-warn" : "bg-ok"}`} style={{ width: `${Math.min(100, pct)}%` }} />
        </div>
        <div className="mt-1.5 flex flex-wrap items-center justify-between gap-x-4 gap-y-0.5 text-[13px]">
          <span className="tabular-nums"><b className={tone === "err" ? "text-err" : ""}>{fmtFull(l.used)}</b> / {fmtFull(l.limit)} <span className="text-muted">({Math.round(pct)}%)</span> · <span className="text-muted">{fmtFull(Math.max(0, l.remaining))} remaining</span></span>
          <span className="text-muted">{left <= 0 ? "Resets now" : `${l.mode === "rolling" ? "Frees up in" : "Resets in"} ${dur(left)}`}</span>
        </div>
      </div>
    </Card>
  );
}

const pctOf = (l: QuotaLimit) => (l.limit > 0 ? (l.used / l.limit) * 100 : 0);
const toneOf = (l: QuotaLimit) => (l.exhausted || pctOf(l) >= 90 ? "err" : pctOf(l) >= 70 ? "warn" : "ok");
const barCls = { err: "bg-err", warn: "bg-warn", ok: "bg-ok" } as const;
const WIN = { hour: "hour", day: "day", week: "week", month: "month" } as const;

/** One limit as a compact bar: "100 req/day · 42 used · resets in 3h". */
function MiniLimit({ l, now, onEdit }: { l: QuotaLimit; now: number; onEdit: () => void }) {
  const pct = pctOf(l), tone = toneOf(l), left = new Date(l.resetAt).getTime() - now;
  return (
    <button type="button" onClick={onEdit} title={`${l.name || l.targetName} — click to edit`} className="block w-full text-left rounded-md px-2 py-1.5 hover:bg-surface-2/70 cursor-pointer">
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className="truncate"><b className="tabular-nums">{fmtFull(l.limit)}</b> {l.metric === "tokens" ? "tok" : "req"}/{WIN[l.window]}{l.enforce && <span className="text-warn"> · blocks</span>}</span>
        <span className={cn("tabular-nums shrink-0", tone === "err" && "text-err")}>{fmtAxis(l.used)} · {Math.round(pct)}%</span>
      </div>
      <div role="progressbar" aria-label={`${l.name || l.targetName} usage`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, Math.round(pct))} className="mt-1 h-1.5 overflow-hidden rounded-full bg-surface-2">
        <div className={cn("h-full rounded-full transition-[width]", barCls[tone])} style={{ width: `${Math.min(100, pct)}%` }} />
      </div>
      <div className="mt-0.5 text-[11px] text-muted">{left <= 0 ? "Resets now" : `${l.mode === "rolling" ? "Frees up in" : "Resets in"} ${dur(left)}`}</div>
    </button>
  );
}

function ConnectionRow({ c, limits, now, onLimit, onEdit }: { c: TrackedConnection; limits: QuotaLimit[]; now: number; onLimit: () => void; onEdit: (l: QuotaLimit) => void }) {
  const ok = c.isActive && c.testStatus !== "error";
  return (
    <li className={cn("grid gap-x-4 gap-y-2 px-4 py-3 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1.6fr)_minmax(0,1.4fr)]", !c.isActive && "opacity-60")}>
      <div className="min-w-0">
        <div className="flex items-center gap-2 min-w-0">
          <i aria-hidden className={cn("size-2 rounded-full shrink-0", !c.isActive ? "bg-muted" : ok ? "bg-ok" : "bg-err")} />
          <span className="font-medium truncate" title={c.name}>{c.name}</span>
          <Badge>{c.authType === "oauth" ? "OAuth" : "API key"}</Badge>
        </div>
        <p className="mt-0.5 text-xs text-muted">{c.lastUsed ? `Last used ${ago(c.lastUsed, now)}` : "Never used"}{!c.isActive && " · disabled"}</p>
      </div>
      <dl className="grid grid-cols-3 gap-2 text-[13px] tabular-nums">
        {([["Today", "today"], ["7 days", "week"], ["Month", "month"]] as const).map(([label, k]) => (
          <div key={k} className="min-w-0"><dt className="text-[11px] text-muted">{label}</dt><dd className="truncate">{fmtAxis(c.requests[k])} req</dd><dd className="truncate text-muted text-xs">{fmtAxis(c.tokens[k])} tok</dd></div>
        ))}
      </dl>
      <div className="min-w-0 space-y-1">
        {limits.map((l) => <MiniLimit key={l.id} l={l} now={now} onEdit={() => onEdit(l)} />)}
        <Button size="sm" variant="ghost" onClick={onLimit}><Plus className="size-4" />{limits.length ? "Add limit" : "Set limit"}</Button>
      </div>
    </li>
  );
}

type Sort = "limit" | "usage" | "name";

export function CustomLimits() {
  const [auto, setAuto] = useState(true), q = useQuota(auto), toast = useToast(), del = useDeleteQuota(), now = useNow(30_000);
  const [dialog, setDialog] = useState<{ edit?: QuotaLimit; preset?: { scope: "provider" | "connection"; target: string } } | null>(null), [confirm, setConfirm] = useState<QuotaLimit | null>(null);
  const [provider, setProvider] = useState(""), [sort, setSort] = useState<Sort>("limit"), [showLimits, setShowLimits] = useState(false);
  const limits = q.data?.limits ?? [], conns = q.data?.connections ?? [];

  const groups = useMemo(() => {
    const byP = new Map<string, { id: string; name: string; conns: TrackedConnection[] }>();
    for (const c of conns) {
      if (provider && c.provider !== provider) continue;
      if (!byP.has(c.provider)) byP.set(c.provider, { id: c.provider, name: c.providerName, conns: [] });
      byP.get(c.provider)!.conns.push(c);
    }
    const rows = [...byP.values()].map((g) => {
      const pl = limits.filter((l) => l.scope === "provider" && l.target === g.id);
      const cl = limits.filter((l) => l.scope === "connection" && g.conns.some((c) => c.id === l.target));
      return { ...g, conns: [...g.conns].sort((a, b) => a.priority - b.priority), pl, worst: Math.max(0, ...[...pl, ...cl].map(pctOf)), today: g.conns.reduce((s, c) => s + c.tokens.today, 0), reqToday: g.conns.reduce((s, c) => s + c.requests.today, 0), month: g.conns.reduce((s, c) => s + c.tokens.month, 0), reqMonth: g.conns.reduce((s, c) => s + c.requests.month, 0) };
    });
    return rows.sort((a, b) => (sort === "limit" ? b.worst - a.worst || b.today - a.today : sort === "usage" ? b.today - a.today : 0) || a.name.localeCompare(b.name));
  }, [conns, limits, provider, sort]);

  const providers = useMemo(() => [...new Map(conns.map((c) => [c.provider, c.providerName])).entries()].sort((a, b) => a[1].localeCompare(b[1])), [conns]);
  const reqToday = conns.reduce((s, c) => s + c.requests.today, 0), tokToday = conns.reduce((s, c) => s + c.tokens.today, 0);
  const near = limits.filter((l) => l.exhausted || pctOf(l) >= 70).length;

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3 mb-4">
        <div>
          <h2 className="text-lg font-semibold tracking-tight">Custom limits</h2>
          <p className="text-muted mt-0.5">What each provider and key has used, against the budgets you set for them</p>
        </div>
        <div className="flex items-center gap-2">
          <label className="flex items-center gap-2 text-[13px] text-muted">Auto-refresh<Switch checked={auto} onChange={setAuto} label="Auto-refresh custom limits every 15 seconds" /></label>
          <Button onClick={() => q.refetch()} disabled={q.isFetching} aria-label="Refresh custom limits"><RefreshCw className={cn("size-4", q.isFetching && "animate-spin")} /></Button>
          <Button variant="primary" onClick={() => setDialog({})}><Plus className="size-4" />New limit</Button>
        </div>
      </div>
      {q.isError && !q.data ? <Card><EmptyState icon={<Hourglass className="size-6" />} title="Could not load the tracker" body={errText(q.error)} action={<Button onClick={() => q.refetch()}>Retry</Button>} /></Card> : (
        <div className="space-y-5">
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
            {!q.data ? [0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-[76px]" />) : <>
              {[["Keys tracked", String(conns.length), `${limits.filter((l) => l.scope === "connection").length} with a key limit`], ["Requests today", fmtAxis(reqToday), "all providers"], ["Tokens today", fmtAxis(tokToday), "input + output"], ["Limits near or over", String(near), `of ${limits.length} set`]].map(([label, value, sub]) => (
                <Card key={label} className="p-3"><div className="text-xs text-muted">{label}</div><div className={cn("text-xl font-semibold tabular-nums", label === "Limits near or over" && near > 0 && "text-warn")}>{value}</div><div className="text-[11px] text-muted">{sub}</div></Card>
              ))}
            </>}
          </div>

          {conns.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <Select aria-label="Provider" className="max-w-56" value={provider} onChange={(e) => setProvider(e.target.value)}>
                <option value="">All providers</option>{providers.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
              </Select>
              <Select aria-label="Sort" className="max-w-56" value={sort} onChange={(e) => setSort(e.target.value as Sort)}>
                <option value="limit">Closest to limit</option><option value="usage">Most used today</option><option value="name">Name</option>
              </Select>
            </div>
          )}

          {!q.data ? <Skeleton className="h-48" /> : conns.length === 0 ? (
            <Card><EmptyState icon={<Hourglass className="size-6" />} title="Nothing to track yet" body="Quota Tracker lists every provider key and what it has used. Add a key first, then set a budget on it."
              action={<Link to="/dashboard/providers"><Button variant="primary">Go to Providers</Button></Link>} /></Card>
          ) : groups.map((g) => (
            <Card key={g.id} className="overflow-hidden">
              <div className="flex flex-wrap items-start justify-between gap-3 px-4 py-3 border-b border-border">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2"><h2 className="font-semibold truncate">{g.name}</h2><Badge>{g.conns.length} key{g.conns.length === 1 ? "" : "s"}</Badge></div>
                  <p className="text-[13px] text-muted tabular-nums">Today {fmtAxis(g.reqToday)} req · {fmtAxis(g.today)} tok · Month {fmtAxis(g.reqMonth)} req · {fmtAxis(g.month)} tok</p>
                </div>
                <div className="flex items-start gap-2 min-w-0 flex-1 basis-64 justify-end">
                  <div className="min-w-0 flex-1 max-w-sm">{g.pl.map((l) => <MiniLimit key={l.id} l={l} now={now} onEdit={() => setDialog({ edit: l })} />)}</div>
                  <Button size="sm" onClick={() => setDialog({ preset: { scope: "provider", target: g.id } })}><Plus className="size-4" />Provider limit</Button>
                </div>
              </div>
              <ul className="divide-y divide-border">
                {g.conns.map((c) => <ConnectionRow key={c.id} c={c} now={now} limits={limits.filter((l) => l.scope === "connection" && l.target === c.id)}
                  onLimit={() => setDialog({ preset: { scope: "connection", target: c.id } })} onEdit={(l) => setDialog({ edit: l })} />)}
              </ul>
            </Card>
          ))}

          {limits.length > 0 && (
            <section>
              <button type="button" aria-expanded={showLimits} onClick={() => setShowLimits((o) => !o)} className="flex items-center gap-2 text-sm font-semibold cursor-pointer rounded">
                <ChevronDown className={cn("size-4 transition-transform", !showLimits && "-rotate-90")} aria-hidden />All limits ({limits.length})
              </button>
              {showLimits && <div className="mt-3 space-y-3">{limits.map((l) => <LimitCard key={l.id} l={l} now={now} onEdit={() => setDialog({ edit: l })} onDelete={() => setConfirm(l)} />)}</div>}
            </section>
          )}
        </div>
      )}
      {dialog && <LimitDialog edit={dialog.edit} preset={dialog.preset} onClose={() => setDialog(null)} />}
      <ConfirmModal open={!!confirm} onClose={() => setConfirm(null)} loading={del.isPending} title="Delete limit" body={`Delete "${confirm?.name || confirm?.targetName}"? Routing is no longer restricted by it; the tracker keeps showing usage.`}
        onConfirm={() => confirm && del.mutate(confirm.id, { onSuccess: () => { setConfirm(null); toast("ok", "Limit deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </>
  );
}
