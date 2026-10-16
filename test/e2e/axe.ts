import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";

// What the panel is held to: WCAG 2.2 AA (docs/architecture/conventions.md).
// axe checks what a machine can; keyboard flows have their own tests.
const wcag22aa = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

/**
 * What axe finds on the page, or in the part of it a selector names, one
 * line per rule with the elements that break it, so a failure says what to
 * fix.
 */
export async function violations(page: Page, include?: string): Promise<string[]> {
  const builder = new AxeBuilder({ page }).withTags(wcag22aa);
  const results = await (include ? builder.include(include) : builder).analyze();
  return results.violations.map(
    (v) =>
      `${v.id} (${v.impact ?? "unknown impact"}): ${v.help}; ${v.nodes
        .map((n) => `${n.target.join(" ")}${n.any[0]?.message ? ` (${n.any[0].message})` : ""}`)
        .join(", ")}`,
  );
}

/**
 * What axe finds on the page in light and in dark mode, each line naming
 * the mode. Less motion has colors change at once rather than fade, but a
 * transition still ends on the next frame, so axe waits for every one to
 * end rather than read the colors of the mode before.
 */
export async function violationsInEachMode(page: Page, include?: string): Promise<string[]> {
  const found: string[] = [];
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
    await page.waitForFunction(() =>
      document.getAnimations().every((animation) => animation.playState !== "running"),
    );
    found.push(...(await violations(page, include)).map((v) => `${colorScheme}: ${v}`));
  }
  await page.emulateMedia({ colorScheme: null, reducedMotion: null });
  return found;
}
