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

export async function listJobs(options: ListJobsOptions = {}, fetcher: typeof fetch = fetch): Promise<JobPage> {
  const params = new URLSearchParams({ limit: "20" });
  if (options.state) params.set("state", options.state);
  if (options.cursor) params.set("cursor", options.cursor);
  const response = await fetcher(`/v1/jobs?${params}`, {
    headers: { Accept: "application/json" }, signal: options.signal,
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
  const page: unknown = await response.json();
  if (!record(page) || !Array.isArray(page.jobs) || page.jobs.length > 20 ||
    (page.next_cursor !== undefined && typeof page.next_cursor !== "string")) {
    throw new ApiError("The API returned an invalid job page.", 502, "invalid_response");
  }
  return { jobs: page.jobs.map(parseJob), next_cursor: page.next_cursor as string | undefined };
}
