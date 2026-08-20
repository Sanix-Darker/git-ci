import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";

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
  const pipeline = page.locator("details.workflow-detail").filter({ hasText: "Alpha CI" });
  await pipeline.locator("summary").click();
  await expect(pipeline).toHaveAttribute("open", "");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("Prepare");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("AFTER PREPARE");
  await expect(pipeline.getByLabel("Pipeline dependency graph")).toContainText("Deploy");
  await expect(pipeline.locator('input[name="ref"]')).toHaveValue("main");
  await expect(pipeline.locator('input[name="commitSha"]')).toBeVisible();
  await expect(pipeline.getByRole("button", { name: /RUN WORKFLOW/ })).toBeVisible();
});
