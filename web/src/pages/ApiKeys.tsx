import { Eye, EyeOff, Gauge, KeyRound, ListChecks, Pencil, Plus, ShieldCheck, Trash2, X } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge, Button, Card, ConfirmModal, CopyButton, EmptyState, errText, fmtTime, Input, Modal, PageHeader, Skeleton, Switch, useToast, Field } from "../components/ui";
import { fmtTokens, parseTokens } from "../components/CapBadges";
import { ModelPicker, type PickGroup } from "../components/ModelPicker";
import type { ApiKey } from "../lib/api";
import { useAliases, useAllModels, useCombos, useCreateKey, useDeleteKey, useEditKey, useKeys, useNodes, useAllNodes, useSettings, useToggleKey, useUpdateKeyAccess, useUpdateSettings } from "../lib/hooks";

function AccessModal({ k, onClose }: { k: ApiKey; onClose: () => void }) {
  const toast = useToast(), save = useUpdateKeyAccess();
  const nodes = useAllNodes(), models = useAllModels(nodes.data), combos = useCombos(), aliases = useAliases();
  const [restricted, setRestricted] = useState(k.access?.restricted ?? false), [allow, setAllow] = useState(k.access?.allow ?? []);
  const [max, setMax] = useState(String(k.access?.maxTokens ?? 0)), [picking, setPicking] = useState(false), [draft, setDraft] = useState<string[]>([]);
  const maxTokens = parseTokens(max);

  // same grouping as the combo picker, plus the combos themselves (a key may be allowed a combo by name)
  const groups = useMemo<PickGroup[]>(() => {
    const byPrefix = new Map<string, string[]>();
    for (const m of models.data ?? []) { const p = m.slice(0, m.indexOf("/")); byPrefix.set(p, [...(byPrefix.get(p) ?? []), m]); }
    const out: PickGroup[] = [...byPrefix].map(([prefix, ms]) => ({ key: prefix, label: nodes.data?.find((n) => n.prefix === prefix)?.name ?? prefix, models: ms }));
    const cs = combos.data?.map((c) => c.name) ?? [], al = Object.keys(aliases.data ?? {});
    return [...(cs.length ? [{ key: "__combos", label: "Combos", models: cs }] : []), ...out, ...(al.length ? [{ key: "__aliases", label: "Aliases", models: al }] : [])];
  }, [models.data, nodes.data, combos.data, aliases.data]);

  if (picking) {
    return (
      <Modal open onClose={() => setPicking(false)} title="Select models"
        footer={<><Button onClick={() => setPicking(false)}>Back</Button><Button variant="primary" onClick={() => { setAllow(draft); setPicking(false); }}>Apply · {draft.length} selected</Button></>}>
        <p className="text-[13px] text-muted -mt-1">Tick the models and combos this key may call.</p>
        <ModelPicker groups={groups} selected={draft} onChange={setDraft} />
      </Modal>
    );
  }
  return (
    <Modal open onClose={onClose} title={`Model access · ${k.name}`}
      footer={<><Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" loading={save.isPending} disabled={maxTokens === null}
          onClick={() => save.mutate({ id: k.id, access: { restricted, allow, maxTokens: maxTokens ?? 0 } }, { onSuccess: () => { toast("ok", "Access updated"); onClose(); }, onError: (e) => toast("err", errText(e)) })}>Save</Button></>}>
      <div className="flex items-center justify-between gap-4">
        <div><div className="font-medium">Restrict to specific models</div><p className="text-xs text-muted">Off: this key can use every model and combo.</p></div>
        <Switch checked={restricted} onChange={setRestricted} label="Restrict models" />
      </div>
      {restricted && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-[13px] font-medium">Allowed models and combos <span className="text-muted font-normal">· {allow.length}/200</span></div>
            <Button size="sm" onClick={() => { setDraft(allow); setPicking(true); }}><ListChecks className="size-4" />Select models</Button>
          </div>
          {allow.length ? (
            <ul className="rounded-lg border border-border divide-y divide-border max-h-64 overflow-y-auto">
              {allow.map((m) => (
                <li key={m} className="flex items-center gap-1 pl-3 pr-1 h-9">
                  <span className="font-mono text-[13px] flex-1 truncate" title={m}>{m}</span>
                  <Button size="sm" variant="ghost" aria-label={`Remove ${m}`} onClick={() => setAllow(allow.filter((x) => x !== m))}><X className="size-4" /></Button>
                </li>
              ))}
            </ul>
          ) : <p className="text-xs text-warn">With an empty list this key cannot call any model.</p>}
        </div>
      )}
      <Field label="Max tokens" error={maxTokens === null ? "Enter a number like 500000, 500k or 2m" : undefined}
        hint={`Total prompt + completion tokens this key may use. 0 = unlimited. Used so far: ${fmtTokens(k.usedTokens ?? 0)}. Past the limit, requests get 429.`}>
        {(id) => <Input id={id} inputMode="numeric" className="font-mono" value={max} onChange={(e) => setMax(e.target.value)} placeholder="0" />}
      </Field>
    </Modal>
  );
}

function EditModal({ k, onClose }: { k: ApiKey; onClose: () => void }) {
  const toast = useToast(), save = useEditKey();
  const [name, setName] = useState(k.name), [key, setKey] = useState(k.key);
  const valid = name.trim() !== "" && key.trim().length >= 16 && !/\s/.test(key.trim());
  const submit = () => save.mutate({ id: k.id, name: name.trim(), key: key.trim() }, { onSuccess: () => { toast("ok", "Key updated"); onClose(); }, onError: (e) => toast("err", errText(e)) });
  return (
    <Modal open onClose={onClose} title={`Edit key · ${k.name}`}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={save.isPending} disabled={!valid} onClick={submit}>Save</Button></>}>
      <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      <Field label="Key" hint="At least 16 characters, no spaces. Changing it makes clients using the old key get 401.">{(id) => <Input id={id} className="font-mono" value={key} onChange={(e) => setKey(e.target.value)} onKeyDown={(e) => e.key === "Enter" && valid && submit()} />}</Field>
    </Modal>
  );
}

function KeyRow({ k }: { k: ApiKey }) {
  const toast = useToast(), toggle = useToggleKey(), del = useDeleteKey();
  const [show, setShow] = useState(false), [confirm, setConfirm] = useState(false), [access, setAccess] = useState(false), [edit, setEdit] = useState(false);
  const restricted = k.access?.restricted, max = k.access?.maxTokens ?? 0;
  const shown = show ? k.key : k.key.slice(0, 7) + "•".repeat(14) + k.key.slice(-4);
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
      <div className="min-w-0 flex-1 basis-56">
        <div className="font-medium">{k.name}</div>
        <div className="flex flex-wrap items-baseline gap-x-3 text-xs text-muted">
          <span className="font-mono break-all">{shown}</span>
          <span className="border-l border-border pl-3">{fmtTime(k.createdAt)}</span>
        </div>
      </div>
      <button type="button" onClick={() => setAccess(true)} aria-label={`Edit model access for ${k.name}`} className="cursor-pointer">
        <Badge tone={restricted ? "warn" : "neutral"}><ShieldCheck className="size-3" />{restricted ? `${k.access!.allow.length} allowed` : "All models"}</Badge>
        <Badge className="ml-1.5"><Gauge className="size-3" />{max ? `${fmtTokens(k.usedTokens ?? 0)} / ${fmtTokens(max)}` : "Unlimited"}</Badge>
      </button>
      <div className="ml-auto flex shrink-0 items-center gap-1">
        <Switch checked={k.isActive} label={`${k.name} enabled`} disabled={toggle.isPending} onChange={(v) => toggle.mutate({ id: k.id, isActive: v }, { onError: (e) => toast("err", errText(e)) })} />
        <span aria-hidden className="mx-2 h-5 w-px bg-border" />
        <Button size="sm" variant="ghost" onClick={() => setShow((s) => !s)} aria-label={show ? `Hide ${k.name}` : `Reveal ${k.name}`}>{show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}</Button>
        <CopyButton value={k.key} label={`Copy ${k.name}`} />
        <Button size="sm" variant="ghost" aria-label={`Edit ${k.name}`} onClick={() => setEdit(true)}><Pencil className="size-4" /></Button>
        <Button size="sm" variant="ghost" className="hover:text-err" aria-label={`Delete ${k.name}`} onClick={() => setConfirm(true)}><Trash2 className="size-4" /></Button>
      </div>
      {edit && <EditModal k={k} onClose={() => setEdit(false)} />}
      {access && <AccessModal k={k} onClose={() => setAccess(false)} />}
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete API key" body={`Clients using "${k.name}" will immediately get 401.`}
        onConfirm={() => del.mutate(k.id, { onSuccess: () => { setConfirm(false); toast("ok", "Key deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </li>
  );
}

export default function ApiKeys() {
  const keys = useKeys(), nodes = useNodes(), create = useCreateKey(), toast = useToast(), settings = useSettings(), updSettings = useUpdateSettings();
  const [adding, setAdding] = useState(false), [name, setName] = useState("");
  const base = `${location.origin}/v1`;
  const sample = nodes.data?.find((n) => n.apiType === "chat");
  const firstKey = keys.data?.find((k) => k.isActive)?.key ?? "YOUR_API_KEY";
  const curl = `curl ${base}/chat/completions \\\n  -H "Authorization: Bearer ${firstKey}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${sample ? sample.prefix + "/<model>" : "<prefix>/<model>"}", "messages": [{"role": "user", "content": "Hello"}]}'`;

  const messages = `curl ${base}/messages \\\n  -H "x-api-key: ${firstKey}" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${sample ? sample.prefix + "/<model>" : "<prefix>/<model>"}", "max_tokens": 256, "messages": [{"role": "user", "content": "Hello"}]}'`;
  const submit = () => create.mutate(name.trim(), { onSuccess: () => { toast("ok", "API key created"); setAdding(false); setName(""); }, onError: (e) => toast("err", errText(e)) });
  return (
    <>
      <PageHeader title="API Keys" subtitle="Keys for your clients, and the endpoint they should call" actions={<Button variant="primary" onClick={() => setAdding(true)}><Plus className="size-4" />New API key</Button>} />
      <Card className="p-4 mb-4">
        <div className="text-[13px] text-muted mb-1.5">Base URL</div>
        <div className="flex items-center gap-1 rounded-lg bg-surface-2 pl-3"><code className="font-mono text-sm flex-1 truncate py-2">{base}</code><CopyButton value={base} label="Copy base URL" /></div>
        <div className="flex gap-1.5 mt-3 flex-wrap">{["/chat/completions", "/responses", "/messages", "/embeddings", "/models"].map((p) => <Badge key={p} className="font-mono">{p}</Badge>)}</div>
      </Card>
      <Card className="mb-4 flex items-center justify-between gap-6 px-4 py-4">
        <div><div className="font-medium">Require API key</div><p className="text-muted text-[13px] mt-0.5 max-w-lg">Clients must send a valid gateway key to /v1. Disable only on a trusted network.</p></div>
        <Switch checked={settings.data?.requireApiKey ?? true} label="Require API key" disabled={!settings.data || updSettings.isPending}
          onChange={(v) => updSettings.mutate({ requireApiKey: v }, { onSuccess: () => toast("ok", "Saved"), onError: (e) => toast("err", errText(e)) })} />
      </Card>
      <Card className="mb-4">
        <div className="px-4 h-12 flex items-center border-b border-border"><h2 className="font-semibold">API keys</h2></div>
        {keys.isLoading ? <Skeleton className="h-20 m-4" /> : keys.data?.length ? <ul className="divide-y divide-border">{keys.data.map((k) => <KeyRow key={k.id} k={k} />)}</ul>
          : <EmptyState icon={<KeyRound className="size-6" />} title="No API keys" body="Create a key for your clients. Keys can be disabled or deleted at any time." action={<Button variant="primary" onClick={() => setAdding(true)}>Create key</Button>} />}
      </Card>
      <Card className="p-4">
        <div className="flex items-center justify-between mb-2"><h2 className="font-semibold">Quick start</h2><CopyButton value={curl} label="Copy example" /></div>
        <pre className="font-mono text-[12.5px] leading-relaxed bg-surface-2 rounded-lg p-3 overflow-x-auto">{curl}</pre>
        <div className="flex items-center justify-between mt-4 mb-2"><h3 className="font-medium">Anthropic format</h3><CopyButton value={messages} label="Copy Anthropic example" /></div>
        <pre className="font-mono text-[12.5px] leading-relaxed bg-surface-2 rounded-lg p-3 overflow-x-auto">{messages}</pre>
        <p className="text-[13px] text-muted mt-3"><code className="font-mono">/v1/messages</code> (Anthropic format) and <code className="font-mono">/v1/responses</code> work with any model — the gateway translates between formats automatically, including streaming, tools and images. Combos and aliases can be used as the model name too.</p>
      </Card>
      <Modal open={adding} onClose={() => setAdding(false)} title="New API key"
        footer={<><Button onClick={() => setAdding(false)}>Cancel</Button><Button variant="primary" loading={create.isPending} disabled={!name.trim()} onClick={submit}>Create</Button></>}>
        <Field label="Name" hint="Helps you tell clients apart, e.g. “laptop” or “ci”.">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && name.trim() && submit()} placeholder="laptop" />}</Field>
      </Modal>
    </>
  );
}
