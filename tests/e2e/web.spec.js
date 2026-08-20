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

  await page.getByRole("link", { name: "Workflows" }).click();
  await expect(page).toHaveURL(/\/app\/workflows$/);
  await expect(page.getByText("DOMAIN NOT ENABLED YET.")).toBeVisible();

  await page.goto("/app/projects");
  await expect(page.locator(".project-rows")).toContainText("alpha-service");

  await page.getByRole("button", { name: "LOG OUT" }).click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByLabel("Token")).toBeVisible();
  const protectedResponse = await page.request.get("/api/v1/projects");
  expect(protectedResponse.status()).toBe(401);
});
