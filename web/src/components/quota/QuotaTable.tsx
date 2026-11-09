import { EyeOff } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { cn } from "../ui";
import { formatResetTime, formatResetTimeDisplay, getRemainingPercentage, getStatusColor, STATUS_STYLE, type NormQuota } from "./limits";

const PAGE_SIZE = 10;
const pgBtn = "flex h-6 items-center rounded-md border border-border px-2 text-[10px] transition-colors hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer";

/** Compact per-connection quota list: name, remaining bar with used/total, reset countdown (port of XRouter QuotaTable, compact mode). */
export function QuotaTable({ quotas, now, showSortLabel, onHide }: { quotas: NormQuota[]; now: number; showSortLabel?: boolean; onHide?: (q: NormQuota) => void }) {
  const [page, setPage] = useState(1);
  const rows = useMemo(() => quotas.map((q) => ({ ...q, remaining: getRemainingPercentage(q) })), [quotas]);
  const totalPages = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const names = quotas.map((q) => q.name).join("|");
  useEffect(() => setPage(1), [names]); // parent passes a fresh array every render, so key on content
  const cur = Math.min(page, totalPages);
  if (rows.length === 0) return null;
  const start = (cur - 1) * PAGE_SIZE + 1, end = Math.min(cur * PAGE_SIZE, rows.length);

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="text-[10px] text-muted">{rows.length} quota{rows.length > 1 ? "s" : ""}</div>
        {showSortLabel && <div className="rounded-md border border-border bg-surface-2/50 px-2 py-1 text-[10px] text-muted">Sorted by account remaining</div>}
      </div>

      <div className="space-y-px">
        {rows.slice(start - 1, end).map((q, i) => {
          const c = STATUS_STYLE[getStatusColor(q.remaining)];
          const countdown = formatResetTime(q.resetAt, now), resetDisplay = formatResetTimeDisplay(q.resetAt, now);
          // one-shot packs (recurring:false) have a hard expiry rather than a refresh
          const countdownLabel = q.recurring ? `in ${countdown}` : `expires in ${countdown}`;
          const usedTotal = `${q.used.toLocaleString()} / ${q.total > 0 ? q.total.toLocaleString() : "∞"}`;
          return (
            <div key={`${q.name}-${i}`} className="flex items-center gap-2 border-b border-border/60 px-1.5 py-1 transition-colors hover:bg-surface-2/50">
              <div className="flex w-36 min-w-0 items-center gap-1.5">
                <span className="shrink-0 text-[10px]" aria-hidden>{c.emoji}</span>
                <span className="truncate text-[11px] font-medium" title={q.name}>{q.name}</span>
              </div>

              <div className="min-w-0 flex-1 space-y-1">
                {!q.unlimited && (
                  <div role="progressbar" aria-label={`${q.name} remaining`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(q.remaining, 100)}
                    className={cn("h-1 overflow-hidden rounded-full border", c.track, q.remaining === 0 ? "border-border" : "border-transparent")}>
                    <div className={cn("h-full transition-all duration-300", c.bg)} style={{ width: `${Math.min(q.remaining, 100)}%` }} />
                  </div>
                )}
                <div className="flex min-w-0 items-center justify-between gap-1 text-[10px]">
                  <span className="truncate text-muted tabular-nums" title={q.unlimited ? `${q.used.toLocaleString()} used · Unlimited` : usedTotal}>
                    {q.unlimited ? `${q.used.toLocaleString()} used · Unlimited` : usedTotal}
                  </span>
                  <span className={cn("shrink-0 font-medium", q.unlimited ? "text-ok" : c.text)}>{q.unlimited ? "Unlimited" : `${q.remaining}%`}</span>
                </div>
              </div>

              <div className="min-w-0 shrink">
                {countdown !== "-" ? (
                  <div className="truncate text-[11px] font-medium" title={resetDisplay ?? ""}>{countdownLabel}</div>
                ) : resetDisplay ? (
                  <div className="truncate text-[11px] font-medium" title={resetDisplay}>{resetDisplay}</div>
                ) : (
                  <div className="text-[11px] italic text-muted">N/A</div>
                )}
              </div>

              {onHide && (
                <button type="button" onClick={() => onHide(q)} title="Hide this quota row" aria-label={`Hide quota ${q.name}`}
                  className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted transition-colors hover:bg-surface-2 hover:text-text cursor-pointer">
                  <EyeOff className="size-[15px]" />
                </button>
              )}
            </div>
          );
        })}
      </div>

      {totalPages > 1 && (
        <div className="rounded-md border border-border bg-surface-2/50 px-2 py-1.5">
          <div className="flex items-center justify-between gap-2 text-[10px] text-muted">
            <span>Showing {start}-{end} of {rows.length}</span>
            <span>Page {cur} / {totalPages}</span>
          </div>
          <div className="mt-1.5 flex items-center justify-end gap-1">
            <button type="button" className={pgBtn} disabled={cur === 1} onClick={() => setPage(Math.max(1, cur - 1))}>Prev</button>
            <button type="button" className={pgBtn} disabled={cur === totalPages} onClick={() => setPage(Math.min(totalPages, cur + 1))}>Next</button>
          </div>
        </div>
      )}
    </div>
  );
}
