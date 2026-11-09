// Free-tier catalogue grouped per provider (port of XRouter-Next FreeTierList): connected providers first, the rest behind a toggle.
import { useMemo, useState } from "react";
import type { FreeRefresh } from "../../lib/api";
import { useBuiltinProviders, useConnections, useFreeTiers } from "../../lib/hooks";
import { Badge, Button, Card, errText, Skeleton } from "../ui";

const REFRESH_LABELS: Record<FreeRefresh, string> = { daily: "Daily", weekly: "Weekly", monthly: "Monthly", "one-time": "One-time", rolling: "Rolling" };
// Router-Go has no purple/cyan token, so monthly and rolling stay neutral; the label tells them apart.
const REFRESH_TONE: Record<FreeRefresh, "ok" | "accent" | "warn" | "neutral"> = { daily: "ok", weekly: "accent", monthly: "neutral", "one-time": "warn", rolling: "neutral" };

type Group = { id: string; name: string; alias: string | null; refresh: FreeRefresh | null; models: { id: string; name: string }[]; connected: boolean };

export function FreeTierList() {
  const tiers = useFreeTiers(), conns = useConnections(), builtin = useBuiltinProviders();
  const [query, setQuery] = useState(""), [showUnconnected, setShowUnconnected] = useState(false);

  // ids of providers with at least one enabled connection (plus their alias); null while connections are still loading
  const active = useMemo(() => {
    if (!conns.data) return conns.isError ? new Set<string>() : null;
    const alias = new Map((builtin.data ?? []).map((p) => [p.id, p.alias]));
    const ids = new Set<string>();
    for (const c of conns.data) if (c.isActive) { ids.add(c.provider); const a = alias.get(c.provider); if (a) ids.add(a); }
    return ids;
  }, [conns.data, conns.isError, builtin.data]);

  const groups = useMemo(() => {
    const by = new Map<string, Omit<Group, "connected">>();
    for (const t of tiers.data ?? []) {
      const g = by.get(t.providerId) ?? { id: t.providerId, name: t.providerName || t.providerId, alias: t.providerAlias || null, refresh: t.freeRefresh, models: [] };
      by.set(t.providerId, g);
      if (!g.refresh && t.freeRefresh) g.refresh = t.freeRefresh;
      if (t.modelId) g.models.push({ id: t.modelId, name: t.modelName || t.modelId });
    }
    const needle = query.trim().toLowerCase();
    return [...by.values()]
      .map((g): Group => ({ ...g, connected: !!active && (active.has(g.id) || (!!g.alias && active.has(g.alias))) }))
      .filter((g) => !needle || g.name.toLowerCase().includes(needle) || (g.alias ?? "").toLowerCase().includes(needle) || g.models.some((m) => m.name.toLowerCase().includes(needle) || m.id.toLowerCase().includes(needle)))
      .sort((a, b) => (a.connected !== b.connected ? (a.connected ? -1 : 1) : a.name.localeCompare(b.name)));
  }, [tiers.data, active, query]);

  const connectedCount = groups.filter((g) => g.connected).length, hiddenCount = active ? groups.length - connectedCount : 0;
  const visible = showUnconnected || query.trim() ? groups : groups.filter((g) => g.connected);

  return (
    <Card className="p-5">
      <h2 className="font-semibold">Available free tiers</h2>
      <p className="mt-0.5 text-[13px] text-muted">Provider free allowances from the registry. Connected providers first; the rest are behind the toggle.</p>
      <div className="mt-4">
        {tiers.isLoading ? <Skeleton className="h-24" /> : tiers.isError ? (
          <div className="py-6 text-center"><p className="text-sm text-err">{errText(tiers.error)}</p><Button className="mt-3" onClick={() => void tiers.refetch()}>Retry</Button></div>
        ) : !tiers.data?.length ? (
          <p className="py-6 text-center text-sm text-muted">No free tiers found.</p>
        ) : (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Filter by provider or model…" aria-label="Filter free tiers by provider or model"
                className="min-w-[12rem] flex-1 rounded-lg border border-border bg-surface px-2.5 py-1.5 text-xs focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/25" />
              <span className="text-[11px] text-muted">{connectedCount} connected{active === null ? " (checking…)" : ` · ${hiddenCount} without an account`}</span>
              {hiddenCount > 0 && !query.trim() && (
                <button type="button" onClick={() => setShowUnconnected((v) => !v)} aria-pressed={showUnconnected}
                  className="rounded-lg border border-border px-2.5 py-1.5 text-xs text-muted transition-colors hover:border-accent hover:text-accent cursor-pointer">
                  {showUnconnected ? "Hide" : "Show"} providers without an account
                </button>
              )}
            </div>

            {visible.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted">{active === null ? "Checking your connections…" : "No free tier matches. Add a connection for a provider below, or clear the filter."}</p>
            ) : (
              <div className="flex flex-col divide-y divide-border">
                {visible.map((g) => (
                  <div key={g.id} className="flex flex-col gap-1.5 py-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">{g.name}</span>
                      {g.alias && g.alias !== g.name && <span className="font-mono text-[10px] text-muted">{g.alias}</span>}
                      {g.connected ? <Badge tone="ok">Connected</Badge> : active ? <span className="text-[11px] italic text-muted">no account yet</span> : null}
                      {g.refresh ? <Badge tone={REFRESH_TONE[g.refresh] ?? "neutral"}>{REFRESH_LABELS[g.refresh] ?? g.refresh}</Badge>
                        : <span className="text-xs italic text-muted" title="Reset cadence not documented" aria-label="Reset cadence not documented">—</span>}
                      <span className="ml-auto text-[11px] text-muted">{g.models.length === 0 ? "free tier available" : `${g.models.length} free model${g.models.length > 1 ? "s" : ""}`}</span>
                    </div>
                    {g.models.length > 0 && (
                      <div className="flex flex-wrap gap-1.5">
                        {g.models.map((m) => <span key={m.id} title={m.id} className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-muted">{m.name}</span>)}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </Card>
  );
}
