import { ExternalLink, Loader2 } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, type BuiltinProvider } from "../../lib/api";
import { Button, errText, Field, Input, Modal, useToast } from "../ui";

type Start = { kind: "code" | "poll"; authUrl: string; state: string; interval: number; expiresIn: number };
export const OAUTH_CHANNEL = "router-go-oauth";

/** The code a pasted redirect URL carries (or the pasted text itself). */
function codeFrom(text: string): string {
  const t = text.trim();
  try { return new URL(t).searchParams.get("code") ?? t; } catch { return t; }
}

/**
 * Browser sign-in. Each provider differs:
 *  - "code" (Cline): the provider's page redirects back to /oauth/callback with a code, which that page relays here.
 *  - "poll" (CodeBuddy): the provider shows its own confirmation page; we poll until it is done.
 */
export function OAuthModal({ p, onClose }: { p: BuiltinProvider; onClose: () => void }) {
  const toast = useToast(), qc = useQueryClient();
  const [start, setStart] = useState<Start | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [left, setLeft] = useState(0), [paste, setPaste] = useState("");
  const done = useRef(false), redirectUri = `${location.origin}/oauth/callback?provider=${p.id}`;

  const finish = () => { done.current = true; toast("ok", `Signed in to ${p.name}`); qc.invalidateQueries(); onClose(); };
  const fail = (e: unknown) => { setError(errText(e)); setBusy(false); };

  const begin = async () => {
    setBusy(true); setError("");
    try {
      const s = await api<Start>(`/api/oauth/${p.id}/start`, { method: "POST", json: { redirectUri } });
      setStart(s); setLeft(s.expiresIn || 600);
      window.open(s.authUrl, "_blank");
    } catch (e) { fail(e); } finally { setBusy(false); }
  };

  const exchange = async (code: string) => {
    if (done.current || !code) return;
    setBusy(true); setError("");
    try { await api(`/api/oauth/${p.id}/exchange`, { method: "POST", json: { code, redirectUri } }); finish(); } catch (e) { fail(e); }
  };

  // countdown while waiting
  useEffect(() => {
    if (!start) return;
    const t = setInterval(() => setLeft((s) => Math.max(0, s - 1)), 1000);
    return () => clearInterval(t);
  }, [start]);

  // poll flows: ask until the user has signed in
  useEffect(() => {
    if (!start || start.kind !== "poll" || left <= 0) return;
    let stop = false;
    const tick = async () => {
      try {
        const r = await api<{ status: string }>(`/api/oauth/${p.id}/poll`, { method: "POST", json: { state: start.state } });
        if (!stop && r.status === "ok") finish();
      } catch (e) { if (!stop) { setError(errText(e)); setStart(null); } }
    };
    const t = setInterval(tick, Math.max(2, start.interval) * 1000);
    return () => { stop = true; clearInterval(t); };
  }, [start]); // eslint-disable-line react-hooks/exhaustive-deps

  // code flows: the callback tab reports the code over a BroadcastChannel
  useEffect(() => {
    if (!start || start.kind !== "code") return;
    const ch = new BroadcastChannel(OAUTH_CHANNEL);
    ch.onmessage = (ev: MessageEvent<{ provider?: string; code?: string; error?: string }>) => {
      if (ev.data?.provider !== p.id) return;
      if (ev.data.error) setError(ev.data.error); else if (ev.data.code) exchange(ev.data.code);
    };
    return () => ch.close();
  }, [start]); // eslint-disable-line react-hooks/exhaustive-deps

  const expired = !!start && left <= 0, waiting = !!start && !expired;
  const mm = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;

  return (
    <Modal open onClose={onClose} title={`Sign in to ${p.name}`}
      footer={<><Button onClick={onClose}>Cancel</Button>
        {(!start || expired) && <Button variant="primary" loading={busy} onClick={begin}><ExternalLink className="size-4" />{expired ? "Start again" : "Open login page"}</Button>}</>}>
      {!start && <p className="text-[13px] text-muted">
        {p.oauthKind === "code"
          ? `A ${p.name} page opens in a new tab. Sign in there and you are sent back here automatically.`
          : `A ${p.name} login page opens in a new tab. Confirm the sign-in there; this window notices by itself.`}
      </p>}
      {waiting && (
        <div role="status" className="space-y-3">
          <div className="flex items-center gap-3 rounded-lg border border-border bg-surface-2/50 px-3 py-3 text-[13px]">
            <Loader2 className="size-4 animate-spin text-accent shrink-0" aria-hidden />
            <span className="min-w-0">{start!.kind === "poll" ? "Waiting for you to confirm the sign-in…" : "Waiting for the login to finish…"} <span className="text-muted tabular-nums">expires in {mm}</span></span>
          </div>
          <p className="text-[13px] text-muted">Nothing opened? <a className="text-accent hover:underline break-all" href={start!.authUrl} target="_blank" rel="noreferrer">Open the login page</a>.</p>
          {start!.kind === "code" && (
            <Field label="Or paste the code" hint="If this window does not continue by itself, copy the address of the page you land on (it contains ?code=…) and paste it here.">
              {(id) => <Input id={id} className="font-mono" value={paste} onChange={(e) => setPaste(e.target.value)} placeholder="https://…/oauth/callback?code=…" onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); exchange(codeFrom(paste)); } }} />}
            </Field>
          )}
          {start!.kind === "code" && <Button variant="primary" loading={busy} disabled={!paste.trim()} onClick={() => exchange(codeFrom(paste))}>Use this code</Button>}
        </div>
      )}
      {expired && <p className="text-[13px] text-warn">The sign-in expired. Start again.</p>}
      {error && <div role="alert" className="rounded-lg bg-err-soft px-3 py-2 text-[13px] text-err break-words">{error}</div>}
      <p className="text-xs text-muted">Signing in stores the tokens on this machine and refreshes them automatically.{p.status === "experimental" ? " This provider is experimental: the service may reject the router." : ""}</p>
    </Modal>
  );
}
