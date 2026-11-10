import { AudioLines, Brain, Eye, type LucideIcon } from "lucide-react";
import type { Caps } from "../lib/api";
import { cn } from "./ui";

/** 128000 -> "128K", 1500000 -> "1.5M". */
export const fmtTokens = (n: number) => (n >= 1e6 ? `${+(n / 1e6).toFixed(1)}M` : n >= 1000 ? `${Math.round(n / 1000)}K` : String(n));
/** Accepts `128000`, `128k`, `1m`, `1.5M`; null when it is not a number. */
export function parseTokens(s: string): number | null {
  const m = /^(\d+(?:\.\d+)?)\s*([km]?)$/i.exec(s.trim());
  return m ? Math.round(+m[1] * (m[2].toLowerCase() === "m" ? 1e6 : m[2] ? 1e3 : 1)) : null;
}

export const CAP_FLAGS: { key: "vision" | "reasoning" | "audioInput"; label: string; Icon: LucideIcon; tone: string }[] = [
  { key: "vision", label: "Vision", Icon: Eye, tone: "text-accent bg-accent-soft" },
  { key: "reasoning", label: "Reasoning", Icon: Brain, tone: "text-warn bg-warn-soft" },
  { key: "audioInput", label: "Audio input", Icon: AudioLines, tone: "text-ok bg-ok-soft" },
];

const Dot = () => <span aria-hidden className="absolute -top-0.5 -right-0.5 size-1.5 rounded-full bg-warn" />;

/**
 * Icon chips for the capabilities that are on, plus the context window. Pass `detected` to mark manual overrides with a dot.
 * size "xs": bare icons only (for tight chips); "sm": chips with the context window.
 */
export function CapBadges({ caps, detected, size = "sm", className }: { caps?: Caps; detected?: Caps; size?: "xs" | "sm"; className?: string }) {
  if (!caps) return null;
  const xs = size === "xs", on = CAP_FLAGS.filter((c) => caps[c.key]);
  const ctx = !xs && caps.contextWindow > 0;
  if (!on.length && !ctx) return null;
  const manual = (k: keyof Caps) => !!detected && detected[k] !== caps[k];
  return (
    <span className={cn("inline-flex items-center gap-1 shrink-0", className)}>
      {on.map(({ key, label, Icon, tone }) => {
        const t = manual(key) ? `${label} (set manually)` : label;
        return (
          <span key={key} role="img" aria-label={t} title={t} className={cn("relative inline-grid place-items-center", tone, xs ? "size-6 rounded-md" : "size-5 rounded-md")}>
            <Icon className="size-3.5" aria-hidden />{manual(key) && <Dot />}
          </span>
        );
      })}
      {ctx && (() => {
        const t = `Context window: ${caps.contextWindow.toLocaleString("en-US")} tokens${manual("contextWindow") ? " (set manually)" : ""}`;
        return <span role="img" aria-label={t} title={t} className="relative inline-flex items-center h-5 rounded-md border border-border px-1.5 text-[11px] font-medium text-muted tabular-nums">{fmtTokens(caps.contextWindow)}{manual("contextWindow") && <Dot />}</span>;
      })()}
    </span>
  );
}
