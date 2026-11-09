import { Shield, Wand2, Eraser } from "lucide-react";
import { useEffect, useState } from "react";
import { Button, Card, errText, PageHeader, SegmentedControl, Skeleton, Switch, useToast } from "../components/ui";
import type { Settings } from "../lib/api";
import { useSettings, useUpdateSettings } from "../lib/hooks";

const LEVELS = [
  { value: "lite", label: "Lite", desc: "Builds what you ask, names the lazier alternative in one line." },
  { value: "full", label: "Full", desc: "Enforces the ladder: stdlib and native first, shortest diff." },
  { value: "ultra", label: "Ultra", desc: "YAGNI extremist: deletion first, challenges the rest of the requirement." },
] as const;

function Row({ title, body, checked, onChange, disabled, last }: { title: string; body: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; last?: boolean }) {
  return (
    <div className={`flex items-center justify-between gap-6 py-4 ${last ? "" : "border-b border-border"}`}>
      <div className="min-w-0"><div className="font-medium">{title}</div><p className="text-muted text-[13px] mt-0.5 max-w-xl">{body}</p></div>
      <Switch checked={checked} label={title} disabled={disabled} onChange={onChange} />
    </div>
  );
}

function CardTitle({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return <h2 className="font-semibold flex items-center gap-2"><span className="text-accent">{icon}</span>{children}</h2>;
}

const MAX_PROMPT = 8000;

export default function TokenSaver() {
  const s = useSettings(), upd = useUpdateSettings(), toast = useToast();
  const d: Settings | undefined = s.data;
  const [prompt, setPrompt] = useState("");
  useEffect(() => { if (d) setPrompt(d.customPrompt ?? ""); }, [d?.customPrompt]); // eslint-disable-line react-hooks/exhaustive-deps
  if (s.isLoading || !d) return <Skeleton className="h-64" />;

  const set = (patch: Record<string, unknown>) => upd.mutate(patch, { onSuccess: () => toast("ok", "Saved"), onError: (e) => toast("err", errText(e)) });
  const level = d.ponytailLevel ?? "full";
  const on = (v: boolean | undefined, dflt: boolean) => v ?? dflt;

  return (
    <>
      <PageHeader title="Token Saver" subtitle="Rewrite requests before they reach the provider" />

      <Card className="p-4 mb-4">
        <div className="flex items-start justify-between gap-6">
          <div className="min-w-0">
            <CardTitle icon={<Wand2 className="size-4" />}>Ponytail</CardTitle>
            <p className="text-muted text-[13px] mt-1 max-w-xl">Bias the model toward minimal code: YAGNI, reuse the stdlib, deletion over addition. Adds a short instruction to the system prompt of every request.</p>
          </div>
          <Switch checked={on(d.ponytailEnabled, false)} label="Ponytail" disabled={upd.isPending} onChange={(v) => set({ ponytailEnabled: v })} />
        </div>
        {d.ponytailEnabled && (
          <div className="mt-4">
            <SegmentedControl label="Ponytail level" value={level} options={LEVELS.map(({ value, label }) => ({ value, label }))} onChange={(v) => set({ ponytailLevel: v })} />
            <p className="text-muted text-[13px] mt-2">{LEVELS.find((l) => l.value === level)?.desc}</p>
          </div>
        )}
      </Card>

      <Card className="p-4 mb-4">
        <div className="flex items-start justify-between gap-6">
          <div className="min-w-0">
            <CardTitle icon={<Eraser className="size-4" />}>Watermarks Remover</CardTitle>
            <p className="text-muted text-[13px] mt-1 max-w-xl">Strip AI provenance marks (invisible Unicode, C2PA) and AI transition clichés from outputs. Works by instructing the model through the system prompt; it does not filter responses.</p>
          </div>
          <Switch checked={on(d.watermarksRemoverEnabled, false)} label="Watermarks Remover" disabled={upd.isPending} onChange={(v) => set({ watermarksRemoverEnabled: v })} />
        </div>
      </Card>

      <Card className="p-4">
        <CardTitle icon={<Shield className="size-4" />}>Guards &amp; Shields</CardTitle>
        <div className="mt-2">
          <Row title="Loop Guard" body="Detects repeating tool-call sequences and text-only planning loops, and injects a correction to stop infinite agent loops."
            checked={on(d.loopGuardEnabled, true)} disabled={upd.isPending} onChange={(v) => set({ loopGuardEnabled: v })} />
          <Row title="Circuit Breaker" body="Fails fast on a provider that keeps erroring (5xx, timeouts), so combos move on instead of waiting. Probes again after 30 seconds."
            checked={on(d.circuitBreakerEnabled, true)} disabled={upd.isPending} onChange={(v) => set({ circuitBreakerEnabled: v })} />
          <Row title="Semaphore (concurrency limiter)" body="At most 3 simultaneous requests per provider key; extra requests queue instead of triggering rate limits (429)."
            checked={on(d.semaphoreEnabled, true)} disabled={upd.isPending} onChange={(v) => set({ semaphoreEnabled: v })} />
          <Row last title="Custom system prompt" body="Appends your own instruction to every request, independently of the options above."
            checked={on(d.customPromptEnabled, false)} disabled={upd.isPending} onChange={(v) => set({ customPromptEnabled: v })} />
        </div>
        {d.customPromptEnabled && (
          <div className="pt-3">
            <textarea aria-label="Custom system prompt" value={prompt} maxLength={MAX_PROMPT} rows={5} onChange={(e) => setPrompt(e.target.value)}
              placeholder="e.g. Always answer in the user's language."
              className="w-full rounded-lg border border-border bg-surface p-3 text-sm focus:outline-none focus:ring-2 focus:ring-accent/40" />
            <div className="flex items-center justify-between mt-2">
              <span className="text-xs text-muted tabular-nums">{prompt.length} / {MAX_PROMPT}</span>
              <Button variant="primary" loading={upd.isPending} disabled={prompt === (d.customPrompt ?? "")} onClick={() => set({ customPrompt: prompt })}>Save prompt</Button>
            </div>
          </div>
        )}
      </Card>
    </>
  );
}
