import { Check, CheckCircle2, ListPlus, Loader2, Plus, RefreshCw, Search, SlidersHorizontal, TriangleAlert, Trash2, X, Zap } from "lucide-react";
import { useMemo, useState } from "react";
import type { ProviderNode } from "../../lib/api";
import { useAddCustomModel, useCustomModels, useDeleteCustomModel, useNodeModels, useTestModel } from "../../lib/hooks";
import { CapBadges, fmtTokens } from "../CapBadges";
import { CapsEditor } from "./CapsEditor";
import { Badge, Button, Card, ConfirmModal, CopyButton, errText, Input, Skeleton, cn, useToast } from "../ui";
import { fmtMs } from "./shared";

type Result = { state: "testing" } | { state: "ok" | "fail"; msg: string };

/**
 * A provider's models in two lists. Only ADDED models can be used by clients (as `prefix/model`);
 * the upstream's own /models list is just a catalogue to pick from.
 */
export function ModelsCard({ node }: { node: ProviderNode }) {
  const toast = useToast(), custom = useCustomModels(), upstream = useNodeModels(node.id);
  const add = useAddCustomModel(), del = useDeleteCustomModel(), testModel = useTestModel();
  const [draft, setDraft] = useState(""), [search, setSearch] = useState(""), [showAdded, setShowAdded] = useState(false);
  const [adding, setAdding] = useState<string | null>(null), [bulk, setBulk] = useState(false);
  const [editing, setEditing] = useState<string | null>(null), [results, setResults] = useState<Record<string, Result>>({}), [confirm, setConfirm] = useState<string | null>(null);

  const added = (custom.data ?? []).filter((m) => m.providerAlias === node.prefix);
  const addedIds = useMemo(() => new Set(added.map((m) => m.id.toLowerCase())), [added]);
  const up = upstream.data?.upstream ?? [];
  const pending = up.filter((id) => !addedIds.has(id.toLowerCase()));
  const needle = search.trim().toLowerCase();
  const listed = (showAdded ? up : pending).filter((id) => !needle || id.toLowerCase().includes(needle));
  const code = (id: string) => `${node.prefix}/${id}`;
  const errors = Object.entries(results).filter((e): e is [string, { state: "fail"; msg: string }] => e[1].state === "fail" && !addedIds.has(e[0].toLowerCase()));

  const addOne = (id: string, done?: () => void) => {
    setAdding(id);
    add.mutate({ providerAlias: node.prefix, id }, {
      onSuccess: () => { toast("ok", `${id} added — clients can now use ${code(id)}`); done?.(); },
      onError: (e) => toast("err", errText(e)), onSettled: () => setAdding(null),
    });
  };
  const addDraft = () => {
    const id = draft.trim();
    if (!id) return;
    if (addedIds.has(id.toLowerCase())) return toast("err", `${id} is already added`);
    addOne(id, () => setDraft(""));
  };
  const addAll = async () => {
    setBulk(true);
    try {
      for (let i = 0; i < pending.length; i += 8) await Promise.all(pending.slice(i, i + 8).map((id) => add.mutateAsync({ providerAlias: node.prefix, id })));
      toast("ok", `Added ${pending.length} model${pending.length === 1 ? "" : "s"}`);
    } catch (e) { toast("err", errText(e)); } finally { setBulk(false); }
  };
  const test = async (id: string) => {
    setResults((s) => ({ ...s, [id]: { state: "testing" } }));
    let res: Result;
    try { const r = await testModel.mutateAsync(code(id)); res = r.ok ? { state: "ok", msg: `Works${r.latencyMs != null ? ` · ${fmtMs(r.latencyMs)}` : ""}` } : { state: "fail", msg: r.error ?? "Test failed" }; toast(r.ok ? "ok" : "err", `${code(id)}: ${r.ok ? r.reply || "works, but returned no text" : res.msg}`); }
    catch (e) { res = { state: "fail", msg: errText(e) }; toast("err", `${code(id)}: ${res.msg}`); }
    setResults((s) => ({ ...s, [id]: res }));
  };

  const ResultNote = ({ id }: { id: string }) => {
    const r = results[id];
    return r && r.state !== "testing" ? <span role="status" className={cn("text-xs break-words min-w-0", r.state === "ok" ? "text-ok" : "text-err")}>{r.msg}</span> : null;
  };
  const TestBtn = ({ id }: { id: string }) => (
    <Button size="sm" variant="ghost" onClick={() => test(id)} disabled={results[id]?.state === "testing"} aria-label={`Test ${code(id)}`}>
      {results[id]?.state === "testing" ? <Loader2 className="size-4 animate-spin" /> : <Zap className="size-4" />}Test
    </Button>
  );

  return (
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 border-b border-border">
        <div className="flex items-center gap-2"><h2 className="font-semibold">Models</h2><Badge tone={added.length ? "ok" : "warn"}>{added.length} added</Badge></div>
        <Button size="sm" onClick={() => upstream.refetch()} disabled={upstream.isFetching} aria-label="Refresh the upstream model list"><RefreshCw className={cn("size-4", upstream.isFetching && "animate-spin")} />Refresh upstream</Button>
      </div>
      <div className="p-4 space-y-6">
        {/* ---- usable ---- */}
        <section aria-labelledby="added-h" className="space-y-3">
          <div>
            <h3 id="added-h" className="text-sm font-semibold">Added models <span className="text-muted font-normal">· usable by clients</span></h3>
            <p className="text-[13px] text-muted mt-0.5">Clients request these as <span className="font-mono">{node.prefix}/&lt;model-id&gt;</span>. A model that is not added here is rejected, even if the provider supports it.</p>
          </div>
          <div className="flex gap-2">
            <Input aria-label="Model ID" className="flex-1 font-mono" placeholder="Add a model by ID, e.g. gpt-4o" value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); addDraft(); } }} />
            <Button variant="primary" onClick={addDraft} loading={!!adding && adding === draft.trim()} disabled={!draft.trim()}><Plus className="size-4" />Add</Button>
          </div>
          {custom.isLoading ? <Skeleton className="h-12" /> : added.length ? (
            <ul className="space-y-2">
              {added.map((m) => {
                const r = results[m.id];
                return (
                  <li key={m.id} className={cn("flex flex-col gap-1.5 rounded-lg border px-3 py-2", r?.state === "ok" ? "border-ok/60" : r?.state === "fail" ? "border-err/60" : "border-border")}>
                    <div className="flex flex-wrap items-center gap-2">
                      {r?.state === "ok" ? <Check className="size-4 shrink-0 text-ok" aria-label="Test passed" /> : r?.state === "fail" ? <X className="size-4 shrink-0 text-err" aria-label="Test failed" /> : <CheckCircle2 className="size-4 shrink-0 text-muted" aria-hidden />}
                      <span className="font-mono text-[13px] truncate min-w-0 flex-1" title={m.id}>{m.id}</span>
                      <span className="inline-flex items-center rounded-md bg-surface-2 pl-2 font-mono text-xs text-muted max-w-full"><span className="truncate">{code(m.id)}</span><CopyButton value={code(m.id)} label={`Copy ${code(m.id)}`} /></span>
                      <CapBadges caps={m.effective} detected={m.detected} />
                      <Button size="sm" variant="ghost" onClick={() => setEditing(m.id)} aria-haspopup="dialog" aria-label={`Edit capabilities of ${m.id}`} title="Edit capabilities"><SlidersHorizontal className="size-4" /></Button>
                      <TestBtn id={m.id} />
                      <Button size="sm" variant="ghost" className="text-err hover:text-err" onClick={() => setConfirm(m.id)} aria-label={`Remove ${m.id}`}><Trash2 className="size-4" />Remove</Button>
                    </div>
                    <ResultNote id={m.id} />
                  </li>
                );
              })}
            </ul>
          ) : (
            <div role="alert" className="flex items-start gap-3 rounded-lg border border-warn/40 bg-warn-soft px-3 py-3 text-[13px]">
              <TriangleAlert className="size-4 shrink-0 text-warn mt-0.5" aria-hidden />
              <p>No models added yet, so clients cannot use this provider. Add one above, or pick from the upstream list below.</p>
            </div>
          )}
        </section>

        {/* ---- catalogue ---- */}
        <section aria-labelledby="up-h" className="space-y-3 border-t border-border pt-5">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 id="up-h" className="text-sm font-semibold">Available from upstream <span className="text-muted font-normal">· not usable until added</span></h3>
              <p className="text-[13px] text-muted mt-0.5">What {node.name} reports on its <span className="font-mono">/models</span> endpoint.</p>
            </div>
            {pending.length > 1 && <Button size="sm" onClick={addAll} loading={bulk}>{!bulk && <ListPlus className="size-4" />}Add all ({pending.length})</Button>}
          </div>
          {upstream.isLoading ? <Skeleton className="h-24" /> : !up.length ? (
            <p className="rounded-lg border border-dashed border-border px-4 py-5 text-[13px] text-muted break-words">
              {upstream.data?.error ? `Could not list models: ${upstream.data.error}. ` : upstream.isError ? `Could not list models: ${errText(upstream.error)}. ` : "The provider reported no models. "}
              Add model IDs by hand above.
            </p>
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-3">
                {up.length > 8 && (
                  <div className="relative flex-1 basis-48 min-w-0">
                    <Search className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 size-4 text-muted" aria-hidden />
                    <Input aria-label="Filter upstream models" className="pl-8" placeholder={`Filter ${up.length} models`} value={search} onChange={(e) => setSearch(e.target.value)} />
                  </div>
                )}
                <label className="flex items-center gap-2 text-[13px] text-muted cursor-pointer"><input type="checkbox" checked={showAdded} onChange={(e) => setShowAdded(e.target.checked)} className="size-4 accent-[var(--accent)]" />Show already added</label>
              </div>
              {errors.length > 0 && (
                <div role="alert" className="rounded-lg border border-err/40 bg-err-soft px-3 py-2.5 text-[13px]">
                  <div className="flex items-center justify-between gap-2">
                    <span className="inline-flex items-center gap-2 font-medium text-err"><TriangleAlert className="size-4" aria-hidden />Test failed for {errors.length} model{errors.length === 1 ? "" : "s"}</span>
                    <button type="button" onClick={() => setResults((s) => Object.fromEntries(Object.entries(s).filter(([, r]) => r.state !== "fail")))} className="text-xs text-muted hover:text-text cursor-pointer rounded">Dismiss all</button>
                  </div>
                  <ul className="mt-1.5 space-y-1">
                    {errors.map(([id, r]) => (
                      <li key={id} className="flex items-start gap-2">
                        <span className="font-mono text-xs shrink-0">{id}</span><span className="text-err break-words min-w-0 flex-1">{r.msg}</span>
                        <button type="button" aria-label={`Dismiss ${id}`} onClick={() => setResults((s) => { const n = { ...s }; delete n[id]; return n; })} className="shrink-0 text-muted hover:text-text cursor-pointer rounded"><X className="size-3.5" /></button>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {!listed.length ? <p className="text-[13px] text-muted px-1">{pending.length === 0 && !needle ? "Every upstream model is already added." : "No models match."}</p> : (
                <ul className="flex flex-wrap gap-2 max-h-72 overflow-y-auto p-0.5" aria-label="Upstream models">
                  {listed.map((id) => {
                    const has = addedIds.has(id.toLowerCase()), r = results[id], testing = r?.state === "testing", uc = upstream.data?.upstreamCaps?.[id], ctxTitle = uc?.contextWindow ? ` · ${fmtTokens(uc.contextWindow)} context` : "";
                    return (
                      <li key={id} className={cn("inline-flex items-center rounded-lg border border-dashed text-[13px] transition-colors", has ? "border-ok/50 bg-ok-soft/40" : r?.state === "fail" ? "border-err/60" : r?.state === "ok" ? "border-ok/60 border-solid" : "border-border hover:border-accent/60")}>
                        {has ? (
                          <span className="inline-flex items-center gap-1.5 pl-2.5 pr-2.5 py-1.5 text-muted"><Check className="size-4 text-ok" aria-hidden />{id}<CapBadges caps={uc} size="xs" /><span className="sr-only"> (added)</span></span>
                        ) : (
                          <>
                            <button type="button" onClick={() => addOne(id)} disabled={adding === id} title={`Add ${code(id)}${ctxTitle}`} aria-label={`Add ${id}`}
                              className="inline-flex items-center gap-1.5 rounded-l-lg pl-2 pr-2.5 py-1.5 hover:bg-surface-2 disabled:opacity-60 cursor-pointer">
                              {adding === id ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Plus className="size-4 text-muted" aria-hidden />}{id}<CapBadges caps={uc} size="xs" />
                            </button>
                            <button type="button" onClick={() => test(id)} disabled={testing} title={`Test ${code(id)}`} aria-label={`Test ${id}`}
                              className="grid place-items-center self-stretch rounded-r-lg border-l border-dashed border-border px-2 text-muted hover:bg-surface-2 hover:text-accent disabled:opacity-60 cursor-pointer">
                              {testing ? <Loader2 className="size-4 animate-spin" aria-hidden /> : r?.state === "ok" ? <Check className="size-4 text-ok" aria-hidden /> : <Zap className={cn("size-4", r?.state === "fail" && "text-err")} aria-hidden />}
                              {r?.state === "ok" && <span className="sr-only">Test passed: {r.msg}</span>}
                            </button>
                          </>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}
            </>
          )}
        </section>
      </div>
      {editing && added.find((m) => m.id === editing) && <CapsEditor model={added.find((m) => m.id === editing)!} onClose={() => setEditing(null)} />}
      <ConfirmModal open={!!confirm} onClose={() => setConfirm(null)} loading={del.isPending} title="Remove model" body={`Remove "${confirm}" from ${node.name}? Clients can no longer use ${confirm ? code(confirm) : ""}. You can add it again later.`}
        onConfirm={() => del.mutate({ providerAlias: node.prefix, id: confirm! }, { onSuccess: () => { setConfirm(null); toast("ok", "Model removed"); }, onError: (e) => toast("err", errText(e)) })} />
    </Card>
  );
}
