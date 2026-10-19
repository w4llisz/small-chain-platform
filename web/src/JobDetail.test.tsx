import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import JobDetail from "./JobDetail";
import { ApiError, type EventsClient, type SnapshotClient, type JobSnapshot, type EventPage } from "./api";

const job: JobSnapshot = {
  id: "job-1", spec: { kind: "demo.checksum", payload: "hello", max_attempts: 3, timeout_ms: 1000 },
  state: "succeeded", attempts: 2, result: "checksum-result",
  created_at: "2026-10-19T10:00:00Z", updated_at: "2026-10-19T10:01:00Z",
};
const first = { id: "9007199254740993", kind: "submitted", state: "queued" as const, attempt: 0, created_at: job.created_at };
const second = { ...first, id: "9007199254740994", kind: "claimed", state: "running" as const, attempt: 1, lease_owner: "worker-a" };

describe("JobDetail", () => {
  it("starts independent snapshot and history requests without waiting for either", async () => {
    let resolveSnapshot!: (value: JobSnapshot) => void;
    const snapshot = vi.fn<SnapshotClient>().mockReturnValue(new Promise((resolve) => { resolveSnapshot = resolve; }));
    const events = vi.fn<EventsClient>().mockResolvedValue({ events: [first], next_cursor: first.id });
    render(<JobDetail id={job.id} loadSnapshot={snapshot} loadEvents={events} />);
    expect(snapshot).toHaveBeenCalledTimes(1);
    expect(events).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("submitted")).toBeTruthy();
    expect(screen.getByText("Loading snapshot…")).toBeTruthy();
    await act(async () => { resolveSnapshot(job); });
    expect(screen.getByText(job.result!)).toBeTruthy();
    expect(screen.getByText(/"payload": "hello"/)).toBeTruthy();
  });

  it("keeps string cursors and appends later events", async () => {
    const events = vi.fn<EventsClient>().mockResolvedValueOnce({ events: [first], next_cursor: first.id })
      .mockResolvedValueOnce({ events: [second] });
    render(<JobDetail id={job.id} loadSnapshot={vi.fn<SnapshotClient>().mockResolvedValue(job)} loadEvents={events} />);
    await userEvent.click(await screen.findByRole("button", { name: "Load later events" }));
    expect(await screen.findByText("claimed")).toBeTruthy();
    expect(screen.getByText("submitted")).toBeTruthy();
    expect(screen.getByText(`Event ${first.id}`)).toBeTruthy();
    expect(events.mock.calls[1]).toEqual([job.id, { cursor: first.id, signal: expect.any(AbortSignal) }]);
    expect(screen.queryByRole("button", { name: "Load later events" })).toBeNull();
  });

  it("preserves history and retries the same cursor after a page failure", async () => {
    const events = vi.fn<EventsClient>().mockResolvedValueOnce({ events: [first], next_cursor: first.id })
      .mockRejectedValueOnce(new Error("database unavailable"))
      .mockResolvedValueOnce({ events: [second] });
    render(<JobDetail id={job.id} loadSnapshot={vi.fn<SnapshotClient>().mockResolvedValue(job)} loadEvents={events} />);
    await userEvent.click(await screen.findByRole("button", { name: "Load later events" }));
    expect((await screen.findByRole("alert")).textContent).toContain("database unavailable");
    expect(screen.getByText("submitted")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Retry events" }));
    expect(await screen.findByText("claimed")).toBeTruthy();
    expect(events.mock.calls[2][1]?.cursor).toBe(first.id);
  });

  it("retries an initial event error and renders an empty history", async () => {
    const events = vi.fn<EventsClient>().mockRejectedValueOnce(new Error("temporary failure")).mockResolvedValueOnce({ events: [] });
    render(<JobDetail id={job.id} loadSnapshot={vi.fn<SnapshotClient>().mockResolvedValue(job)} loadEvents={events} />);
    await userEvent.click(await screen.findByRole("button", { name: "Retry events" }));
    expect(await screen.findByText("No retained events.")).toBeTruthy();
  });

  it("explains retained-away snapshots while history remains independently usable", async () => {
    const snapshot = vi.fn<SnapshotClient>().mockRejectedValueOnce(new ApiError("not found", 404)).mockResolvedValueOnce(job);
    render(<JobDetail id={job.id} loadSnapshot={snapshot} loadEvents={vi.fn<EventsClient>().mockResolvedValue({ events: [] })} />);
    expect(await screen.findByText(/Retention may have removed it/)).toBeTruthy();
    expect(await screen.findByText("No retained events.")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Retry snapshot" }));
    expect(await screen.findByText(job.result!)).toBeTruthy();
  });

  it("aborts both resources and discards old results when selecting a new job", async () => {
    let resolveSnapshot!: (value: JobSnapshot) => void;
    let resolveEvents!: (value: EventPage) => void;
    const snapshot = vi.fn<SnapshotClient>().mockReturnValueOnce(new Promise((resolve) => { resolveSnapshot = resolve; }))
      .mockResolvedValueOnce({ ...job, id: "job-2", result: "new-result" });
    const events = vi.fn<EventsClient>().mockReturnValueOnce(new Promise((resolve) => { resolveEvents = resolve; }))
      .mockResolvedValueOnce({ events: [] });
    const view = render(<JobDetail id={job.id} loadSnapshot={snapshot} loadEvents={events} />);
    view.rerender(<JobDetail id="job-2" loadSnapshot={snapshot} loadEvents={events} />);
    expect(await screen.findByText("new-result")).toBeTruthy();
    expect(snapshot.mock.calls[0][1]?.aborted).toBe(true);
    expect(events.mock.calls[0][1]?.signal?.aborted).toBe(true);
    await act(async () => { resolveSnapshot(job); resolveEvents({ events: [first] }); });
    expect(screen.queryByText(job.result!)).toBeNull();
    expect(screen.queryByText("submitted")).toBeNull();
  });
});
