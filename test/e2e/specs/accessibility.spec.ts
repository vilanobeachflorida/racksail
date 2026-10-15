import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";

// Every screen meets WCAG 2.2 AA (docs/architecture/conventions.md). axe
// checks what a machine can; keyboard flows have their own tests.
const wcag22aa = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

// violations lists what axe finds on the page, one line per rule with the
// elements that break it, so a failure says what to fix.
async function violations(page: Page): Promise<string[]> {
  const results = await new AxeBuilder({ page }).withTags(wcag22aa).analyze();
  return results.violations.map(
    (v) => `${v.id} (${v.impact ?? "unknown impact"}): ${v.help}; ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`,
  );
}

test.describe("accessibility", () => {
  test("the first screen meets WCAG 2.2 AA", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("networkidle");
    expect(await violations(page)).toEqual([]);
  });

  test("the page for addresses that don't exist meets WCAG 2.2 AA", async ({ page }) => {
    await page.goto("/no-such-page");
    await page.waitForLoadState("networkidle");
    expect(await violations(page)).toEqual([]);
  });
});
