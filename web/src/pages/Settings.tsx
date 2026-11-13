import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Button, Card, ConfirmModal, errText, Field, Input, PageHeader, Skeleton, Switch, useToast } from "../components/ui";
import { api } from "../lib/api";
import type { Settings } from "../lib/api";
import { useSettings, useCheckUpdate, useUpdateSettings } from "../lib/hooks";

function Row({ title, body, children }: { title: string; body: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-6 px-4 py-4">
      <div><div className="font-medium">{title}</div><p className="text-muted text-[13px] mt-0.5 max-w-lg">{body}</p></div>
      {children}
    </div>
  );
}

const SOURCE: Record<string, string> = { flag: "the --port argument", env: "the PORT environment variable", settings: "Settings", default: "the default" };

function PortCard({ d }: { d: Settings }) {
  const upd = useUpdateSettings(), toast = useToast();
  const [port, setPort] = useState(String(d.port ?? d.activePort));
  const n = Number(port);
  const valid = Number.isInteger(n) && n >= 1 && n <= 65535;
  const overridden = d.portSource === "flag" || d.portSource === "env";
  const save = (value: number) => upd.mutate({ port: value }, {
    onError: (e) => toast("err", errText(e)),
    onSuccess: (r) => {
      if (overridden) { toast("ok", `Saved ${value} for the next start without ${SOURCE[d.portSource]}`); return; }
      toast("ok", `Moved to port ${r.activePort}`);
      // the server now listens on the new port only: follow it (the login cookie is shared across ports)
      if (r.activePort !== Number(location.port || (location.protocol === "https:" ? 443 : 80)))
        setTimeout(() => { location.href = `${location.protocol}//${location.hostname}:${r.activePort}${location.pathname}`; }, 800);
    },
  });
  return (
    <Card className="p-4 mb-4">
      <h2 className="font-semibold">Port</h2>
      <p className="text-muted text-[13px] mt-1 max-w-xl">
        Listening on <span className="font-mono text-text">{d.activePort}</span> (from {SOURCE[d.portSource]}). Default is {d.defaultPort}.
        {overridden ? ` ${d.portSource === "flag" ? "--port" : "PORT"} takes priority, so a saved port only applies when you start without it.` : " Changing it moves the server right away and reloads this page on the new port."}
      </p>
      <form className="flex items-end gap-2 mt-3 max-w-sm" onSubmit={(e) => { e.preventDefault(); if (valid) save(n); }}>
        <div className="flex-1"><Field label="Port" hint={port && !valid ? "Enter a number from 1 to 65535" : undefined}>{(id) => <Input id={id} className="font-mono" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} />}</Field></div>
        <Button type="submit" variant="primary" loading={upd.isPending} disabled={!valid || n === (d.port ?? d.activePort)}>Save</Button>
        {d.port !== undefined && d.port !== d.defaultPort && <Button type="button" disabled={upd.isPending} onClick={() => { setPort(String(d.defaultPort)); save(d.defaultPort); }}>Reset</Button>}
      </form>
    </Card>
  );
}

function BackupCard() {
  const toast = useToast(), qc = useQueryClient(), pick = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<{ name: string; data: unknown } | null>(null), [busy, setBusy] = useState(false);
  const choose = async (f?: File) => {
    if (!f) return;
    try { setFile({ name: f.name, data: JSON.parse(await f.text()) }); } catch { toast("err", "That file is not valid JSON"); }
    if (pick.current) pick.current.value = ""; // the same file can be chosen again
  };
  const restore = async () => {
    setBusy(true);
    try {
      await api("/api/restore", { method: "POST", json: file!.data });
      toast("ok", "Backup restored");
      setFile(null);
      qc.invalidateQueries();
    } catch (e) { toast("err", errText(e)); } finally { setBusy(false); }
  };
  return (
    <Card className="p-4 mb-4">
      <h2 className="font-semibold">Backup &amp; restore</h2>
      <p className="text-muted text-[13px] mt-1 mb-3 max-w-2xl">
        Providers, keys, combos, models, proxy pools and settings as one JSON file. It contains your upstream keys, OAuth tokens, gateway keys and the password hash in clear text, so keep it private.
        Usage history, and this machine's port and Local only setting, are not included.
      </p>
      <div className="flex flex-wrap gap-2">
        <Button type="button" onClick={() => { location.href = "/api/backup"; /* attachment: the page stays */ }}>Download backup</Button>
        <Button type="button" onClick={() => pick.current?.click()}>Restore from file…</Button>
        <input ref={pick} type="file" accept="application/json,.json" className="sr-only" aria-label="Backup file to restore" tabIndex={-1} onChange={(e) => choose(e.target.files?.[0])} />
      </div>
      <ConfirmModal open={!!file} onClose={() => setFile(null)} onConfirm={restore} loading={busy} confirmLabel="Restore" title="Restore this backup?"
        body={`${file?.name ?? ""} replaces your current providers, keys, combos, models, proxy pools and settings (including the password). This cannot be undone: download a backup first if you may need the current setup.`} />
    </Card>
  );
}

function UpdateCard({ d }: { d: Settings }) {
  const toast = useToast(), upd = useUpdateSettings(), check = useCheckUpdate();
  const [url, setUrl] = useState(d.updateUrl ?? "");
  const saved = d.updateUrl ?? "";
  const fromEnv = d.updateUrlFromEnv;
  const valid = !url || /^https?:\/\/[^\s/]+/i.test(url);
  const insecure = /^http:\/\//i.test(url) && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])(:|\/|$)/i.test(url);
  const save = () => upd.mutate({ updateUrl: url.trim() }, {
    onSuccess: () => toast("ok", url.trim() ? "Update URL saved" : "Automatic updates off"),
    onError: (e) => toast("err", errText(e)),
  });
  return (
    <Card className="p-4 mb-4">
      <h2 className="font-semibold">Updates</h2>
      <p className="text-muted text-[13px] mt-1 max-w-2xl">
        Router-Go compares the SHA-256 of its own file with the binary at this URL, at start and every 6 hours. A different one is installed and the process restarts.
        {fromEnv && " ROUTER_GO_UPDATE_URL is set, so it overrides this."}
      </p>
      <form className="mt-3 max-w-2xl" onSubmit={(e) => { e.preventDefault(); if (valid && url.trim() !== saved) save(); }}>
        <Field label="Update URL" error={url && !valid ? "Must start with http:// or https://" : undefined}
          hint={insecure ? "Plain http: anyone between this machine and the host can replace the binary." : "Whatever this URL serves runs with your keys."}>{(id) => <Input id={id} className="font-mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/router-go" disabled={fromEnv} />}</Field>
        <div className="flex flex-wrap items-center gap-2 mt-3">
          <Button type="submit" variant={url.trim() !== saved ? "primary" : undefined} loading={upd.isPending} disabled={fromEnv || !valid || url.trim() === saved}>Save</Button>
          <Button type="button" disabled={check.isPending || (!saved && !fromEnv)} loading={check.isPending}
            onClick={() => check.mutate(undefined, {
              onSuccess: (r) => {
                toast("ok", r.message);
                // the background loop installs and execs a new binary: follow it once it is up
                if (r.restarting) setTimeout(() => location.reload(), 6000);
              },
              onError: (e) => toast("err", errText(e)),
            })}>Check for update</Button>
        </div>
      </form>
      {check.data && <p className="text-[13px] mt-2">{check.data.message}</p>}
    </Card>
  );
}

export default function SettingsPage() {
  const s = useSettings(), upd = useUpdateSettings(), toast = useToast();
  const [cur, setCur] = useState(""), [next, setNext] = useState("");
  const set = (patch: Record<string, unknown>, ok: string) => upd.mutate(patch, { onSuccess: () => toast("ok", ok), onError: (e) => toast("err", errText(e)) });
  if (s.isLoading || !s.data) return <Skeleton className="h-64" />;
  const d = s.data;
  return (
    <>
      <PageHeader title="Settings" />
      <Card className="divide-y divide-border mb-4">
        <Row title="Require login" body="Protect the dashboard with the password. Turning this off exposes the dashboard API to anyone who can reach this port.">
          <Switch checked={d.requireLogin} label="Require login" disabled={upd.isPending} onChange={(v) => set({ requireLogin: v }, "Saved")} />
        </Row>
        <Row title="Local only" body={d.hostFromEnv
          ? `HOSTNAME_BIND / ROUTER_GO_ADDR fixes the bind address to ${d.bindHost}, so this only applies when you start without it.`
          : "Listen on 127.0.0.1 only, so other machines cannot reach the dashboard or the API. Off (default) listens on all interfaces. Turning it on from a remote browser cuts that browser off."}>
          <Switch checked={d.localOnly} label="Local only" disabled={upd.isPending} onChange={(v) => set({ localOnly: v }, v ? "Listening on 127.0.0.1 only" : "Listening on all interfaces")} />
        </Row>
      </Card>
      <PortCard d={d} />
      <UpdateCard d={d} />
      <BackupCard />
      <Card className="p-4 mb-4">
        <h2 className="font-semibold mb-3">Change password</h2>
        <form className="grid sm:grid-cols-2 gap-4 max-w-2xl" onSubmit={(e) => { e.preventDefault(); upd.mutate({ currentPassword: cur, newPassword: next }, { onSuccess: () => { toast("ok", "Password changed"); setCur(""); setNext(""); }, onError: (er) => toast("err", errText(er)) }); }}>
          <Field label="Current password" hint={d.hasPassword ? undefined : "Not set yet — use the initial password (default 123456)"}>{(id) => <Input id={id} type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} />}</Field>
          <Field label="New password" hint="At least 8 characters">{(id) => <Input id={id} type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />}</Field>
          <div className="sm:col-span-2"><Button type="submit" variant="primary" loading={upd.isPending} disabled={!cur || next.length < 8}>Update password</Button></div>
        </form>
      </Card>
      <Card className="p-4">
        <h2 className="font-semibold mb-3">About</h2>
        <dl className="grid grid-cols-[120px_1fr] gap-y-2 text-[13px]">
          <dt className="text-muted">Version</dt><dd className="font-mono">{d.version}</dd>
          <dt className="text-muted">Data directory</dt><dd className="font-mono break-all">{d.dataDir}</dd>
          <dt className="text-muted">Machine ID</dt><dd className="font-mono">{d.machineId}</dd>
        </dl>
      </Card>
    </>
  );
}
