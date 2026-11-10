import { Server } from "lucide-react";
import { Badge, Card, cn, EmptyState, Skeleton } from "../ui";
import type { UsageSummary } from "../../lib/api";
import { useActiveRequests, useAllNodes } from "../../lib/hooks";
import { useNow } from "./shared";

const IDLE_MS = 24 * 60 * 60 * 1000;

/** Every provider with the models it served in the last 24h; models being served right now glow green. */
export function ProviderActivity({ data }: { data: UsageSummary | undefined }) {
  const nodes = useAllNodes();
  const active = useActiveRequests();
  const now = useNow(30_000);
  const loading = nodes.isLoading || !data;

  const activeModels = new Map<string, Set<string>>();
  for (const a of active.data ?? []) {
    if (!activeModels.has(a.provider)) activeModels.set(a.provider, new Set());
    activeModels.get(a.provider)!.add(a.model);
  }
  const rows = (nodes.data ?? []).map((n) => {
    const act = data?.providerActivity.find((p) => p.provider === n.id);
    const hot = activeModels.get(n.id) ?? new Set<string>();
    // models in flight right now come first, even if they have no history yet
    const models = [...hot, ...(act?.models ?? []).map((m) => m.model).filter((m) => !hot.has(m))];
    const lastMs = act ? new Date(act.lastUsed).getTime() : 0;
    const state: "active" | "on" | "idle" = hot.size ? "active" : lastMs && now - lastMs < IDLE_MS ? "on" : "idle";
    return { node: n, models, hot, lastMs, state };
  }).sort((a, b) => (b.hot.size ? 1 : 0) - (a.hot.size ? 1 : 0) || b.lastMs - a.lastMs || a.node.name.localeCompare(b.node.name));
  const activeCount = rows.filter((r) => r.state === "active").length;

  return (
    <Card className="p-4 h-full flex flex-col gap-3 min-w-0">
      <div className="flex items-center justify-between gap-2">
        <h2 className="font-semibold">Provider activity <span className="text-muted font-normal text-[13px]">· last 24h</span></h2>
        {activeCount > 0 && <Badge tone="ok">{activeCount} active</Badge>}
      </div>
      {loading ? <div className="space-y-2">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-12" />)}</div>
        : !rows.length ? <EmptyState icon={<Server className="size-5" />} title="No providers connected" />
        : (
          <ul className="space-y-1.5 max-h-80 lg:max-h-[22rem] overflow-y-auto pr-0.5">
            {rows.map(({ node, models, hot, state }) => (
              <li key={node.id} className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5 min-w-0">
                <span className="grid size-8 shrink-0 place-items-center rounded-md bg-accent-soft text-accent text-sm font-bold" aria-hidden>{node.name.slice(0, 2).toUpperCase()}</span>
                <span className="w-28 sm:w-40 shrink-0 truncate text-sm font-medium" title={node.name}>{node.name}</span>
                <div className="flex flex-1 min-w-0 items-center gap-1.5 overflow-x-auto pb-0.5">
                  {models.length === 0 ? <span className="text-xs text-muted">No active models</span> : models.map((m) => (
                    <span key={m} title={m} className={cn("inline-flex max-w-[16rem] shrink-0 items-center gap-1 rounded-md border px-2 py-0.5 text-xs",
                      hot.has(m) ? "border-ok/40 bg-ok-soft text-ok" : "border-border bg-surface-2 text-muted")}>
                      {hot.has(m) && <span className="size-1.5 shrink-0 rounded-full bg-ok" />}
                      <span className="truncate font-mono">{m}</span>
                    </span>
                  ))}
                </div>
                <div className="flex w-16 shrink-0 items-center justify-end gap-1.5">
                  {state === "idle" ? <span className="text-xs font-medium text-muted">Idle</span> : (
                    <>
                      <span className="relative flex size-2.5">
                        {state === "active" && <span className="absolute inline-flex size-full animate-ping rounded-full bg-ok opacity-75" />}
                        <span className="relative inline-flex size-2.5 rounded-full bg-ok" />
                      </span>
                      <span className="text-xs font-medium text-ok">{state === "active" ? "Active" : "On"}</span>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
    </Card>
  );
}
