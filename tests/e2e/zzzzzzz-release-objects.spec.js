const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("successful run becomes an artifact-linked published release @responsive", async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", message => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", error => browserErrors.push(error.message));

  const slug = `release-service-${testInfo.project.name}`;
  const root = path.join(process.cwd(), `build/e2e-web/projects/${slug}`);
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(path.join(root, ".github/workflows"), { recursive: true });
  fs.writeFileSync(path.join(root, ".github/workflows/release.yml"), `name: Release pipeline
on: workflow_dispatch
jobs:
  package:
    runs-on: self-hosted
    steps:
      - run: mkdir -p dist && printf 'release bundle' > dist/bundle.txt
      - uses: actions/upload-artifact@v4
        with:
          name: release-bundle
          path: dist/bundle.txt
`);
  execFileSync("git", ["init", "-b", "main"], { cwd: root });
  execFileSync("git", ["config", "user.email", "release@gci.invalid"], { cwd: root });
  execFileSync("git", ["config", "user.name", "gci release e2e"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "release candidate"], { cwd: root });
  const head = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  execFileSync("git", ["tag", "v3.0.0"], { cwd: root });

  const headers = { Authorization: `Bearer ${token()}` };
  const project = await (await page.request.post("/api/v1/projects", { headers, data: { slug, name: "Release service", path: root } })).json();
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  const workflow = workflows.items.find(item => item.name === "Release pipeline");
  expect(workflow).toBeTruthy();
  const queued = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, { headers, data: { ref: "refs/heads/main", commitSha: head } });
  expect(queued.status()).toBe(202);
  const run = await queued.json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${run.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("succeeded");
  const artifactPayload = await (await page.request.get(`/api/v1/runs/${run.id}/artifacts`, { headers })).json();
  expect(artifactPayload.count).toBe(1);
  const deploymentResponse = await page.request.post(`/api/v1/projects/${project.id}/deployments`, { headers, data: { runId: run.id, environment: "production" } });
  expect(deploymentResponse.status()).toBe(201);
  const deployment = await deploymentResponse.json();
  await page.request.patch(`/api/v1/deployments/${deployment.id}`, { headers, data: { status: "running" } });
  await page.request.patch(`/api/v1/deployments/${deployment.id}`, { headers, data: { status: "succeeded", reason: "release accepted" } });
  const created = await page.request.post(`/api/v1/projects/${project.id}/releases`, { headers, data: { runId: run.id, tagName: "v3.0.0", name: "Version 3", notes: "Initial release notes" } });
  expect(created.status()).toBe(201);
  const release = await created.json();

  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/releases/${release.id}`);
  await expect(page.getByRole("heading", { name: "Version 3" })).toBeVisible();
  await expect(page.locator(".release-links")).toContainText("release-bundle");
  await expect(page.locator(".release-links")).toContainText("production");
  await expect(page.locator(".release-facts")).toContainText(run.id.slice(0, 8));
  await page.locator("form.release-edit textarea[name=notes]").fill("Validated release notes");
  await page.locator("form.release-edit").getByRole("button", { name: /UPDATE DRAFT/ }).click();
  await expect(page.locator("form.release-edit textarea[name=notes]")).toHaveValue("Validated release notes");
  await page.locator(".release-actions details").first().locator("summary").click();
  await page.getByRole("button", { name: "CONFIRM PUBLISH" }).click();
  await expect(page.locator(".release-detail-head")).toContainText("PUBLISHED");
  await expect(page.locator(".release-notes")).toContainText("Validated release notes");
  await page.goto(`/app/releases?project=${project.id}&state=published&q=Version%203`);
  await expect(page.locator(".release-row").filter({ hasText: "Version 3" })).toContainText("PUBLISHED");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  expect(await page.locator(".release-row").first().evaluate(element => getComputedStyle(element).transitionDuration)).toBe("0s");
  expect(browserErrors).toEqual([]);
});
