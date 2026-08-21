const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("GitLab regex rules select jobs in the parsed catalog and live DAG @responsive", async ({ page }) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", message => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", error => browserErrors.push(error.message));
  const root = path.join(process.cwd(), "build/e2e-web/projects/regex-service");
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, ".gitlab-ci.yml"), `variables:\n  BRANCH_PATTERN: '/^main$/'\nregex-match:\n  rules:\n    - if: '$CI_COMMIT_BRANCH =~ $BRANCH_PATTERN'\n  script: ['printf regex-match']\nregex-skip:\n  rules:\n    - if: '$CI_COMMIT_BRANCH !~ /^main$/'\n  script: ['printf should-not-run']\n`);
  execFileSync("git", ["init", "-b", "main"], { cwd: root });
  execFileSync("git", ["config", "user.email", "e2e@gci.invalid"], { cwd: root });
  execFileSync("git", ["config", "user.name", "gci e2e"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "regex fixture"], { cwd: root });
  const headers = { Authorization: `Bearer ${token()}` };
  const projects = await (await page.request.get("/api/v1/projects", { headers })).json();
  let project = projects.items.find(item => item.slug === "regex-service");
  if (!project) project = await (await page.request.post("/api/v1/projects", { headers, data: { slug: "regex-service", name: "Regex service", path: root } })).json();
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  const workflow = workflows.items.find(item => item.definition && item.definition.provider === "gitlab");
  expect(workflow).toBeTruthy();
  const queued = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, { headers, data: { ref: "main" } });
  expect(queued.status()).toBe(202);
  const run = await queued.json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("succeeded");
  const graph = await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json();
  expect(graph.jobs.find(item => item.job.key === "regex-match").job.status).toBe("succeeded");
  expect(graph.jobs.find(item => item.job.key === "regex-skip").job.status).toBe("skipped");
  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/runs/${run.id}`);
  await expect(page.locator(".run-node").filter({ has: page.locator("code", { hasText: /^regex-match$/ }) })).toContainText("SUCCEEDED");
  await expect(page.locator(".run-node").filter({ has: page.locator("code", { hasText: /^regex-skip$/ }) })).toContainText("SKIPPED");
  await expect(page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "regex-match", exact: true }) })).toContainText("1 RULES");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  expect(browserErrors).toEqual([]);
});

