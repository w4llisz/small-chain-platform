import { describe, expect, it, vi } from "vitest";
import { getJob, listEvents, listJobs } from "./api";

describe("listJobs", () => {
  it("passes the filter, opaque cursor and abort signal", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ jobs: [] })));
    const controller = new AbortController();
    await expect(listJobs({ state: "failed", cursor: "a b", signal: controller.signal }, fetcher)).resolves.toEqual({ jobs: [] });
    expect(fetcher).toHaveBeenCalledWith("/v1/jobs?limit=20&state=failed&cursor=a+b", {
      headers: { Accept: "application/json" }, signal: controller.signal,
    });
  });
  it("preserves the API's structured error", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "job_list_unavailable", message: "PostgreSQL required" },
    }), { status: 501 }));
    await expect(listJobs({}, fetcher)).rejects.toMatchObject({ status: 501, code: "job_list_unavailable", message: "PostgreSQL required" });
  });
  it("rejects malformed summaries", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ jobs: [{ state: "unknown" }] })));
    await expect(listJobs({}, fetcher)).rejects.toMatchObject({ code: "invalid_response" });
  });
});

describe("detail clients", () => {
  it("encodes the selected ID and validates its snapshot", async () => {
    const job = { id: "job/id", spec: { kind: "demo.checksum", payload: "hello", max_attempts: 3, timeout_ms: 1000 },
      state: "succeeded", attempts: 1, result: "checksum", created_at: "2026-10-19T10:00:00Z", updated_at: "2026-10-19T10:01:00Z" };
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(job)));
    await expect(getJob(job.id, undefined, fetcher)).resolves.toEqual(job);
    expect(fetcher.mock.calls[0][0]).toBe("/v1/jobs/job%2Fid");
  });
  it("rejects a snapshot for the wrong job", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ id: "wrong" })));
    await expect(getJob("selected", undefined, fetcher)).rejects.toMatchObject({ code: "invalid_response" });
  });
  it("round-trips a cursor beyond Number.MAX_SAFE_INTEGER without conversion", async () => {
    const id = "9007199254740993";
    const event = { id, kind: "claimed", state: "running", attempt: 1, created_at: "2026-10-19T10:00:00Z" };
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ events: [event], next_cursor: id })));
    await expect(listEvents("job-1", { cursor: id }, fetcher)).resolves.toEqual({ events: [event], next_cursor: id });
    expect(fetcher.mock.calls[0][0]).toBe(`/v1/jobs/job-1/events?limit=20&cursor=${id}`);
  });
  it("rejects numeric event IDs and malformed cursors", async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ events: [{ id: 9007199254740992 }] })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ events: [], next_cursor: "not-a-decimal" })));
    await expect(listEvents("job-1", {}, fetcher)).rejects.toMatchObject({ code: "invalid_response" });
    await expect(listEvents("job-1", {}, fetcher)).rejects.toMatchObject({ code: "invalid_response" });
  });
});
