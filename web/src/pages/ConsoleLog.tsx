import { ArrowDownToLine, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Badge, Button, Card, cn, errText, Input, PageHeader, Select, Switch, useToast } from "../components/ui";
import { api, type LogEntry } from "../lib/api";

const MAX = 1000; // same cap as the server buffer
const LEVEL_COLOR: Record<string, string> = { INFO: "var(--term-info)", WARN: "var(--term-warn)", ERROR: "var(--term-error)" };
const clock = (t: string) => new Date(t).toLocaleTimeString([], { hour12: false }) + "." + String(new Date(t).getMilliseconds()).padStart(3, "0");

/** "[CHAT] rest" → a coloured tag followed by the message. */
function Line({ e }: { e: LogEntry }) {
  const m = /^\[([^\]]+)\]\s?(.*)$/s.exec(e.msg);
  return (
    <div className="flex gap-2 leading-5 whitespace-pre-wrap break-words">
      <span className="shrink-0 text-[var(--term-muted)] tabular-nums">{clock(e.t)}</span>
      <span className="shrink-0 w-11 font-semibold" style={{ color: LEVEL_COLOR[e.level] }}>{e.level}</span>
      <span className="min-w-0" style={e.level === "INFO" ? undefined : { color: LEVEL_COLOR[e.level] }}>
        {m ? <><span className="text-[var(--term-tag)]">[{m[1]}]</span> {m[2]}</> : e.msg}
      </span>
    </div>
  );
}

export default function ConsoleLog() {
  const toast = useToast();
  const [logs, setLogs] = useState<LogEntry[]>([]), [live, setLive] = useState(false);
  const [follow, setFollow] = useState(true), [level, setLevel] = useState(""), [q, setQ] = useState("");
  const pane = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const es = new EventSource("/api/console-logs/stream");
    es.onopen = () => setLive(true);
    es.onerror = () => setLive(false); // EventSource reconnects by itself and replays a fresh snapshot
    es.onmessage = (ev) => {
      const m = JSON.parse(ev.data) as { type: "init" | "lines" | "clear"; entries?: LogEntry[] };
      if (m.type === "init") setLogs((m.entries ?? []).slice(-MAX));
      else if (m.type === "lines") setLogs((prev) => [...prev, ...(m.entries ?? [])].slice(-MAX));
      else setLogs([]);
    };
    return () => es.close();
  }, []);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return logs.filter((e) => (!level || e.level === level) && (!needle || e.msg.toLowerCase().includes(needle)));
  }, [logs, level, q]);

  useEffect(() => { // keep the newest line in view while following
    if (follow && pane.current) pane.current.scrollTop = pane.current.scrollHeight;
  }, [shown, follow]);

  const clear = () => api("/api/console-logs", { method: "DELETE" }).then(() => toast("ok", "Logs cleared")).catch((e) => toast("err", errText(e)));
  const count = (l: string) => logs.filter((e) => e.level === l).length;

  return (
    <>
      <PageHeader title="Console Log" subtitle="Live server log: requests, fallbacks, cooldowns, guards"
        actions={<Badge tone={live ? "ok" : "warn"}><i aria-hidden className={cn("size-1.5 rounded-full", live ? "bg-ok animate-pulse" : "bg-warn")} />{live ? "Live" : "Reconnecting…"}</Badge>} />
      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center gap-2 p-3 border-b border-border">
          <Input aria-label="Search logs" className="sm:w-64" placeholder="Search…" value={q} onChange={(e) => setQ(e.target.value)} />
          <Select aria-label="Level" className="w-auto" value={level} onChange={(e) => setLevel(e.target.value)}>
            <option value="">All levels</option><option value="INFO">Info ({count("INFO")})</option><option value="WARN">Warn ({count("WARN")})</option><option value="ERROR">Error ({count("ERROR")})</option>
          </Select>
          <span className="text-[13px] text-muted tabular-nums">{shown.length}{shown.length !== logs.length && ` of ${logs.length}`} lines</span>
          <div className="ml-auto flex items-center gap-3">
            <label className="flex items-center gap-2 text-[13px] text-muted"><ArrowDownToLine className="size-4" aria-hidden />Follow<Switch checked={follow} onChange={setFollow} label="Follow new lines" /></label>
            <Button size="sm" onClick={clear} disabled={!logs.length}><Trash2 className="size-4" />Clear</Button>
          </div>
        </div>
        <div ref={pane} tabIndex={0} role="log" aria-live="off" aria-label="Server log"
          onScroll={(e) => { const el = e.currentTarget; if (follow && el.scrollHeight - el.scrollTop - el.clientHeight > 40) setFollow(false); }}
          className="h-[calc(100vh-17rem)] min-h-64 overflow-y-auto bg-[var(--term-bg)] p-4 font-mono text-xs text-[var(--term-text)]">
          {shown.length === 0 ? <span className="text-[var(--term-muted)]">{logs.length ? "No lines match." : "No logs yet. Send a request through /v1 and it shows up here."}</span>
            : shown.map((e) => <Line key={e.id} e={e} />)}
        </div>
      </Card>
    </>
  );
}
