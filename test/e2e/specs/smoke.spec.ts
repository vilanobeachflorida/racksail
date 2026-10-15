import { type APIRequestContext, expect, test } from "@playwright/test";

type Version = { version: string; channel: string };

async function panelVersion(request: APIRequestContext): Promise<Version> {
  const response = await request.get("/api/v1/version");
  expect(response.ok()).toBeTruthy();
  return (await response.json()) as Version;
}

test.describe("the panel", () => {
  test("serves the web UI with its security headers", async ({ page }) => {
    const response = await page.goto("/");
    expect(response?.status()).toBe(200);
    await expect(page).toHaveTitle(/Racksail/);
    const headers = response?.headers() ?? {};
    expect(headers["content-security-policy"]).toContain("default-src 'self'");
    expect(headers["content-security-policy"]).toContain("frame-ancestors 'none'");
    expect(headers["x-content-type-options"]).toBe("nosniff");
  });

  test("was built with the web UI", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.getByText("This binary was built without the web UI"),
      "the binary under test has no UI; run make ui before make e2e-local",
    ).toHaveCount(0);
  });

  test("loads nothing from other origins", async ({ page, baseURL }) => {
    const origin = new URL(baseURL ?? "").origin;
    const elsewhere: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.protocol !== "data:" && url.protocol !== "blob:" && url.origin !== origin) {
        elsewhere.push(request.url());
      }
    });
    await page.goto("/");
    await page.waitForLoadState("networkidle");
    expect(elsewhere).toEqual([]);
  });

  test("starts without errors", async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") {
        errors.push(message.text());
      }
    });
    await page.goto("/");
    await page.waitForLoadState("networkidle");
    expect(errors).toEqual([]);
  });

  test("reports its health and version", async ({ request }) => {
    const health = await request.get("/api/v1/health");
    expect(health.status()).toBe(200);
    expect(await health.json()).toMatchObject({ status: "ok" });
    const version = await panelVersion(request);
    expect(["stable", "beta", "nightly", "dev"]).toContain(version.channel);
    const expected = process.env.RACKSAIL_E2E_VERSION;
    if (expected) {
      expect(version.version).toBe(expected.replace(/^v/, ""));
    }
  });
});

test.describe("the shell", () => {
  test.beforeEach(async ({ request }) => {
    const version = await panelVersion(request);
    test.skip(version.channel !== "dev", "the shell needs a signed-in session, and racksail dev signs everyone in");
  });

  test("has its landmarks", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("main")).toBeVisible();
    await expect(page.getByRole("navigation").first()).toBeAttached();
    await expect(page.getByRole("banner")).toBeVisible();
  });

  test("skips to the content from the keyboard", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("main")).toBeVisible();
    await page.keyboard.press("Tab");
    await expect(page.getByRole("link", { name: "Skip to content" })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("main")).toBeFocused();
  });

  test("shows a page for addresses that don't exist", async ({ page }) => {
    await page.goto("/no-such-page");
    const main = page.getByRole("main");
    await expect(main.getByRole("heading", { level: 1 })).toBeVisible();
    await main.getByRole("link", { name: /home/i }).click();
    await expect(page).toHaveURL(/\/$/);
  });
});
