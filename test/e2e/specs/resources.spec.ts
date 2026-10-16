import { expect, type Page, test } from "@playwright/test";
import { waitForQuota } from "../quota";

// The generic screens every kind gets, against racksail dev, which signs
// everyone in: a webhook created through the change preview, edited, the
// edit undone from its toast and from the history, and the webhook deleted.
// Each test makes its own webhook, so the desktop and phone runs never meet.
test.skip(!process.env.RACKSAIL_E2E_DEV, "the generic screens need racksail dev, which signs everyone in");

// Everyone racksail dev signs in is the same person, whose requests share
// one rate limit, so the changes here are made one test at a time, each
// once the panel would take a burst of requests.
test.describe.configure({ mode: "serial" });
test.beforeEach(async ({ request }) => {
  await waitForQuota(request);
});

/** A webhook address of this test's own. */
function hookURL(name: string): string {
  return `https://hooks.example.com/${name}-${test.info().project.name}-${Date.now()}`;
}

/** A toast that says it, from Sonner's list of them. */
function toast(page: Page, text: string) {
  return page.locator("[data-sonner-toast]").filter({ hasText: text });
}

/** Creates a webhook through its form and preview, and waits for its page. */
async function createWebhook(page: Page, url: string): Promise<void> {
  await waitForQuota(page.request);
  await page.goto("/core/webhooks/new");
  await expect(page.getByRole("heading", { level: 1, name: "New webhook" })).toBeVisible();
  await page.getByRole("textbox", { name: "URL" }).fill(url);
  await page.getByRole("button", { name: "Add to Events" }).click();
  // Events no module of racksail dev sends, so nothing is delivered.
  await page.getByRole("textbox", { name: "Events 1" }).fill("web.site.created");
  await page.getByLabel("Signing secret").fill("end-to-end-signing-secret");
  await page.getByRole("button", { name: "Review the change" }).click();
  const preview = page.getByRole("dialog", { name: "Create the webhook" });
  await expect(preview.getByText("Create webhook")).toBeVisible();
  await preview.getByRole("button", { name: "Apply" }).click();
  await expect(page.getByRole("heading", { level: 1, name: url })).toBeVisible();
  await expect(toast(page, "Applied: Create webhook")).toBeVisible();
}

/** Changes the webhook's description through the edit form and its preview. */
async function describeWebhook(page: Page, url: string, description: string): Promise<void> {
  await waitForQuota(page.request);
  await page.getByRole("link", { name: "Edit" }).click();
  await expect(page.getByRole("heading", { level: 1, name: `Edit ${url}` })).toBeVisible();
  await page.getByRole("textbox", { name: "Description" }).fill(description);
  await page.getByRole("button", { name: "Review the change" }).click();
  const preview = page.getByRole("dialog", { name: `Change ${url}` });
  await expect(preview.getByText(description)).toBeVisible();
  await preview.getByRole("button", { name: "Apply" }).click();
  await expect(page.getByRole("heading", { level: 1, name: url })).toBeVisible();
  await expect(page.getByText(description).first()).toBeVisible();
}

/** Deletes the webhook on screen through its preview. */
async function deleteWebhook(page: Page, url: string): Promise<void> {
  await waitForQuota(page.request);
  await page.getByRole("button", { name: "Delete" }).click();
  const preview = page.getByRole("dialog", { name: `Delete the webhook ${url}?` });
  await preview.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/\/core\/webhooks$/);
  await expect(page.getByRole("link", { name: url })).toHaveCount(0);
}

test("creates a resource, edits it and undoes the edit from its toast", async ({ page }) => {
  const url = hookURL("toast");
  await createWebhook(page, url);
  await describeWebhook(page, url, "Sent by the end-to-end tests");

  await waitForQuota(page.request);
  await toast(page, "Applied: Update webhook").getByRole("button", { name: "Undo" }).click();
  await expect(toast(page, "Applied: Undo: Update webhook")).toBeVisible();
  await expect(page.getByText("Sent by the end-to-end tests")).toHaveCount(0);

  // The list shows it, and the changes screen has all three changes.
  await waitForQuota(page.request);
  await page.goto("/core/webhooks");
  await expect(page.getByRole("link", { name: url })).toBeVisible();
  await waitForQuota(page.request);
  await page.goto("/changes");
  await expect(page.getByRole("link", { name: "Undo: Update webhook" }).first()).toBeVisible();

  await waitForQuota(page.request);
  await page.goto("/core/webhooks");
  await page.getByRole("link", { name: url }).click();
  await deleteWebhook(page, url);
});

test("undoes a change from the resource's history", async ({ page }) => {
  const url = hookURL("history");
  await createWebhook(page, url);
  await describeWebhook(page, url, "Kept in the history");

  await waitForQuota(page.request);
  await page.getByRole("tab", { name: "History" }).click();
  const versions = page.getByRole("list", { name: "Versions of the settings, newest first" });
  await expect(versions.getByText("Version 2")).toBeVisible();
  await versions.getByRole("listitem").filter({ hasText: "Version 2" }).getByRole("button", { name: "Undo" }).click();
  await expect(toast(page, "Applied: Undo: Update webhook")).toBeVisible();
  await page.getByRole("tab", { name: "Overview" }).click();
  await expect(page.getByText("Kept in the history")).toHaveCount(0);

  await deleteWebhook(page, url);
});

test("finds a resource and runs its actions from the command bar", async ({ page, isMobile }) => {
  test.skip(isMobile, "phones have no keyboard to open the command bar with");
  const url = hookURL("command");
  await createWebhook(page, url);

  await page.keyboard.press("ControlOrMeta+k");
  const bar = page.getByRole("dialog", { name: "Command bar" });
  await bar.getByRole("combobox").fill(url.slice("https://".length));
  const found = bar.getByRole("group", { name: "Resources" });
  await expect(found.getByRole("option", { name: `${url} Webhook` })).toBeVisible();
  await page.keyboard.press("Escape");

  await page.keyboard.press("ControlOrMeta+k");
  await page.getByRole("dialog", { name: "Command bar" }).getByRole("option", { name: "Send a test" }).click();
  const action = page.getByRole("dialog", { name: "Send a test" });
  await action.getByRole("button", { name: "Apply" }).click();
  // The test delivery can't reach hooks.example.com, and the result says so.
  await expect(action.getByRole("region", { name: "Result" })).toBeVisible();
  await action.getByRole("button", { name: "Close" }).first().click();

  await deleteWebhook(page, url);
});

test("shows what needs attention on the home screen", async ({ page }) => {
  await page.goto("/");
  const inbox = page.getByRole("region", { name: /Needs your attention/ });
  await expect(inbox).toBeVisible();
  // racksail dev's machine has disks nothing uses yet.
  await expect(inbox.getByText(/disks? (isn't|aren't) used yet/)).toBeVisible();
});
