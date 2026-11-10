// Built-in providers (Cline, Qoder, OpenCode Free, ...): list card, detail page body, add-key modal and model list.
import { ArrowLeft, ExternalLink, Info, TriangleAlert } from "lucide-react";
import { useState, type CSSProperties } from "react";
import { Link } from "react-router-dom";
import type { BuiltinProvider, Connection } from "../../lib/api";
import { useConnections, useCreateConnection } from "../../lib/hooks";
import { Badge, Button, Card, errText, Field, Input, Modal, Skeleton, cn, useToast } from "../ui";
import { BuiltinConnections } from "./BuiltinConnections";
import { BuiltinHeaders } from "./BuiltinHeaders";
import { BuiltinModels } from "./BuiltinModels";
import { StatusLine, summarize } from "./shared";

const TONE = { oauth: "var(--accent)", free: "var(--ok)", apikey: "var(--warn)" } as const;

// Providers with a logo in public/providers/{id}.png (copied from XRouter-Next); every other provider keeps its initials.
const LOGOS = new Set(["opencode", "freebuff", "codebuddy-intl", "qoder", "cline", "kiro"]);

function Tile({ p, className = "size-10 text-sm" }: { p: BuiltinProvider; className?: string }) {
  const [broken, setBroken] = useState(false);
  if (LOGOS.has(p.id) && !broken) return <img src={`/providers/${p.id}.png`} alt="" aria-hidden onError={() => setBroken(true)} className={`shrink-0 rounded-xl object-cover ${className}`} />;
  const c = TONE[p.category];
  const style: CSSProperties = { color: c, background: `color-mix(in srgb, ${c} 16%, var(--surface))`, borderColor: `color-mix(in srgb, ${c} 30%, var(--surface))` };
  return <div aria-hidden style={style} className={`grid shrink-0 place-items-center rounded-xl border font-semibold ${className}`}>{p.name.slice(0, 2).toUpperCase()}</div>;
}

function StatusBadge({ p }: { p: BuiltinProvider }) {
  return p.status === "experimental" ? <Badge tone="warn">Experimental</Badge> : p.status === "unsupported" ? <Badge>Not available yet</Badge> : null;
}

/** Compact list card: tile, name, one status line. */
export function BuiltinCard({ p, conns, now }: { p: BuiltinProvider; conns: Connection[]; now: number }) {
  return (
    <Link to={`/dashboard/providers/${p.id}`} aria-label={`Open ${p.name}`}
      className={cn("flex items-center gap-3 rounded-xl border border-border bg-surface px-3 py-2.5 shadow-[var(--shadow)] transition-colors hover:border-accent/50", p.status === "unsupported" && "opacity-60")}>
      <Tile p={p} className="size-9 text-xs" />
      <div className="min-w-0 flex-1">
        <h3 className="font-semibold text-[13px] truncate" title={p.name}>{p.name}</h3>
        <StatusLine s={summarize(conns, now)} empty={p.status === "unsupported" ? "Not available yet" : "No connections"} />
      </div>
    </Link>
  );
}

function AddKeyModal({ p, count, onClose }: { p: BuiltinProvider; count: number; onClose: () => void }) {
  const toast = useToast(), create = useCreateConnection();
  const [name, setName] = useState(`Key ${count + 1}`), [key, setKey] = useState("");
  return (
    <Modal open onClose={onClose} title={`Add API key to ${p.name}`}
      footer={<><Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" loading={create.isPending} disabled={!name.trim() || (!p.noAuth && !key.trim())}
          onClick={() => create.mutate({ provider: p.id, name: name.trim(), apiKey: key.trim() }, { onSuccess: () => { toast("ok", "Key added"); onClose(); }, onError: (e) => toast("err", errText(e)) })}>Add key</Button></>}>
      <p className="text-muted text-[13px]">Keys are tried in priority order; if one is rate-limited or rejected the next takes over automatically.</p>
      <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      <Field label="API key / token" hint={p.authHint || undefined}>{(id) => <Input id={id} type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} />}</Field>
    </Modal>
  );
}

function Website({ url }: { url: string }) {
  return url ? <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-accent hover:underline rounded">Website<ExternalLink className="size-3.5" aria-hidden /></a> : null;
}

export function BuiltinDetail({ provider: p }: { provider: BuiltinProvider }) {
  const conns = useConnections();
  const [adding, setAdding] = useState(false);
  const mine = (conns.data ?? []).filter((c) => c.provider === p.id), n = mine.length, unsupported = p.status === "unsupported";

  return (
    <div className="space-y-4">
      <div>
        <Link to="/dashboard/providers" className="inline-flex items-center gap-1.5 text-[13px] text-muted hover:text-text mb-4 rounded"><ArrowLeft className="size-4" />Back to Providers</Link>
        <div className="flex items-center gap-4">
          <Tile p={p} className="size-12 text-base" />
          <div className="min-w-0">
            <h1 className="text-xl font-semibold tracking-tight break-words">{p.name}</h1>
            <p className="text-muted mt-0.5">{n} connection{n === 1 ? "" : "s"}</p>
          </div>
          <StatusBadge p={p} />
        </div>
      </div>

      {p.status === "experimental" ? (
        <div role="alert" className="flex items-start gap-3 rounded-xl border border-warn/40 bg-warn-soft px-4 py-3 text-[13px]">
          <TriangleAlert className="size-4 shrink-0 text-warn mt-0.5" aria-hidden />
          <p className="min-w-0 break-words">{p.note} <Website url={p.website} /></p>
        </div>
      ) : (
        <Card className="p-4">
          <div className="flex items-start gap-3 text-[13px]">
            <Info className="size-4 shrink-0 text-muted mt-0.5" aria-hidden />
            <p className="min-w-0 break-words text-muted">{unsupported ? p.note : p.authHint} <Website url={p.website} /></p>
          </div>
        </Card>
      )}

      {conns.isLoading ? <Skeleton className="h-40" /> : <BuiltinConnections p={p} conns={mine} onAdd={() => setAdding(true)} />}
      {!unsupported && <BuiltinHeaders p={p} />}
      {!unsupported && <BuiltinModels p={p} />}
      {adding && <AddKeyModal p={p} count={n} onClose={() => setAdding(false)} />}
    </div>
  );
}
