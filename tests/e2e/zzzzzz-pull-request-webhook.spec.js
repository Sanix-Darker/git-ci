const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("pull-request webhook admission connects the parsed catalog to the live DAG @responsive", async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", message => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", error => browserErrors.push(error.message));

  const root = path.join(process.cwd(), "build/e2e-web/projects/pull-request-service");
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(path.join(root, ".github/workflows"), { recursive: true });
  fs.writeFileSync(path.join(root, ".github/workflows/pull-request.yml"), `name: Pull request CI
on:
  pull_request:
    branches: [main]
    paths: ['src/**']
jobs:
  verify:
    runs-on: self-hosted
    steps:
      - run: test -f src/change.txt
`);
  fs.writeFileSync(path.join(root, "README.md"), "base\n");
  execFileSync("git", ["init", "-b", "main"], { cwd: root });
  execFileSync("git", ["config", "user.email", "pull-request@gci.invalid"], { cwd: root });
  execFileSync("git", ["config", "user.name", "gci pull-request e2e"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "base"], { cwd: root });
  const base = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  execFileSync("git", ["switch", "-c", "feature/webhook"], { cwd: root });
  fs.mkdirSync(path.join(root, "src"), { recursive: true });
  fs.writeFileSync(path.join(root, "src/change.txt"), "changed\n");
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "feature"], { cwd: root });
  const head = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();

  const headers = { Authorization: `Bearer ${token()}` };
  const projects = await (await page.request.get("/api/v1/projects", { headers })).json();
  let project = projects.items.find(item => item.slug === "pull-request-service");
  if (!project) {
    project = await (await page.request.post("/api/v1/projects", { headers, data: { slug: "pull-request-service", name: "Pull request service", path: root } })).json();
  }
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  const workflow = workflows.items.find(item => item.definition && item.definition.provider === "github" && item.name === "Pull request CI");
  expect(workflow).toBeTruthy();
  const endpoint = await (await page.request.post(`/api/v1/projects/${project.id}/webhooks`, { headers, data: { name: `pull-request-${testInfo.project.name}`, provider: "github", workflowId: workflow.id, ref: "refs/heads/main" } })).json();
  const deliveryHeaders = { "X-Git-CI-Token": endpoint.token, "X-GitHub-Event": "pull_request" };
  const payload = (action, target = "main") => ({ action, pull_request: { base: { ref: target, sha: base }, head: { ref: "feature/webhook", sha: head } } });

  const closed = await page.request.post(`/hooks/${endpoint.endpoint.id}`, { headers: { ...deliveryHeaders, "X-GitHub-Delivery": `closed-${testInfo.project.name}` }, data: payload("closed") });
  expect(closed.status()).toBe(202);
  expect((await closed.json()).run).toBeUndefined();
  const wrongTarget = await page.request.post(`/hooks/${endpoint.endpoint.id}`, { headers: { ...deliveryHeaders, "X-GitHub-Delivery": `target-${testInfo.project.name}` }, data: payload("opened", "develop") });
  expect(wrongTarget.status()).toBe(202);
  expect((await wrongTarget.json()).run).toBeUndefined();
  const accepted = await page.request.post(`/hooks/${endpoint.endpoint.id}`, { headers: { ...deliveryHeaders, "X-GitHub-Delivery": `accepted-${testInfo.project.name}` }, data: payload("opened") });
  expect(accepted.status()).toBe(202);
  const run = (await accepted.json()).run;
  expect(run.ref).toBe("refs/heads/feature/webhook");
  expect(run.commitSha).toBe(head);
  expect(run.triggerType).toBe("webhook");
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("succeeded");

  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/projects/${project.id}`);
  const catalog = page.locator("details.workflow-detail").filter({ hasText: "Pull request CI" });
  await catalog.locator("summary").click();
  await expect(catalog).toContainText("PULL REQUEST");
  await expect(catalog).toContainText("BRANCH main");
  await expect(catalog).toContainText("PATH src/**");
  await page.goto(`/app/runs/${run.id}`);
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED");
  await expect(page.locator(".run-node").filter({ hasText: "verify" })).toContainText("SUCCEEDED");
  await expect(page.locator("main")).toContainText("WEBHOOK");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  expect(browserErrors).toEqual([]);
});
