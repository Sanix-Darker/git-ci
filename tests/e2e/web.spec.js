const fs = require("node:fs/promises");
const path = require("node:path");
const { test, expect } = require("@playwright/test");

const tokenPath = path.join(process.cwd(), "build/e2e-web/state/admin.token");
const browserErrors = new WeakMap();

async function ensureCompletedRun(page, adminToken) {
  const headers = { Authorization: `Bearer ${adminToken}` };
  const projectsResponse = await page.request.get("/api/v1/projects", { headers });
  const projectsPayload = await projectsResponse.json();
  let project = projectsPayload.items.find((item) => item.slug === "alpha-service");
  if (!project) {
    const created = await page.request.post("/api/v1/projects", {
      headers,
      data: {
        slug: "alpha-service",
        name: "Alpha service",
        path: `${process.cwd()}/build/e2e-web/projects/alpha-service`,
      },
    });
    project = await created.json();
  }
  await page.request.post(`/api/v1/projects/${project.id}/workflows/sync`, { headers });
  let runs = await (await page.request.get(`/api/v1/projects/${project.id}/runs`, { headers })).json();
  if (!runs.items.length) {
    const workflows = await (await page.request.get(`/api/v1/projects/${project.id}/workflows`, { headers })).json();
    const workflow = workflows.items.find((item) => item.name === "Alpha CI");
    const queued = await page.request.post(`/api/v1/workflows/${workflow.id}/runs`, {
      headers,
      data: { ref: "main" },
    });
    const run = await queued.json();
    await expect.poll(async () => {
      const response = await page.request.get(`/api/v1/runs/${run.id}`, { headers });
      return (await response.json()).run.status;
    }, { timeout: 30_000 }).toBe("succeeded");
  }
}

test.beforeEach(async ({ page }) => {
  const errors = [];
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(`console: ${message.text()}`);
  });
  page.on("pageerror", (error) => errors.push(`page: ${error.message}`));
  browserErrors.set(page, errors);
});

test.afterEach(async ({ page }) => {
  expect(browserErrors.get(page)).toEqual([]);
});

test("@responsive public page presents the CLI and self-hosted service", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveTitle(/git-ci/);
  await expect(page.getByRole("heading", { level: 1 })).toContainText("RUN CI");
  await expect(page.getByText(/CI\/CD alternative to GitHub Actions and GitLab CI/)).toBeVisible();
  await expect(page.getByText("CLI FIRST.")).toBeVisible();
  await expect(page.getByRole("link", { name: /OPERATOR LOGIN/ })).toHaveAttribute("href", "/login");
  expect(await page.locator("body").evaluate((node) => getComputedStyle(node).backgroundImage)).toBe("none");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("operator uses HTMX login, navigation, project registration, persistence, and logout", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: /YOUR CI/ })).toBeVisible();

  await page.getByLabel("Token").fill("invalid-token");
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "not valid" })).toBeVisible();
  await expect(page).toHaveURL(/\/login$/);

  const token = (await fs.readFile(tokenPath, "utf8")).trim();
  await page.getByLabel("Token").fill(token);
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await expect(page).toHaveURL(/\/app$/);
  await expect(page.getByRole("heading", { name: "DASHBOARD" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Control plane" })).toContainText("Jobs");
  await expect(page.getByLabel("Run volume histogram")).toBeVisible();

  await page.route("**/app/projects", async (route) => {
    if (route.request().method() === "GET") await new Promise((resolve) => setTimeout(resolve, 240));
    await route.continue();
  });
  await page.getByRole("link", { name: /Projects/ }).click();
  await expect(page.locator("#app-frame")).toHaveClass(/is-loading/);
  await expect(page).toHaveURL(/\/app\/projects$/);
  await page.unroute("**/app/projects");
  await expect(page.getByRole("heading", { name: "PROJECTS" })).toBeVisible();
  const projectSearch = page.getByRole("combobox", { name: "SEARCH CHECKOUTS" });
  await expect(projectSearch).toHaveAttribute("list", "project-suggestions");
  await projectSearch.fill("beta-worker");
  const betaCandidate = page.locator("[data-project-candidate]", { hasText: "beta-worker" });
  if (await betaCandidate.count()) {
    await expect(page.locator("[data-project-candidate]", { hasText: "alpha-service" })).toBeHidden();
    await expect(betaCandidate).toBeVisible();
  }
  await projectSearch.fill("");
  const alpha = page.locator("article.candidate", { hasText: "alpha-service" });
  if (await alpha.count()) {
    await expect(alpha).toBeVisible();
    await alpha.getByRole("button", { name: /REGISTER/ }).click();
    await expect(page.locator("[data-project-candidate]", { hasText: "alpha-service" })).toHaveCount(0);
    await expect(page.getByRole("status").filter({ hasText: "PROJECT REGISTERED" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Dismiss notification" })).toBeVisible();
  }
  await expect(page.locator(".project-rows")).toContainText("alpha-service");

  const beta = page.locator("article.candidate", { hasText: "beta-worker" });
  if (await beta.count()) {
    await expect(beta).toBeVisible();
    await beta.getByRole("button", { name: /REGISTER/ }).click();
  }
  await expect(page.locator(".project-rows")).toContainText("beta-worker");

  await page.getByRole("link", { name: "Workflows" }).click();
  await expect(page).toHaveURL(/\/app\/workflows$/);
  await page.getByRole("button", { name: "SYNC ALPHA-SERVICE" }).click();
  await expect(page.locator("details.workflow-detail", { hasText: "Alpha CI" })).toBeVisible();
  await page.getByRole("button", { name: "SYNC BETA-WORKER" }).click();
  await expect(page.locator("details.workflow-detail", { hasText: ".gitlab-ci.yml" })).toBeVisible();
  await expect(page.getByText("GITHUB", { exact: true })).toHaveCount(8);
  await expect(page.getByText("GITLAB", { exact: true })).toBeVisible();

  await page.getByRole("link", { name: "Secrets" }).click();
  await expect(page).toHaveURL(/\/app\/secrets$/);
  await expect(page.getByRole("heading", { name: "SECRETS", exact: true })).toBeVisible({ timeout: 15_000 });
  await page.getByLabel("PROJECT").selectOption({ label: "alpha-service" });
  await page.getByLabel("NAME").fill("DEPLOY_TOKEN");
  await page.getByLabel("VALUE").fill("e2e-super-secret");
  await page.getByRole("button", { name: /ENCRYPT \+ STORE/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "AES-256-GCM" })).toBeVisible();
  await expect(page.locator(".compact-list")).toContainText("DEPLOY_TOKEN");
  await expect(page.locator("body")).not.toContainText("e2e-super-secret");

  await page.getByRole("link", { name: "Workflows" }).click();

  const alphaWorkflow = page.locator("details.workflow-detail", { hasText: "Alpha CI" });
  await alphaWorkflow.locator("summary").click();
  await alphaWorkflow.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\/[A-Za-z0-9_-]+$/);
  await expect(page.getByRole("heading", { name: "Alpha CI" })).toBeVisible();
  await expect(page.locator(".run-node").filter({ has: page.getByText("Prepare", { exact: true }) })).toBeVisible();
  await expect(page.locator(".run-node").filter({ has: page.getByText("Test", { exact: true }) })).toContainText("AFTER PREPARE");
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 15000 });
  const outputs = page.getByLabel("Run outputs");
  await expect(outputs).toContainText("alpha-build");
  await expect(outputs.getByRole("link", { name: /alpha-build/ })).toHaveAttribute("href", /\/artifacts\//);
  const runID = page.url().split("/").pop();
  const authorization = { Authorization: `Bearer ${token}` };
  const artifactResponse = await page.request.get(`/api/v1/runs/${runID}/artifacts`, { headers: authorization });
  expect(artifactResponse.ok()).toBeTruthy();
  const artifactPayload = await artifactResponse.json();
  expect(artifactPayload.count).toBe(1);
  expect(artifactPayload.items[0].sha256).toHaveLength(64);
  const downloadResponse = await page.request.get(`/api/v1/runs/${runID}/artifacts/${artifactPayload.items[0].id}`, { headers: authorization });
  expect(downloadResponse.ok()).toBeTruthy();
  expect(downloadResponse.headers()["content-type"]).toContain("application/zip");
  const cacheResponse = await page.request.get(`/api/v1/projects/${artifactPayload.items[0].projectId}/caches`, { headers: authorization });
  expect(cacheResponse.ok()).toBeTruthy();
  expect((await cacheResponse.json()).count).toBeGreaterThan(0);
  await expect(page.locator(".pipeline-stage")).toHaveCount(3);
  const prepareLogs = page.getByLabel("Logs for Prepare");
  const testLogs = page.getByLabel("Logs for Test");
  const secretLogs = page.getByLabel("Logs for Secret mask");
	const testSummary = page.getByLabel("Step summary for Test");
	const secretSummary = page.getByLabel("Step summary for Secret mask");
	await expect(testSummary).toContainText("Test summary");
	await expect(testSummary).toContainText("artifact: alpha-build");
	await expect(secretSummary).toContainText("secret=***");
	await expect(secretSummary).toContainText("dynamic=***");
	await expect(secretSummary).not.toContainText("***-runtime");
	await expect(secretSummary).not.toContainText("e2e-super-secret");
  await prepareLogs.scrollIntoViewIfNeeded();
  await expect(prepareLogs).toContainText("prepared");
  await testLogs.scrollIntoViewIfNeeded();
  await expect(testLogs).toContainText("tests passed");
  await secretLogs.scrollIntoViewIfNeeded();
  await expect(secretLogs).toContainText("***");
	await expect(secretLogs).toContainText("dynamic=***");
	await expect(secretLogs).not.toContainText("***-runtime");
  await expect(secretLogs).not.toContainText("e2e-super-secret");
	const logGroups = secretLogs.locator(".log-group");
	await expect(logGroups).toHaveCount(2);
	await expect(logGroups.nth(0).locator("summary")).toContainText("Runtime diagnostics");
	await expect(logGroups.nth(0)).toHaveAttribute("open", "");
	await expect(logGroups.nth(0)).toContainText("github group payload");
	await expect(logGroups.nth(1).locator("summary")).toContainText("GitLab setup");
	await expect(logGroups.nth(1)).not.toHaveAttribute("open", "");
	await logGroups.nth(1).locator("summary").click();
	await expect(logGroups.nth(1)).toContainText("gitlab section payload");
	await expect(secretLogs).not.toContainText("section_start");
	await expect(secretLogs).not.toContainText("::group::");
	const annotations = page.getByLabel("Annotations for Secret mask");
	await expect(annotations.locator(".step-annotation")).toHaveCount(3);
	await expect(annotations).toContainText("Compile hint");
	await expect(annotations).toContainText("src/app.go:12:4");
	await expect(annotations).toContainText("masked ***");
	await expect(annotations).toContainText("real warning");
	await expect(annotations).toContainText("diagnostic error");
	await expect(annotations).not.toContainText("ignored warning");
	await expect(annotations).not.toContainText("***-runtime");
	const graphResponse = await page.request.get(`/api/v1/runs/${runID}`, { headers: authorization });
	expect(graphResponse.ok()).toBeTruthy();
	const graphPayload = await graphResponse.json();
	const summarizedSteps = graphPayload.jobs.flatMap((job) => job.steps).filter((step) => step.summary);
	expect(summarizedSteps.some((step) => step.summary.includes("# Test summary"))).toBeTruthy();
	const secretStep = graphPayload.jobs.flatMap((job) => job.steps).find((step) => step.name === "Secret mask");
	expect(secretStep.annotations).toHaveLength(3);
	expect(secretStep.summary).toContain("dynamic=***");
	expect(JSON.stringify(secretStep.annotations)).not.toContain("***-runtime");
	expect(JSON.stringify(graphPayload)).not.toContain("e2e-super-secret");
  const sourceRunURL = page.url();
  const testJob = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "Test", exact: true }) });
  await testJob.getByRole("button", { name: "REPLAY STEP" }).first().click();
  await expect(testJob.getByRole("button", { name: "CONFIRM REPLAY STEP" }).first()).toBeVisible();
  await testJob.getByRole("button", { name: "CONFIRM REPLAY STEP" }).first().click();
  await expect(page).toHaveURL(/\/app\/runs\/[A-Za-z0-9_-]+\?notice=REPLAY(?:\+|%20)QUEUED$/);
  await expect(page.getByRole("status").filter({ hasText: "REPLAY QUEUED" })).toBeVisible();
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 15000 });
  await expect(page.getByLabel("Run provenance")).toContainText("STEP REPLAY");
  await expect(page.getByLabel("Run provenance")).toContainText("SOURCE RUN");
  await expect(page.locator("article.job-detail")).toHaveCount(1);
  await expect(page.locator(".step-detail")).toHaveCount(1);
  await page.goto(sourceRunURL);
  const replayJob = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "Test", exact: true }) });
  await replayJob.getByRole("button", { name: "REPLAY JOB" }).click();
  await replayJob.getByRole("button", { name: "CONFIRM REPLAY JOB" }).click();
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED", { timeout: 15000 });
  await expect(page.locator("article.job-detail")).toHaveCount(2);
  await expect(page.getByLabel("Run provenance")).toContainText("JOB REPLAY");

  await page.getByRole("link", { name: "Workflows" }).click();
  const failureWorkflow = page.locator("details.workflow-detail", { hasText: "Failure CI" });
  await failureWorkflow.locator("summary").click();
  await failureWorkflow.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page.locator(".run-detail-state")).toContainText("FAILED", { timeout: 15000 });
  const failedRunURL = page.url();
  const failedJob = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "Fail", exact: true }) });
  await failedJob.getByRole("button", { name: "REPLAY STEP" }).click();
  await expect(page.locator(".run-detail-state")).toContainText("FAILED", { timeout: 15000 });
  await expect(page.getByLabel("Run provenance")).toContainText("STEP REPLAY");
  await page.goto(failedRunURL);
  const failedJobAgain = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "Fail", exact: true }) });
  await failedJobAgain.getByRole("button", { name: "REPLAY JOB" }).click();
  await expect(page.locator(".run-detail-state")).toContainText("FAILED", { timeout: 15000 });
  await expect(page.getByLabel("Run provenance")).toContainText("JOB REPLAY");
  await page.getByRole("link", { name: /Runs/ }).click();
  await expect(page.getByRole("group", { name: "Time range" })).toBeVisible();
  await page.getByRole("button", { name: "ALL", exact: true }).click();
  await expect(page).toHaveURL(/range=all/);
  await expect(page.getByLabel("Run duration histogram")).toBeVisible();

  await page.getByRole("link", { name: /Jobs/ }).click();
  await expect(page).toHaveURL(/\/app\/jobs$/);
  await expect(page.locator(".job-table")).toContainText("Prepare");
  await expect(page.locator(".job-table")).toContainText("Test");

  await page.getByRole("link", { name: "Deployments" }).click();
  await expect(page.getByRole("heading", { name: "APPROVAL QUEUE" })).toBeVisible();
  const policyForm = page.locator("form.environment-policy-form");
  await policyForm.getByLabel("POLICY PROJECT").selectOption({ label: "alpha-service" });
  await policyForm.getByLabel("ENVIRONMENT NAME").fill("production");
  await policyForm.getByRole("button", { name: /STORE POLICY/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "ENVIRONMENT POLICY STORED" })).toBeVisible();
  await expect(page.locator(".environment-cards")).toContainText("PRODUCTION");
  const environmentSecretForm = page.locator('form[action="/app/environment-secrets"]');
  await environmentSecretForm.getByLabel("SECRET ENVIRONMENT").selectOption({ label: "alpha-service / production" });
  await environmentSecretForm.getByLabel("SECRET NAME").fill("DEPLOY_TOKEN");
  await environmentSecretForm.getByLabel("SECRET VALUE").fill("environment-e2e-secret");
  await environmentSecretForm.getByRole("button", { name: /ENCRYPT \+ SCOPE/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "ENVIRONMENT SECRET STORED" })).toBeVisible();
  await expect(page.locator(".secret-scope-grid")).toContainText("DEPLOY_TOKEN");
  await expect(page.locator("body")).not.toContainText("environment-e2e-secret");

  await page.getByRole("link", { name: "Workflows" }).click();
  await alphaWorkflow.locator("summary").click();
  await alphaWorkflow.getByRole("button", { name: /RUN WORKFLOW/ }).click();
  await expect(page.locator(".run-detail-state")).toContainText("WAITING", { timeout: 15000 });
  await page.getByRole("link", { name: "Deployments" }).click();
  const approval = page.locator("article.approval-card").filter({ has: page.getByRole("heading", { name: "Deploy", exact: true }) });
  await expect(approval).toBeVisible();
  await approval.getByLabel("DECISION REASON").fill("e2e release window");
  await approval.getByRole("button", { name: "APPROVE" }).click();
  await expect(page.getByRole("status").filter({ hasText: "DEPLOYMENT APPROVED" })).toBeVisible();
  await expect(page.locator("article.approval-card")).toHaveCount(0);
  const latestDeployment = page.locator(".deployment-table .data-row").first();
  await expect(latestDeployment).toContainText("SUCCEEDED", { timeout: 15000 });
  await latestDeployment.click();
  await expect(page.locator(".run-detail-state")).toContainText("SUCCEEDED");
  const deployLogs = page.getByLabel("Logs for Deploy production");
  await deployLogs.scrollIntoViewIfNeeded();
  await expect(deployLogs).toContainText("deployed ***");
  await expect(deployLogs).not.toContainText("environment-e2e-secret");

  const deploymentJob = page.locator("article.job-detail").filter({ has: page.getByRole("heading", { name: "Deploy", exact: true }) });
  await deploymentJob.getByRole("button", { name: "REPLAY JOB" }).click();
  await expect(deploymentJob.locator(".replay-confirm[open] .replay-confirm-panel").getByText(/DEPLOYMENT APPROVAL REQUIRED/)).toBeVisible();
  await deploymentJob.getByRole("button", { name: "CONFIRM REPLAY JOB" }).click();
  await expect(page.locator(".run-detail-state")).toContainText("WAITING", { timeout: 15000 });
  await page.getByRole("link", { name: "Deployments" }).click();
  const replayApproval = page.locator("article.approval-card").filter({ has: page.getByRole("heading", { name: "Deploy", exact: true }) });
  await replayApproval.getByLabel("DECISION REASON").fill("e2e replay gate");
  await replayApproval.getByRole("button", { name: "APPROVE" }).click();
  const replayDeployment = page.locator(".deployment-table .data-row").first();
  await expect(replayDeployment).toContainText("SUCCEEDED", { timeout: 15000 });
  await replayDeployment.click();
  await expect(page.getByLabel("Run provenance")).toContainText("JOB REPLAY");

  await page.getByRole("link", { name: "Deployments" }).click();
  const rollbackRecord = page.locator(".deployment-record").first();
  await expect(rollbackRecord.getByRole("button", { name: /ROLL BACK/ })).toBeVisible();
  await rollbackRecord.getByRole("button", { name: /ROLL BACK/ }).click();
  await expect(page).toHaveURL(/\/app\/runs\/[A-Za-z0-9_-]+$/);
  await expect(page.locator(".run-detail-state")).toContainText("WAITING", { timeout: 15000 });
  await page.getByRole("link", { name: "Deployments" }).click();
  const rollbackApproval = page.locator("article.approval-card").filter({ has: page.getByRole("heading", { name: "Deploy", exact: true }) });
  await rollbackApproval.getByLabel("DECISION REASON").fill("e2e rollback window");
  await rollbackApproval.getByRole("button", { name: "APPROVE" }).click();
  const rollbackDeployment = page.locator(".deployment-table .data-row").first();
  await expect(rollbackDeployment).toContainText("SUCCEEDED", { timeout: 15000 });
  await rollbackDeployment.click();
  const rollbackLogs = page.getByLabel("Logs for Rollback production");
  await rollbackLogs.scrollIntoViewIfNeeded();
  await expect(rollbackLogs).toContainText("rolled back");

  await page.getByRole("link", { name: "Schedules" }).click();
  await expect(page).toHaveURL(/\/app\/schedules$/);
  await page.getByLabel("WORKFLOW").selectOption({ label: "alpha-service / Alpha CI" });
  await page.getByLabel("CRON").fill("*/5 * * * *");
  await page.getByRole("button", { name: /CREATE \+ ENABLE/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "SCHEDULE ARMED" })).toBeVisible();
  await expect(page.locator(".schedule-list")).toContainText("Alpha CI");
  await page.locator(".schedule-list").getByRole("button", { name: "PAUSE" }).click();
  await expect(page.getByRole("status").filter({ hasText: "SCHEDULE UPDATED" })).toBeVisible();

  await page.getByRole("link", { name: "Runners" }).click();
  await expect(page).toHaveURL(/\/app\/runners$/);
  await expect(page.getByRole("heading", { name: "RUNNERS" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "LOCAL RUNNER" })).toBeVisible();
  await expect(page.locator("[data-runner-card]")).toContainText("ONLINE / SERIAL / CAPACITY 1");
  const runnerResponse = await page.request.get("/api/v1/runners");
  expect(runnerResponse.status()).toBe(200);
  expect((await runnerResponse.json()).count).toBe(1);
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page.getByRole("heading", { name: "EMAIL ALERTS" })).toBeVisible();
  await page.getByLabel("RECIPIENTS").fill("ops@example.com");
  await expect(page.getByText("UI PREVIEW / DELIVERY NOT ACTIVE")).toBeVisible();
  await expect(page.getByRole("button", { name: "DELIVERY NOT ENABLED" })).toBeDisabled();
  const navFontSize = await page.locator(".main-nav a").first().evaluate((element) => Number.parseFloat(getComputedStyle(element).fontSize));
  expect(navFontSize).toBeGreaterThanOrEqual(10);
  await page.getByLabel("WORKFLOW").selectOption({ label: "alpha-service / Alpha CI" });
  await page.getByLabel("NAME").fill("github-push");
  await page.getByRole("button", { name: /CREATE ENDPOINT/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "WEBHOOK TOKEN" })).toBeVisible();
  await expect(page.locator(".configuration-grid .compact-list")).toContainText("github-push");

  await page.goto("/app/projects");
  await expect(page.locator(".project-rows")).toContainText("alpha-service");
  const projectsBeforeLifecycle = await (await page.request.get("/api/v1/projects", { headers: authorization })).json();
  const betaProject = projectsBeforeLifecycle.items.find((item) => item.slug === "beta-worker");
  expect(betaProject).toBeTruthy();
  await page.goto(`/app/projects/${betaProject.id}`);
  await expect(page.getByRole("heading", { level: 2, name: "beta-worker", exact: true })).toBeVisible();
  const unregisterForm = page.locator("form.project-unregister");
  await unregisterForm.getByLabel("CONFIRM PROJECT SLUG").fill("beta-worker");
  await unregisterForm.getByRole("button", { name: /UNREGISTER PROJECT/ }).click();
  await expect(page).toHaveURL(/\/app\/projects\?notice=PROJECT(?:\+|%20)UNREGISTERED$/);
  await expect(page.getByRole("status").filter({ hasText: "PROJECT UNREGISTERED" })).toBeVisible();
  await expect(page.locator(".project-rows")).not.toContainText("beta-worker");
  const inactiveProjects = await (await page.request.get("/api/v1/projects?state=inactive", { headers: authorization })).json();
  expect(inactiveProjects.items.find((item) => item.id === betaProject.id)?.active).toBe(false);
  const returnedCandidate = page.locator("article.candidate", { hasText: "beta-worker" });
  await expect(returnedCandidate).toBeVisible();
  await returnedCandidate.getByRole("button", { name: /REGISTER/ }).click();
  await expect(page.getByRole("status").filter({ hasText: "PROJECT REGISTERED" })).toBeVisible();
  await expect(page.locator(".project-rows")).toContainText("beta-worker");
  const projectsAfterLifecycle = await (await page.request.get("/api/v1/projects", { headers: authorization })).json();
  const reactivatedBeta = projectsAfterLifecycle.items.find((item) => item.slug === "beta-worker");
  expect(reactivatedBeta.id).toBe(betaProject.id);
  expect(reactivatedBeta.active).toBe(true);

  const logout = page.getByRole("button", { name: "LOG OUT" });
  await expect(logout).toBeInViewport();
  await logout.click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByLabel("Token")).toBeVisible();
  const protectedResponse = await page.request.get("/api/v1/projects");
  expect(protectedResponse.status()).toBe(401);
});

test("@responsive operator surfaces preserve padding, mobile records, and reduced motion", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/login");
  const token = (await fs.readFile(tokenPath, "utf8")).trim();
  await page.getByLabel("Token").fill(token);
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await expect(page.getByRole("heading", { name: "DASHBOARD" })).toBeVisible();
  await ensureCompletedRun(page, token);
  const layout = await page.evaluate(() => {
    const workspace = getComputedStyle(document.querySelector(".workspace"));
    const animated = getComputedStyle(document.querySelector(".histogram-fill"));
    return {
      paddingLeft: parseFloat(workspace.paddingLeft),
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      animationDuration: animated.animationDuration,
      gradient: getComputedStyle(document.body).backgroundImage,
    };
  });
  expect(layout.paddingLeft).toBeGreaterThanOrEqual(16);
  expect(layout.overflow).toBeLessThanOrEqual(0);
  expect(layout.gradient).toBe("none");
  expect(["0s", "0.001s", "1ms"]).toContain(layout.animationDuration);
  for (const [name, heading] of [["Runners", "RUNNERS"], ["Settings", "SETTINGS"]]) {
    await page.getByRole("link", { name }).click();
    await expect(page.getByRole("heading", { name: heading })).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow, `${name} should not overflow the mobile viewport`).toBeLessThanOrEqual(0);
  }
  await page.locator('.main-nav a[href="/app/runs"]').click();
  const firstRun = page.locator(".run-table .data-row").first();
  await firstRun.click();
  const playSize = await page.locator(".play-control button, .replay-confirm > summary").first().evaluate((element) => {
    const style = getComputedStyle(element);
    return { width: parseFloat(style.width), height: parseFloat(style.height), transition: style.transitionDuration };
  });
  expect(playSize.width).toBeGreaterThanOrEqual(44);
  expect(playSize.height).toBeGreaterThanOrEqual(44);
  expect(["0s", "0.001s", "1ms"]).toContain(playSize.transition);
});

test("@responsive compatibility center exposes honest provider filters", async ({ page }) => {
  await page.goto("/login");
  const token = (await fs.readFile(tokenPath, "utf8")).trim();
  await page.getByLabel("Token").fill(token);
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await page.getByRole("link", { name: /Compatibility/ }).click();
  await expect(page).toHaveURL(/\/app\/compatibility$/);
  await expect(page.getByRole("heading", { name: "COMPATIBILITY", exact: true })).toBeVisible();
  await expect(page.getByLabel("Compatibility summary")).toContainText("SUPPORTED");
  const apiResponse = await page.request.get("/api/v1/compatibility?provider=github&state=partial&q=actions");
  expect(apiResponse.status()).toBe(200);
  const apiReport = await apiResponse.json();
  expect(apiReport.count).toBeGreaterThan(0);
  const filters = page.locator("form.compatibility-filters");
  await filters.getByLabel("PROVIDER").selectOption("github");
  await filters.getByLabel("STATE").selectOption("partial");
  await filters.getByLabel("SEARCH").fill("actions");
  await filters.getByRole("button", { name: /APPLY FILTERS/ }).click();
  await expect(page).toHaveURL(/provider=github/);
  const entries = page.locator(".compatibility-entry");
  await expect(entries.first()).toBeVisible();
  expect(await entries.count()).toBe(apiReport.count);
  for (const entry of await entries.all()) {
    await expect(entry).toHaveAttribute("data-provider", "github");
    await expect(entry).toHaveAttribute("data-state", "partial");
  }
  const layout = await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth - innerWidth, gradient: getComputedStyle(document.body).backgroundImage }));
  expect(layout.overflow).toBeLessThanOrEqual(0);
  expect(layout.gradient).toBe("none");
});

test("@responsive audit ledger filters immutable events and renders time buckets", async ({ page }) => {
  await page.goto("/login");
  const token = (await fs.readFile(tokenPath, "utf8")).trim();
  await page.getByLabel("Token").fill(token);
  await page.getByRole("button", { name: /ENTER CONTROL PLANE/ }).click();
  await page.getByRole("link", { name: /Audit/ }).click();
  await expect(page).toHaveURL(/\/app\/audit$/);
  await expect(page.getByRole("heading", { name: "AUDIT", exact: true })).toBeVisible();
  await expect(page.getByLabel("Audit event histogram")).toBeVisible();
  const apiResponse = await page.request.get("/api/v1/audit?range=24h&q=session.login&limit=100");
  expect(apiResponse.status()).toBe(200);
  const apiReport = await apiResponse.json();
  expect(apiReport.total).toBeGreaterThan(0);
  expect(apiReport.buckets).toHaveLength(12);
  const filters = page.locator("form.audit-filters");
  await filters.getByLabel("SEARCH").fill("session.login");
  await filters.getByRole("button", { name: /APPLY FILTERS/ }).click();
  await expect(page).toHaveURL(/q=session\.login/);
  const events = page.locator(".audit-event");
  await expect(events.first()).toBeVisible();
  expect(await events.count()).toBe(apiReport.count);
  await expect(events.first()).toHaveAttribute("data-action", "session.login");
  await events.first().locator("summary").click();
  await expect(events.first().locator("pre")).toBeVisible();
  const layout = await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth - innerWidth, gradient: getComputedStyle(document.body).backgroundImage }));
  expect(layout.overflow).toBeLessThanOrEqual(0);
  expect(layout.gradient).toBe("none");
});
