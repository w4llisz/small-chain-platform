import { useEffect, useRef, useState } from "react";
import { ApiError, type CancelClient } from "./api";

export interface CancelNotice { text: string; error: boolean }

export default function Cancellation({ id, cancel, onSettled }: {
  id: string; cancel: CancelClient; onSettled: (notice: CancelNotice) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);

  async function submit() {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    const request = new AbortController();
    controller.current = request;
    let notice: CancelNotice;
    try {
      await cancel(id, request.signal);
      notice = { text: "Cancellation confirmed. Reloading current state and history.", error: false };
    } catch (reason) {
      notice = reason instanceof ApiError && reason.status === 409
        ? { text: "The job finished before cancellation. Reloading the authoritative result.", error: false }
        : reason instanceof ApiError && reason.status === 404
          ? { text: "This job is no longer available. Retention may have removed it.", error: true }
          : { text: "Cancellation could not be confirmed. The request may have reached the server. Reloading current state; no automatic cancel retry.", error: true };
    }
    // Aborting a fetch cannot undo a cancellation already committed by the API.
    if (!request.signal.aborted) onSettled(notice);
  }

  return <div className="cancel-action">
    {confirming ? <>
      <p>Cancel this job? Cancellation is cooperative and cannot undo completed side effects.</p>
      <button type="button" disabled={busy} onClick={() => void submit()}>{busy ? "Canceling…" : "Confirm cancellation"}</button>
      <button type="button" disabled={busy} onClick={() => setConfirming(false)}>Keep job</button>
    </> : <button type="button" onClick={() => setConfirming(true)}>Cancel job</button>}
  </div>;
}
