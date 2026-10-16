import { expect, test } from "@playwright/test";
import { violationsInEachMode } from "../axe";
import { waitForQuota } from "../quota";
import { screens } from "../screens";

// Every screen meets WCAG 2.2 AA (docs/architecture/conventions.md), in
// light and dark mode: the first screen, each one the navigation reaches,
// the panel's own screens, your account's and the page for addresses that
// don't exist, on a desktop and on a phone. The sign-in suite checks the
// screens of signing in, and pages that need people and accounts.
test("every screen meets WCAG 2.2 AA", async ({ page }) => {
  const all = await screens(page);
  // Each screen loads and is analyzed in turn, in both modes.
  test.setTimeout(30_000 + all.length * 15_000);
  for (const screen of all) {
    await test.step(screen, async () => {
      await waitForQuota(page.request);
      await page.goto(screen);
      await page.waitForLoadState("networkidle");
      expect.soft(await violationsInEachMode(page), `${screen} doesn't meet WCAG 2.2 AA`).toEqual([]);
    });
  }
});
