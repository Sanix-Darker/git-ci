import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

const token = () => readFileSync("build/e2e-web/state/admin.token", "utf8").trim();

test("project workflow catalog exposes the pre-run DAG and explicit dispatch @responsive", async ({ page }, testInfo) => {
  const authorization = { Authorization: `Bearer ${token()}` };
  const projectsResponse = await page.request.get("/api/v1/projects", { headers: authorization });
  const projectsPayload = await projectsResponse.json();
  let project = projectsPayload.items.find((item) => item.slug === "alpha-service");
  if (!project) {
    const createResponse = await page.request.post("/api/v1/projects", {
      headers: authorization,
      data: { slug: "alpha-service", name: "Alpha service", path: `${process.cwd()}/build/e2e-web/projects/alpha-service` },
    });
    project = await createResponse.json();
  }
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers: authorization });
  const loginResponse = await page.request.post("/api/v1/session/login", { data: { token: token() } });
  expect(loginResponse.ok()).toBeTruthy();

  await page.goto("/app/workflows");
  const flow = page.getByLabel("GCI use-case flow");
  await expect(flow).toContainText("CHECKOUT TO CI/CD");
  await expect(flow).toContainText("REGISTER");
  await expect(flow).toContainText("INSPECT");
  await expect(flow).toContainText("RUN");
  await expect(flow).toContainText("AUTOMATE");
  await expect(flow).toContainText("DELIVER");
  const skipLink = page.getByRole("link", { name: "Skip to workspace" });
  const hiddenSkipBox = await skipLink.boundingBox();
  expect(hiddenSkipBox.y + hiddenSkipBox.height).toBeLessThanOrEqual(0);
  await page.keyboard.press("Tab");
  await expect(skipLink).toBeFocused();
  await expect(skipLink).toHaveCSS("top", "0px");
  const focusedSkipBox = await skipLink.boundingBox();
  expect(focusedSkipBox.y).toBeGreaterThanOrEqual(0);
  await page.keyboard.press("Tab");
  const pipeline = page.locator("details.workflow-detail").filter({ hasText: "Alpha CI" });
  await expect(pipeline.locator("summary")).toContainText("VIEW DAG + RUN");
  await expect(pipeline.locator("summary")).toContainText("DAG");
  await pipeline.locator("summary").click();
  await expect(pipeline).toHaveAttribute("open", "");
  await expect(pipeline.getByLabel("Workflow use-case flow")).toContainText("PRE-RUN DAG");
  await expect(pipeline.getByLabel("Workflow use-case flow")).toContainText("PARSED POLICY");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("Prepare");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("AFTER PREPARE");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("Deploy");
  await expect(pipeline.getByLabel("REF")).toHaveValue("main");
  await expect(pipeline.locator('input[name="commitSha"]')).toBeVisible();
  await expect(pipeline.getByRole("button", { name: /RUN WORKFLOW/ })).toBeVisible();

  const matrix = page.locator("details.workflow-detail").filter({ hasText: "Matrix Preview" });
  await matrix.locator("summary").click();
  await expect(pipeline).not.toHaveAttribute("open", "");
  const matrixGraph = matrix.getByLabel("Pipeline dependency graph");
  await expect(matrixGraph).toContainText("DEPENDENCY DAG");
  await expect(matrixGraph).toContainText("03 NODES / 02 EDGES");
  await expect(matrixGraph).toContainText("MATRIX 01/02");
  await expect(matrixGraph).toContainText("OS=linux");
  await expect(matrixGraph).toContainText("IF matrix.os != 'blocked'");
  await expect(matrix).toContainText("LOCK preview-${{ github.ref }} / CANCEL OLD");

  const runtime = page.locator("details.workflow-detail").filter({ hasText: "Runtime Topology" });
  await runtime.locator("summary").click();
  const runtimeGraph = runtime.getByLabel("Pipeline dependency graph");
  await expect(runtimeGraph).toContainText("CONTAINER alpine:3.20");
  await expect(runtimeGraph).toContainText("SERVICE redis = redis:7-alpine");
  await expect(runtimeGraph).toContainText("01 NODES / 00 EDGES");

  const reusable = page.locator("details.workflow-detail").filter({ hasText: "Reusable Delivery" });
  await reusable.locator("summary").click();
  const reusableGraph = reusable.getByLabel("Pipeline dependency graph");
  await expect(reusableGraph).toContainText("04 NODES / 03 EDGES");
  await expect(reusableGraph).toContainText("Shared / Compile");
  await expect(reusableGraph).toContainText("REUSE ./.github/workflows/shared.yml");
  await expect(reusableGraph.locator(".run-node").filter({ hasText: "Publish" })).toContainText("AFTER SHARED/AUDIT");
  await reusable.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\//);
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 15000 });
  await page.goto("/app/workflows");

  const composite = page.locator("details.workflow-detail").filter({ hasText: "Composite Delivery" });
  await composite.locator("summary").click();
  await expect(composite).toContainText("Local check / Prepare input");
  await expect(composite).toContainText("Local check / Verify input");
  await composite.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\//);
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 15000 });
  await page.goto("/app/workflows");

  const gpu = page.locator("details.workflow-detail").filter({ hasText: "GPU Delivery" });
  await gpu.locator("summary").click();
  await expect(gpu.getByLabel("Pipeline dependency graph")).toContainText("NO RUNNER");
  await expect(gpu).toContainText("MISSING GPU");
  await expect(gpu.getByRole("button", { name: "RUNNER REQUIRED" })).toBeDisabled();
  const gpuWorkflowID = (await gpu.getAttribute("id")).replace("workflow-", "");
  const csrfToken = await gpu.locator('input[name="_csrf"]').inputValue();
  const rejected = await page.request.post(`/api/v1/workflows/${gpuWorkflowID}/runs`, {
    data: { ref: "main" },
    headers: { "X-CSRF-Token": csrfToken },
  });
  expect(rejected.status()).toBe(409);
  expect((await rejected.json()).error.code).toBe("runner_unavailable");

  const manual = page.locator("details.workflow-detail").filter({ hasText: "Failure CI" });
  await manual.locator("summary").click();
  await expect(pipeline).not.toHaveAttribute("open", "");
  await expect(manual.getByLabel(/TARGET/)).toHaveValue("staging");
  await manual.getByLabel(/TARGET/).selectOption("production");
  await expect(manual.getByLabel(/DRY-RUN/)).toHaveValue("true");
  await manual.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\//);

  await page.goto("/app/projects");
  await expect(page.getByLabel("GCI use-case flow")).toContainText("Pick a local repo");
  const activeSearch = page.getByLabel("SEARCH REGISTERED");
  await activeSearch.fill("no-such-registered-project");
  await expect(page.getByText("NO REGISTERED PROJECT MATCHES")).toBeVisible();
  await activeSearch.fill("alpha-service");
  const projectCard = page.locator("details.resource-card").filter({ hasText: "alpha-service" });
  await expect(projectCard).toBeVisible();
  await projectCard.locator("summary").click();
  await projectCard.getByRole("link", { name: /OPEN PROJECT/ }).click();
  await expect(page).toHaveURL(/\/app\/projects\/[A-Za-z0-9_-]+$/);
  await expect(page.getByLabel("GCI use-case flow")).toHaveCount(1);
  await expect(page.getByLabel("GCI use-case flow")).toContainText("CHECKOUT TO CI/CD");
  const workspace = page.getByLabel("Project workspace");
  await expect(workspace).toContainText("alpha-service");
  await expect(workspace).toContainText("LOCAL COMMIT WATCH");
  const workspaceWorkflow = page.locator("details.workflow-detail").filter({ hasText: "Alpha CI" });
  await workspaceWorkflow.locator("summary").click();
  await expect(workspaceWorkflow.getByLabel("Pipeline dependency graph")).toContainText("AFTER PREPARE");
  await expect(workspaceWorkflow.getByLabel("Workflow trigger policies")).toContainText("CRON 31 5 * * *");
  await expect(workspaceWorkflow.getByRole("button", { name: /RUN WORKFLOW/ })).toBeVisible();
  await workspaceWorkflow.getByRole("button", { name: /ARM DECLARED CRON 31 5 \* \* \*/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "SCHEDULE ARMED" })).toBeVisible();
  await expect(page).toHaveURL(/\/app\/projects\/[A-Za-z0-9_-]+$/);
  const automation = page.getByLabel("Project automation");
  await expect(automation).toContainText("04 SOURCES");
  await expect(automation.locator(".schedule-list")).toContainText("31 5 * * *");
  const scheduleForm = automation.locator("form.schedule-form");
  const scheduleWorkflow = await scheduleForm.locator("option").filter({ hasText: "Alpha CI" }).getAttribute("value");
  await scheduleForm.getByLabel("WORKFLOW").selectOption(scheduleWorkflow);
  await scheduleForm.getByLabel("CRON").fill("17 * * * *");
  await scheduleForm.getByRole("button", { name: /CREATE \+ ENABLE/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "SCHEDULE ARMED" })).toBeVisible();
  await expect(page).toHaveURL(/\/app\/projects\/[A-Za-z0-9_-]+$/);
  await expect(automation.locator(".schedule-list")).toContainText("17 * * * *");
  const webhookForm = automation.locator("form.webhook-form");
  const webhookWorkflow = await webhookForm.locator("option").filter({ hasText: "Alpha CI" }).getAttribute("value");
  await webhookForm.getByLabel("WORKFLOW").selectOption(webhookWorkflow);
  const webhookName = `project-${testInfo.project.name}-push`;
  await webhookForm.getByLabel("NAME").fill(webhookName);
  await webhookForm.getByRole("button", { name: /CREATE ENDPOINT/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "WEBHOOK TOKEN" })).toBeVisible();
  await expect(page).toHaveURL(/\/app\/projects\/[A-Za-z0-9_-]+$/);
  await expect(automation).toContainText(webhookName);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const watcher = workspace.locator("form.commit-trigger");
  await watcher.locator('input[name="enabled"]').check();
  await watcher.getByRole("button", { name: /SAVE COMMIT WATCH/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "COMMIT WATCH ENABLED" })).toBeVisible();
  await expect(page).toHaveURL(/\/app\/projects\/[A-Za-z0-9_-]+$/);

  const repository = `${process.cwd()}/build/e2e-web/projects/alpha-service`;
  execFileSync("git", ["-C", repository, "commit", "--allow-empty", "-m", "E2E watched commit"]);
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/projects/${project.id}/runs`, { headers: authorization });
    const payload = await response.json();
    return payload.items.filter((run) => run.triggerType === "commit").length;
  }, { timeout: 15000 }).toBeGreaterThan(0);
});
