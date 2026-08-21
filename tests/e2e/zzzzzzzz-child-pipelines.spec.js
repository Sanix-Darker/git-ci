const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { test, expect } = require("@playwright/test");

const token = () => fs.readFileSync(path.join(process.cwd(), "build/e2e-web/state/admin.token"), "utf8").trim();

test("mirrored GitLab child pipeline is attached to the parent DAG @responsive", async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  const browserErrors = [];
  page.on("console", message => { if (message.type() === "error") browserErrors.push(message.text()); });
  page.on("pageerror", error => browserErrors.push(error.message));
  const slug = `child-pipeline-${testInfo.project.name}`;
  const root = path.join(process.cwd(), `build/e2e-web/projects/${slug}`);
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(path.join(root, ".gci"), { recursive: true });
  fs.writeFileSync(path.join(root, ".gitlab-ci.yml"), `variables:
  ROOT_SIGNAL: inherited
prepare:
  script: ["printf prepare"]
service-tests:
  needs: [prepare]
  variables:
    SERVICE: api
  trigger:
    include:
      local: .gci/service.yml
    strategy: mirror
finish:
  needs: [service-tests]
  script: ["test ! -f child-proof.txt && printf parent-finished"]
`);
  fs.writeFileSync(path.join(root, ".gci/service.yml"), `verify:
  script:
    - test "$ROOT_SIGNAL" = inherited
    - test "$SERVICE" = api
    - printf child > child-proof.txt
`);
  execFileSync("git", ["init", "-b", "main"], { cwd: root });
  execFileSync("git", ["config", "user.email", "child@gci.invalid"], { cwd: root });
  execFileSync("git", ["config", "user.name", "gci child e2e"], { cwd: root });
  execFileSync("git", ["add", "."], { cwd: root });
  execFileSync("git", ["commit", "-m", "child pipeline"], { cwd: root });
  const head = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  const headers = { Authorization: `Bearer ${token()}` };
  const project = await (await page.request.post("/api/v1/projects", { headers, data: { slug, name: "Child pipeline service", path: root } })).json();
  const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
  expect(workflows.count).toBe(1);
  const queued = await page.request.post(`/api/v1/workflows/${workflows.items[0].id}/runs`, { headers, data: { ref: "refs/heads/main", commitSha: head } });
  expect(queued.status()).toBe(202);
  const parent = await queued.json();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/runs/${parent.id}`, { headers })).json()).run.status, { timeout: 30_000 }).toBe("succeeded");
  const graph = await (await page.request.get(`/api/v1/runs/${parent.id}`, { headers })).json();
  expect(graph.childPipelines).toMatchObject([{ sourceFile: ".gci/service.yml", strategy: "mirror", depth: 1, childStatus: "succeeded" }]);
  const childID = graph.childPipelines[0].childRunId;
  const child = await (await page.request.get(`/api/v1/runs/${childID}`, { headers })).json();
  expect(child.parentPipeline.parentRunId).toBe(parent.id);
  expect(child.run.commitSha).toBe(head);
  const roots = await (await page.request.get(`/api/v1/projects/${project.id}/runs`, { headers })).json();
  expect(roots.items.map(item => item.id)).toEqual([parent.id]);
  expect((await page.request.post("/api/v1/session/login", { data: { token: token() } })).ok()).toBeTruthy();
  await page.goto(`/app/runs/${parent.id}`);
  const downstream = page.locator(".downstream-card");
  await expect(downstream).toContainText("DOWNSTREAM / SUCCEEDED");
  await expect(downstream).toContainText(".gci/service.yml");
  await expect(downstream).toContainText("SAME COMMIT");
  await downstream.getByRole("link").click();
  await expect(page).toHaveURL(new RegExp(`/app/runs/${childID}$`));
  await expect(page.locator(".pipeline-upstream")).toContainText("UPSTREAM PIPELINE");
  await expect(page.locator(".pipeline-upstream")).toContainText(".gci/service.yml");
  await expect(page.locator(".run-facts")).toContainText(head.slice(0, 10));
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  expect(await page.locator(".pipeline-upstream a").evaluate(element => getComputedStyle(element).transitionDuration)).toBe("0s");
  expect(browserErrors).toEqual([]);
});
