import { expect, test } from "@playwright/test";

type Job = { id: string; state: string };

test("operator inspects and cancels a durable running job", async ({ page, request }) => {
  const key = `playwright-${Date.now()}`;
  const submitted = await request.post("/v1/jobs", {
    headers: { "Idempotency-Key": key },
    data: {
      kind: "demo.checksum",
      payload: "playwright-cancellation",
      delay_ms: 5_000,
      max_attempts: 1,
      timeout_ms: 10_000,
    },
  });
  expect(submitted.status()).toBe(202);
  const job = (await submitted.json()) as Job;

  await expect.poll(async () => {
    const response = await request.get(`/v1/jobs/${job.id}`);
    expect(response.ok()).toBeTruthy();
    return ((await response.json()) as Job).state;
  }, { timeout: 10_000, message: "worker should claim the seeded job" }).toBe("running");

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Jobs" })).toBeVisible();
  await page.getByRole("button", { name: "Running", exact: true }).click();
  await page.getByRole("button", { name: job.id }).click();

  const detail = page.getByRole("region", { name: `Details for ${job.id}` });
  await expect(detail.getByRole("heading", { name: "Job detail" })).toBeVisible();
  await expect(detail.getByRole("region", { name: "Job snapshot" })).toContainText("running");
  await expect(detail.getByRole("region", { name: "Lifecycle history" })).toContainText("claimed");
  await expect(detail.getByRole("region", { name: "Lifecycle history" })).toContainText("e2e-worker");

  await detail.getByRole("button", { name: "Cancel job" }).click();
  await expect(detail).toContainText("Cancellation is cooperative");
  await detail.getByRole("button", { name: "Confirm cancellation" }).click();

  await expect(detail.getByText("Cancellation confirmed. Reloading current state and history.")).toBeVisible();
  await expect(detail.getByRole("region", { name: "Job snapshot" })).toContainText("canceled");
  await expect(detail.getByRole("region", { name: "Lifecycle history" })).toContainText("canceled");

  const persisted = await request.get(`/v1/jobs/${job.id}`);
  expect(persisted.ok()).toBeTruthy();
  expect(((await persisted.json()) as Job).state).toBe("canceled");
});
