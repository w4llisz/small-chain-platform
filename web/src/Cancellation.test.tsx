import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import JobDetail from "./JobDetail";
import { ApiError, type CancelClient, type SnapshotClient, type EventsClient, type JobSnapshot } from "./api";

const active: JobSnapshot = {
  id: "job-1", spec: { kind: "demo.checksum", payload: "hello", max_attempts: 3, timeout_ms: 1000 },
  state: "running", attempts: 1, created_at: "2026-10-20T10:00:00Z", updated_at: "2026-10-20T10:01:00Z",
};
const canceled: JobSnapshot = { ...active, state: "canceled", error: "canceled by request" };

describe("detail cancellation", () => {
  it("requires confirmation, suppresses duplicate clicks and refreshes all affected reads", async () => {
    let resolveCancel!: (job: JobSnapshot) => void;
    const cancel = vi.fn<CancelClient>().mockReturnValue(new Promise((resolve) => { resolveCancel = resolve; }));
    const snapshot = vi.fn<SnapshotClient>().mockResolvedValueOnce(active).mockResolvedValueOnce(canceled);
    const events = vi.fn<EventsClient>().mockResolvedValue({ events: [] });
    const changed = vi.fn();
    render(<JobDetail id={active.id} loadSnapshot={snapshot} loadEvents={events} cancel={cancel} onChanged={changed} />);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel job" }));
    expect(cancel).not.toHaveBeenCalled();
    await userEvent.dblClick(screen.getByRole("button", { name: "Confirm cancellation" }));
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Canceling…" }).hasAttribute("disabled")).toBe(true);
    await act(async () => { resolveCancel(canceled); });
    expect(await screen.findByText("Cancellation confirmed. Reloading current state and history.")).toBeTruthy();
    expect(await screen.findByText("canceled", { selector: "dd" })).toBeTruthy();
    expect(snapshot).toHaveBeenCalledTimes(2);
    expect(events).toHaveBeenCalledTimes(2);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
  });

  it("lets the operator dismiss confirmation without a write", async () => {
    const cancel = vi.fn<CancelClient>();
    render(<JobDetail id={active.id} loadSnapshot={vi.fn<SnapshotClient>().mockResolvedValue(active)}
      loadEvents={vi.fn<EventsClient>().mockResolvedValue({ events: [] })} cancel={cancel} />);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel job" }));
    await userEvent.click(screen.getByRole("button", { name: "Keep job" }));
    expect(screen.queryByRole("button", { name: "Confirm cancellation" })).toBeNull();
    expect(cancel).not.toHaveBeenCalled();
  });

  it("shows the authoritative winner after a completion/cancel 409", async () => {
    const snapshot = vi.fn<SnapshotClient>().mockResolvedValueOnce(active)
      .mockResolvedValueOnce({ ...active, state: "succeeded", result: "completed-result" });
    const cancel = vi.fn<CancelClient>().mockRejectedValue(new ApiError("job already finished", 409, "conflict"));
    render(<JobDetail id={active.id} loadSnapshot={snapshot}
      loadEvents={vi.fn<EventsClient>().mockResolvedValue({ events: [] })} cancel={cancel} />);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel job" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm cancellation" }));
    expect(await screen.findByText(/finished before cancellation/)).toBeTruthy();
    expect(await screen.findByText("completed-result")).toBeTruthy();
    expect(screen.queryByText(/Cancellation confirmed/)).toBeNull();
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it("does not claim success or automatically repeat a write after an ambiguous transport failure", async () => {
    const cancel = vi.fn<CancelClient>().mockRejectedValue(new TypeError("connection reset"));
    const snapshot = vi.fn<SnapshotClient>().mockResolvedValueOnce(active).mockResolvedValueOnce(canceled);
    render(<JobDetail id={active.id} loadSnapshot={snapshot}
      loadEvents={vi.fn<EventsClient>().mockResolvedValue({ events: [] })} cancel={cancel} />);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel job" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm cancellation" }));
    expect((await screen.findByRole("alert")).textContent).toContain("could not be confirmed");
    expect(await screen.findByText("canceled", { selector: "dd" })).toBeTruthy();
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it.each(["succeeded", "failed", "canceled"] as const)("does not offer cancellation for %s", async (state) => {
    render(<JobDetail id={active.id} loadSnapshot={vi.fn<SnapshotClient>().mockResolvedValue({ ...active, state })}
      loadEvents={vi.fn<EventsClient>().mockResolvedValue({ events: [] })} />);
    await screen.findByText(state, { selector: "dd" });
    expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
  });

  it("aborts a pending cancellation and ignores its late result when selection changes", async () => {
    let resolveCancel!: (job: JobSnapshot) => void;
    const cancel = vi.fn<CancelClient>().mockReturnValue(new Promise((resolve) => { resolveCancel = resolve; }));
    const snapshot = vi.fn<SnapshotClient>().mockResolvedValue(active);
    const events = vi.fn<EventsClient>().mockResolvedValue({ events: [] });
    const changed = vi.fn();
    const view = render(<JobDetail id={active.id} loadSnapshot={snapshot} loadEvents={events} cancel={cancel} onChanged={changed} />);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel job" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm cancellation" }));
    view.rerender(<JobDetail id="job-2" loadSnapshot={snapshot} loadEvents={events} cancel={cancel} onChanged={changed} />);
    expect(cancel.mock.calls[0][1]?.aborted).toBe(true);
    await act(async () => { resolveCancel(canceled); });
    expect(changed).not.toHaveBeenCalled();
    expect(screen.queryByText(/Cancellation confirmed/)).toBeNull();
  });
});
