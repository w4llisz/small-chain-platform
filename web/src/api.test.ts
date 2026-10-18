import { describe, expect, it, vi } from "vitest";
import { listJobs } from "./api";

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
