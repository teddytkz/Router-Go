import { CheckCircle2, XCircle } from "lucide-react";
import type { TestBatch } from "../../lib/api";
import { Button, Modal } from "../ui";
import { fmtMs } from "./shared";

/** The log of a "Test all" run: one line per key with outcome, latency and the error, if any. */
export function TestResultsModal({ data, onClose }: { data: TestBatch; onClose: () => void }) {
  const { total, passed, failed } = data.summary;
  return (
    <Modal open onClose={onClose} title="Test results" footer={<Button variant="primary" onClick={onClose}>Close</Button>}>
      <div role="status" className="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">
        <span>Total <b className="tabular-nums">{total}</b></span>
        <span className="text-ok">Passed <b className="tabular-nums">{passed}</b></span>
        <span className={failed ? "text-err" : "text-muted"}>Failed <b className="tabular-nums">{failed}</b></span>
      </div>
      {total === 0 ? <p className="text-muted">No enabled API keys to test.</p> : (
        <ul className="max-h-[50vh] overflow-y-auto divide-y divide-border rounded-lg border border-border font-mono text-xs">
          {data.results.map((r) => (
            <li key={r.connectionId} className="flex items-start gap-2 px-3 py-2">
              {r.ok ? <CheckCircle2 className="size-4 shrink-0 text-ok mt-px" aria-label="passed" /> : <XCircle className="size-4 shrink-0 text-err mt-px" aria-label="failed" />}
              <div className="min-w-0 flex-1">
                <div className="truncate"><span className="font-semibold">{r.providerName}</span> <span className="text-muted">/ {r.name}</span></div>
                {!r.ok && r.error && <div className="text-err break-words">{r.error}</div>}
              </div>
              <span className="shrink-0 tabular-nums text-muted">{fmtMs(r.latencyMs)}</span>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}
