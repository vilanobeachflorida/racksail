import { fileURLToPath } from "node:url";
import { defineConfig, devices } from "@playwright/test";
import { hiddenBelow } from "./hidden";

// The pseudo-locale suite: the panel's UI built with the pseudo-locales
// (pseudo/serve.mjs) in front of a racksail dev of its own, with addresses
// and a data directory apart from make dev's and make e2e-local's.
//
//   npx playwright test --config pseudo.config.ts
const devAddr = process.env.RACKSAIL_E2E_ADDR ?? "127.0.0.1:7270";
const devData = process.env.RACKSAIL_E2E_DATA ?? ".dev-e2e-pseudo";
const port = process.env.RACKSAIL_E2E_PSEUDO_PORT ?? "7271";

const pseudo = fileURLToPath(new URL("./pseudo", import.meta.url));

export default defineConfig({
  testDir: pseudo,
  testMatch: "*.spec.ts",
  testIgnore: hiddenBelow(pseudo),
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  timeout: 120_000,
  reporter: [["list"], ["html", { open: "never", outputFolder: process.env.RACKSAIL_E2E_REPORT ?? "playwright-report" }]],
  outputDir: process.env.RACKSAIL_E2E_RESULTS ?? "test-results",
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "en-XA desktop", use: { ...devices["Desktop Chrome"], locale: "en-XA" } },
    { name: "en-XA phone", use: { ...devices["Pixel 7"], locale: "en-XA" } },
    { name: "ar-XB desktop", use: { ...devices["Desktop Chrome"], locale: "ar-XB" } },
    { name: "ar-XB phone", use: { ...devices["Pixel 7"], locale: "ar-XB" } },
  ],
  webServer: [
    {
      command: `go run ./cmd/racksail dev --addr ${devAddr} --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data ${devData}`,
      cwd: "../..",
      url: `http://${devAddr}/api/v1/health`,
      reuseExistingServer: !process.env.CI,
      timeout: 180_000,
      stdout: "ignore",
      stderr: "pipe",
    },
    {
      command: `node pseudo/serve.mjs --port ${port}`,
      env: { RACKSAIL_DEV_ADDR: devAddr },
      url: `http://127.0.0.1:${port}/`,
      reuseExistingServer: !process.env.CI,
      timeout: 300_000,
      stdout: "pipe",
      stderr: "pipe",
    },
  ],
});
