import { expect, test } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

const token = () => readFileSync("build/e2e-web/state/admin.token", "utf8").trim();

test("GitHub cache restore and save split actions expose cache-hit outputs @responsive", async ({ page }, testInfo) => {
  const suffix = testInfo.project.name.toLowerCase().replace(/[^a-z0-9]+/g, "-");
  const slug = `cache-split-${suffix}`;
  const repository = `${process.cwd()}/build/e2e-web/projects/${slug}`;
  const workflowDirectory = `${repository}/.github/workflows`;
  const restored = `${repository}/restored.marker`;
  rmSync(repository, { recursive: true, force: true });
  mkdirSync(workflowDirectory, { recursive: true });
  writeFileSync(`${workflowDirectory}/cache.yml`, `name: Cache Split
on: [workflow_dispatch]
jobs:
  seed:
    runs-on: ubuntu-latest
    steps:
      - run: mkdir -p deps && printf seed > deps/value.txt
      - id: save
        uses: actions/cache/save@v4
        with:
          path: deps
          key: split-${suffix}
  restore:
    needs: seed
    runs-on: ubuntu-latest
    steps:
      - run: rm -rf deps
      - id: restore-cache
        uses: actions/cache/restore@v4
        with:
          path: deps
          key: split-${suffix}
      - if: steps.restore-cache.outputs.cache-hit != 'true'
        run: exit 99
      - run: test "\${{ steps.restore-cache.outputs.cache-primary-key }}" = split-${suffix}
      - run: test "\${{ steps.restore-cache.outputs.cache-matched-key }}" = split-${suffix}
      - run: printf "%s" "$(cat deps/value.txt)" > ${restored}
`);
  if (!existsSync(`${repository}/.git`)) {
    execFileSync("git", ["-C", repository, "init", "-q", "-b", "main"]);
  }
  execFileSync("git", ["-C", repository, "config", "user.name", "gci e2e"]);
  execFileSync("git", ["-C", repository, "config", "user.email", "gci-e2e@localhost"]);
  execFileSync("git", ["-C", repository, "add", ".github/workflows/cache.yml"]);
  execFileSync("git", ["-C", repository, "commit", "-q", "--allow-empty", "-m", "GitHub cache split fixture"]);

  const authorization = { Authorization: `Bearer ${token()}` };
  const createResponse = await page.request.post("/api/v1/projects", {
    headers: authorization,
    data: { slug, name: `Cache split ${suffix}`, path: repository },
  });
  expect(createResponse.status()).toBe(201);
  const project = await createResponse.json();
  const syncResponse = await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers: authorization });
  expect(syncResponse.ok()).toBeTruthy();
  const workflows = (await syncResponse.json()).items;
  const workflow = workflows.find((item) => item.name === "Cache Split");
  expect(workflow).toBeTruthy();

  const runResponse = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, {
    headers: authorization,
    data: { ref: "main" },
  });
  expect(runResponse.status()).toBe(202);
  const run = await runResponse.json();
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/runs/${run.id}`, { headers: authorization });
    return (await response.json()).run.status;
  }, { timeout: 30000 }).toBe("succeeded");
  expect(readFileSync(restored, "utf8")).toBe("seed");

  const graph = await (await page.request.get(`/api/v1/runs/${run.id}`, { headers: authorization })).json();
  const restore = graph.jobs.find((item) => item.job.key === "restore");
  expect(restore.job.status).toBe("succeeded");
  expect(restore.steps.find((step) => step.command === "exit 99").status).toBe("skipped");
  const caches = await (await page.request.get(`/api/v1/projects/${project.id}/caches`, { headers: authorization })).json();
  expect(caches.items.some((entry) => entry.key === `split-${suffix}`)).toBeTruthy();

  const loginResponse = await page.request.post("/api/v1/session/login", { data: { token: token() } });
  expect(loginResponse.ok()).toBeTruthy();
  await page.goto(`/app/projects/${project.id}`);
  const catalog = page.locator("details.workflow-detail").filter({ hasText: "Cache Split" });
  await catalog.locator("summary").click();
  await expect(catalog.getByLabel("Pipeline dependency graph")).toContainText("02 NODES / 01 EDGES");
  await page.goto(`/app/runs/${run.id}`);
  await expect(page.getByLabel("Pipeline dependency graph")).toContainText("02 NODES / 01 EDGES");
  await expect(page.locator(".run-node").filter({ hasText: "restore" })).toContainText("SUCCEEDED");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
