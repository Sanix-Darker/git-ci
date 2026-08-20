const fs = require("node:fs/promises");
const path = require("node:path");
const { test, expect } = require("@playwright/test");

const tokenPath = path.join(process.cwd(), "build/e2e-web/state/admin.token");

test("@responsive public page presents the CLI and self-hosted service", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveTitle(/git-ci/);
  await expect(page.getByRole("heading", { level: 1 })).toContainText("RUN CI");
  await expect(page.getByText("CLI FIRST.")).toBeVisible();
  await expect(page.getByRole("link", { name: /OPERATOR LOGIN/ })).toHaveAttribute("href", "/login");
  expect(await page.locator("body").evaluate((node) => getComputedStyle(node).backgroundImage)).toBe("none");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("operator uses HTMX login, navigation, project registration, persistence, and logout", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: /YOUR CI/ })).toBeVisible();

  await page.getByLabel("Token").fill("invalid-token");
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await expect(page.getByRole("status")).toContainText("not valid");
  await expect(page).toHaveURL(/\/login$/);

  const token = (await fs.readFile(tokenPath, "utf8")).trim();
  await page.getByLabel("Token").fill(token);
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await expect(page).toHaveURL(/\/app$/);
  await expect(page.getByRole("heading", { name: "DASHBOARD" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Control plane" })).toContainText("Jobs");

  await page.getByRole("link", { name: /Projects/ }).click();
  await expect(page).toHaveURL(/\/app\/projects$/);
  await expect(page.getByRole("heading", { name: "PROJECTS" })).toBeVisible();
  const alpha = page.locator("article.candidate", { hasText: "alpha-service" });
  await expect(alpha).toBeVisible();
  await alpha.getByRole("button", { name: /REGISTER/ }).click();
  await expect(page.locator(".project-rows")).toContainText("alpha-service");

  const beta = page.locator("article.candidate", { hasText: "beta-worker" });
  await expect(beta).toBeVisible();
  await beta.getByRole("button", { name: /REGISTER/ }).click();
  await expect(page.locator(".project-rows")).toContainText("beta-worker");

  await page.getByRole("link", { name: "Workflows" }).click();
  await expect(page).toHaveURL(/\/app\/workflows$/);
  await page.getByRole("button", { name: "SYNC ALPHA-SERVICE" }).click();
  await expect(page.locator("article.workflow-card", { hasText: "Alpha CI" })).toBeVisible();
  await page.getByRole("button", { name: "SYNC BETA-WORKER" }).click();
  await expect(page.locator("article.workflow-card", { hasText: ".gitlab-ci.yml" })).toBeVisible();
  await expect(page.getByText("GITHUB", { exact: true })).toBeVisible();
  await expect(page.getByText("GITLAB", { exact: true })).toBeVisible();

  await page.getByRole("link", { name: "Secrets" }).click();
  await expect(page).toHaveURL(/\/app\/secrets$/);
  await page.getByLabel("PROJECT").selectOption({ label: "alpha-service" });
  await page.getByLabel("NAME").fill("DEPLOY_TOKEN");
  await page.getByLabel("VALUE").fill("e2e-super-secret");
  await page.getByRole("button", { name: /ENCRYPT \+ STORE/ }).click();
  await expect(page.getByRole("status")).toContainText("AES-256-GCM");
  await expect(page.locator(".compact-list")).toContainText("DEPLOY_TOKEN");
  await expect(page.locator("body")).not.toContainText("e2e-super-secret");

  await page.getByRole("link", { name: "Workflows" }).click();

  const alphaWorkflow = page.locator("article.workflow-card", { hasText: "Alpha CI" });
  await alphaWorkflow.getByRole("button", { name: /RUN NOW/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\/[A-Za-z0-9_-]+$/);
  await expect(page.getByRole("heading", { name: "Alpha CI" })).toBeVisible();
  await expect(page.locator(".run-node").filter({ has: page.getByText("Prepare", { exact: true }) })).toBeVisible();
  await expect(page.locator(".run-node").filter({ has: page.getByText("Test", { exact: true }) })).toContainText("AFTER PREPARE");
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 15000 });
  await expect(page.getByLabel("Logs for Prepare")).toContainText("prepared");
  await expect(page.getByLabel("Logs for Test")).toContainText("tests passed");
  await expect(page.getByLabel("Logs for Secret mask")).toContainText("***");
  await expect(page.getByLabel("Logs for Secret mask")).not.toContainText("e2e-super-secret");

  await page.getByRole("link", { name: /Jobs/ }).click();
  await expect(page).toHaveURL(/\/app\/jobs$/);
  await expect(page.locator(".job-table")).toContainText("Prepare");
  await expect(page.locator(".job-table")).toContainText("Test");

  await page.getByRole("link", { name: "Schedules" }).click();
  await expect(page).toHaveURL(/\/app\/schedules$/);
  await page.getByLabel("WORKFLOW").selectOption({ label: "alpha-service / Alpha CI" });
  await page.getByLabel("CRON").fill("*/5 * * * *");
  await page.getByRole("button", { name: /CREATE \+ ENABLE/ }).click();
  await expect(page.getByRole("status")).toContainText("SCHEDULE ARMED");
  await expect(page.locator(".schedule-list")).toContainText("Alpha CI");
  await page.locator(".schedule-list").getByRole("button", { name: "PAUSE" }).click();
  await expect(page.getByRole("status")).toContainText("SCHEDULE UPDATED");

  await page.getByRole("link", { name: "Settings" }).click();
  await page.getByLabel("WORKFLOW").selectOption({ label: "alpha-service / Alpha CI" });
  await page.getByLabel("NAME").fill("github-push");
  await page.getByRole("button", { name: /CREATE ENDPOINT/ }).click();
  await expect(page.getByRole("status")).toContainText("WEBHOOK TOKEN");
  await expect(page.locator(".compact-list")).toContainText("github-push");

  await page.goto("/app/projects");
  await expect(page.locator(".project-rows")).toContainText("alpha-service");

  await page.getByRole("button", { name: "LOG OUT" }).click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByLabel("Token")).toBeVisible();
  const protectedResponse = await page.request.get("/api/v1/projects");
  expect(protectedResponse.status()).toBe(401);
});
