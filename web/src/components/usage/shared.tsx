// Small helpers shared by the usage charts/tables: palette, number formats, hover tooltip, clock.
import { useEffect, useRef, useState, type PointerEvent, type ReactNode } from "react";

// Token-derived so light/dark both work; color-mix variants give 8 distinguishable bars.
export const PALETTE = [
  "var(--accent)", "var(--ok)", "var(--warn)", "var(--err)",
  "color-mix(in srgb, var(--accent) 55%, var(--surface))", "color-mix(in srgb, var(--ok) 55%, var(--surface))",
  "color-mix(in srgb, var(--warn) 55%, var(--surface))", "color-mix(in srgb, var(--err) 55%, var(--surface))",
];

/** 1.2K / 3.4M axis format. */
export const fmtAxis = (n: number) => {
  const f = (v: number, s: string) => v.toFixed(1).replace(/\.0$/, "") + s;
  return n >= 1e9 ? f(n / 1e9, "B") : n >= 1e6 ? f(n / 1e6, "M") : n >= 1e3 ? f(n / 1e3, "K") : String(Math.round(n));
};
export const fmtFull = (n: number) => Math.round(n).toLocaleString();

/** Round a max up to 1/2/5 x 10^k so the 4 y ticks land on clean numbers. */
export const niceMax = (m: number) => {
  if (m <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(m)), r = m / p;
  return (r <= 1 ? 1 : r <= 2 ? 2 : r <= 5 ? 5 : 10) * p;
};

export function ago(iso: string | undefined, now = Date.now()) {
  if (!iso) return "Never";
  const m = Math.floor((now - new Date(iso).getTime()) / 60000);
  if (m < 1) return "Just now";
  if (m < 60) return `${m}m ago`;
  if (m < 1440) return `${Math.floor(m / 60)}h ago`;
  return new Date(iso).toLocaleDateString();
}

/** Re-renders the caller every `ms` and returns the current time. */
export function useNow(ms: number) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), ms); return () => clearInterval(t); }, [ms]);
  return now;
}

/** Pointer-tracked tooltip: call `at(e, value)` from a pointer handler, render `<Tip>` inside the `ref` container. */
export function useTip<T = number>() {
  const ref = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<{ x: number; y: number; v: T } | null>(null);
  const at = (e: PointerEvent, v: T) => {
    const r = ref.current?.getBoundingClientRect();
    if (r) setTip({ x: e.clientX - r.left, y: e.clientY - r.top, v });
  };
  return { ref, tip, at, clear: () => setTip(null) };
}

export function Tip({ tip, boxRef, children }: { tip: { x: number; y: number } | null; boxRef: React.RefObject<HTMLDivElement | null>; children: ReactNode }) {
  if (!tip) return null;
  const flip = tip.x > (boxRef.current?.clientWidth ?? 0) / 2;
  return (
    <div role="tooltip" className="pointer-events-none absolute z-10 rounded-lg border border-border bg-surface px-2.5 py-1.5 text-xs shadow-lg whitespace-nowrap"
      style={{ left: tip.x, top: tip.y, transform: `translate(${flip ? "calc(-100% - 12px)" : "12px"}, -50%)` }}>
      {children}
    </div>
  );
}

export const Dot = ({ color }: { color: string }) => <i className="inline-block size-2 rounded-full mr-1.5 align-middle" style={{ background: color }} />;
