import { existsSync } from "node:fs";
import { dirname } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { screenshotName, screens } from "../screens";

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

test("every screen looks as it did", async ({ page }, testInfo) => {
  const updating = testInfo.config.updateSnapshots === "all" || testInfo.config.updateSnapshots === "changed";
  test.skip(
    !updating && !existsSync(dirname(testInfo.snapshotPath("home.png"))),
    "there are no baselines yet; the Screenshots workflow writes them",
  );
  await steady(page);
  const all = await screens(page);
  // A baseline is found by its screen's name, which is the same on every
  // panel, and belongs to that screen alone.
  const names = all.map(screenshotName);
  expect(names.filter((name, i) => names.indexOf(name) !== i), "screens sharing a baseline").toEqual([]);
  for (const screen of all) {
    await test.step(screen.name, async () => {
      await page.goto(screen.path);
      await page.waitForLoadState("networkidle");
      await page.evaluate(() => document.fonts.ready);
      const name = screenshotName(screen);
      const baseline = testInfo.snapshotPath(name);
      if (!updating && !existsSync(baseline)) {
        await testInfo.attach(name, { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
        expect.soft(existsSync(baseline), `${screen.name} has no baseline; run the Screenshots workflow on this branch to add one`).toBe(true);
        return;
      }
      // Times are the one thing a screen shows that a pinned clock can't
      // hold still when the server reports them.
      await expect.soft(page).toHaveScreenshot(name, { fullPage: true, mask: [page.locator("time")] });
    });
  }
});
