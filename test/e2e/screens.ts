import type { Page } from "@playwright/test";

/** A screen the browser suites check. */
export interface Screen {
  /** Where the screen is on the panel under test. */
  path: string;
  /**
   * What the suites call it, in their steps and in the names of the visual
   * baselines. It's the same on every panel: a panel makes its IDs afresh
   * when it starts, so a path with one in it is named with a word in the
   * ID's place.
   */
  name: string;
}

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
 * The screens the browser suites check, given the paths the navigation
 * links to and the ID of the user the suites act as, when the panel has a
 * session: the first screen, every one the navigation offers, the panel's
 * own screens, the page of that user, and the page for addresses that
 * don't exist, each once.
 */
export function screenList(linked: string[], user?: string): Screen[] {
  const named = (path: string): Screen => ({ path, name: path });
  const all = [
    named("/"),
    ...linked.map(named),
    ...panelScreens.map(named),
    ...(user ? [{ path: `/users/${encodeURIComponent(user)}`, name: "/users/<your id>" }] : []),
    named("/no-such-page"),
  ];
  return all.filter((screen, i) => all.findIndex((other) => other.path === screen.path) === i);
}

/**
 * The screens the browser suites check on the panel under test, whose
 * navigation includes each module's screens that are on.
 */
export async function screens(page: Page): Promise<Screen[]> {
  await page.goto("/");
  await page.waitForLoadState("networkidle");
  const linked = await page
    .getByRole("navigation")
    .locator("a[href^='/']")
    .evaluateAll((links) => links.map((a) => new URL((a as HTMLAnchorElement).href).pathname));
  const session = await page.request.get("/api/v1/session");
  const user = session.ok() ? ((await session.json()) as { user: { id: string } }).user.id : undefined;
  return screenList(linked, user);
}

/** The file of a screen's visual baseline: home for /, the words of its name otherwise. */
export function screenshotName(screen: Screen): string {
  const words = screen.name.replace(/[^a-z0-9]+/gi, "-").replace(/^-|-$/g, "");
  return `${words || "home"}.png`;
}
