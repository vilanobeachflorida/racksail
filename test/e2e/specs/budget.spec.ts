import { gzipSync } from "node:zlib";
import { expect, test } from "@playwright/test";

// The first screen, the shell and what the home screen loads with it, stays
// under 250 KB compressed (docs/architecture/conventions.md), as npm run
// build checks before the panel embeds it; module screens load lazily on top
// of it. Sizes are gzip at its highest level, whatever the server sends.
const budget = 250_000;

const kb = (bytes: number) => `${(bytes / 1000).toFixed(1)} KB`;

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
  const report = sizes.map(([path, size]) => `${path}: ${kb(size)}`).join("\n");
  expect(total, `the first screen loads ${kb(total)} compressed:\n${report}`).toBeLessThanOrEqual(budget);
});
