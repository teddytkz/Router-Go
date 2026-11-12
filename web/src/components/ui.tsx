import { clsx, type ClassValue } from "clsx";
import { Check, Copy, Loader2, X } from "lucide-react";
import { createContext, useCallback, useContext, useEffect, useId, useRef, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";

export const cn = (...v: ClassValue[]) => clsx(v);

// ---------- Button ----------
type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "ghost" | "danger"; size?: "sm" | "md"; loading?: boolean };
export function Button({ variant = "secondary", size = "md", loading, className, children, disabled, ...p }: BtnProps) {
  return (
    <button
      {...p}
      disabled={disabled || loading}
      className={cn(
        "inline-flex items-center whitespace-nowrap justify-center gap-2 rounded-lg font-medium transition-colors select-none cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed",
        size === "sm" ? "h-8 px-3 text-[13px]" : "h-9 px-4 text-sm",
        variant === "primary" && "bg-accent text-accent-fg hover:brightness-110 shadow-sm",
        variant === "secondary" && "bg-surface border border-border hover:bg-surface-2",
        variant === "ghost" && "hover:bg-surface-2 text-muted hover:text-text",
        variant === "danger" && "bg-err-soft text-err hover:brightness-95 border border-transparent",
        className,
      )}
    >
      {loading && <Loader2 className="size-4 animate-spin" aria-hidden />}
      {children}
    </button>
  );
}

// ---------- Card ----------
export function Card({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("rounded-xl border border-border bg-surface shadow-[var(--shadow)]", className)}>{children}</div>;
}

// ---------- Fields ----------
const field = "w-full h-9 rounded-lg border border-border bg-surface px-3 text-sm placeholder:text-muted/70 focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/25 transition";
export function Field({ label, hint, error, children }: { label: string; hint?: string; error?: string; children: (id: string) => ReactNode }) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-[13px] font-medium">{label}</label>
      {children(id)}
      {error ? <p className="text-xs text-err">{error}</p> : hint ? <p className="text-xs text-muted break-words">{hint}</p> : null}
    </div>
  );
}
export const Input = ({ className, ...p }: InputHTMLAttributes<HTMLInputElement>) => <input {...p} className={cn(field, className)} />;
export const Select = ({ className, ...p }: SelectHTMLAttributes<HTMLSelectElement>) => <select {...p} className={cn(field, "pr-8", className)} />;

export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button
      type="button" role="switch" aria-checked={checked} aria-label={label} disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn("relative h-6 w-11 shrink-0 rounded-full transition-colors cursor-pointer disabled:opacity-50", checked ? "bg-accent" : "bg-border")}
    >
      <span className={cn("absolute top-0.5 left-0.5 size-5 rounded-full bg-white shadow transition-transform", checked && "translate-x-5")} />
    </button>
  );
}

// ---------- SegmentedControl / Tabs ----------
export function SegmentedControl<T extends string>({ value, onChange, options, label, small }: { value: T; onChange: (v: T) => void; options: { value: T; label: string }[]; label: string; small?: boolean }) {
  return (
    <div role="group" aria-label={label} className="inline-flex max-w-full overflow-x-auto rounded-lg border border-border bg-surface p-0.5">
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={value === o.value} onClick={() => onChange(o.value)}
          className={cn("rounded-md font-medium whitespace-nowrap cursor-pointer transition-colors", small ? "h-7 px-2.5 text-xs" : "h-8 px-3 text-[13px]", value === o.value ? "bg-accent-soft text-accent" : "text-muted hover:text-text")}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Tabs<T extends string>({ value, onChange, tabs, label }: { value: T; onChange: (v: T) => void; tabs: { value: T; label: string }[]; label: string }) {
  return (
    <div role="tablist" aria-label={label} className="flex gap-1 border-b border-border overflow-x-auto">
      {tabs.map((t) => (
        <button key={t.value} type="button" role="tab" aria-selected={value === t.value} onClick={() => onChange(t.value)}
          className={cn("h-10 px-4 -mb-px border-b-2 text-sm font-medium whitespace-nowrap cursor-pointer transition-colors", value === t.value ? "border-accent text-accent" : "border-transparent text-muted hover:text-text")}>
          {t.label}
        </button>
      ))}
    </div>
  );
}

// ---------- Badge ----------
export function Badge({ tone = "neutral", children, className }: { tone?: "neutral" | "ok" | "warn" | "err" | "accent"; children: ReactNode; className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
      tone === "neutral" && "bg-surface-2 text-muted", tone === "ok" && "bg-ok-soft text-ok", tone === "warn" && "bg-warn-soft text-warn",
      tone === "err" && "bg-err-soft text-err", tone === "accent" && "bg-accent-soft text-accent", className)}>
      {children}
    </span>
  );
}

export const Skeleton = ({ className }: { className?: string }) => <div className={cn("skeleton", className)} aria-hidden />;

export function EmptyState({ icon, title, body, action }: { icon: ReactNode; title: string; body?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center text-center py-12 px-6">
      <div className="grid size-12 place-items-center rounded-xl bg-accent-soft text-accent mb-4">{icon}</div>
      <h3 className="font-semibold">{title}</h3>
      {body && <p className="text-muted mt-1 max-w-sm">{body}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-3 mb-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="text-muted mt-0.5">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

// ---------- Copy ----------
export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [done, setDone] = useState(false), toast = useToast();
  return (
    <Button size="sm" variant="ghost" aria-label={label} onClick={async () => {
      try {
        await navigator.clipboard.writeText(value);
        toast("ok", "Copied to clipboard");
        setDone(true); setTimeout(() => setDone(false), 1400);
      } catch { toast("err", "Could not copy: the browser blocked clipboard access"); }
    }}>
      {done ? <Check className="size-4 text-ok" /> : <Copy className="size-4" />}
    </Button>
  );
}

// ---------- Modal ----------
export function Modal({ open, onClose, title, children, footer }: { open: boolean; onClose: () => void; title: string; children: ReactNode; footer?: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  // callers pass inline arrows, so keep the latest onClose in a ref: depending on it would re-run the effect on every
  // keystroke in a form field and steal the focus (onto the close button, closing the dialog on the next key)
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onCloseRef.current();
    document.addEventListener("keydown", onKey);
    // focus once, when the dialog opens: the first form field, else the first button that is not the header's close button
    (ref.current?.querySelector<HTMLElement>("input,select,textarea") ?? ref.current?.querySelector<HTMLElement>("button:not([aria-label='Close'])"))?.focus();
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 grid place-items-end sm:place-items-center p-0 sm:p-4" role="dialog" aria-modal="true" aria-label={title}>
      <div className="absolute inset-0 bg-black/50 backdrop-blur-[2px]" onClick={onClose} />
      <div ref={ref} className="pop relative w-full sm:max-w-lg max-h-[92vh] flex flex-col rounded-t-2xl sm:rounded-2xl border border-border bg-surface shadow-2xl">
        <div className="flex items-center justify-between px-5 h-14 border-b border-border shrink-0">
          <h2 className="font-semibold">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close"><X className="size-4" /></Button>
        </div>
        <div className="p-5 overflow-y-auto space-y-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 px-5 py-3 border-t border-border shrink-0">{footer}</div>}
      </div>
    </div>
  );
}

export function ConfirmModal({ open, onClose, onConfirm, title, body, confirmLabel = "Delete", loading }: { open: boolean; onClose: () => void; onConfirm: () => void; title: string; body: string; confirmLabel?: string; loading?: boolean }) {
  return (
    <Modal open={open} onClose={onClose} title={title}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="danger" loading={loading} onClick={onConfirm}>{confirmLabel}</Button></>}>
      <p className="text-muted">{body}</p>
    </Modal>
  );
}

// ---------- Toasts ----------
type Toast = { id: number; tone: "ok" | "err"; text: string };
const ToastCtx = createContext<(tone: Toast["tone"], text: string) => void>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const push = useCallback((tone: Toast["tone"], text: string) => {
    const id = Date.now() + Math.random();
    setItems((s) => [...s, { id, tone, text }]);
    setTimeout(() => setItems((s) => s.filter((t) => t.id !== id)), 4200);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="fixed bottom-4 right-4 z-[60] flex flex-col gap-2 w-[min(360px,calc(100vw-2rem))]" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={cn("pop rounded-lg border px-3.5 py-2.5 text-sm shadow-lg bg-surface flex items-start gap-2.5", t.tone === "ok" ? "border-ok/40" : "border-err/40")}>
            <span className={cn("mt-1.5 size-2 rounded-full shrink-0", t.tone === "ok" ? "bg-ok" : "bg-err")} />
            <span className="break-words min-w-0">{t.text}</span>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

export const errText = (e: unknown) => (e instanceof Error ? e.message : "Something went wrong");

export const fmtNum = (n: number) => (n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e4 ? (n / 1e3).toFixed(1) + "k" : n.toLocaleString());
export const fmtTime = (iso: string) => new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" });
