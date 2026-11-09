import { Plus, Zap } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { BuiltinCard } from "../components/providers/Builtin";
import { HealthTiles } from "../components/providers/HealthTiles";
import { NodeModal } from "../components/providers/NodeModal";
import { ProviderCard } from "../components/providers/ProviderCard";
import { TestResultsModal } from "../components/providers/TestResultsModal";
import { Button, errText, PageHeader, Skeleton, useToast } from "../components/ui";
import { useNow } from "../components/usage/shared";
import type { BuiltinProvider, NodeType, TestBatch } from "../lib/api";
import { useBuiltinProviders, useConnections, useNodes, useTestBatch } from "../lib/hooks";

const GRID = "grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4";

/** Section title on the left, Test All (+ extra actions) on the right, then a compact card grid. */
function Section({ title, count, onTest, testing, actions, loading, empty, children }: {
  title: string; count: number; onTest?: () => void; testing: boolean; actions?: React.ReactNode; loading: boolean; empty: string; children: React.ReactNode;
}) {
  return (
    <section aria-label={title} className="mb-8">
      <div className="flex flex-wrap items-center justify-between gap-2 mb-3">
        <h2 className="text-base font-semibold">{title}<span className="ml-2 text-xs font-normal text-muted tabular-nums">{count}</span></h2>
        <div className="flex flex-wrap gap-2">
          {onTest && <Button size="sm" loading={testing} onClick={onTest} title="Test every enabled key in this section">{!testing && <Zap className="size-4" />}Test All</Button>}
          {actions}
        </div>
      </div>
      {loading ? <div className={GRID}>{[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-14" />)}</div>
        : count ? <div className={GRID}>{children}</div>
        : <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-[13px] text-muted">{empty}</p>}
    </section>
  );
}

export default function Providers() {
  const nodes = useNodes(), conns = useConnections(), navigate = useNavigate(), now = useNow(15_000), toast = useToast(), test = useTestBatch(), builtin = useBuiltinProviders();
  const [adding, setAdding] = useState<NodeType | null>(null), [results, setResults] = useState<TestBatch | null>(null), [busy, setBusy] = useState("");
  const all = conns.data ?? [];
  const has = (id: string) => all.some((c) => c.isActive && c.provider === id);
  const byCat = (c: BuiltinProvider["category"]) => (builtin.data ?? []).filter((p) => p.category === c);

  const run = (section: string, ids: string[]) => { setBusy(section); test.mutate(ids, {
    onSuccess: (r) => {
      const { total, passed, failed, skipped } = r.summary, note = skipped ? ` ${skipped} key(s) of experimental providers were skipped: test those one by one.` : "";
      if (total) { setResults(r); toast(failed ? "err" : "ok", `${passed}/${total} passed${failed ? `, ${failed} failed` : ""}.${note}`); }
      else if (skipped) toast("ok", note.trim());
    },
    onError: (e) => toast("err", errText(e)), onSettled: () => setBusy(""),
  }); };
  // a section's Test All only shows once one of its providers has an enabled key
  const testFor = (section: string, ids: string[]) => (ids.some(has) ? () => run(section, ids) : undefined);

  const custom = (nodes.data ?? []).map((n) => n.id), customLoading = nodes.isLoading || conns.isLoading;
  const cat = (c: BuiltinProvider["category"], title: string, empty: string) => {
    const list = byCat(c);
    return (
      <Section title={title} count={list.length} loading={builtin.isLoading} empty={empty} testing={busy === c} onTest={testFor(c, list.map((p) => p.id))}>
        {list.map((p) => <BuiltinCard key={p.id} p={p} now={now} conns={all.filter((x) => x.provider === p.id)} />)}
      </Section>
    );
  };

  return (
    <>
      <PageHeader title="Providers" subtitle="Custom endpoints and built-in providers, with their key pools" />
      {custom.length > 0 && <HealthTiles />}
      <Section title="Custom Provider" count={custom.length} loading={customLoading} testing={busy === "custom"} onTest={testFor("custom", custom)}
        empty="No custom providers yet — add any OpenAI- or Anthropic-compatible service (OpenRouter, Ollama, vLLM, …)."
        actions={<>
          <Button size="sm" variant="primary" onClick={() => setAdding("anthropic-compatible")}><Plus className="size-4" />Anthropic Compatible</Button>
          <Button size="sm" onClick={() => setAdding("openai-compatible")}><Plus className="size-4" />OpenAI Compatible</Button>
        </>}>
        {(nodes.data ?? []).map((node) => <ProviderCard key={node.id} node={node} conns={all.filter((c) => c.provider === node.id)} now={now} />)}
      </Section>
      {cat("oauth", "OAuth Provider", "No providers here yet.")}
      {cat("free", "Free Provider", "No providers here yet.")}
      {cat("apikey", "API Key Provider", "No providers here yet.")}
      {adding && <NodeModal type={adding} onClose={() => setAdding(null)} onCreated={(n) => navigate(`/dashboard/providers/${n.id}`)} />}
      {results && <TestResultsModal data={results} onClose={() => setResults(null)} />}
    </>
  );
}
