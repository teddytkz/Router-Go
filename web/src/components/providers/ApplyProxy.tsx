import { Network } from "lucide-react";
import { useState } from "react";
import type { Connection } from "../../lib/api";
import { useBulkProxy, useProxyPools } from "../../lib/hooks";
import { Button, errText, Modal, useToast } from "../ui";

/** "Apply Proxy": bind every key of a provider to one proxy pool, spread them over the active pools, or unbind them. */
export function ApplyProxy({ conns }: { conns: Connection[] }) {
  const toast = useToast(), pools = useProxyPools(), apply = useBulkProxy();
  const [open, setOpen] = useState(false);
  const list = pools.data ?? [], active = list.filter((p) => p.isActive);
  if (!conns.length || !list.length) return null;
  const run = (assign: (i: number) => string, what: string) => apply.mutate(conns.map((c, i) => ({ connectionId: c.id, proxyPoolId: assign(i) })), {
    onSuccess: () => { toast("ok", `${what}: ${conns.length} key${conns.length === 1 ? "" : "s"} updated`); setOpen(false); }, onError: (e) => toast("err", errText(e)),
  });
  const item = "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer";
  return (
    <>
      <Button size="sm" onClick={() => setOpen(true)}><Network className="size-4" />Apply Proxy</Button>
      <Modal open={open} onClose={() => !apply.isPending && setOpen(false)} title={`Apply Proxy (${conns.length} key${conns.length === 1 ? "" : "s"})`}
        footer={<Button onClick={() => setOpen(false)} disabled={apply.isPending}>Cancel</Button>}>
        <div className="flex flex-col" role="menu">
          <button type="button" role="menuitem" className={item} disabled={apply.isPending || !active.length} onClick={() => run((i) => active[i % active.length].id, "One-to-one")}>
            <b className="font-medium">One-to-one (rotate)</b><span className="text-muted text-[13px]">keys take turns over the {active.length} active pool{active.length === 1 ? "" : "s"}</span>
          </button>
          <button type="button" role="menuitem" className={item} disabled={apply.isPending} onClick={() => run(() => "", "Unbound")}>None (unbind all)</button>
          {list.map((p) => (
            <button key={p.id} type="button" role="menuitem" className={item} disabled={apply.isPending || !p.isActive} onClick={() => run(() => p.id, p.name)}>
              <span className="truncate">{p.name}</span>{!p.isActive && <span className="text-xs text-muted">(disabled)</span>}
            </button>
          ))}
        </div>
        {apply.isPending && <p className="text-xs text-muted">Applying…</p>}
      </Modal>
    </>
  );
}
