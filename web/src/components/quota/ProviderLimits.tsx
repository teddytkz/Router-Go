// Live provider quota: one card per connection whose provider reports account limits (port of XRouter-Next ProviderLimits).
import { Ban, Check, ChevronDown, ChevronLeft, ChevronRight, CircleAlert, CircleCheck, CloudOff, Copy, EyeOff, Hourglass, LayoutGrid, Loader2, Pencil, RefreshCw, ToggleLeft, ToggleRight, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import type { BuiltinProvider, Connection } from "../../lib/api";
import { useBuiltinProviders, useConnections, useDeleteConnection, useLiveUsages, usePatchConnection, useToggleConnections } from "../../lib/hooks";
import { Button, Card, ConfirmModal, cn, errText, Skeleton, Switch, useToast } from "../ui";
import { LOGOS } from "../providers/Builtin";
import { EditKeyModal } from "../providers/ConnectionsCard";
import { useNow } from "../usage/shared";
import {
  ACCOUNT_FILTER_OPTIONS, ACCOUNT_PAGE_SIZE_MAX, ACCOUNT_PAGE_SIZE_OPTIONS, AUTO_REFRESH_STORAGE_KEY, CONNECTIONS_PAGE_SIZE, HIDDEN_QUOTA_STORAGE_KEY, QUOTA_SORT_OPTIONS, REFRESH_INTERVAL_S,
  getConnectionLabel, getConnectionSecondaryLabel, getConnectionsEmptyMessage, isDepleted, parseQuotaData, readStored, sortVisibleConnections, writeStored,
  type AccountFilter, type NormQuota, type QuotaSort,
} from "./limits";
import { QuotaTable } from "./QuotaTable";

const ctl = "flex h-8 shrink-0 items-center gap-1 rounded-lg border border-border bg-surface px-2 text-xs transition-colors hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer";
const sel = "h-8 rounded-lg border border-border bg-surface px-2 text-xs outline-none transition-colors hover:bg-surface-2 cursor-pointer";
const iconBtn = "flex size-8 items-center justify-center rounded-lg text-muted transition-colors hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer";

const KIRO_METHOD_LABELS: Record<string, string> = { "builder-id": "AWS Builder ID", idc: "IAM Identity Center", google: "Google", github: "GitHub", imported: "Imported Token", api_key: "API Key" };
const kiroMethodLabel = (c: Connection) => KIRO_METHOD_LABELS[c.providerSpecificData?.authMethod ?? ""] ?? (c.authType === "apikey" ? "API Key" : "OAuth");
// social/imported flows omit the region; fall back to the profileArn segment (arn:aws:codewhisperer:<region>:...)
const kiroRegion = (c: Connection) => c.providerSpecificData?.region || c.providerSpecificData?.profileArn?.split(":")[3] || "";

/** Provider logo (same set as the Providers page); initials for providers without one or when the image fails. */
function ProviderTile({ id, name, className = "size-8 text-[11px]" }: { id: string; name: string; className?: string }) {
  const [broken, setBroken] = useState(false);
  if (LOGOS.has(id) && !broken) return <img src={`/providers/${id}.png`} alt="" aria-hidden title={name || id} onError={() => setBroken(true)} className={cn("shrink-0 rounded-md object-cover", className)} />;
  return <div aria-hidden title={name || id} className={cn("grid shrink-0 place-items-center rounded-md bg-accent-soft font-semibold uppercase text-accent", className)}>{(name || id).slice(0, 2)}</div>;
}

function Pill({ tone, children, className }: { tone: "brand" | "ok" | "err" | "neutral" | "info"; children: React.ReactNode; className?: string }) {
  return <span className={cn("rounded-full px-2 py-0.5 text-[10px] font-semibold", tone === "brand" && "bg-accent-soft text-accent", tone === "ok" && "bg-ok-soft text-ok", tone === "err" && "bg-err-soft text-err", tone === "neutral" && "bg-surface-2 text-muted", tone === "info" && "bg-accent-soft text-accent", className)}>{children}</span>;
}

function CopyArn({ arn }: { arn: string }) {
  const [done, setDone] = useState(false);
  return (
    <button type="button" title={arn} onClick={async () => { try { await navigator.clipboard.writeText(arn); } catch { /* insecure context */ } setDone(true); setTimeout(() => setDone(false), 1400); }}
      className="inline-flex max-w-full items-center gap-1 rounded-full border border-border px-2 py-0.5 text-[10px] text-muted transition-colors hover:text-accent cursor-pointer">
      {done ? <Check className="size-3" /> : <Copy className="size-3" />}<code className="truncate font-mono">{arn}</code>
    </button>
  );
}

type Usage = ReturnType<typeof useLiveUsages>["results"][number];

function ConnectionCard({ conn, provider, usage, quotas, now, hidden, showSortLabel, onRefresh, onHide, onShow, onDeleted, onEdited }: {
  conn: Connection; provider?: BuiltinProvider; usage: Usage; quotas: NormQuota[]; now: number; hidden: string[]; showSortLabel: boolean;
  onRefresh: () => void; onHide: (q: NormQuota) => void; onShow: (q: NormQuota) => void; onDeleted: () => void; onEdited: () => void;
}) {
  const toast = useToast(), patch = usePatchConnection(), del = useDeleteConnection();
  const [edit, setEdit] = useState(false), [confirm, setConfirm] = useState(false);
  const loading = usage.isFetching, busy = patch.isPending || del.isPending;
  const inactive = !conn.isActive, label = getConnectionLabel(conn), secondary = getConnectionSecondaryLabel(conn);
  const visible = quotas.filter((q) => !hidden.includes(q.name)), hiddenRows = quotas.filter((q) => hidden.includes(q.name));
  const message = usage.data?.message, name = provider?.name ?? conn.provider;
  const status = conn.testStatus;

  return (
    <Card className={cn("min-w-0 overflow-hidden", inactive && "opacity-60")}>
      <div className="border-b border-border px-3 py-2">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <ProviderTile id={conn.provider} name={name} />
            <div className="min-w-0">
              <h3 className="truncate text-sm font-semibold">{name}</h3>
              {label && <p className="truncate text-xs text-muted">{label}</p>}
              {secondary && <p className="truncate text-[11px] text-muted/80">{secondary}</p>}
              {conn.provider === "kiro" && (
                <div className="mt-1 flex flex-wrap items-center gap-1">
                  <Pill tone="brand">{kiroMethodLabel(conn)}</Pill>
                  {kiroRegion(conn) && <Pill tone="info">{kiroRegion(conn)}</Pill>}
                  <Pill tone={inactive ? "neutral" : status === "active" ? "ok" : status === "error" || status === "unavailable" ? "err" : "neutral"}>{inactive ? "disabled" : status || "unknown"}</Pill>
                  {conn.providerSpecificData?.profileArn && <CopyArn arn={conn.providerSpecificData.profileArn} />}
                </div>
              )}
            </div>
          </div>

          <div className="flex shrink-0 items-center gap-1">
            <button type="button" title="Refresh quota" aria-label="Refresh quota" onClick={onRefresh} disabled={loading || busy} className={iconBtn}>
              <RefreshCw className={cn("size-[18px]", loading && "animate-spin")} />
            </button>
            <button type="button" title="Edit connection" aria-label="Edit connection" onClick={() => setEdit(true)} disabled={busy} className={cn(iconBtn, "hover:text-accent")}><Pencil className="size-[18px]" /></button>
            <button type="button" title="Delete connection" aria-label="Delete connection" onClick={() => setConfirm(true)} disabled={busy} className={cn(iconBtn, "text-err hover:bg-err-soft")}>
              <Trash2 className={cn("size-[18px]", del.isPending && "animate-pulse")} />
            </button>
            <div className="pl-0.5" title={conn.isActive ? "Disable connection" : "Enable connection"}>
              <Switch checked={conn.isActive} label={`${name} ${label ?? ""} enabled`} disabled={busy} onChange={(v) => patch.mutate({ id: conn.id, isActive: v }, { onSuccess: () => toast("ok", `${name}${label ? ` ${label}` : ""} ${v ? "enabled" : "disabled"}`), onError: (e) => toast("err", errText(e)) })} />
            </div>
          </div>
        </div>
      </div>

      <div className="px-2 py-1.5">
        {loading && !usage.data ? (
          <div className="py-5 text-center text-muted"><Loader2 className="mx-auto size-7 animate-spin" aria-label="Loading quota" /></div>
        ) : usage.isError ? (
          <div className="py-5 text-center">
            <CircleAlert className="mx-auto size-7 text-err" aria-hidden />
            <p className="mt-1.5 break-words px-2 text-xs text-muted">{errText(usage.error)}</p>
          </div>
        ) : quotas.length === 0 ? (
          <div className="py-5 text-center"><p className="break-words px-2 text-xs text-muted">{message || "No quota data reported."}</p></div>
        ) : (
          <>
            <QuotaTable quotas={visible} now={now} showSortLabel={showSortLabel} onHide={onHide} />
            {message && <p className="mt-2 px-1 text-[10px] leading-relaxed text-muted">{message}</p>}
          </>
        )}
        {hiddenRows.length > 0 && (
          <div className="mt-2 flex min-w-0 items-center gap-1 border-t border-border/60 pt-2 text-[10px] text-muted">
            <EyeOff className="size-3.5 shrink-0" aria-hidden />
            <span className="shrink-0">Hidden:</span>
            <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto whitespace-nowrap pb-2">
              {hiddenRows.map((q) => (
                <button key={q.name} type="button" title="Show this quota row" onClick={() => onShow(q)} className="shrink-0 rounded-md border border-border px-1.5 py-0.5 transition-colors hover:bg-surface-2 hover:text-text cursor-pointer">{q.name}</button>
              ))}
            </div>
          </div>
        )}
      </div>

      {edit && <EditKeyModal c={conn} onClose={() => { setEdit(false); onEdited(); }} />}
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete connection" body={`Delete "${label ?? name}"? Requests will stop using this connection.`}
        onConfirm={() => del.mutate(conn.id, { onSuccess: () => { setConfirm(false); onDeleted(); toast("ok", "Connection deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </Card>
  );
}

export function ProviderLimits() {
  const conns = useConnections(), builtin = useBuiltinProviders(), toast = useToast(), toggle = useToggleConnections(), now = useNow(30_000);
  const [params, setParams] = useSearchParams();
  const providerFilter = params.get("provider") || "all"; // ?provider= keeps the view bookmarkable
  const [accountFilter, setAccountFilter] = useState<AccountFilter>("all"), [sortMode, setSortMode] = useState<QuotaSort>("default"), [expiringFirst, setExpiringFirst] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false), [page, setPage] = useState(1), [pageSize, setPageSize] = useState(CONNECTIONS_PAGE_SIZE), [customSize, setCustomSize] = useState(String(CONNECTIONS_PAGE_SIZE));
  const [autoRefresh, setAutoRefresh] = useState(() => readStored(AUTO_REFRESH_STORAGE_KEY, true)), [countdown, setCountdown] = useState(REFRESH_INTERVAL_S), [refreshingAll, setRefreshingAll] = useState(false);
  const [hiddenMap, setHiddenMap] = useState(() => readStored<Record<string, string[]>>(HIDDEN_QUOTA_STORAGE_KEY, {}));

  const providers = useMemo(() => new Map((builtin.data ?? []).map((p) => [p.id, p])), [builtin.data]);
  // only providers the backend reports `usage: true` for take part; custom nodes never do
  const eligible = useMemo(() => (conns.data ?? []).filter((c) => providers.get(c.provider)?.usage).sort((a, b) => a.priority - b.priority || a.provider.localeCompare(b.provider)), [conns.data, providers]);
  const providerOptions = useMemo(() => [...new Set(eligible.map((c) => c.provider))].sort(), [eligible]);
  const byProvider = useMemo(() => eligible.filter((c) => providerFilter === "all" || c.provider === providerFilter), [eligible, providerFilter]);
  const filtered = useMemo(() => byProvider.filter((c) => accountFilter === "all" || (accountFilter === "active") === c.isActive), [byProvider, accountFilter]);
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize)), cur = Math.min(page, totalPages);
  const pageConns = useMemo(() => filtered.slice((cur - 1) * pageSize, cur * pageSize), [filtered, cur, pageSize]);

  const { results, load, forget } = useLiveUsages(useMemo(() => pageConns.map((c) => c.id), [pageConns]));
  const usageOf = useMemo(() => new Map(pageConns.map((c, i) => [c.id, results[i]])), [pageConns, results]);
  const quotasOf = useMemo(() => new Map(pageConns.map((c) => [c.id, parseQuotaData(c.provider, usageOf.get(c.id)?.data)])), [pageConns, usageOf]);
  const sorted = useMemo(() => sortVisibleConnections(pageConns, (id) => quotasOf.get(id) ?? [], expiringFirst, providerFilter, sortMode), [pageConns, quotasOf, expiringFirst, providerFilter, sortMode]);
  const lastUpdated = Math.max(0, ...results.map((r) => r.dataUpdatedAt));

  const providerLabel = (id: string) => providers.get(id)?.name ?? id;
  const setProvider = (value: string) => {
    const next = new URLSearchParams(params);
    if (value === "all") next.delete("provider"); else next.set("provider", value);
    setParams(next, { replace: true });
    setPage(1); setMenuOpen(false);
  };
  const setHidden = (provider: string, name: string, hide: boolean) => setHiddenMap((m) => {
    const set = new Set(m[provider] ?? []);
    if (hide) set.add(name); else set.delete(name);
    const next = { ...m, [provider]: [...set] };
    writeStored(HIDDEN_QUOTA_STORAGE_KEY, next);
    return next;
  });
  const applySize = (raw: string) => {
    const n = Number.parseInt(raw, 10);
    if (!Number.isFinite(n)) return setCustomSize(String(pageSize));
    const size = Math.min(ACCOUNT_PAGE_SIZE_MAX, Math.max(1, n));
    setPage(1); setPageSize(size); setCustomSize(String(size));
  };

  // refresh the visible page; `force` asks the backend to skip its cache (manual refresh), auto ticks do not
  const left = useRef(REFRESH_INTERVAL_S), busy = useRef(false), idsRef = useRef<string[]>([]);
  idsRef.current = pageConns.map((c) => c.id);
  const refreshAll = useCallback(async (force: boolean) => {
    if (busy.current) return;
    busy.current = true; setRefreshingAll(true); left.current = REFRESH_INTERVAL_S; setCountdown(REFRESH_INTERVAL_S);
    try { await Promise.all(idsRef.current.map((id) => load(id, force))); } finally { busy.current = false; setRefreshingAll(false); }
  }, [load]);
  useEffect(() => { writeStored(AUTO_REFRESH_STORAGE_KEY, autoRefresh); }, [autoRefresh]);
  useEffect(() => {
    if (!autoRefresh) return;
    const t = setInterval(() => {
      if (document.hidden) return; // paused while the tab is hidden
      left.current -= 1;
      if (left.current <= 0) void refreshAll(false); else setCountdown(left.current);
    }, 1000);
    return () => clearInterval(t);
  }, [autoRefresh, refreshAll]);

  const bulk = (on: boolean) => {
    const ids = sorted.filter((c) => c.isActive !== on && isDepleted(quotasOf.get(c.id) ?? []) !== on).map((c) => c.id);
    if (!ids.length) return toast("ok", "No matching connections on this page");
    toggle.mutate({ ids, isActive: on }, { onSuccess: () => toast("ok", `Turned ${on ? "on" : "off"} ${ids.length} connection${ids.length === 1 ? "" : "s"}`), onError: (e) => toast("err", errText(e)) });
  };

  const loadingList = conns.isLoading || builtin.isLoading;
  if (conns.isError || builtin.isError) return <Card className="p-6 text-center"><CircleAlert className="mx-auto size-8 text-err" /><p className="mt-2 text-sm text-muted">{errText(conns.error ?? builtin.error)}</p><Button className="mt-3" onClick={() => { void conns.refetch(); void builtin.refetch(); }}>Retry</Button></Card>;
  const empty = !loadingList && sorted.length === 0 ? getConnectionsEmptyMessage(eligible.length, filtered.length, providerFilter === "all" ? null : providerLabel(providerFilter), accountFilter) : null;
  const unsupported = (conns.data?.length ?? 0) - eligible.length;
  const isCustomSize = !ACCOUNT_PAGE_SIZE_OPTIONS.includes(pageSize);
  const rangeStart = filtered.length ? (cur - 1) * pageSize + 1 : 0, rangeEnd = Math.min(cur * pageSize, filtered.length);
  const navDisabled = loadingList || refreshingAll;

  return (
    <div className="space-y-6">
      {(loadingList || eligible.length > 0) && (
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-end">
          <div className="flex flex-wrap items-center gap-1.5">
            <div className="relative">
              <button type="button" onClick={() => setMenuOpen((o) => !o)} aria-haspopup="menu" aria-expanded={menuOpen} title="Filter quota providers" className={cn(ctl, "justify-between")}>
                <span className="flex min-w-0 items-center gap-1.5">
                  {providerFilter === "all" ? <LayoutGrid className="size-3.5 text-muted" /> : <ProviderTile id={providerFilter} name={providerLabel(providerFilter)} className="size-[18px] text-[9px]" />}
                  <span className="hidden truncate lg:inline">{providerFilter === "all" ? "All providers" : providerLabel(providerFilter)}</span>
                </span>
                <ChevronDown className="size-3.5 text-muted" />
              </button>
              {menuOpen && (
                <>
                  <button type="button" className="fixed inset-0 z-30 cursor-default bg-transparent" aria-label="Close provider filter" onClick={() => setMenuOpen(false)} />
                  <div role="menu" className="absolute left-0 z-40 mt-2 w-64 max-w-[calc(100vw-2rem)] overflow-hidden rounded-2xl border border-border bg-surface p-1.5 shadow-xl sm:w-72">
                    {[["all", "All providers"] as const, ...providerOptions.map((id) => [id, providerLabel(id)] as const)].map(([id, name], i) => (
                      <div key={id}>
                        {i === 1 && <div className="my-1 h-px bg-border" />}
                        <button type="button" role="menuitem" onClick={() => setProvider(id)} className={cn("flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left text-sm transition-colors cursor-pointer", providerFilter === id ? "bg-accent-soft text-accent" : "hover:bg-surface-2")}>
                          {id === "all" ? <LayoutGrid className="size-[22px]" /> : <ProviderTile id={id} name={name} className="size-6 text-[10px]" />}
                          <span className="truncate font-medium">{name}</span>
                          {providerFilter === id && <Check className="ml-auto size-5 shrink-0" />}
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>

            <select value={accountFilter} onChange={(e) => { setAccountFilter(e.target.value as AccountFilter); setPage(1); }} className={sel} aria-label="Filter accounts by status">
              {ACCOUNT_FILTER_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
            {providerFilter !== "all" && (
              <select value={sortMode} onChange={(e) => setSortMode(e.target.value as QuotaSort)} className={sel} aria-label="Sort quotas by remaining">
                {QUOTA_SORT_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            )}

            <button type="button" onClick={() => setExpiringFirst((v) => !v)} aria-pressed={expiringFirst} title="Sort accounts by earliest quota reset time"
              className={cn(ctl, expiringFirst && "border-warn/40 bg-warn-soft text-warn hover:bg-warn-soft")}>
              <Hourglass className="size-3.5" /><span className="hidden sm:inline">Expiring first</span>
            </button>
            <button type="button" onClick={() => bulk(false)} disabled={toggle.isPending} title="Disable connections with depleted quota on the current page" className={cn(ctl, "border-err/30 text-err hover:bg-err-soft")}>
              <Ban className="size-3.5" /><span className="hidden sm:inline">Turn off Empty</span>
            </button>
            <button type="button" onClick={() => bulk(true)} disabled={toggle.isPending} title="Enable connections that still have quota on the current page" className={cn(ctl, "border-ok/30 text-ok hover:bg-ok-soft")}>
              <CircleCheck className="size-3.5" /><span className="hidden sm:inline">Turn on Available</span>
            </button>

            <button type="button" onClick={() => setAutoRefresh((v) => !v)} aria-pressed={autoRefresh} title={autoRefresh ? "Disable auto-refresh" : "Enable auto-refresh"} className={ctl}>
              {autoRefresh ? <ToggleRight className="size-4 text-accent" /> : <ToggleLeft className="size-4 text-muted" />}
              <span className="hidden sm:inline">Auto-refresh</span>
              {autoRefresh && <span className="text-[10px] tabular-nums text-muted">({countdown}s)</span>}
            </button>
            <button type="button" onClick={() => void refreshAll(true)} disabled={refreshingAll} title="Refresh all" aria-label="Refresh all" className={ctl}>
              <RefreshCw className={cn("size-3.5", refreshingAll && "animate-spin")} />
            </button>
            {lastUpdated > 0 && <span className="text-[10px] text-muted tabular-nums" title="Last quota update">Updated {new Date(lastUpdated).toLocaleTimeString()}</span>}
          </div>
        </div>
      )}

      {expiringFirst && eligible.length > 0 && (
        <div className="rounded-xl border border-warn/30 bg-warn-soft px-3 py-2 text-xs text-warn">
          Expiring-first currently reorders accounts inside the current page. Quota is only loaded for the accounts on this page.
        </div>
      )}

      {loadingList ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">{[0, 1].map((i) => <Skeleton key={i} className="h-40" />)}</div>
      ) : empty ? (
        <Card className="p-6">
          <div className="py-12 text-center">
            <CloudOff className="mx-auto size-16 text-muted opacity-20" aria-hidden />
            <h3 className="mt-4 text-lg font-semibold">{empty.title}</h3>
            <p className="mx-auto mt-2 max-w-md text-sm text-muted">{empty.description}</p>
            {!eligible.length && unsupported > 0 && <p className="mx-auto mt-2 max-w-md text-xs text-muted">{unsupported} connection{unsupported === 1 ? "" : "s"} use providers that do not report live quota and are not listed here.</p>}
          </div>
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {sorted.map((c) => (
            <ConnectionCard key={c.id} conn={c} provider={providers.get(c.provider)} usage={usageOf.get(c.id)!} quotas={quotasOf.get(c.id) ?? []} now={now} hidden={hiddenMap[c.provider] ?? []}
              showSortLabel={providerFilter !== "all" && sortMode !== "default"}
              onRefresh={() => void load(c.id, true)} onHide={(q) => setHidden(c.provider, q.name, true)} onShow={(q) => setHidden(c.provider, q.name, false)}
              onDeleted={() => forget(c.id)} onEdited={() => void load(c.id, false)} />
          ))}
        </div>
      )}

      {eligible.length > 0 && (
        <div className="rounded-xl border border-border bg-surface-2/50 px-3 py-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs text-muted">Showing {rangeStart}-{rangeEnd} of {filtered.length}</span>
            <div className="flex flex-wrap items-center gap-2">
              <select value={isCustomSize ? "custom" : String(pageSize)} aria-label="Accounts per page" className={sel}
                onChange={(e) => { if (e.target.value === "custom") return; const n = Number.parseInt(e.target.value, 10); if (Number.isFinite(n)) { setPage(1); setPageSize(n); setCustomSize(String(n)); } }}>
                {ACCOUNT_PAGE_SIZE_OPTIONS.map((o) => <option key={o} value={String(o)}>{o} / page</option>)}
                <option value="custom">Custom</option>
              </select>
              <input type="number" min={1} max={ACCOUNT_PAGE_SIZE_MAX} inputMode="numeric" value={customSize} placeholder="Custom" aria-label="Custom accounts per page"
                onChange={(e) => setCustomSize(e.target.value)} onBlur={() => applySize(customSize)} onKeyDown={(e) => e.key === "Enter" && applySize(customSize)}
                className="h-8 w-20 rounded-lg border border-border bg-surface px-2 text-xs outline-none transition-colors hover:bg-surface-2 focus:border-accent" />
              <span className="text-xs text-muted">Page {cur} / {totalPages}</span>
            </div>
            <div className="flex items-center gap-1.5">
              <button type="button" className={cn(ctl, "px-3")} disabled={cur <= 1 || navDisabled} onClick={() => setPage(1)}>First Page</button>
              <button type="button" className={cn(ctl, "w-8 justify-center px-0")} aria-label="Previous accounts page" disabled={cur <= 1 || navDisabled} onClick={() => setPage(Math.max(1, cur - 1))}><ChevronLeft className="size-4" /></button>
              <button type="button" className={cn(ctl, "w-8 justify-center px-0")} aria-label="Next accounts page" disabled={cur >= totalPages || navDisabled} onClick={() => setPage(Math.min(totalPages, cur + 1))}><ChevronRight className="size-4" /></button>
              <button type="button" className={cn(ctl, "px-3")} disabled={cur >= totalPages || navDisabled} onClick={() => setPage(totalPages)}>Last Page</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
