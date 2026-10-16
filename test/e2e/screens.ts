import type { Page } from "@playwright/test";

/**
 * The panel's own screens, which the home screen, the command bar and links
 * lead to rather than the navigation, with a kind's generic screens, and
 * the pages of your own account and of people and access the settings
 * lead to.
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
  "/settings/profile",
  "/settings/security",
  "/settings/sessions",
  "/settings/tokens",
  "/settings/ssh-keys",
  "/settings/sign-in",
  "/roles",
];

/**
 * The screens the browser suites check: the first screen, every one the
 * navigation offers, which includes each module's that is on, the panel's
 * own screens, the page of the user the suites act as, and the page for
 * addresses that don't exist.
 */
export async function screens(page: Page): Promise<string[]> {
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const linked = await page
    .getByRole("navigation")
    .locator("a[href^='/']")
    .evaluateAll((links) => links.map((a) => new URL((a as HTMLAnchorElement).href).pathname));
  const session = await page.request.get("/api/v1/session");
  const own = session.ok() ? [`/users/${encodeURIComponent((await session.json()).user.id)}`] : [];
  return [...new Set(["/", ...linked, ...panelScreens, ...own, "/no-such-page"])];
}
