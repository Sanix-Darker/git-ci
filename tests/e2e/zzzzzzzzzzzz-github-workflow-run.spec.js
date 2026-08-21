import { expect, test } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

const token = () => readFileSync("build/e2e-web/state/admin.token", "utf8").trim();

test("GitHub workflow_run links failed CI to conclusion-aware CD @responsive", async ({ page }, testInfo) => {
  const suffix = testInfo.project.name.toLowerCase().replace(/[^a-z0-9]+/g, "-");
  const slug = `workflow-run-${suffix}`;
  const repository = `${process.cwd()}/build/e2e-web/projects/${slug}`;
  const workflowDirectory = `${repository}/.github/workflows`;
  const marker = `${repository}/failure-cd.marker`;
  mkdirSync(workflowDirectory, { recursive: true });
  rmSync(marker, { force: true });
  writeFileSync(`${workflowDirectory}/ci.yml`, `name: CI Gate
on: [workflow_dispatch]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: exit 23
`);
  writeFileSync(`${workflowDirectory}/cd.yml`, `name: Failure Delivery
on:
  workflow_run:
    workflows: [CI Gate]
    types: [completed]
    branches: [main]
jobs:
  deploy:
    if: github.event.workflow_run.conclusion == 'failure'
    runs-on: ubuntu-latest
    steps:
      - run: printf failure-cd > ${marker}
`);
  if (!existsSync(`${repository}/.git`)) {
    execFileSync("git", ["-C", repository, "init", "-q", "-b", "main"]);
  }
  execFileSync("git", ["-C", repository, "config", "user.name", "gci e2e"]);
  execFileSync("git", ["-C", repository, "config", "user.email", "gci-e2e@localhost"]);
  execFileSync("git", ["-C", repository, "add", ".github/workflows"]);
  execFileSync("git", ["-C", repository, "commit", "-q", "--allow-empty", "-m", "GitHub workflow_run fixture"]);

  const authorization = { Authorization: `Bearer ${token()}` };
  const createResponse = await page.request.post("/api/v1/projects", {
    headers: authorization,
    data: { slug, name: `Workflow run ${suffix}`, path: repository },
  });
  expect(createResponse.status()).toBe(201);
  const project = await createResponse.json();
  const syncResponse = await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers: authorization });
  expect(syncResponse.ok()).toBeTruthy();
  const workflows = (await syncResponse.json()).items;
  expect(workflows).toHaveLength(2);
  const sourceWorkflow = workflows.find((workflow) => workflow.name === "CI Gate");
  const targetWorkflow = workflows.find((workflow) => workflow.name === "Failure Delivery");
  expect(targetWorkflow.definition.triggerPolicies[0].workflows).toEqual(["CI Gate"]);

  const sourceResponse = await page.request.post(`/api/v1/workflows/${sourceWorkflow.id}/runs`, {
    headers: authorization,
    data: { ref: "main" },
  });
  expect(sourceResponse.status()).toBe(202);
  const source = await sourceResponse.json();
  let downstream;
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/projects/${project.id}/runs`, { headers: authorization });
    const runs = (await response.json()).items;
    downstream = runs.find((run) => run.triggerType === "workflow_run");
    return downstream?.status;
  }, { timeout: 20000 }).toBe("succeeded");
  expect(readFileSync(marker, "utf8")).toBe("failure-cd");

  const graphResponse = await page.request.get(`/api/v1/runs/${downstream.id}`, { headers: authorization });
  const graph = await graphResponse.json();
  expect(graph.workflowRun.sourceRunId).toBe(source.id);
  expect(graph.workflowRun.sourceWorkflowName).toBe("CI Gate");
  expect(graph.workflowRun.sourceConclusion).toBe("failure");
  expect(graph.workflowRun.depth).toBe(1);
  expect(graph.run.commitSha).toBe(source.commitSha);

  const loginResponse = await page.request.post("/api/v1/session/login", { data: { token: token() } });
  expect(loginResponse.ok()).toBeTruthy();
  await page.goto(`/app/projects/${project.id}`);
  const target = page.locator("details.workflow-detail").filter({ hasText: "Failure Delivery" });
  await target.locator("summary").click();
  const contract = target.getByLabel("Workflow trigger policies");
  await expect(contract).toContainText("AFTER WORKFLOW CI Gate");
  await expect(contract).toContainText("ACTION COMPLETED");
  await expect(target.getByLabel("Pipeline dependency graph")).toContainText("01 NODES / 00 EDGES");

  await page.goto(`/app/runs/${downstream.id}`);
  const provenance = page.getByLabel("Run provenance");
  await expect(provenance).toContainText("WORKFLOW RUN");
  await expect(provenance).toContainText("AFTER WORKFLOW / CI Gate");
  await expect(provenance).toContainText("FAILURE / DEPTH 1");
  await expect(provenance.getByRole("link", { name: /SOURCE RUN/ })).toHaveAttribute("href", `/app/runs/${source.id}`);
  await expect(provenance.getByRole("link", { name: "API V1" })).toHaveAttribute("href", `/api/v1/runs/${downstream.id}`);
  await expect(page.getByLabel("Pipeline dependency graph")).toContainText("01 NODES / 00 EDGES");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
