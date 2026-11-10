import { Network } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import type { Connection } from "../../lib/api";
import { useBulkProxy, useProxyPools } from "../../lib/hooks";
import { Button, errText, Modal, useToast } from "../ui";

/**
 * "Apply Proxy": bind every key of a provider to one proxy pool, spread them over the active pools, or unbind them.
 * `prepare` is for a provider that needs no key and has no connection yet: it creates the default one to bind.
 */
export function ApplyProxy({ conns, prepare }: { conns: Connection[]; prepare?: () => Promise<Connection[]> }) {
  const toast = useToast(), pools = useProxyPools(), apply = useBulkProxy();
  const [open, setOpen] = useState(false), [busy, setBusy] = useState(false);
  const list = pools.data ?? [], active = list.filter((p) => p.isActive);
  if (!conns.length && !prepare) return null;
  const n = conns.length, count = n ? ` (${n} key${n === 1 ? "" : "s"})` : "";
  const run = async (assign: (i: number) => string, what: string) => {
    setBusy(true);
    try {
      const targets = n ? conns : await prepare!();
      await apply.mutateAsync(targets.map((c, i) => ({ connectionId: c.id, proxyPoolId: assign(i) })));
      toast("ok", `${what}: ${targets.length} key${targets.length === 1 ? "" : "s"} updated`); setOpen(false);
    } catch (e) { toast("err", errText(e)); } finally { setBusy(false); }
  };
  const item = "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer";
  return (
    <>
      <Button size="sm" onClick={() => setOpen(true)}><Network className="size-4" />Apply Proxy</Button>
      <Modal open={open} onClose={() => !busy && setOpen(false)} title={`Apply Proxy${count}`}
        footer={<Button onClick={() => setOpen(false)} disabled={busy}>Cancel</Button>}>
        {!list.length ? (
          <p className="text-[13px] text-muted">There are no proxy pools yet. <Link to="/dashboard/proxy-pools" className="text-accent hover:underline">Add one in Proxy Pools</Link>, then come back to bind this provider to it.</p>
        ) : (
          <div className="flex flex-col" role="menu">
            <button type="button" role="menuitem" className={item} disabled={busy || !active.length} onClick={() => run((i) => active[i % active.length].id, "One-to-one")}>
              <b className="font-medium">One-to-one (rotate)</b><span className="text-muted text-[13px]">keys take turns over the {active.length} active pool{active.length === 1 ? "" : "s"}</span>
            </button>
            <button type="button" role="menuitem" className={item} disabled={busy} onClick={() => run(() => "", "Unbound")}>None (unbind all)</button>
            {list.map((p) => (
              <button key={p.id} type="button" role="menuitem" className={item} disabled={busy || !p.isActive} onClick={() => run(() => p.id, p.name)}>
                <span className="truncate">{p.name}</span>{!p.isActive && <span className="text-xs text-muted">(disabled)</span>}
              </button>
            ))}
          </div>
        )}
        {busy && <p className="text-xs text-muted">Applying…</p>}
      </Modal>
    </>
  );
}
