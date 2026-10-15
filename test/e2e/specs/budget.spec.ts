import { gzipSync } from "node:zlib";
import { expect, test } from "@playwright/test";

// The shell, the code and styles the first screen loads, stays under 250 KB
// compressed (docs/architecture/conventions.md); module screens load lazily
// on top of it. Sizes are gzip at its highest level, whatever the server
// sends.
const budget = 250 * 1024;

test("the first screen loads within the bundle budget", async ({ page, baseURL }) => {
  test.skip(test.info().project.name !== "desktop", "the bundle is the same on every device");
  const origin = new URL(baseURL ?? "").origin;
  const loads: Promise<[string, number]>[] = [];
  page.on("response", (response) => {
    const type = response.request().resourceType();
    const url = new URL(response.url());
    if ((type === "script" || type === "stylesheet") && url.origin === origin && response.status() === 200) {
      loads.push(response.body().then((body) => [url.pathname, gzipSync(body, { level: 9 }).length]));
    }
  });
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const sizes = await Promise.all(loads);
  expect(sizes.length, "the first screen loaded no code").toBeGreaterThan(0);
  const total = sizes.reduce((sum, [, size]) => sum + size, 0);
  const report = sizes.map(([path, size]) => `${path}: ${(size / 1024).toFixed(1)} KiB`).join("\n");
  expect(total, `the first screen loads ${(total / 1024).toFixed(1)} KiB compressed:\n${report}`).toBeLessThan(budget);
});
