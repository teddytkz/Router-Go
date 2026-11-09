import { CheckCircle2, XCircle, Zap } from "lucide-react";
import { useState } from "react";
import type { NodeType, ProviderNode } from "../../lib/api";
import { useCreateNode, useUpdateNode, useValidateNode } from "../../lib/hooks";
import { Button, errText, Field, Input, Modal, Select, useToast } from "../ui";

const DEFAULT_URL: Record<NodeType, string> = { "openai-compatible": "https://api.openai.com/v1", "anthropic-compatible": "https://api.anthropic.com/v1" };

/** Create (type given) or edit (node given) a custom provider node. Type and API type are read-only when editing. */
export function NodeModal({ node, type: newType, onClose, onCreated }: { node?: ProviderNode; type?: NodeType; onClose: () => void; onCreated?: (n: ProviderNode) => void }) {
  const toast = useToast(), createNode = useCreateNode(), updateNode = useUpdateNode(), validate = useValidateNode();
  const type = node?.type ?? newType ?? "openai-compatible", anthropic = type === "anthropic-compatible";
  const [f, setF] = useState({ name: node?.name ?? "", prefix: node?.prefix ?? "", apiType: node?.apiType ?? "chat", baseUrl: node?.baseUrl ?? DEFAULT_URL[type], apiKey: "", modelId: "" });
  const [check, setCheck] = useState<{ ok: boolean; text: string } | null>(null);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => { setF({ ...f, [k]: e.target.value }); setCheck(null); };

  const runCheck = () => validate.mutate({ type, baseUrl: f.baseUrl.trim(), ...(anthropic ? {} : { apiType: f.apiType }), apiKey: f.apiKey, modelId: f.modelId.trim() || undefined }, {
    onSuccess: (r) => setCheck({ ok: r.valid, text: r.valid ? (r.models ? `Valid — ${r.models.length} model(s) found` : "Valid") : `Invalid — ${r.error ?? "check failed"}` }),
    onError: (e) => setCheck({ ok: false, text: `Invalid — ${errText(e)}` }),
  });

  const save = async () => {
    const body = { name: f.name.trim(), prefix: f.prefix.trim(), baseUrl: f.baseUrl.trim() };
    try {
      if (node) { await updateNode.mutateAsync({ id: node.id, ...body }); toast("ok", "Provider updated"); onClose(); }
      else {
        const { node: n } = await createNode.mutateAsync({ ...body, type, ...(anthropic ? {} : { apiType: f.apiType }) });
        toast("ok", "Provider added"); onClose(); onCreated?.(n);
      }
    } catch (e) { toast("err", errText(e)); }
  };
  const valid = f.name.trim() && f.prefix.trim() && f.baseUrl.trim();
  const title = node ? "Edit provider" : anthropic ? "Add Anthropic Compatible" : "Add OpenAI Compatible";

  return (
    <Modal open onClose={onClose} title={title}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={createNode.isPending || updateNode.isPending} disabled={!valid} onClick={save}>{node ? "Save" : "Create"}</Button></>}>
      <div className="grid sm:grid-cols-2 gap-4">
        <Field label="Name">{(id) => <Input id={id} value={f.name} onChange={set("name")} placeholder={anthropic ? "Anthropic Proxy" : "OpenAI Proxy"} />}</Field>
        <Field label="Prefix" hint={f.prefix ? `Clients use model "${f.prefix}/<model>"` : "Used in the model name"}>{(id) => <Input id={id} className="font-mono" value={f.prefix} onChange={set("prefix")} placeholder={anthropic ? "ac-prod" : "oc-prod"} />}</Field>
      </div>
      {node ? (
        <Field label="Type" hint="Cannot change after creation">{(id) => <Input id={id} readOnly disabled value={anthropic ? "Anthropic Compatible · Messages API" : `OpenAI Compatible · ${node.apiType === "responses" ? "Responses API" : "Chat Completions"}`} />}</Field>
      ) : !anthropic && (
        <Field label="API type" hint="'chat' → /chat/completions · 'responses' → /responses">
          {(id) => <Select id={id} value={f.apiType} onChange={set("apiType")}><option value="chat">Chat Completions</option><option value="responses">Responses</option></Select>}
        </Field>
      )}
      <Field label="Base URL" hint={`Requests go to {baseUrl}/${anthropic ? "messages" : f.apiType === "responses" ? "responses" : "chat/completions"}`}>{(id) => <Input id={id} className="font-mono" value={f.baseUrl} onChange={set("baseUrl")} placeholder={DEFAULT_URL[type]} />}</Field>
      {node ? <p className="text-xs text-muted">Changing the prefix changes the model names clients must use.</p> : (
        <>
          <Field label="API key (for check)" hint="Used only to check the endpoint — it is not saved. Add keys on the next page.">{(id) => <Input id={id} type="password" autoComplete="off" value={f.apiKey} onChange={set("apiKey")} placeholder={anthropic ? "sk-ant-…" : "sk-…"} />}</Field>
          <Field label="Model ID (optional)" hint="Probe through a chat request if /models is unavailable">{(id) => <Input id={id} className="font-mono" value={f.modelId} onChange={set("modelId")} placeholder={anthropic ? "claude-sonnet-4-5" : "gpt-4o-mini"} />}</Field>
          <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-surface-2/50 p-3">
            <Button onClick={runCheck} loading={validate.isPending} disabled={!f.baseUrl.trim()} className="shrink-0">{!validate.isPending && <Zap className="size-4" />}Check connection</Button>
            {check ? (
              <span role="status" className={`inline-flex min-w-0 flex-1 basis-48 items-start gap-2 text-[13px] ${check.ok ? "text-ok" : "text-err"}`}>
                {check.ok ? <CheckCircle2 className="size-4 shrink-0 mt-0.5" aria-hidden /> : <XCircle className="size-4 shrink-0 mt-0.5" aria-hidden />}
                <span className="break-words min-w-0">{check.text}</span>
              </span>
            ) : <span className="min-w-0 flex-1 basis-48 text-[13px] text-muted">Tests the Base URL and API key above before you create the provider.</span>}
          </div>
        </>
      )}
    </Modal>
  );
}
