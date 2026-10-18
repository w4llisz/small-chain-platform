import { useEffect, useRef, useState } from "react";
import { jobStates, listJobs, type JobsClient, type JobState, type JobSummary } from "./api";
import "./styles.css";

const label = (state: string) => state.charAt(0).toUpperCase() + state.slice(1);
function timestamp(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toISOString().replace("T", " ").slice(0, 16);
}

export default function App({ loadJobs = listJobs }: { loadJobs?: JobsClient }) {
  const [filter, setFilter] = useState<JobState | "">("");
  const [jobs, setJobs] = useState<JobSummary[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string>();
  const [reload, setReload] = useState(0);
  const epoch = useRef(0);
  const moreController = useRef<AbortController | null>(null);
  const morePending = useRef(false);

  useEffect(() => {
    const version = ++epoch.current;
    const controller = new AbortController();
    moreController.current?.abort();
    morePending.current = false;
    setLoadingMore(false);
    setLoading(true);
    setError(undefined);
    setJobs([]);
    setCursor(undefined);
    loadJobs({ state: filter || undefined, signal: controller.signal })
      .then((page) => {
        if (version !== epoch.current || controller.signal.aborted) return;
        setJobs(page.jobs);
        setCursor(page.next_cursor);
      })
      .catch((reason: unknown) => {
        if (version === epoch.current && !controller.signal.aborted)
          setError(reason instanceof Error ? reason.message : "Unable to load jobs.");
      })
      .finally(() => {
        if (version === epoch.current && !controller.signal.aborted) setLoading(false);
      });
    return () => { controller.abort(); moreController.current?.abort(); };
  }, [filter, reload, loadJobs]);

  async function loadOlder() {
    if (!cursor || morePending.current) return;
    const version = epoch.current;
    const controller = new AbortController();
    moreController.current = controller;
    morePending.current = true;
    setLoadingMore(true);
    setError(undefined);
    try {
      const page = await loadJobs({ state: filter || undefined, cursor, signal: controller.signal });
      if (version !== epoch.current || controller.signal.aborted) return;
      setJobs((current) => [...current, ...page.jobs]);
      setCursor(page.next_cursor);
    } catch (reason) {
      if (version === epoch.current && !controller.signal.aborted)
        setError(reason instanceof Error ? reason.message : "Unable to load older jobs.");
    } finally {
      if (version === epoch.current) {
        morePending.current = false;
        setLoadingMore(false);
      }
    }
  }

  return <>
    <header className="topbar"><div className="topbar-inner">
      <span className="brand-mark" aria-hidden="true">◆</span>
      <strong>Small Chain</strong><span className="section-name">Operations</span>
      <span className="backend">PostgreSQL</span>
    </div></header>
    <main>
      <h1>Jobs</h1>
      <p className="intro">Inspect durable work and follow failures.</p>
      <nav className="filters" aria-label="Filter jobs by state">
        {(["", ...jobStates] as const).map((state) => <button key={state} type="button"
          aria-pressed={filter === state} onClick={() => setFilter(state)}>
          {state ? label(state) : "All"}
        </button>)}
      </nav>
      <section className="panel" aria-label="Job results" aria-busy={loading || loadingMore}>
        {error && <div className="error" role="alert"><div><strong>Could not load jobs</strong><p>{error}</p></div>
          <button type="button" onClick={() => cursor ? void loadOlder() : setReload((n) => n + 1)}>Try again</button>
        </div>}
        {loading ? <p className="notice" role="status">Loading durable jobs…</p> :
          jobs.length === 0 && !error ? <div className="notice"><strong>No jobs found</strong>
            <p>{filter ? `No ${filter} jobs in the retained window.` : "The retained job window is empty."}</p></div> :
          jobs.length > 0 && <div className="table-scroll" tabIndex={0} role="region" aria-label="Scrollable jobs table">
            <table><thead><tr>{["Job", "Kind", "State", "Attempts", "Created (UTC)", "Updated (UTC)"].map((name) =>
              <th key={name} scope="col">{name}</th>)}</tr></thead>
              <tbody>{jobs.map((job) => <tr key={job.id}>
                <td className="job-cell"><code>{job.id}</code>{job.error && <span className="job-error">{job.error}</span>}</td>
                <td>{job.kind}</td><td><span className={`status status-${job.state}`}>{label(job.state)}</span></td>
                <td>{job.attempts} / {job.max_attempts}</td>
                <td><time dateTime={job.created_at}>{timestamp(job.created_at)}</time></td>
                <td><time dateTime={job.updated_at}>{timestamp(job.updated_at)}</time></td>
              </tr>)}</tbody>
            </table>
          </div>}
        <footer><span aria-live="polite">{jobs.length} jobs loaded · newest first</span>
          {cursor && <button type="button" disabled={loadingMore} onClick={() => void loadOlder()}>
            {loadingMore ? "Loading…" : "Load older jobs"}
          </button>}
        </footer>
      </section>
      <p className="footnote">Read-only view. Jobs may change or expire between pages. Reload to see the latest state.</p>
    </main>
  </>;
}
