import type { Page } from "@playwright/test";

/**
 * The panel's own screens, which the home screen, the command bar and links
 * lead to rather than the navigation, with a kind's generic screens.
 */
const panelScreens = [
  "/jobs",
  "/events",
  "/changes",
  "/resources",
  "/search",
  "/core/webhooks",
  "/core/webhooks/new",
  "/settings/general",
];

/**
 * The screens the browser suites check: the first screen, every one the
 * navigation offers, which includes each module's that is on, the panel's
 * own screens, and the page for addresses that don't exist.
 */
export async function screens(page: Page): Promise<string[]> {
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const linked = await page
    .getByRole("navigation")
    .locator("a[href^='/']")
    .evaluateAll((links) => links.map((a) => new URL((a as HTMLAnchorElement).href).pathname));
  return [...new Set(["/", ...linked, ...panelScreens, "/no-such-page"])];
}
