export const jobStates = ["queued", "running", "retrying", "succeeded", "failed", "canceled"] as const;
export type JobState = (typeof jobStates)[number];
export interface JobSummary {
  id: string;
  kind: string;
  state: JobState;
  attempts: number;
  max_attempts: number;
  error?: string;
  created_at: string;
  updated_at: string;
}
export interface JobPage { jobs: JobSummary[]; next_cursor?: string }
export interface ListJobsOptions { state?: JobState; cursor?: string; signal?: AbortSignal }
export type JobsClient = (options?: ListJobsOptions) => Promise<JobPage>;

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) {
    super(message);
    this.name = "ApiError";
  }
}

const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null;

function parseJob(value: unknown): JobSummary {
  if (!record(value) || typeof value.id !== "string" || typeof value.kind !== "string" ||
    !jobStates.some((state) => state === value.state) ||
    !Number.isInteger(value.attempts) || !Number.isInteger(value.max_attempts) ||
    typeof value.created_at !== "string" || typeof value.updated_at !== "string" ||
    (value.error !== undefined && typeof value.error !== "string")) {
    throw new ApiError("The API returned an invalid job summary.", 502, "invalid_response");
  }
  return value as unknown as JobSummary;
}

async function readJSON(path: string, signal?: AbortSignal, fetcher: typeof fetch = fetch): Promise<unknown> {
  const response = await fetcher(path, {
    headers: { Accept: "application/json" }, signal,
  });
  if (!response.ok) {
    let payload: unknown;
    try { payload = await response.json(); } catch { /* Non-JSON proxy errors use a bounded fallback. */ }
    const error = record(payload) && record(payload.error) ? payload.error : {};
    throw new ApiError(
      typeof error.message === "string" ? error.message : `Request failed with status ${response.status}.`,
      response.status, typeof error.code === "string" ? error.code : undefined,
    );
  }
  return response.json();
}

export async function listJobs(options: ListJobsOptions = {}, fetcher: typeof fetch = fetch): Promise<JobPage> {
  const params = new URLSearchParams({ limit: "20" });
  if (options.state) params.set("state", options.state);
  if (options.cursor) params.set("cursor", options.cursor);
  const page = await readJSON(`/v1/jobs?${params}`, options.signal, fetcher);
  if (!record(page) || !Array.isArray(page.jobs) || page.jobs.length > 20 ||
    (page.next_cursor !== undefined && typeof page.next_cursor !== "string")) {
    throw new ApiError("The API returned an invalid job page.", 502, "invalid_response");
  }
  return { jobs: page.jobs.map(parseJob), next_cursor: page.next_cursor as string | undefined };
}

export interface JobSnapshot {
  id: string;
  spec: {
    kind: string; payload: string; max_attempts: number; timeout_ms: number;
    delay_ms?: number; fail_first_attempts?: number;
  };
  state: JobState;
  attempts: number;
  result?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface JobEvent {
  id: string;
  kind: string;
  state: JobState;
  attempt: number;
  message?: string;
  lease_owner?: string;
  created_at: string;
}
export interface EventPage { events: JobEvent[]; next_cursor?: string }
export type SnapshotClient = (id: string, signal?: AbortSignal) => Promise<JobSnapshot>;
export type EventsClient = (id: string, options?: { cursor?: string; signal?: AbortSignal }) => Promise<EventPage>;

const optionalString = (value: unknown) => value === undefined || typeof value === "string";
const decimalString = (value: unknown): value is string => typeof value === "string" && /^[1-9][0-9]*$/.test(value);

export async function getJob(id: string, signal?: AbortSignal, fetcher: typeof fetch = fetch): Promise<JobSnapshot> {
  const job = await readJSON(`/v1/jobs/${encodeURIComponent(id)}`, signal, fetcher);
  if (!record(job) || !record(job.spec) || job.id !== id ||
    typeof job.spec.kind !== "string" || typeof job.spec.payload !== "string" ||
    !Number.isInteger(job.spec.max_attempts) || !Number.isInteger(job.spec.timeout_ms) ||
    !jobStates.some((state) => state === job.state) || !Number.isInteger(job.attempts) ||
    !optionalString(job.result) || !optionalString(job.error) ||
    typeof job.created_at !== "string" || typeof job.updated_at !== "string" ||
    (job.spec.delay_ms !== undefined && !Number.isInteger(job.spec.delay_ms)) ||
    (job.spec.fail_first_attempts !== undefined && !Number.isInteger(job.spec.fail_first_attempts))) {
    throw new ApiError("The API returned an invalid job snapshot.", 502, "invalid_response");
  }
  return job as unknown as JobSnapshot;
}

export async function listEvents(id: string, options: { cursor?: string; signal?: AbortSignal } = {}, fetcher: typeof fetch = fetch): Promise<EventPage> {
  const params = new URLSearchParams({ limit: "20" });
  if (options.cursor) params.set("cursor", options.cursor);
  const page = await readJSON(`/v1/jobs/${encodeURIComponent(id)}/events?${params}`, options.signal, fetcher);
  if (!record(page) || !Array.isArray(page.events) || page.events.length > 20 ||
    (page.next_cursor !== undefined && !decimalString(page.next_cursor))) {
    throw new ApiError("The API returned an invalid event page.", 502, "invalid_response");
  }
  const events = page.events.map((event: unknown): JobEvent => {
    if (!record(event) || !decimalString(event.id) || typeof event.kind !== "string" ||
      !jobStates.some((state) => state === event.state) || !Number.isInteger(event.attempt) ||
      !optionalString(event.message) || !optionalString(event.lease_owner) || typeof event.created_at !== "string") {
      throw new ApiError("The API returned an invalid lifecycle event.", 502, "invalid_response");
    }
    // Keep the 64-bit ordering ID as a string; never coerce it to Number.
    return event as unknown as JobEvent;
  });
  return { events, next_cursor: page.next_cursor as string | undefined };
}
