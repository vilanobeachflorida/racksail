import { defineConfig, devices } from "@playwright/test";

// The panel under test: RACKSAIL_E2E_URL for a running server, such as one
// the QEMU harness installed, or otherwise a racksail dev that Playwright
// starts on its own address and data directory, next to any development
// instance.
const external = process.env.RACKSAIL_E2E_URL;
const devAddr = process.env.RACKSAIL_E2E_ADDR ?? "127.0.0.1:7170";
const devData = process.env.RACKSAIL_E2E_DATA ?? ".dev-e2e";

// RACKSAIL_E2E_DEV tells the tests the panel is racksail dev, which signs
// everyone in. A panel at RACKSAIL_E2E_URL is an installed server unless
// whoever started it says otherwise; the workers inherit what is set here.
if (!external) {
  process.env.RACKSAIL_E2E_DEV = "1";
}

export default defineConfig({
  testDir: "./specs",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  timeout: 30_000,
  reporter: [["list"], ["html", { open: "never", outputFolder: process.env.RACKSAIL_E2E_REPORT ?? "playwright-report" }]],
  outputDir: process.env.RACKSAIL_E2E_RESULTS ?? "test-results",
  // The visual baselines, one directory per project, which the Screenshots
  // workflow writes on Linux.
  snapshotPathTemplate: "{testDir}/__screenshots__/{projectName}/{arg}{ext}",
  use: {
    baseURL: external ?? `http://${devAddr}`,
    ignoreHTTPSErrors: Boolean(process.env.RACKSAIL_E2E_INSECURE),
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"] } },
    { name: "phone", use: { ...devices["Pixel 7"] } },
  ],
  webServer: external
    ? undefined
    : {
        command: `go run ./cmd/racksail dev --addr ${devAddr} --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data ${devData}`,
        cwd: "../..",
        url: `http://${devAddr}/api/v1/health`,
        reuseExistingServer: !process.env.CI,
        timeout: 180_000,
        stdout: "ignore",
        stderr: "pipe",
      },
});
