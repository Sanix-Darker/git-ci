const { defineConfig, devices } = require("@playwright/test");

module.exports = defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? [["line"], ["html", { open: "never" }]] : "line",
  use: {
    baseURL: "http://127.0.0.1:18089",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    { name: "desktop-chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium", grep: /@responsive/, use: { ...devices["Pixel 7"] } },
  ],
  webServer: {
    command: "bash scripts/start-web-e2e.sh",
    url: "http://127.0.0.1:18089/healthz",
    reuseExistingServer: false,
    timeout: 120000,
  },
});
