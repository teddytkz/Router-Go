import { CloudUpload, Loader2, Network, Pencil, Plus, Trash2, Zap } from "lucide-react";
import { useState } from "react";
import { Badge, Button, Card, ConfirmModal, EmptyState, errText, Field, Input, Modal, PageHeader, Skeleton, Switch, useToast } from "../components/ui";
import { ago, useNow } from "../components/usage/shared";
import type { ProxyPool } from "../lib/api";
import { useCreateProxyPool, useDeleteProxyPool, useDeployRelay, useProxyPools, useTestProxyPool, useUpdateProxyPool } from "../lib/hooks";

const act = "grid size-7 place-items-center rounded-md cursor-pointer disabled:opacity-60";
const TYPE = { http: "Proxy", vercel: "Vercel relay", cloudflare: "Cloudflare relay" } as const;

// ---------- add / edit a plain proxy ----------
function PoolModal({ pool, onClose }: { pool?: ProxyPool; onClose: () => void }) {
  const toast = useToast(), create = useCreateProxyPool(), update = useUpdateProxyPool();
  const [name, setName] = useState(pool?.name ?? ""), [url, setUrl] = useState(pool?.proxyUrl ?? ""), [noProxy, setNoProxy] = useState(pool?.noProxy ?? ""), [strict, setStrict] = useState(pool?.strictProxy ?? false);
  const relay = pool && pool.type !== "http", busy = create.isPending || update.isPending;
  const body = { name: name.trim(), proxyUrl: url.trim(), noProxy: noProxy.trim(), strictProxy: strict };
  const done = { onSuccess: () => { toast("ok", pool ? "Pool updated" : "Pool added"); onClose(); }, onError: (e: unknown) => toast("err", errText(e)) };
  return (
    <Modal open onClose={onClose} title={pool ? `Edit ${pool.name}` : "Add proxy"}
      footer={<><Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" loading={busy} disabled={!name.trim() || !url.trim()} onClick={() => (pool ? update.mutate({ id: pool.id, ...body }, done) : create.mutate(body, done))}>{pool ? "Save" : "Add proxy"}</Button></>}>
      <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="Office proxy" />}</Field>
      <Field label={relay ? "Relay URL" : "Proxy URL"} hint={relay ? "The deployed worker's address. Its secret key is kept on the server." : "http://user:pass@host:port, https:// or socks5://"}>
        {(id) => <Input id={id} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://127.0.0.1:8080" autoComplete="off" />}
      </Field>
      <Field label="No proxy for" hint="Comma-separated hosts that skip the proxy (optional)">{(id) => <Input id={id} value={noProxy} onChange={(e) => setNoProxy(e.target.value)} placeholder="localhost, .internal" />}</Field>
      <label className="flex items-start gap-3 text-[13px]">
        <Switch checked={strict} onChange={setStrict} label="Strict mode" />
        <span><b>Strict mode</b><br /><span className="text-muted">If this pool is disabled, fail the request instead of falling back to your direct IP.</span></span>
      </label>
    </Modal>
  );
}

// ---------- deploy a relay ----------
function DeployModal({ kind, onClose }: { kind: "vercel" | "cloudflare"; onClose: () => void }) {
  const toast = useToast(), deploy = useDeployRelay();
  const [token, setToken] = useState(""), [account, setAccount] = useState(""), [name, setName] = useState("");
  const cf = kind === "cloudflare", ready = token.trim() && (!cf || account.trim());
  const go = () => deploy.mutate({ kind, body: cf ? { accountId: account.trim(), apiToken: token.trim(), projectName: name.trim() } : { vercelToken: token.trim(), projectName: name.trim() } }, {
    onSuccess: (r) => { toast("ok", `Deployed: ${r.deployUrl}`); onClose(); }, onError: (e) => toast("err", errText(e)),
  });
  return (
    <Modal open onClose={deploy.isPending ? () => {} : onClose} title={cf ? "Deploy relay to Cloudflare" : "Deploy relay to Vercel"}
      footer={<><Button onClick={onClose} disabled={deploy.isPending}>Cancel</Button><Button variant="primary" loading={deploy.isPending} disabled={!ready} onClick={go}>{!deploy.isPending && <CloudUpload className="size-4" />}Deploy</Button></>}>
      <p className="text-muted text-[13px]">
        Creates a small forwarding {cf ? "Worker" : "Edge function"} in your own account. Requests of keys bound to it leave from {cf ? "Cloudflare" : "Vercel"}'s network instead of this machine.
        The token is used for this deploy only and is not stored.{deploy.isPending && !cf && " Waiting for the build, this can take a minute…"}
      </p>
      {cf && <Field label="Account ID">{(id) => <Input id={id} value={account} onChange={(e) => setAccount(e.target.value)} autoComplete="off" />}</Field>}
      <Field label={cf ? "API token" : "Vercel API token"} hint={cf ? "Needs Workers Scripts: Edit" : "Create one at vercel.com/account/tokens"}>
        {(id) => <Input id={id} type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} />}
      </Field>
      <Field label={cf ? "Worker name" : "Project name"} hint="Lowercase letters, digits and dashes. Leave empty for a random name.">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="relay-ab12cd" />}</Field>
    </Modal>
  );
}

// ---------- card ----------
function PoolCard({ p, now, onEdit }: { p: ProxyPool; now: number; onEdit: () => void }) {
  const toast = useToast(), test = useTestProxyPool(), update = useUpdateProxyPool(), del = useDeleteProxyPool();
  const [confirm, setConfirm] = useState(false);
  const tone = p.testStatus === "active" ? "ok" : p.testStatus === "error" ? "err" : "neutral";
  const run = () => test.mutate(p.id, { onSuccess: (r) => toast(r.ok ? "ok" : "err", r.ok ? `${p.name}: OK (${r.elapsedMs}ms)` : `${p.name}: ${r.error}`), onError: (e) => toast("err", errText(e)) });
  return (
    <Card className={`p-4 ${p.isActive ? "" : "opacity-60"}`}>
      <div className="flex items-start gap-3">
        <div className="grid size-9 shrink-0 place-items-center rounded-xl bg-accent-soft text-accent"><Network className="size-4" aria-hidden /></div>
        <div className="min-w-0 flex-1">
          <h3 className="font-semibold truncate" title={p.name}>{p.name}</h3>
          <p className="font-mono text-xs text-muted break-all">{p.proxyUrl}</p>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            <Badge tone="accent">{TYPE[p.type]}</Badge>
            <Badge tone={tone}>{p.testStatus === "unknown" ? "untested" : p.testStatus === "active" ? "working" : "failing"}</Badge>
            {p.strictProxy && <Badge tone="warn">Strict</Badge>}
            <Badge>{p.boundConnectionCount} key{p.boundConnectionCount === 1 ? "" : "s"}</Badge>
            {p.lastTestedAt && <span className="text-xs text-muted">tested {ago(p.lastTestedAt, now)}</span>}
          </div>
          {p.testStatus === "error" && p.lastError && <p className="mt-1.5 text-xs text-err break-words">{p.lastError}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          <button type="button" className={`${act} text-warn hover:bg-warn-soft`} disabled={test.isPending} onClick={run} aria-label={`Test ${p.name}`} title="Test">{test.isPending ? <Loader2 className="size-4 animate-spin" /> : <Zap className="size-4" />}</button>
          <button type="button" className={`${act} text-accent hover:bg-accent-soft`} onClick={onEdit} aria-label={`Edit ${p.name}`} title="Edit"><Pencil className="size-4" /></button>
          <button type="button" className={`${act} text-err hover:bg-err-soft`} onClick={() => setConfirm(true)} aria-label={`Delete ${p.name}`} title="Delete"><Trash2 className="size-4" /></button>
          <Switch checked={p.isActive} label={`${p.name} enabled`} disabled={update.isPending} onChange={(v) => update.mutate({ id: p.id, isActive: v }, { onError: (e) => toast("err", errText(e)) })} />
        </div>
      </div>
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete proxy pool"
        body={`Remove "${p.name}"?${p.boundConnectionCount ? ` ${p.boundConnectionCount} key(s) using it will connect directly again.` : ""}${p.type !== "http" ? " The deployed worker stays in your account; delete it there too." : ""}`}
        onConfirm={() => del.mutate(p.id, { onSuccess: () => { setConfirm(false); toast("ok", "Pool deleted"); }, onError: (e) => toast("err", errText(e)) })} />
    </Card>
  );
}

export default function ProxyPools() {
  const pools = useProxyPools(), now = useNow(15_000);
  const [modal, setModal] = useState<{ edit?: ProxyPool } | "add" | "vercel" | "cloudflare" | null>(null);
  const list = pools.data ?? [];
  return (
    <>
      <PageHeader title="Proxy Pools" subtitle="Route a key's upstream traffic through a proxy, or through a relay you deploy to Vercel or Cloudflare"
        actions={<>
          <Button onClick={() => setModal("vercel")}><CloudUpload className="size-4" />Deploy to Vercel</Button>
          <Button onClick={() => setModal("cloudflare")}><CloudUpload className="size-4" />Deploy to Cloudflare</Button>
          <Button variant="primary" onClick={() => setModal("add")}><Plus className="size-4" />Add proxy</Button>
        </>} />
      {pools.isLoading ? <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">{[0, 1].map((i) => <Skeleton key={i} className="h-32" />)}</div>
        : list.length ? <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">{list.map((p) => <PoolCard key={p.id} p={p} now={now} onEdit={() => setModal({ edit: p })} />)}</div>
        : <Card><EmptyState icon={<Network className="size-6" />} title="No proxy pools" body="Add a proxy, or deploy a relay to your Vercel or Cloudflare account, then pick it in a key's Edit dialog (Providers → a provider → Edit)." /></Card>}
      {modal === "add" && <PoolModal onClose={() => setModal(null)} />}
      {typeof modal === "object" && modal?.edit && <PoolModal pool={modal.edit} onClose={() => setModal(null)} />}
      {(modal === "vercel" || modal === "cloudflare") && <DeployModal kind={modal} onClose={() => setModal(null)} />}
    </>
  );
}
