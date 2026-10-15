import { existsSync } from "node:fs";
import { dirname } from "node:path";
import { expect, type Page, test } from "@playwright/test";

// Every screen, on a desktop and on a phone, against screenshots CI took
// before: a change that moves, restyles or breaks what people see shows up
// as a difference to review. The baselines are rendered on Linux by the
// Screenshots workflow and committed in __screenshots__; RACKSAIL_E2E_VISUAL
// turns the comparison on, as CI does.
test.skip(!process.env.RACKSAIL_E2E_VISUAL, "visual comparisons run with RACKSAIL_E2E_VISUAL=1");
test.skip(process.platform !== "linux", "the baselines are rendered on Linux, where CI runs");
test.skip(!process.env.RACKSAIL_E2E_DEV, "the screens need racksail dev, which signs everyone in");

// What changes from build to build is pinned, so only the screens count.
async function steady(page: Page): Promise<void> {
  await page.clock.setFixedTime(new Date("2026-01-15T12:00:00Z"));
  await page.route("**/api/v1/version", async (route) => {
    const response = await route.fetch();
    const version = await response.json();
    await route.fulfill({
      response,
      json: { ...version, commit: "0".repeat(40), commitTime: "2026-01-15T00:00:00Z", goVersion: "go1.27.0", os: "linux", arch: "amd64" },
    });
  });
}

async function screens(page: Page): Promise<string[]> {
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const linked = await page
    .getByRole("navigation")
    .locator("a[href^='/']")
    .evaluateAll((links) => links.map((a) => new URL((a as HTMLAnchorElement).href).pathname));
  return [...new Set(["/", ...linked, "/no-such-page"])];
}

/** A screenshot's name: home for /, the path's words otherwise. */
function screenshotName(path: string): string {
  return `${path.replace(/^\/|\/$/g, "").replace(/[^a-z0-9]+/gi, "-") || "home"}.png`;
}

test("every screen looks as it did", async ({ page }, testInfo) => {
  const updating = testInfo.config.updateSnapshots === "all" || testInfo.config.updateSnapshots === "changed";
  test.skip(
    !updating && !existsSync(dirname(testInfo.snapshotPath("home.png"))),
    "there are no baselines yet; the Screenshots workflow writes them",
  );
  await steady(page);
  for (const screen of await screens(page)) {
    await test.step(screen, async () => {
      await page.goto(screen);
      await page.waitForLoadState("networkidle");
      await page.evaluate(() => document.fonts.ready);
      const name = screenshotName(screen);
      const baseline = testInfo.snapshotPath(name);
      if (!updating && !existsSync(baseline)) {
        await testInfo.attach(name, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
        expect.soft(existsSync(baseline), `${screen} has no baseline; run the Screenshots workflow on this branch to add one`).toBe(true);
        return;
      }
      // Times are the one thing a screen shows that a pinned clock can't
      // hold still when the server reports them.
      await expect.soft(page).toHaveScreenshot(name, { fullPage: true, mask: [page.locator("time")] });
    });
  }
});
