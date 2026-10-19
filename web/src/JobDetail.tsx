import { useEffect, useRef, useState } from "react";
import { ApiError, getJob, listEvents, type SnapshotClient, type EventsClient, type JobSnapshot, type JobEvent } from "./api";

function message(reason: unknown): string {
  if (reason instanceof ApiError && reason.status === 404)
    return "This job is no longer available. Retention may have removed it.";
  return reason instanceof Error ? reason.message : "Request failed. Try again.";
}

function Snapshot({ id, load }: { id: string; load: SnapshotClient }) {
  const [job, setJob] = useState<JobSnapshot>();
  const [error, setError] = useState<string>();
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError(undefined);
    load(id, controller.signal).then((value) => {
      if (!controller.signal.aborted) setJob(value);
    }).catch((reason: unknown) => {
      if (!controller.signal.aborted) setError(message(reason));
    });
    return () => controller.abort();
  }, [id, load, retry]);
  return <section aria-label="Job snapshot">
    <h3>Snapshot</h3>
    {error ? <div role="alert"><p>{error}</p><button type="button" onClick={() => setRetry((n) => n + 1)}>Retry snapshot</button></div> :
      !job ? <p role="status">Loading snapshot…</p> : <>
        <dl className="snapshot-fields">
          <div><dt>State</dt><dd className={`status status-${job.state}`}>{job.state}</dd></div>
          <div><dt>Attempts</dt><dd>{job.attempts} / {job.spec.max_attempts}</dd></div>
          <div><dt>Created (UTC)</dt><dd><time dateTime={job.created_at}>{job.created_at}</time></dd></div>
          <div><dt>Updated (UTC)</dt><dd><time dateTime={job.updated_at}>{job.updated_at}</time></dd></div>
        </dl>
        {job.error ? <p className="job-error">{job.error}</p> : null}
        {job.result ? <><h4>Result</h4><pre>{job.result}</pre></> : null}
        <h4>Normalized spec</h4><pre>{JSON.stringify(job.spec, null, 2)}</pre>
      </>}
  </section>;
}

function Events({ id, load }: { id: string; load: EventsClient }) {
  const [events, setEvents] = useState<JobEvent[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();
  const [retry, setRetry] = useState(0);
  const current = useRef<AbortController | null>(null);
  const pending = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    current.current = controller;
    pending.current = true;
    setLoading(true);
    setError(undefined);
    load(id, { signal: controller.signal }).then((page) => {
      if (controller.signal.aborted) return;
      setEvents(page.events);
      setCursor(page.next_cursor);
    }).catch((reason: unknown) => {
      if (!controller.signal.aborted) setError(message(reason));
    }).finally(() => {
      if (!controller.signal.aborted) { pending.current = false; setLoading(false); }
    });
    return () => { controller.abort(); current.current?.abort(); };
  }, [id, load, retry]);

  async function loadMore() {
    if (!cursor || pending.current) return;
    const controller = new AbortController();
    current.current = controller;
    pending.current = true;
    setLoading(true);
    setError(undefined);
    try {
      const page = await load(id, { cursor, signal: controller.signal });
      if (controller.signal.aborted) return;
      setEvents((existing) => [...existing, ...page.events]);
      setCursor(page.next_cursor);
    } catch (reason) {
      if (!controller.signal.aborted) setError(message(reason));
    } finally {
      if (!controller.signal.aborted) { pending.current = false; setLoading(false); }
    }
  }

  return <section aria-label="Lifecycle history" aria-busy={loading}>
    <h3>Lifecycle history</h3>
    <p className="footnote">Oldest first. Heartbeats are omitted. Snapshot and history are separate reads.</p>
    {error ? <div role="alert"><p>{error}</p><button type="button" onClick={() => cursor ? void loadMore() : setRetry((n) => n + 1)}>Retry events</button></div> : null}
    {events.length > 0 ? <ol className="event-list">{events.map((event) => <li key={event.id}>
      <div><strong>{event.kind}</strong><span className={`status status-${event.state}`}>{event.state}</span></div>
      <p>Attempt {event.attempt} · <time dateTime={event.created_at}>{event.created_at}</time></p>
      <code>Event {event.id}</code>
      {event.lease_owner ? <p>Worker: {event.lease_owner}</p> : null}
      {event.message ? <p>{event.message}</p> : null}
    </li>)}</ol> : !loading && !error ? <p>No retained events.</p> : null}
    {loading ? <p role="status">Loading events…</p> : null}
    {cursor ? <button type="button" disabled={loading} onClick={() => void loadMore()}>Load later events</button> : null}
  </section>;
}

export default function JobDetail({ id, loadSnapshot = getJob, loadEvents = listEvents }: {
  id: string; loadSnapshot?: SnapshotClient; loadEvents?: EventsClient;
}) {
  // Keyed children also reset correctly when the component is reused outside App.
  return <div className="detail-content">
    <Snapshot key={`snapshot-${id}`} id={id} load={loadSnapshot} />
    <Events key={`events-${id}`} id={id} load={loadEvents} />
  </div>;
}
