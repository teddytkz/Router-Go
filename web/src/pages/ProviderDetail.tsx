import { ArrowLeft, Pencil, Plus, Server, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AddKeyModal, ConnectionsCard } from "../components/providers/ConnectionsCard";
import { ModelsCard } from "../components/providers/ModelsCard";
import { NodeModal } from "../components/providers/NodeModal";
import { apiLabel, endpointPath, NodeIcon, isAnthropic, TypeBadge } from "../components/providers/shared";
import { Button, Card, ConfirmModal, EmptyState, errText, Skeleton, useToast } from "../components/ui";
import type { ProviderNode } from "../lib/api";
import { BuiltinDetail } from "../components/providers/Builtin";
import { useConnections, useDeleteNode, useProvider } from "../lib/hooks";

const back = (
  <Link to="/dashboard/providers" className="inline-flex items-center gap-1.5 text-[13px] text-muted hover:text-text mb-4 rounded"><ArrowLeft className="size-4" />Back to Providers</Link>
);

export default function ProviderDetail() {
  const { id } = useParams(), { data, isLoading } = useProvider(id);
  if (isLoading) return <>{back}<div className="space-y-4"><Skeleton className="h-16" /><Skeleton className="h-28" /><Skeleton className="h-48" /></div></>;
  if (!data) return <>{back}<Card><EmptyState icon={<Server className="size-6" />} title="Provider not found" body="It may have been deleted, or the link is wrong." action={<Link to="/dashboard/providers"><Button variant="primary">Go to Providers</Button></Link>} /></Card></>;
  return data.kind === "builtin" ? <BuiltinDetail provider={data.provider} /> : <CustomDetail node={data.node} />;
}

function CustomDetail({ node }: { node: ProviderNode }) {
  const navigate = useNavigate(), toast = useToast(), conns = useConnections(), del = useDeleteNode();
  const [adding, setAdding] = useState(false), [edit, setEdit] = useState(false), [confirm, setConfirm] = useState(false);
  const mine = (conns.data ?? []).filter((c) => c.provider === node.id), n = mine.length;
  const anth = isAnthropic(node);
  const onDelete = () => del.mutate(node.id, { onSuccess: () => { toast("ok", "Provider deleted"); navigate("/dashboard/providers"); }, onError: (e) => toast("err", errText(e)) });

  return (
    <div className="space-y-4">
      <div>
        {back}
        <div className="flex items-center gap-4">
          <NodeIcon node={node} className="size-12" />
          <div className="min-w-0">
            <h1 className="text-xl font-semibold tracking-tight break-words">{node.name}</h1>
            <p className="text-muted mt-0.5">{n} connection{n === 1 ? "" : "s"}</p>
          </div>
          <TypeBadge node={node} />
        </div>
      </div>

      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 flex-1 basis-64">
            <h2 className="font-semibold">{anth ? "Anthropic" : "OpenAI"} Compatible Details</h2>
            <p className="text-[13px] text-muted mt-1 break-all">{apiLabel(node)} · {node.baseUrl.replace(/\/+$/, "")}/{endpointPath(node)}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" size="sm" onClick={() => setAdding(true)}><Plus className="size-4" />Add API Key</Button>
            <Button size="sm" onClick={() => setEdit(true)}><Pencil className="size-4" />Edit</Button>
            <Button size="sm" variant="danger" onClick={() => setConfirm(true)}><Trash2 className="size-4" />Delete</Button>
          </div>
        </div>
      </Card>

      {conns.isLoading ? <Skeleton className="h-40" /> : <ConnectionsCard providerId={node.id} conns={mine} onAdd={() => setAdding(true)} />}
      <ModelsCard node={node} />

      {adding && <AddKeyModal node={node} count={n} onClose={() => setAdding(false)} />}
      {edit && <NodeModal node={node} onClose={() => setEdit(false)} />}
      <ConfirmModal open={confirm} onClose={() => setConfirm(false)} loading={del.isPending} title="Delete provider"
        body={`Delete "${node.name}"? This also removes its ${n} API key${n === 1 ? "" : "s"} and custom models. This cannot be undone.`} onConfirm={onDelete} />
    </div>
  );
}
