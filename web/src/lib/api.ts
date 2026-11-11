// Typed fetch wrapper + shared API types. Every call goes through `api()` so errors surface uniformly.
export class ApiError extends Error {
  constructor(public status: number, message: string, public body?: Record<string, unknown>) {
    super(message);
  }
}

export async function api<T>(path: string, init?: RequestInit & { json?: unknown }): Promise<T> {
  const { json, ...rest } = init ?? {};
  const res = await fetch(path, {
    credentials: "same-origin",
    ...rest,
    headers: { ...(json !== undefined ? { "Content-Type": "application/json" } : {}), ...rest.headers },
    body: json !== undefined ? JSON.stringify(json) : rest.body,
  });
  const text = await res.text();
  let data: Record<string, unknown> = {};
  try { data = text ? JSON.parse(text) : {}; } catch { /* non-JSON error page */ }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith("/api/auth/")) window.dispatchEvent(new Event("auth:expired"));
    const err = data.error; // string, or {message} (OpenAI-style) from newer endpoints
    const msg = typeof err === "string" ? err : (err as { message?: string } | undefined)?.message;
    throw new ApiError(res.status, msg || `Request failed (${res.status})`, data);
  }
  return data as T;
}

export type AuthStatus = { requireLogin: boolean; authenticated: boolean; hasPassword: boolean };
export type Settings = {
  requireLogin: boolean; requireApiKey: boolean; hasPassword: boolean;
  dataDir: string; version: string; machineId: string;
  port?: number; localOnly: boolean; hostFromEnv: boolean; bindHost: string; activePort: number; defaultPort: number; portSource: "flag" | "env" | "settings" | "default";
  ponytailEnabled?: boolean; ponytailLevel?: "lite" | "full" | "ultra"; watermarksRemoverEnabled?: boolean;
  loopGuardEnabled?: boolean; circuitBreakerEnabled?: boolean; semaphoreEnabled?: boolean;
  customPromptEnabled?: boolean; customPrompt?: string;
  capacityAdapter?: CapacityAdapter;
};
export type CapacityPool = { enabled: boolean; roundRobin: boolean; models: string[] };
export type CapacityAdapter = { vision?: CapacityPool; audioInput?: CapacityPool };
// maxTokens: total prompt+completion tokens the key may use, 0 = unlimited.
export type KeyAccess = { restricted: boolean; allow: string[]; maxTokens?: number };
export type ApiKey = { id: string; key: string; name: string; isActive: boolean; createdAt: string; access?: KeyAccess; usedTokens?: number };
export type NodeType = "openai-compatible" | "anthropic-compatible";
// apiType is absent on anthropic-compatible nodes (upstream is always {baseUrl}/messages).
export type ProviderNode = { id: string; type: NodeType; name: string; prefix: string; apiType?: "chat" | "responses"; baseUrl: string; createdAt: string };
export type ComboStrategy = "fallback" | "round-robin";
export type Combo = { id: string; name: string; kind: string; models: string[]; strategy: ComboStrategy; context_length: number | null; createdAt: string; updatedAt: string };
// Model capabilities; contextWindow is in tokens, 0 = unknown.
export type Caps = { vision: boolean; reasoning: boolean; audioInput: boolean; contextWindow: number };
/** Explicit manual overrides: an absent key means "auto-detect". */
export type ModelCapsOverride = Partial<Caps>;
export type CustomModel = { providerAlias: string; id: string; type: string; name: string; caps?: ModelCapsOverride; detected: Caps; effective: Caps };
export type BuiltinModel = { id: string; name: string; enabled: boolean; custom?: boolean; detected: Caps; effective: Caps; caps: ModelCapsOverride | null };
export type BuiltinProvider = {
  id: string; alias: string; name: string; category: "oauth" | "free" | "apikey"; status: "ready" | "experimental" | "unsupported";
  note: string; website: string; authHint: string; noAuth: boolean; passthrough: boolean; local?: boolean; baseUrl: string; models: BuiltinModel[];
  oauth: boolean; oauthKind: "code" | "poll" | "";
  usage?: boolean; // live account quota via GET /api/usage/{connectionId}
  config: { roundRobin: boolean; sticky: number };
  headers: { defaults: Record<string, string>; custom: Record<string, string> | null; effective: Record<string, string>; customized: boolean };
};
export type NodeModels = { models: string[]; upstream: string[]; upstreamCaps?: Record<string, Caps>; error?: string };
export type Connection = {
  id: string; provider: string; name: string; priority: number; isActive: boolean; apiKeyMasked: string; authType?: "apikey" | "oauth";
  testStatus?: "unknown" | "active" | "error" | "unavailable"; lastError?: string; errorCode?: string | number; lastErrorAt?: string; rateLimitedUntil?: string;
  defaultModel?: string; email?: string; displayName?: string;
  providerSpecificData?: { proxyPoolId?: string; authMethod?: string; region?: string; profileArn?: string };
};
export type ProxyPool = {
  id: string; name: string; type: "http" | "vercel" | "cloudflare" | "postcard"; proxyUrl: string; noProxy: string; isActive: boolean; strictProxy: boolean; keyless: boolean;
  testStatus: "unknown" | "active" | "error"; lastTestedAt?: string; lastError?: string; boundConnectionCount: number;
};
export type ProxyPoolBody = { name?: string; type?: ProxyPool["type"]; proxyUrl?: string; noProxy?: string; isActive?: boolean; strictProxy?: boolean; relayKey?: string; keyless?: boolean };
export type ValidateResult = { valid: boolean; error?: string; method?: string; models?: string[] };
export type Stats = {
  requests: number; errors: number; promptTokens: number; completionTokens: number; cacheTokens: number; avgLatencyMs: number;
  bucket: "hour" | "day";
  series: { t: string; requests: number; errors: number; promptTokens: number; completionTokens: number; cacheTokens: number }[];
  topModels: { model: string; requests: number; tokens: number }[];
};
export type UsageRow = {
  id: number; timestamp: string; provider: string; model: string; connectionId?: string; endpoint: string;
  promptTokens: number; completionTokens: number; latencyMs: number; status: string;
};
export type UsagePeriod = "month" | "today" | "24h" | "7d" | "30d" | "60d" | "all";
export type UsageBreakdownRow = {
  model: string; provider: string; providerName: string; connectionId: string; account: string; apiKey: string; keyName: string; endpoint: string;
  requests: number; promptTokens: number; completionTokens: number; cacheTokens: number; lastUsed: string;
};
export type UsageSummary = {
  period: UsagePeriod; requests: number; errors: number; promptTokens: number; completionTokens: number; cacheTokens: number;
  bucket: "hour" | "day";
  series: Stats["series"];
  breakdown: UsageBreakdownRow[];
  live: { requests: number; modelsActive: number; rpm: number; avgLatencyMs: number };
  recent: UsageRow[];
  providerActivity: { provider: string; lastUsed: string; models: { model: string; requests: number; lastUsed: string }[] }[];
};
export type ActiveRequest = { provider: string; model: string; connectionId: string; account: string; endpoint: string; startedAt: string };
export type Circuit = "CLOSED" | "OPEN" | "HALF_OPEN";
// Last 15 minutes; avgLatencyMs/successRate (0-100) are null when there was no traffic.
export type RoutingHealthRow = { provider: string; name: string; type: string; circuit: Circuit; retryAfterMs: number; failures: number; avgLatencyMs: number | null; successRate: number | null; requests: number };
export type ModelTestResult = { ok: boolean; error?: string; latencyMs?: number; reply?: string };

export type TestBatchResult = { connectionId: string; name: string; provider: string; providerName: string; ok: boolean; latencyMs: number; method?: string; error?: string };
export type TestBatch = { results: TestBatchResult[]; summary: { total: number; passed: number; failed: number; skipped?: number } };
export type SysSample = { t: string; cpu: number; rss: number };
export type SysSnapshot = { current: SysSample; cpus: number; goHeap: number; goroutines: number; history: SysSample[] };
export type LogEntry = { id: number; t: string; level: "INFO" | "WARN" | "ERROR"; msg: string };

export type QuotaScope = "provider" | "connection";
export type QuotaBody = { name?: string; scope: QuotaScope; target: string; metric: "requests" | "tokens"; window: "hour" | "day" | "week" | "month"; mode?: "calendar" | "rolling"; limit: number; enforce: boolean };
export type QuotaLimit = QuotaBody & {
  id: string; targetName: string; mode: "calendar" | "rolling"; createdAt: string;
  used: number; remaining: number; percent: number; windowStart: string; resetAt: string; exhausted: boolean;
};
export type QuotaUsage = { provider: string; name: string; requests: { today: number; month: number }; tokens: { today: number; month: number } };
export type TrackedConnection = {
  id: string; provider: string; providerName: string; name: string; authType: "apikey" | "oauth"; isActive: boolean; testStatus?: string; priority: number; lastUsed: string;
  requests: { today: number; week: number; month: number }; tokens: { today: number; week: number; month: number };
};
export type QuotaData = { limits: QuotaLimit[]; usage: QuotaUsage[]; connections: TrackedConnection[] };

// Live provider quota (GET /api/usage/{connectionId}); XRouter's shape. Object key order is not guaranteed by the backend.
export type LiveQuota = { used: number; total: number; remaining?: number; resetAt?: string | null; unlimited?: boolean; displayName?: string; recurring?: boolean };
export type LiveUsage = { plan?: string; message?: string; quotas?: Record<string, LiveQuota>; raw?: unknown };
// A provider row has modelId null and carries the cadence; its model rows follow.
