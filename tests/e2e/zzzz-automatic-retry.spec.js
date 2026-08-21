const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("GitLab automatic retry is durable and visible in the live run graph @responsive", async ({ page }) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", (message) => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", (error) => browserErrors.push(error.message));
  const root = path.join(process.cwd(), "build/e2e-web/projects/retry-service");
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, ".gitlab-ci.yml"), `retry-visible:\n  retry:\n    max: 2\n    when: script_failure\n    exit_codes: [17]\n  script:\n    - if test -f .gci-retried; then printf recovered; else touch .gci-retried; exit 17; fi\n`);
  execFileSync("git", ["init", "-b", "main"], { cwd: root });
  execFileSync("git", ["config", "user.email", "e2e@gci.invalid"], { cwd: root });
  execFileSync("git", ["config", "user.name", "gci e2e"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "retry fixture"], { cwd: root });
  const headers = { Authorization: `Bearer ${token()}` };
  const projects = await (await page.request.get("/api/v1/projects", { headers })).json();
  let project = projects.items.find((item) => item.slug === "retry-service");
  if (!project) {
    project = await (await page.request.post("/api/v1/projects", { headers, data: { slug: "retry-service", name: "Retry service", path: root } })).json();
  }
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  const workflow = workflows.items.find((item) => item.definition && item.definition.provider === "gitlab");
  expect(workflow).toBeTruthy();
  const queued = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, { headers, data: { ref: "main" } });
  expect(queued.status()).toBe(202);
  const run = await queued.json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("succeeded");
  const graph = await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json();
  const job = graph.jobs.find((item) => item.job.key === "retry-visible").job;
  expect(job.attempts).toHaveLength(2);
  expect(job.attempts.map((item) => item.status)).toEqual(["failed", "succeeded"]);
  expect(job.attempts[0]).toMatchObject({ attemptNumber: 1, failureKind: "script_failure", exitCode: 17, willRetry: true });
  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/runs/${run.id}`);
  const node = page.locator(".run-node").filter({ has: page.locator("code", { hasText: /^retry-visible$/ }) });
  await expect(node).toContainText("2 ATTEMPTS");
  const card = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "retry-visible", exact: true }) });
  await expect(card).toContainText("ATTEMPT 1");
  await expect(card).toContainText("ATTEMPT 2");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  expect(browserErrors).toEqual([]);
});

