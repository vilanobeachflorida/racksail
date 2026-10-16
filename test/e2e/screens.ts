import type { Page } from "@playwright/test";

/**
 * The screens the browser suites check: the first screen, every one the
 * navigation offers, which includes each module's that is on, and the page
 * for addresses that don't exist.
 */
export async function screens(page: Page): Promise<string[]> {
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const linked = await page
    .getByRole("navigation")
    .locator("a[href^='/']")
    .evaluateAll((links) => links.map((a) => new URL((a as HTMLAnchorElement).href).pathname));
  return [...new Set(["/", ...linked, "/no-such-page"])];
}
