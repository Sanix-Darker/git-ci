const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("blocking GitLab manual job plays from the live DAG and resumes the same run @responsive", async ({ page }) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", (message) => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", (error) => browserErrors.push(error.message));
  const headers = { Authorization: `Bearer ${token()}` };
  const projects = await (await page.request.get("/api/v1/projects", { headers })).json();
  let project = projects.items.find((item) => item.slug === "manual-service");
  if (!project) {
    project = await (await page.request.post("/api/v1/projects", { headers, data: { slug: "manual-service", name: "Manual service", path: `${process.cwd()}/build/e2e-web/projects/manual-service` } })).json();
  }
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  const workflow = workflows.items.find((item) => item.definition && item.definition.provider === "gitlab");
  expect(workflow).toBeTruthy();
  const queued = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, { headers, data: { ref: "main" } });
  const queuedBody = await queued.text();
  expect(queued.status(), queuedBody).toBe(202);
  const run = JSON.parse(queuedBody);
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("waiting");
  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/runs/${run.id}`);
  await expect(page.locator(".run-detail-state")).toContainText("WAITING");
  const releaseNode = page.locator(".run-node").filter({ has: page.locator("code", { hasText: /^manual-release$/ }) });
  await expect(releaseNode).toContainText("MANUAL");
  await expect(releaseNode).toContainText("CONFIRM PLAY");
  const releaseJob = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "manual-release", exact: true }) });
  await releaseJob.getByRole("button", { name: "PLAY JOB" }).click();
  await expect(releaseJob.locator(".manual-play-panel")).toContainText("Release this commit to production?");
  await releaseJob.getByLabel("VARIABLES PLAIN / NOT SECRET").fill("RELEASE_NOTE=e2e");
  await releaseJob.getByRole("button", { name: "CONFIRM PLAY JOB" }).click();
  await expect(page.getByRole("status").filter({ hasText: "MANUAL JOB QUEUED" })).toBeVisible();
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 30_000 });
  await expect(releaseJob).toContainText("PLAYED BY admin");
  const logs = releaseJob.locator(".log-console");
  await logs.scrollIntoViewIfNeeded();
  await expect(logs).toContainText("release e2e");
  const graph = await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json();
  expect(graph.run.id).toBe(run.id);
  const release = graph.jobs.find((item) => item.job.key === "manual-release").job;
  expect(release.status).toBe("succeeded");
  expect(release.manualPlay.variables.RELEASE_NOTE).toBe("e2e");
  const audit = await (await page.request.get("/api/v1/audit?range=24h&q=job.played", { headers })).json();
  expect(audit.items.some((item) => item.action === "job.played" && item.resourceId === release.id)).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  expect(browserErrors).toEqual([]);
});
