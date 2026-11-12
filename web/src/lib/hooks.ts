// TanStack Query hooks. Query keys are grouped so a mutation can invalidate exactly what it changed.
import { useCallback, useMemo } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, type ActiveRequest, type ApiKey, type AuthStatus, type BuiltinProvider, type Combo, type ComboStrategy, type Connection, type CustomModel, type LiveUsage, type KeyAccess, type ModelCapsOverride, type ModelTestResult, type NodeModels, type NodeType, type ProviderNode, type ProxyPool, type ProxyPoolBody, type QuotaBody, type QuotaData, type SysSnapshot, type TestBatch, type Settings, type Stats, type UsagePeriod, type UsageRow, type UsageSummary, type ValidateResult } from "./api";

export const useAuthStatus = () => useQuery({ queryKey: ["auth"], queryFn: () => api<AuthStatus>("/api/auth/status"), staleTime: 30_000 });
export const useSettings = () => useQuery({ queryKey: ["settings"], queryFn: () => api<Settings>("/api/settings") });
export const useKeys = () => useQuery({ queryKey: ["keys"], queryFn: () => api<{ keys: ApiKey[] }>("/api/keys").then((r) => r.keys) });
export const useNodes = () => useQuery({ queryKey: ["nodes"], queryFn: () => api<{ nodes: ProviderNode[] }>("/api/provider-nodes").then((r) => r.nodes) });
/**
 * Custom nodes plus the built-in providers a request can reach (no key needed, or at least one connection).
 * /api/provider-nodes only lists custom nodes, so pickers and usage views use this to include Cline, CodeBuddy, OpenCode Free, Freebuff, ….
 */
export type RouteNode = Pick<ProviderNode, "id" | "name" | "prefix">;
export function useAllNodes() {
  const n = useNodes(), b = useBuiltinProviders(), c = useConnections();
  const data = useMemo<RouteNode[] | undefined>(() => {
    if (!n.data) return undefined;
    const keyed = new Set((c.data ?? []).map((x) => x.provider));
    return [...n.data, ...(b.data ?? []).filter((p) => p.noAuth || keyed.has(p.id)).map((p) => ({ id: p.id, name: p.name, prefix: p.alias }))];
  }, [n.data, b.data, c.data]);
  return { data, isLoading: n.isLoading || b.isLoading || c.isLoading };
}
export const useNode = (id?: string) => { const q = useNodes(); return { ...q, data: q.data?.find((n) => n.id === id) }; };
export const useBuiltinProviders = () => useQuery({ queryKey: ["builtinProviders"], queryFn: () => api<{ providers: BuiltinProvider[] }>("/api/builtin-providers").then((r) => r.providers), staleTime: 60_000 });
/** Resolves a provider id to a built-in registry entry or a custom node. */
export function useProvider(id?: string) {
  const b = useBuiltinProviders(), n = useNodes();
  const provider = b.data?.find((p) => p.id === id), node = n.data?.find((x) => x.id === id);
  const found = provider ? ({ kind: "builtin", provider } as const) : node ? ({ kind: "custom", node } as const) : undefined;
  return { data: found, isLoading: !found && (b.isLoading || n.isLoading) };
}
export const useConnections = () => useQuery({ queryKey: ["connections"], queryFn: () => api<{ connections: Connection[] }>("/api/providers").then((r) => r.connections) });
export const useStats = (period: UsagePeriod = "24h") => useQuery({ queryKey: ["usage", "stats", period], queryFn: () => api<Stats>(`/api/usage/stats?period=${period}`), refetchInterval: 15_000 });
export const useNodeModels = (id: string | null) =>
  useQuery({ queryKey: ["models", id], enabled: !!id, queryFn: () => api<NodeModels>(`/api/provider-nodes/${id}/models`) });
export const useAllModels = (nodes: RouteNode[] | undefined) =>
  useQuery({
    queryKey: ["models", "all", nodes?.map((n) => n.id).join(",")],
    enabled: !!nodes?.length,
    queryFn: async () => {
      const res = await Promise.all(nodes!.map((n) => api<{ models: string[] }>(`/api/provider-nodes/${n.id}/models`).then((r) => r.models.map((m) => `${n.prefix}/${m}`)).catch(() => [] as string[])));
      return res.flat();
    },
  });
export const useCombos = () => useQuery({ queryKey: ["combos"], queryFn: () => api<{ combos: Combo[] }>("/api/combos").then((r) => r.combos) });
export const useAliases = () => useQuery({ queryKey: ["aliases"], queryFn: () => api<{ aliases: Record<string, string> }>("/api/models/alias").then((r) => r.aliases) });
export const useCustomModels = () => useQuery({ queryKey: ["customModels"], queryFn: () => api<{ models: CustomModel[] }>("/api/models/custom").then((r) => r.models) });
/** Added models keyed by lower-cased `prefix/id`, for looking up capabilities of combo members. */
export function useCapsMap() {
  const { data } = useCustomModels();
  return useMemo(() => new Map((data ?? []).map((m) => [`${m.providerAlias}/${m.id}`.toLowerCase(), m])), [data]);
}
export const useUsageSummary = (period: UsagePeriod) =>
  useQuery({ queryKey: ["usage", "summary", period], queryFn: () => api<UsageSummary>(`/api/usage/summary?period=${period}`), refetchInterval: 10_000, placeholderData: (prev) => prev });
export const useActiveRequests = () =>
  useQuery({ queryKey: ["usage", "active"], queryFn: () => api<{ active: ActiveRequest[] }>("/api/usage/active").then((r) => r.active), refetchInterval: 2_000, placeholderData: (prev) => prev });
export const useHistory = (p: { limit: number; offset: number; provider: string; model: string; status: string }, refetchInterval: number | false = false) =>
  useQuery({
    refetchInterval,
    queryKey: ["usage", "history", p],
    queryFn: () => api<{ rows: UsageRow[]; total: number }>(`/api/usage/history?${new URLSearchParams({ limit: String(p.limit), offset: String(p.offset), provider: p.provider, model: p.model, status: p.status })}`),
    placeholderData: (prev) => prev,
  });

/** Mutation that invalidates the given query-key prefixes on success. */
function useMut<TIn, TOut>(fn: (v: TIn) => Promise<TOut>, invalidate: string[][]) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: fn, onSuccess: () => invalidate.forEach((k) => qc.invalidateQueries({ queryKey: k })) });
}

export const useLogin = () => useMut((password: string) => api("/api/auth/login", { method: "POST", json: { password } }), [["auth"]]);
export const useLogout = () => useMut(() => api("/api/auth/logout", { method: "POST" }), [["auth"]]);
export const useUpdateSettings = () => useMut((v: Record<string, unknown>) => api<Settings>("/api/settings", { method: "PATCH", json: v }), [["settings"], ["auth"]]);
export const useCreateKey = () => useMut((name: string) => api<{ key: ApiKey }>("/api/keys", { method: "POST", json: { name } }), [["keys"]]);
export const useToggleKey = () => useMut((v: { id: string; isActive: boolean }) => api(`/api/keys/${v.id}`, { method: "PATCH", json: { isActive: v.isActive } }), [["keys"]]);
export const useEditKey = () => useMut((v: { id: string; name: string; key: string }) => api(`/api/keys/${v.id}`, { method: "PATCH", json: { name: v.name, key: v.key } }), [["keys"]]);
export const useDeleteKey = () => useMut((id: string) => api(`/api/keys/${id}`, { method: "DELETE" }), [["keys"]]);
export const useUpdateKeyAccess = () => useMut((v: { id: string; access: KeyAccess }) => api(`/api/keys/${v.id}`, { method: "PATCH", json: { access: v.access } }), [["keys"]]);
export const useSaveCombo = () =>
  useMut((v: { id?: string; name: string; models: string[]; strategy: ComboStrategy }) => {
    const { id, ...body } = v;
    return id ? api(`/api/combos/${id}`, { method: "PUT", json: body }) : api("/api/combos", { method: "POST", json: body });
  }, [["combos"], ["keys"]]);
export const useDeleteCombo = () => useMut((id: string) => api(`/api/combos/${id}`, { method: "DELETE" }), [["combos"]]);
export const useSetAlias = () => useMut((v: { alias: string; model: string }) => api("/api/models/alias", { method: "PUT", json: v }), [["aliases"]]);
export const useDeleteAlias = () => useMut((alias: string) => api(`/api/models/alias?${new URLSearchParams({ alias })}`, { method: "DELETE" }), [["aliases"]]);
export const useAddCustomModel = () => useMut((v: { providerAlias: string; id: string; name?: string }) => api("/api/models/custom", { method: "POST", json: v }), [["customModels"], ["models"], ["builtinProviders"]]);
/** `caps: null` resets every capability to auto-detect. */
export const useSetModelCaps = () => useMut((v: { providerAlias: string; id: string; caps: ModelCapsOverride | null }) => api("/api/models/custom", { method: "PATCH", json: v }), [["customModels"], ["models"], ["builtinProviders"]]);
export const useDeleteCustomModel = () => useMut((v: { providerAlias: string; id: string }) => api(`/api/models/custom?${new URLSearchParams(v)}`, { method: "DELETE" }), [["customModels"], ["models"], ["builtinProviders"]]);
export const useCreateNode = () => useMut((v: Record<string, unknown>) => api<{ node: ProviderNode }>("/api/provider-nodes", { method: "POST", json: v }), [["nodes"]]);
export const useUpdateNode = () => useMut((v: { id: string } & Record<string, unknown>) => api(`/api/provider-nodes/${v.id}`, { method: "PATCH", json: v }), [["nodes"], ["connections"], ["models"]]);
export const useDeleteNode = () => useMut((id: string) => api(`/api/provider-nodes/${id}`, { method: "DELETE" }), [["nodes"], ["connections"], ["models"]]);
export const useValidateNode = () => useMut((v: { type: NodeType; baseUrl: string; apiType?: string; apiKey: string; modelId?: string }) => api<ValidateResult>("/api/provider-nodes/validate", { method: "POST", json: v }), []);
export const useCreateConnection = () => useMut((v: { provider: string; name: string; apiKey: string; priority?: number; proxyPoolId?: string }) => api<{ connection: Connection }>("/api/providers", { method: "POST", json: v }), [["connections"], ["models"], ["builtinProviders"]]);
export const useCheckBuiltinKey = () => useMut((v: { id: string; apiKey: string }) => api<ValidateResult>(`/api/builtin-providers/${v.id}/validate`, { method: "POST", json: { apiKey: v.apiKey } }), []);
export const usePatchConnection = () => useMut((v: { id: string } & Record<string, unknown>) => api(`/api/providers/${v.id}`, { method: "PATCH", json: v }), [["connections"], ["models"], ["builtinProviders"]]);
export const useDeleteConnection = () => useMut((id: string) => api(`/api/providers/${id}`, { method: "DELETE" }), [["connections"], ["models"], ["builtinProviders"]]);
export const useTestConnection = () => useMut((v: { id: string; modelId?: string }) => api<ValidateResult>(`/api/providers/${v.id}/test`, { method: "POST", json: { modelId: v.modelId } }), [["connections"], ["models"]]);
export const useResetUsage = () => useMut(() => api<{ deleted: number }>("/api/usage/reset", { method: "POST" }), [["usage"]]);
export const useTestModel = () => useMut((model: string) => api<ModelTestResult>("/api/models/test", { method: "POST", json: { model } }), []);
/** Enable/disable every connection of a provider at once, optimistically. */
export function useToggleConnections() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { ids: string[]; isActive: boolean }) => Promise.all(v.ids.map((id) => api(`/api/providers/${id}`, { method: "PATCH", json: { isActive: v.isActive } }))),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: ["connections"] });
      const prev = qc.getQueryData<Connection[]>(["connections"]);
      qc.setQueryData<Connection[]>(["connections"], (cs) => cs?.map((c) => (v.ids.includes(c.id) ? { ...c, isActive: v.isActive } : c)));
      return { prev };
    },
    onError: (_e, _v, ctx) => qc.setQueryData(["connections"], ctx?.prev),
    onSettled: () => { qc.invalidateQueries({ queryKey: ["connections"] }); qc.invalidateQueries({ queryKey: ["models"] }); },
  });
}

export const useSystem = () => useQuery({ queryKey: ["system"], queryFn: () => api<SysSnapshot>("/api/system"), refetchInterval: 3_000, placeholderData: (prev) => prev });
export const useTestBatch = () => useMut((providers?: string[]) => api<TestBatch>("/api/providers/test-batch", { method: "POST", json: { providers } }), [["connections"], ["models"], ["health"]]);

// ---------- built-in provider detail ----------
const BP = [["builtinProviders"]];
export const useBuiltinConfig = () => useMut((v: { providerId: string; roundRobin: boolean; sticky: number }) => api(`/api/provider-config/${v.providerId}`, { method: "PUT", json: { roundRobin: v.roundRobin, sticky: v.sticky } }), BP);
export const useSaveHeaders = () => useMut((v: { id: string; headers: Record<string, string> }) => api(`/api/builtin-providers/${v.id}/headers`, { method: "PUT", json: { headers: v.headers } }), BP);
export const useResetHeaders = () => useMut((id: string) => api(`/api/builtin-providers/${id}/headers`, { method: "DELETE" }), BP);
export const useBulkModels = () => useMut((v: { id: string; action: "enable-all" | "disable-all" }) => api(`/api/builtin-providers/${v.id}/models`, { method: "POST", json: { action: v.action } }), [["builtinProviders"], ["customModels"], ["models"]]);

// ---------- proxy pools ----------
const PP = [["proxyPools"], ["connections"]];
export const useProxyPools = () => useQuery({ queryKey: ["proxyPools"], queryFn: () => api<{ proxyPools: ProxyPool[] }>("/api/proxy-pools").then((r) => r.proxyPools) });
export const useCreateProxyPool = () => useMut((v: ProxyPoolBody) => api<{ proxyPool: ProxyPool }>("/api/proxy-pools", { method: "POST", json: v }), PP);
export const useUpdateProxyPool = () => useMut((v: { id: string } & ProxyPoolBody) => api(`/api/proxy-pools/${v.id}`, { method: "PUT", json: v }), PP);
export const useBulkProxy = () => useMut((assignments: { connectionId: string; proxyPoolId: string }[]) => api("/api/providers/bulk-proxy", { method: "POST", json: { assignments } }), PP);
export const useDeleteProxyPool = () => useMut((id: string) => api(`/api/proxy-pools/${id}`, { method: "DELETE" }), PP);
export const useTestProxyPool = () => useMut((id: string) => api<{ ok: boolean; status: number; elapsedMs: number; error?: string }>(`/api/proxy-pools/${id}/test`, { method: "POST" }), PP);
export const useDeployRelay = () => useMut((v: { kind: "vercel" | "cloudflare"; body: Record<string, string> }) => api<{ proxyPool: ProxyPool; deployUrl: string }>(`/api/proxy-pools/${v.kind}-deploy`, { method: "POST", json: v.body }), PP);

// ---------- quota ----------
export const useQuota = (auto = true) => useQuery({ queryKey: ["quota"], queryFn: () => api<QuotaData>("/api/quota"), refetchInterval: auto ? 15_000 : false });
export const useSaveQuota = () => useMut((v: { id?: string; body: QuotaBody }) => (v.id ? api(`/api/quota/${v.id}`, { method: "PUT", json: v.body }) : api("/api/quota", { method: "POST", json: v.body })), [["quota"]]);
export const useDeleteQuota = () => useMut((id: string) => api(`/api/quota/${id}`, { method: "DELETE" }), [["quota"]]);

// ---------- live provider quota (XRouter-style Quota Tracker) ----------

const QUOTA_CACHE_KEY = "quotaCacheData";
type QuotaCache = Record<string, { data: LiveUsage; cachedAt: string }>;
const readQuotaCache = (): QuotaCache => { try { return JSON.parse(localStorage.getItem(QUOTA_CACHE_KEY) ?? "{}"); } catch { return {}; } };
function writeQuotaCache(id: string, data: LiveUsage | null) {
  try {
    const c = readQuotaCache();
    if (data) c[id] = { data, cachedAt: new Date().toISOString() }; else delete c[id];
    localStorage.setItem(QUOTA_CACHE_KEY, JSON.stringify(c));
  } catch { /* storage blocked: the cache is only a first-paint convenience */ }
}
const liveUsageKey = (id: string) => ["liveUsage", id];
async function fetchLiveUsage(id: string, force: boolean): Promise<LiveUsage> {
  let data: LiveUsage;
  // 401 (provider rejected the token) and 404 become an in-card message instead of an error state, like XRouter.
  try { data = await api<LiveUsage>(`/api/usage/${id}${force ? "?force=1" : ""}`); }
  catch (e) { if (e instanceof ApiError && (e.status === 401 || e.status === 404)) data = { quotas: {}, message: e.message }; else throw e; }
  writeQuotaCache(id, data);
  return data;
}
/** One query per connection id; the last answer in localStorage paints first while the fresh one loads. `load` re-fetches one id (force = bypass the server cache). */
export function useLiveUsages(ids: string[]) {
  const qc = useQueryClient(), key = ids.join(",");
  const cache = useMemo(readQuotaCache, [key]);
  const results = useQueries({ queries: ids.map((id) => ({ queryKey: liveUsageKey(id), queryFn: () => fetchLiveUsage(id, false), staleTime: 0, ...(cache[id] ? { initialData: cache[id].data, initialDataUpdatedAt: Date.parse(cache[id].cachedAt) } : {}) })) });
  const load = useCallback((id: string, force: boolean) => qc.fetchQuery({ queryKey: liveUsageKey(id), queryFn: () => fetchLiveUsage(id, force), staleTime: 0 }).then(() => undefined, () => undefined), [qc]);
  const forget = useCallback((id: string) => { qc.removeQueries({ queryKey: liveUsageKey(id) }); writeQuotaCache(id, null); }, [qc]);
  return { results, load, forget };
}
