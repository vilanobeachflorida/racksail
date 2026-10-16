import { expect, type Page, test } from "@playwright/test";

// The generic screens every kind gets, against racksail dev, which signs
// everyone in: a webhook created through the change preview, edited, the
// edit undone from its toast and from the history, and the webhook deleted.
// Each test makes its own webhook, so tests running side by side, and the
// desktop and phone runs, never meet. racksail dev doesn't rate-limit the
// developer it signs everyone in as, so nothing here waits for a limit.
test.skip(!process.env.RACKSAIL_E2E_DEV, "the generic screens need racksail dev, which signs everyone in");

/** A webhook address of this test's own. */
function hookURL(name: string): string {
  return `https://hooks.example.com/${name}-${test.info().project.name}-${Date.now()}`;
}

/** A toast that says it, from Sonner's list of them. */
function toast(page: Page, text: string) {
  return page.locator("[data-sonner-toast]").filter({ hasText: text });
}

/** Creates a webhook through its form and preview, waits for its page, and returns its ID. */
async function createWebhook(page: Page, url: string): Promise<string> {
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
  return new URL(page.url()).pathname.split("/").at(-1) ?? "";
}

/** Changes the webhook's description through the edit form and its preview. */
async function describeWebhook(page: Page, url: string, description: string): Promise<void> {
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

  await toast(page, "Applied: Update webhook").getByRole("button", { name: "Undo" }).click();
  await expect(toast(page, "Applied: Undo: Update webhook")).toBeVisible();
  await expect(page.getByText("Sent by the end-to-end tests")).toHaveCount(0);

  // The list shows it, and the changes screen has all three changes.
  await page.goto("/core/webhooks");
  await expect(page.getByRole("link", { name: url })).toBeVisible();
  await page.goto("/changes");
  await expect(page.getByRole("link", { name: "Undo: Update webhook" }).first()).toBeVisible();

  await page.goto("/core/webhooks");
  await page.getByRole("link", { name: url }).click();
  await deleteWebhook(page, url);
});

test("undoes a change from the resource's history", async ({ page }) => {
  const url = hookURL("history");
  await createWebhook(page, url);
  await describeWebhook(page, url, "Kept in the history");

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

test("deletes a resource from its list, and the dialog goes once that's applied", async ({ page }) => {
  const url = hookURL("list");
  await createWebhook(page, url);
  await page.goto("/core/webhooks");
  await page.getByRole("button", { name: `Actions for ${url}` }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  const preview = page.getByRole("dialog", { name: `Delete the webhook ${url}?` });
  await preview.getByRole("button", { name: "Apply" }).click();
  await expect(toast(page, "Applied: Delete webhook")).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("link", { name: url })).toHaveCount(0);
});

test("won't apply an edit over someone else's, and starts again from theirs", async ({ page }) => {
  const url = hookURL("conflict");
  const id = await createWebhook(page, url);
  await page.getByRole("link", { name: "Edit" }).click();
  await expect(page.getByRole("heading", { level: 1, name: `Edit ${url}` })).toBeVisible();
  await page.getByRole("textbox", { name: "Description" }).fill("Mine");

  // Someone else sends the webhook one more event through the API meanwhile.
  const path = `/api/v1/core/webhooks/${id}`;
  const current = await (await page.request.get(path)).json();
  const theirs = await page.request.put(path, {
    data: { spec: { ...current.spec, events: [...current.spec.events, "web.site.deleted"] }, revision: current.revision },
  });
  expect(theirs.status()).toBe(202);

  const changed = page.getByRole("alert").filter({ hasText: "Someone else changed this webhook since you opened it" });
  await expect(changed).toBeVisible();
  await expect(changed.getByRole("list", { name: "What they changed" })).toContainText("web.site.deleted");
  await page.getByRole("button", { name: "Review the change" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  // Their change stands.
  const after = await (await page.request.get(path)).json();
  expect(after.spec.events).toContain("web.site.deleted");
  expect(after.spec.description ?? "").toBe("");

  await changed.getByRole("button", { name: "Load the latest settings" }).click();
  await expect(changed).toHaveCount(0);
  await page.getByRole("textbox", { name: "Description" }).fill("Mine");
  await page.getByRole("button", { name: "Review the change" }).click();
  const preview = page.getByRole("dialog", { name: `Change ${url}` });
  await preview.getByRole("button", { name: "Apply" }).click();
  await expect(page.getByRole("heading", { level: 1, name: url })).toBeVisible();
  const mine = await (await page.request.get(path)).json();
  expect(mine.spec.description).toBe("Mine");
  expect(mine.spec.events).toContain("web.site.deleted");
  await deleteWebhook(page, url);
});

test("says a kept preview with a secret can't be applied, and where to make the change", async ({ page }) => {
  const url = hookURL("kept");
  await page.goto("/core/webhooks/new");
  await page.getByRole("textbox", { name: "URL" }).fill(url);
  await page.getByRole("button", { name: "Add to Events" }).click();
  await page.getByRole("textbox", { name: "Events 1" }).fill("web.site.created");
  await page.getByLabel("Signing secret").fill("end-to-end-signing-secret");
  await page.getByRole("button", { name: "Review the change" }).click();
  const preview = page.getByRole("dialog", { name: "Create the webhook" });
  await expect(preview.getByText("Create webhook")).toBeVisible();
  await preview.getByRole("button", { name: "Cancel" }).click();

  // The preview stays among those not applied.
  const previews = await (await page.request.get("/api/v1/changesets?previews=true&limit=200")).json();
  const kept = previews.changeSets.find(
    (cs: { changes?: { after?: { url?: string } }[] }) => cs.changes?.[0]?.after?.url === url,
  );
  expect(kept).toBeDefined();
  await page.goto(`/changes/${kept.id}`);
  await page.getByRole("button", { name: "Apply" }).click();
  const refused = page.getByRole("alert").filter({ hasText: "The change couldn't be applied" });
  await expect(refused).toContainText("carried secret values, which previews don't keep");
  await refused.getByRole("link", { name: "Make the change again" }).click();
  await expect(page).toHaveURL(/\/core\/webhooks\/new$/);
});
