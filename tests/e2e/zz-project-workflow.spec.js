import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

const token = () => readFileSync("build/e2e-web/state/admin.token", "utf8").trim();

test("project workflow catalog exposes the pre-run DAG and explicit dispatch @responsive", async ({ page }) => {
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
  await pipeline.locator("summary").click();
  await expect(pipeline).toHaveAttribute("open", "");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("Prepare");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("AFTER PREPARE");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("Deploy");
  await expect(pipeline.locator('input[name="ref"]')).toHaveValue("main");
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

  const manual = page.locator("details.workflow-detail").filter({ hasText: "Failure CI" });
  await manual.locator("summary").click();
  await expect(pipeline).not.toHaveAttribute("open", "");
  await expect(manual.getByLabel(/TARGET/)).toHaveValue("staging");
  await manual.getByLabel(/TARGET/).selectOption("production");
  await expect(manual.getByLabel(/DRY-RUN/)).toHaveValue("true");
  await manual.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\//);

  await page.goto("/app/projects");
  const projectCard = page.locator("details.resource-card").filter({ hasText: "alpha-service" });
  await projectCard.locator("summary").click();
  const watcher = projectCard.locator("form.commit-trigger");
  await watcher.locator('input[name="enabled"]').check();
  await watcher.getByRole("button", { name: /SAVE COMMIT WATCH/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "COMMIT WATCH ENABLED" })).toBeVisible();

  const repository = `${process.cwd()}/build/e2e-web/projects/alpha-service`;
  execFileSync("git", ["-C", repository, "commit", "--allow-empty", "-m", "E2E watched commit"]);
  await expect.poll(async () => {
    const response = await page.request.get(`/api/v1/projects/${project.id}/runs`, { headers: authorization });
    const payload = await response.json();
    return payload.items.filter((run) => run.triggerType === "commit").length;
  }, { timeout: 15000 }).toBeGreaterThan(0);
});
