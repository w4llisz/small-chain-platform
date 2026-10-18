import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import App from "./App";
import type { JobsClient, JobPage, JobSummary } from "./api";

const job: JobSummary = { id: "job-1", kind: "demo.checksum", state: "failed", attempts: 3, max_attempts: 3,
  error: "attempt deadline exceeded", created_at: "2026-10-14T10:00:00Z", updated_at: "2026-10-14T10:01:00Z" };

describe("operations console", () => {
  it("renders summaries and their failure context", async () => {
    render(<App loadJobs={vi.fn<JobsClient>().mockResolvedValue({ jobs: [job] })} />);
    expect(screen.getByRole("status").textContent).toContain("Loading");
    expect(await screen.findByText(job.id)).toBeTruthy();
    expect(screen.getByText("attempt deadline exceeded")).toBeTruthy();
    expect(screen.getByText("3 / 3")).toBeTruthy();
  });
  it("resets the cursor chain when filtering", async () => {
    const client = vi.fn<JobsClient>().mockResolvedValueOnce({ jobs: [job], next_cursor: "older" }).mockResolvedValueOnce({ jobs: [] });
    render(<App loadJobs={client} />);
    await screen.findByText(job.id);
    await userEvent.click(screen.getByRole("button", { name: "Queued" }));
    expect(await screen.findByText("No jobs found")).toBeTruthy();
    expect(client.mock.calls[1][0]).toMatchObject({ state: "queued" });
    expect(client.mock.calls[1][0]?.cursor).toBeUndefined();
  });
  it("appends older pages", async () => {
    const client = vi.fn<JobsClient>().mockResolvedValueOnce({ jobs: [job], next_cursor: "older" })
      .mockResolvedValueOnce({ jobs: [{ ...job, id: "job-2" }] });
    render(<App loadJobs={client} />);
    await userEvent.click(await screen.findByRole("button", { name: "Load older jobs" }));
    expect(await screen.findByText("job-2")).toBeTruthy();
    expect(screen.getByText("job-1")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load older jobs" })).toBeNull();
  });
  it("recovers from an initial API failure", async () => {
    const client = vi.fn<JobsClient>().mockRejectedValueOnce(new Error("database unavailable")).mockResolvedValueOnce({ jobs: [] });
    render(<App loadJobs={client} />);
    expect((await screen.findByRole("alert")).textContent).toContain("database unavailable");
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No jobs found")).toBeTruthy();
  });
  it("ignores an old page that resolves after a filter change", async () => {
    let resolveOld!: (page: JobPage) => void;
    const oldPage = new Promise<JobPage>((resolve) => { resolveOld = resolve; });
    const client = vi.fn<JobsClient>().mockReturnValueOnce(oldPage).mockResolvedValueOnce({ jobs: [] });
    render(<App loadJobs={client} />);
    await userEvent.click(screen.getByRole("button", { name: "Queued" }));
    await screen.findByText("No jobs found");
    await act(async () => { resolveOld({ jobs: [job] }); });
    expect(screen.queryByText(job.id)).toBeNull();
    expect(client.mock.calls[0][0]?.signal?.aborted).toBe(true);
  });
  it("retries a failed older page without discarding loaded rows", async () => {
    const client = vi.fn<JobsClient>().mockResolvedValueOnce({ jobs: [job], next_cursor: "older" })
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockResolvedValueOnce({ jobs: [{ ...job, id: "job-2" }] });
    render(<App loadJobs={client} />);
    await userEvent.click(await screen.findByRole("button", { name: "Load older jobs" }));
    await screen.findByRole("alert");
    expect(screen.getByText(job.id)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("job-2")).toBeTruthy();
    expect(client.mock.calls[2][0]?.cursor).toBe("older");
  });
});
