import { useState } from "react";
import type { CustomModel, ModelCapsOverride } from "../../lib/api";
import { useSetModelCaps } from "../../lib/hooks";
import { CAP_FLAGS, fmtTokens, parseTokens } from "../CapBadges";
import { Button, errText, Field, Input, Modal, Switch, useToast } from "../ui";

/** Dialog to override a model's detected capabilities; anything left equal to the detected value stays on auto. */
export function CapsEditor({ model, onClose }: { model: CustomModel; onClose: () => void }) {
  const toast = useToast(), set = useSetModelCaps(), d = model.detected, e = model.effective;
  const [flags, setFlags] = useState({ vision: e.vision, reasoning: e.reasoning, audioInput: e.audioInput });
  const [ctx, setCtx] = useState(model.caps?.contextWindow ? String(model.caps.contextWindow) : "");
  const parsed = ctx.trim() ? parseTokens(ctx) : undefined;
  const err = parsed === null ? "Enter a number such as 128000, 128k or 1m" : parsed !== undefined && (parsed < 1000 || parsed > 1e8) ? "Must be between 1,000 and 100,000,000 tokens" : undefined;
  const hasOverride = !!model.caps && Object.keys(model.caps).length > 0;

  const send = (caps: ModelCapsOverride | null, msg: string) => set.mutate({ providerAlias: model.providerAlias, id: model.id, caps }, { onSuccess: () => { toast("ok", msg); onClose(); }, onError: (x) => toast("err", errText(x)) });
  const save = () => {
    const caps: ModelCapsOverride = {};
    for (const { key } of CAP_FLAGS) if (flags[key] !== d[key]) caps[key] = flags[key];
    if (parsed && parsed !== d.contextWindow) caps.contextWindow = parsed;
    send(caps, "Capabilities saved");
  };

  return (
    <Modal open onClose={onClose} title={`Capabilities · ${model.id}`}
      footer={<>
        <Button variant="ghost" className="mr-auto" disabled={!hasOverride || set.isPending} onClick={() => send(null, "Reset to auto-detect")}>Reset to auto-detect</Button>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" loading={set.isPending} disabled={!!err} onClick={save}>Save</Button>
      </>}>
      <p className="text-[13px] text-muted -mt-1">Detected from the model name. Change anything that is wrong: the router uses these to send images, audio and long requests to a model that can handle them.</p>
      <div className="space-y-2">
        {CAP_FLAGS.map(({ key, label }) => (
          <div key={key} className="flex items-center justify-between gap-3 rounded-lg border border-border bg-surface px-3 py-2.5">
            <div className="min-w-0"><div className="text-[13px] font-medium">{label}</div><div className="text-xs text-muted">Auto-detected: {d[key] ? "yes" : "no"}{flags[key] !== d[key] && <span className="text-accent"> · changed</span>}</div></div>
            <Switch checked={flags[key]} onChange={(v) => setFlags((s) => ({ ...s, [key]: v }))} label={label} />
          </div>
        ))}
      </div>
      <Field label="Context window (tokens)" error={err} hint={`Auto-detected: ${d.contextWindow ? fmtTokens(d.contextWindow) : "unknown"} · accepts 128k or 1m. Leave empty to keep it.`}>
        {(id) => <Input id={id} className="font-mono" inputMode="text" placeholder={d.contextWindow ? fmtTokens(d.contextWindow) : "unknown"} value={ctx} onChange={(ev) => setCtx(ev.target.value)} onKeyDown={(ev) => { if (ev.key === "Enter" && !err) { ev.preventDefault(); save(); } }} />}
      </Field>
    </Modal>
  );
}
