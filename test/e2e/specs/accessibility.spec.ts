import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { waitForQuota } from "../quota";
import { screens } from "../screens";

// Every screen meets WCAG 2.2 AA (docs/architecture/conventions.md): the
// first screen, each one the navigation reaches and the page for addresses
// that don't exist, on a desktop and on a phone. axe checks what a machine
// can; keyboard flows have their own tests.
const wcag22aa = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

// violations lists what axe finds on the page, one line per rule with the
// elements that break it, so a failure says what to fix.
async function violations(page: Page): Promise<string[]> {
  const results = await new AxeBuilder({ page }).withTags(wcag22aa).analyze();
  return results.violations.map(
    (v) => `${v.id} (${v.impact ?? "unknown impact"}): ${v.help}; ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`,
  );
}

test("every screen meets WCAG 2.2 AA", async ({ page }) => {
  const all = await screens(page);
  // Each screen loads and is analyzed in turn.
  test.setTimeout(30_000 + all.length * 10_000);
  for (const screen of all) {
    await test.step(screen, async () => {
      await waitForQuota(page.request);
      await page.goto(screen);
      await page.waitForLoadState("networkidle");
      expect.soft(await violations(page), `${screen} doesn't meet WCAG 2.2 AA`).toEqual([]);
    });
  }
});
