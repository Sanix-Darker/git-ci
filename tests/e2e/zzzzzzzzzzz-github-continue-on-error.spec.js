import { expect, test } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

const token = () => readFileSync("build/e2e-web/state/admin.token", "utf8").trim();

test("GitHub continue-on-error is evaluated per immutable matrix cell @responsive", async ({ page }, testInfo) => {
  const suffix = testInfo.project.name.toLowerCase().replace(/[^a-z0-9]+/g, "-");
  const slug = `continue-error-${suffix}`;
  const repository = `${process.cwd()}/build/e2e-web/projects/${slug}`;
  const workflowDirectory = `${repository}/.github/workflows`;
  mkdirSync(workflowDirectory, { recursive: true });
  writeFileSync(`${workflowDirectory}/continue.yml`, `name: Matrix Continue Error
on: [workflow_dispatch]
jobs:
  matrix:
    runs-on: ubuntu-latest
    continue-on-error: \${{ matrix.experimental }}
    strategy:
      fail-fast: false
      matrix:
        experimental: [false, true]
    steps:
      - name: Fail cell
        run: exit 17
`);
  if (!existsSync(`${repository}/.git`)) {
    execFileSync("git", ["-C", repository, "init", "-q", "-b", "main"]);
  }
  execFileSync("git", ["-C", repository, "config", "user.name", "gci e2e"]);
  execFileSync("git", ["-C", repository, "config", "user.email", "gci-e2e@localhost"]);
  execFileSync("git", ["-C", repository, "add", ".github/workflows/continue.yml"]);
  execFileSync("git", ["-C", repository, "commit", "-q", "--allow-empty", "-m", "GitHub continue-on-error fixture"]);

  const authorization = { Authorization: `Bearer ${token()}` };
  const projectsResponse = await page.request.get("/api/v1/projects", { headers: authorization });
  const projectsPayload = await projectsResponse.json();
  let project = projectsPayload.items.find((item) => item.slug === slug);
  if (!project) {
    const createResponse = await page.request.post("/api/v1/projects", {
      headers: authorization,
      data: { slug, name: `Continue error ${suffix}`, path: repository },
    });
    expect(createResponse.status()).toBe(201);
    project = await createResponse.json();
  }

  const syncResponse = await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers: authorization });
  expect(syncResponse.ok()).toBeTruthy();
  const syncPayload = await syncResponse.json();
  expect(syncPayload.items).toHaveLength(1);
  const workflow = syncPayload.items[0];
  expect(workflow.definition.jobs).toHaveLength(2);
  const previewStable = workflow.definition.jobs.find((job) => job.matrix?.experimental === "false");
  const previewExperimental = workflow.definition.jobs.find((job) => job.matrix?.experimental === "true");
  expect(previewStable.allowFailure).toBe(false);
  expect(previewExperimental.allowFailure).toBe(true);

  const runResponse = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, {
    headers: authorization,
    data: { ref: "main" },
  });
  expect(runResponse.status()).toBe(202);
  const queued = await runResponse.json();
  let detail;
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/runs/${queued.id}`, { headers: authorization });
    detail = await response.json();
    return detail.run.status;
  }, { timeout: 15000 }).toBe("failed");

  expect(detail.jobs).toHaveLength(2);
  const runtimeStable = detail.jobs.find(({ job }) => job.environment.MATRIX_EXPERIMENTAL === "false");
  const runtimeExperimental = detail.jobs.find(({ job }) => job.environment.MATRIX_EXPERIMENTAL === "true");
  expect(runtimeStable.job.status).toBe("failed");
  expect(runtimeStable.job.allowFailure).toBe(false);
  expect(runtimeExperimental.job.status).toBe("failed");
  expect(runtimeExperimental.job.allowFailure).toBe(true);

  const loginResponse = await page.request.post("/api/v1/session/login", { data: { token: token() } });
  expect(loginResponse.ok()).toBeTruthy();
  await page.goto(`/app/runs/${queued.id}`);
  const graph = page.getByLabel("Pipeline dependency graph");
  await expect(graph).toContainText("02 NODES / 00 EDGES");
  const stableNode = graph.locator("article.run-node").filter({ hasText: "EXPERIMENTAL=false" });
  const experimentalNode = graph.locator("article.run-node").filter({ hasText: "EXPERIMENTAL=true" });
  await expect(stableNode).toContainText("FAILED");
  await expect(stableNode).not.toContainText("ALLOW FAILURE");
  await expect(experimentalNode).toContainText("FAILED");
  await expect(experimentalNode).toContainText("ALLOW FAILURE");
  await expect(page.locator("article.job-detail").filter({ hasText: "EXPERIMENTAL=true" })).toContainText("ALLOWED FAILURE");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
