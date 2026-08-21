const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("GitLab optional needs remain visible and admit a skipped dependency @responsive", async ({ page }) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", message => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", error => browserErrors.push(error.message));
  const root = path.join(process.cwd(), "build/e2e-web/projects/optional-needs-service");
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, ".gitlab-ci.yml"), `skipped:\n  rules:\n    - if: '$CI_COMMIT_BRANCH == "feature"'\n  script: ['printf skipped']\nconsumer:\n  needs:\n    - job: skipped\n      optional: true\n  script: ['printf consumer']\n`);
  execFileSync("git", ["init", "-b", "main"], { cwd: root });
  execFileSync("git", ["config", "user.email", "e2e@gci.invalid"], { cwd: root });
  execFileSync("git", ["config", "user.name", "gci e2e"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "optional needs fixture"], { cwd: root });
  const headers = { Authorization: `Bearer ${token()}` };
  const projects = await (await page.request.get("/api/v1/projects", { headers })).json();
  let project = projects.items.find(item => item.slug === "optional-needs-service");
  if (!project) project = await (await page.request.post("/api/v1/projects", { headers, data: { slug: "optional-needs-service", name: "Optional needs service", path: root } })).json();
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  const workflow = workflows.items.find(item => item.definition && item.definition.provider === "gitlab");
  expect(workflow).toBeTruthy();
  const consumer = workflow.definition.jobs.find(item => item.key === "consumer");
  expect(consumer.needsOptional).toEqual({ skipped: true });

  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/workflows?project=${project.id}`);
  const workflowCard = page.locator(`#workflow-${workflow.id}`);
  await workflowCard.locator("summary").click();
  const parsedNode = workflowCard.locator(".run-node").filter({ has: page.locator("code", { hasText: /^consumer$/ }) });
  await expect(parsedNode).toHaveClass(/optional-edge/);
  await expect(parsedNode).toContainText("OPTIONAL NEED SKIPPED");

  const queued = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, { headers, data: { ref: "main" } });
  expect(queued.status()).toBe(202);
  const run = await queued.json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("succeeded");
  const graph = await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json();
  expect(graph.jobs.find(item => item.job.key === "skipped").job.status).toBe("skipped");
  expect(graph.jobs.find(item => item.job.key === "consumer").job.status).toBe("succeeded");
  await page.goto(`/app/runs/${run.id}`);
  const liveNode = page.locator(".run-node").filter({ has: page.locator("code", { hasText: /^consumer$/ }) });
  await expect(liveNode).toHaveClass(/optional-edge/);
  await expect(liveNode).toContainText("OPTIONAL NEED SKIPPED");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  expect(browserErrors).toEqual([]);
});
