import { Link } from "react-router-dom";
import type { Connection, ProviderNode } from "../../lib/api";
import { useToggleConnections } from "../../lib/hooks";
import { errText, Switch, useToast } from "../ui";
import { NodeIcon, StatusLine, summarize } from "./shared";

/** Compact list card: tile, name, one status line; hover reveals the enable/disable switch. */
export function ProviderCard({ node, conns, now }: { node: ProviderNode; conns: Connection[]; now: number }) {
  const toast = useToast(), toggle = useToggleConnections();
  const s = summarize(conns, now);
  const onToggle = (isActive: boolean) => toggle.mutate({ ids: conns.map((c) => c.id), isActive }, {
    onSuccess: () => toast("ok", `${node.name} ${isActive ? "enabled" : "disabled"}`), onError: (e) => toast("err", errText(e)),
  });
  return (
    <div className="group relative rounded-xl border border-border bg-surface shadow-[var(--shadow)] transition-colors hover:border-accent/50">
      <Link to={`/dashboard/providers/${node.id}`} className="flex items-center gap-3 px-3 py-2.5 rounded-xl" aria-label={`Open ${node.name}`}>
        <NodeIcon node={node} className="size-9" />
        <div className="min-w-0 flex-1 pr-10">
          <h3 className="font-semibold text-[13px] truncate" title={node.name}>{node.name}</h3>
          <StatusLine s={s} />
        </div>
      </Link>
      {s.total > 0 && (
        <div className="absolute right-3 top-1/2 -translate-y-1/2 transition-opacity opacity-0 group-hover:opacity-100 focus-within:opacity-100 max-sm:opacity-100 [@media(hover:none)]:opacity-100">
          <Switch checked={!s.allDisabled} onChange={onToggle} label={`${node.name} enabled`} disabled={toggle.isPending} />
        </div>
      )}
    </div>
  );
}
