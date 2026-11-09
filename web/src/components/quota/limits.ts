// Pure helpers for the live provider quota view (port of XRouter-Next ProviderLimits/utils.js).
import type { Connection, LiveUsage } from "../../lib/api";

export const REFRESH_INTERVAL_S = 60;
export const DEPLETED_QUOTA_THRESHOLD = 5;
export const AUTO_REFRESH_STORAGE_KEY = "quotaAutoRefresh";
export const HIDDEN_QUOTA_STORAGE_KEY = "quotaHiddenRows";
export const CONNECTIONS_PAGE_SIZE = 20;
export const ACCOUNT_PAGE_SIZE_OPTIONS = [10, 20, 50, 100];
export const ACCOUNT_PAGE_SIZE_MAX = 500;
export const ACCOUNT_FILTER_OPTIONS = [
  { value: "all", label: "All accounts" },
  { value: "active", label: "Active" },
  { value: "inactive", label: "Turned off" },
] as const;
export const QUOTA_SORT_OPTIONS = [
  { value: "default", label: "Default quota order" },
  { value: "remaining-asc", label: "% quota: low to high" },
  { value: "remaining-desc", label: "% quota: high to low" },
] as const;
export type AccountFilter = (typeof ACCOUNT_FILTER_OPTIONS)[number]["value"];
export type QuotaSort = (typeof QUOTA_SORT_OPTIONS)[number]["value"];

/** A quota row after parsing: what the table renders. */
export type NormQuota = { name: string; used: number; total: number; resetAt: string | null; unlimited: boolean; recurring: boolean };

export const getConnectionLabel = (c: Connection) => c.name?.trim() || c.email?.trim() || c.displayName?.trim() || null;

export function getConnectionSecondaryLabel(c: Connection) {
  const name = c.name?.trim(), email = c.email?.trim(), display = c.displayName?.trim();
  if (name && email && name !== email) return email;
  if (name && display && name !== display) return display;
  return null;
}

/** Remaining percentage (0-100). `remaining` from the API is deliberately ignored: it may be an absolute amount, not a percent. */
export function calculatePercentage(used: number, total: number) {
  if (!total) return 0;
  if (!used || used < 0) return 100;
  if (used >= total) return 0;
  return Math.round(((total - used) / total) * 100);
}
export const getRemainingPercentage = (q: Pick<NormQuota, "used" | "total">) => calculatePercentage(q.used, q.total);

/** >70 green, 30-70 yellow, below 30 (including 0) red. */
export function getStatusColor(pct: number) { return pct > 70 ? "ok" : pct >= 30 ? "warn" : "err"; }
export const STATUS_STYLE = {
  ok: { text: "text-ok", bg: "bg-ok", track: "bg-ok/10", emoji: "🟢" },
  warn: { text: "text-warn", bg: "bg-warn", track: "bg-warn/10", emoji: "🟡" },
  err: { text: "text-err", bg: "bg-err", track: "bg-err/10", emoji: "🔴" },
} as const;

/** "2d 5h 30m" / "4h 40m" / "15m", or "-" when there is no future reset. */
export function formatResetTime(date: string | null | undefined, now = Date.now()) {
  if (!date) return "-";
  const diff = new Date(date).getTime() - now;
  if (!Number.isFinite(diff) || diff <= 0) return "-";
  const mins = Math.ceil(diff / 60000);
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ${mins % 60}m`;
  return `${Math.floor(hours / 24)}d ${hours % 24}h ${mins % 60}m`;
}

/** "Today, 12:00 PM" / "Tomorrow, 9:30 AM" / "Nov 12, 3:00 PM". */
export function formatResetTimeDisplay(resetAt: string | null | undefined, now = Date.now()) {
  if (!resetAt) return null;
  const date = new Date(resetAt);
  if (Number.isNaN(date.getTime())) return null;
  const n = new Date(now), today = new Date(n.getFullYear(), n.getMonth(), n.getDate()), tomorrow = new Date(today.getTime() + 86400000), after = new Date(today.getTime() + 2 * 86400000);
  const day = date >= today && date < tomorrow ? "Today" : date >= tomorrow && date < after ? "Tomorrow" : date.toLocaleDateString("en-US", { month: "short", day: "numeric" });
  return `${day}, ${date.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit", hour12: true })}`;
}

// Qoder buckets are keyed user / organization / addOn; the backend may not send a displayName for them.
const QODER_NAMES: Record<string, string> = { user: "Personal", organization: "Organization", addOn: "Add-on" };

/** Backend quotas map -> table rows, in the order the backend sent them. */
export function parseQuotaData(provider: string, data: LiveUsage | undefined): NormQuota[] {
  return Object.entries(data?.quotas ?? {}).flatMap(([key, q]) => {
    if (!q || typeof q !== "object") return [];
    // Qoder reports 0/0 for buckets the account does not have; rendering them is noise.
    if (provider === "qoder" && !q.unlimited && !(Number(q.total) > 0)) return [];
    return [{ name: q.displayName || (provider === "qoder" && QODER_NAMES[key]) || key, used: q.used || 0, total: q.total || 0, resetAt: q.resetAt || null, unlimited: q.unlimited === true, recurring: q.recurring !== false }];
  });
}

export const isDepleted = (quotas: NormQuota[]) => quotas.some((q) => !q.unlimited && q.total > 0 && calculatePercentage(q.used, q.total) <= DEPLETED_QUOTA_THRESHOLD);

/** First quota's remaining %, Infinity when unknown (sorts last when ascending). Used by the "% quota" sort. */
const firstRemaining = (quotas: NormQuota[]) => (quotas[0] && !quotas[0].unlimited ? getRemainingPercentage(quotas[0]) : Number.POSITIVE_INFINITY);

/** Stable group-by-provider: first-seen provider order, original order inside a group. */
const groupByProviderStable = (conns: Connection[]) => {
  const seen = new Map<string, Connection[]>();
  for (const c of conns) seen.set(c.provider, [...(seen.get(c.provider) ?? []), c]);
  return [...seen.values()].flat();
};

export function sortVisibleConnections(conns: Connection[], quotasOf: (id: string) => NormQuota[], expiringFirst: boolean, providerFilter: string, sort: QuotaSort) {
  const label = (c: Connection) => getConnectionLabel(c) || "";
  if (providerFilter !== "all" && sort !== "default") {
    const dir = sort === "remaining-asc" ? 1 : -1;
    return [...conns].sort((a, b) => {
      const ra = firstRemaining(quotasOf(a.id)), rb = firstRemaining(quotasOf(b.id));
      // Infinity - Infinity is NaN, so compare explicitly
      return (ra === rb ? 0 : (ra - rb) * dir) || label(a).localeCompare(label(b));
    });
  }
  if (!expiringFirst) return groupByProviderStable(conns);
  const earliest = (c: Connection) => Math.min(Number.POSITIVE_INFINITY, ...quotasOf(c.id).map((q) => (q.resetAt ? new Date(q.resetAt).getTime() : Number.POSITIVE_INFINITY)).filter(Number.isFinite));
  return [...conns].sort((a, b) => {
    const ea = earliest(a), eb = earliest(b);
    return (ea === eb ? 0 : ea < eb ? -1 : 1) || a.provider.localeCompare(b.provider) || label(a).localeCompare(label(b));
  });
}

export function getConnectionsEmptyMessage(eligible: number, filtered: number, providerLabel: string | null, accountFilter: AccountFilter) {
  if (!eligible) return { title: "No Providers Connected", description: "Connect to providers with OAuth to track your API quota limits and usage." };
  if (!filtered) return { title: "No Accounts Match Current Filters", description: providerLabel === null ? "Try changing the account status filter to see more quota trackers." : `No ${accountFilter === "inactive" ? "turned off" : accountFilter === "active" ? "active" : "matching"} accounts found for ${providerLabel}.` };
  return { title: "No Accounts On This Page", description: "Try moving to another page or refreshing the current filters." };
}

export function readStored<T>(key: string, fallback: T): T {
  try { const v = localStorage.getItem(key); return v === null ? fallback : (JSON.parse(v) as T); } catch { return fallback; }
}
export function writeStored(key: string, value: unknown) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* storage blocked: preference just will not persist */ }
}
