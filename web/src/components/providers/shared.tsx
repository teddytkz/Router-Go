// Helpers shared by the providers list and detail pages: connection status, badges, initials tile.
import type { Connection, ProviderNode } from "../../lib/api";
import { Network } from "lucide-react";
import { useProxyPools } from "../../lib/hooks";
import { Badge } from "../ui";

export type ConnState = "active" | "error" | "cooling" | "disabled" | "unknown";

export const isCooling = (c: Connection, now = Date.now()) => !!c.rateLimitedUntil && new Date(c.rateLimitedUntil).getTime() > now;

/** Effective status: a rate-limit whose window has passed counts as active again. */
export function connState(c: Connection, now = Date.now()): ConnState {
  if (!c.isActive) return "disabled";
  if (isCooling(c, now)) return "cooling";
  if (c.testStatus === "error") return "error";
  if (c.testStatus === "active" || c.testStatus === "unavailable") return "active";
  return "unknown";
}

/** Short tag for the last upstream failure: 429 / 5XX / AUTH / NET / ERR. */
export function errTag(c: Pick<Connection, "errorCode" | "lastError">): string {
  const s = `${c.errorCode ?? ""} ${c.lastError ?? ""}`.toLowerCase();
  if (/\b429\b|rate.?limit|too many/.test(s)) return "429";
  if (/\b5\d\d\b/.test(s)) return "5XX";
  if (/\b40[13]\b|auth|unauthori|forbidden|invalid.*key|api key/.test(s)) return "AUTH";
  if (/timeout|timed out|network|dns|refused|reset|eof|dial|no such host/.test(s)) return "NET";
  return "ERR";
}

export type Summary = { total: number; connected: number; error: number; allDisabled: boolean; last?: Connection };
export function summarize(conns: Connection[], now = Date.now()): Summary {
  const states = conns.map((c) => ({ c, s: connState(c, now) }));
  const errs = states.filter((x) => x.s === "error" || x.s === "cooling").map((x) => x.c);
  const last = errs.filter((c) => c.lastError || c.errorCode).sort((a, b) => (b.lastErrorAt ?? "").localeCompare(a.lastErrorAt ?? ""))[0];
  return { total: conns.length, connected: states.filter((x) => x.s === "active").length, error: errs.length, allDisabled: conns.length > 0 && conns.every((c) => !c.isActive), last };
}

export const isAnthropic = (n: ProviderNode) => n.type === "anthropic-compatible";
export const typeLabel = (n: ProviderNode) => (isAnthropic(n) ? "Messages" : n.apiType === "responses" ? "Responses" : "Chat");
export const apiLabel = (n: ProviderNode) => (isAnthropic(n) ? "Messages API" : n.apiType === "responses" ? "Responses API" : "Chat Completions");
export const endpointPath = (n: ProviderNode) => (isAnthropic(n) ? "messages" : n.apiType === "responses" ? "responses" : "chat/completions");

export function TypeBadge({ node }: { node: ProviderNode }) {
  return <Badge tone={isAnthropic(node) ? "warn" : "ok"}>{typeLabel(node)}</Badge>;
}

/** Vendor logo for a custom provider: OpenAI for OpenAI-compatible, Anthropic for Anthropic-compatible. */
export function NodeIcon({ node, className = "size-10" }: { node: ProviderNode; className?: string }) {
  return <img src={`/providers/${isAnthropic(node) ? "anthropic" : "openai"}.svg`} alt="" aria-hidden className={`shrink-0 rounded-xl object-cover ${className}`} />;
}

export const Dot = ({ tone }: { tone: "ok" | "err" | "warn" | "muted" }) => (
  <i aria-hidden className={`inline-block size-1.5 rounded-full ${tone === "ok" ? "bg-ok" : tone === "err" ? "bg-err" : tone === "warn" ? "bg-warn" : "bg-muted"}`} />
);

/** The one status line under a provider name: "No connections", "2 Connected", "Disabled", ... */
export function StatusLine({ s, empty = "No connections" }: { s: Summary; empty?: string }) {
  if (s.total === 0) return <span className="text-xs text-muted">{empty}</span>;
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {s.allDisabled && <Badge>Disabled</Badge>}
      {s.connected > 0 && <Badge tone="ok"><Dot tone="ok" />{s.connected} Connected</Badge>}
      {s.error > 0 && <Badge tone="err"><Dot tone="err" />{s.error} Error</Badge>}
      {s.connected === 0 && s.error === 0 && !s.allDisabled && <Badge>{s.total} Untested</Badge>}
    </span>
  );
}

export const fmtMs = (n: number | null | undefined) => (n == null ? "—" : n < 1000 ? `${Math.round(n)}ms` : `${(n / 1000).toFixed(1)}s`);

/** "via <pool>" when the connection sends its traffic through a proxy pool. */
export function ProxyBadge({ c }: { c: Connection }) {
  const id = c.providerSpecificData?.proxyPoolId, pools = useProxyPools();
  if (!id || id === "__none__") return null;
  const p = pools.data?.find((x) => x.id === id);
  return <Badge tone={p && p.isActive ? "accent" : "warn"}><Network className="size-3" aria-hidden /><span title={p ? undefined : "This pool no longer exists"}>{p ? p.name : "unknown pool"}</span></Badge>;
}
