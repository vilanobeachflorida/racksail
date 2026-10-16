import { createHmac } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { type CDPSession, expect, type Page, test } from "@playwright/test";
import { violations, violationsInEachMode } from "../axe";

// These run against a panel that signs people in the way a server does:
// racksail dev --sign-in, at RACKSAIL_E2E_SIGNIN_URL, started fresh with its
// data in RACKSAIL_E2E_SIGNIN_DATA, where it keeps the setup code until the
// first admin is made. test/e2e/local.sh starts one. They go one after the
// other, as one person: making the first admin, adding a second step and a
// passkey, and signing in with each. The screens of signing in, and the
// pages that need people and accounts, are checked with axe here, in light
// and dark mode.
const panel = process.env.RACKSAIL_E2E_SIGNIN_URL;
const data = process.env.RACKSAIL_E2E_SIGNIN_DATA;

test.skip(!panel || !data, "needs racksail dev --sign-in, which test/e2e/local.sh starts");
test.describe.configure({ mode: "serial" });
// Passkeys are bound to the panel's name, and localhost is one.
test.use({ baseURL: panel?.replace("127.0.0.1", "localhost") });

const admin = { username: "ada", name: "Ada Lovelace", password: "correct horse battery staple" };
// What the authenticator app was given, and the last time step a code was
// used for, since the panel takes each code once.
let secret = "";
let lastStep = -1;

test.beforeEach(({}, info) => {
  test.skip(info.project.name !== "desktop", "one panel has one first admin");
});

/** Decodes the base32 a TOTP secret is written in. */
function base32(text: string): Buffer {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of text.replace(/[\s=]/g, "").toUpperCase()) {
    bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  }
  const bytes: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    bytes.push(Number.parseInt(bits.slice(i, i + 8), 2));
  }
  return Buffer.from(bytes);
}

/** The code of RFC 6238 for a time step. */
function totp(key: string, step: number): string {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(step));
  const mac = createHmac("sha1", base32(key)).update(counter).digest();
  const offset = (mac[mac.length - 1] ?? 0) & 0xf;
  const value = mac.readUInt32BE(offset) & 0x7fffffff;
  return String(value % 1_000_000).padStart(6, "0");
}

/** A code the panel hasn't seen yet, waiting for the next time step when needed. */
async function freshCode(page: Page): Promise<string> {
  let step = Math.floor(Date.now() / 30_000);
  if (step <= lastStep) {
    await page.waitForTimeout((lastStep + 1) * 30_000 - Date.now() + 500);
    step = Math.floor(Date.now() / 30_000);
  }
  lastStep = step;
  return totp(secret, step);
}

/** Answers the panel's "confirm it's you" with the password, when it asks. */
async function confirmIfAsked(page: Page) {
  const dialog = page.getByRole("dialog", { name: "Confirm it's you" });
  try {
    await dialog.waitFor({ timeout: 3_000 });
  } catch {
    return;
  }
  await dialog.getByRole("textbox", { name: "Password" }).fill(admin.password);
  await dialog.getByRole("button", { name: "Confirm" }).click();
  await expect(dialog).toBeHidden();
}

async function signOut(page: Page) {
  const response = await page.request.post("/api/v1/auth/sign-out");
  expect(response.status()).toBe(204);
  await page.context().clearCookies({ name: "racksail_session" });
}

async function signInWithPassword(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.getByLabel("Username", { exact: true }).fill(admin.username);
  await page.getByLabel("Password", { exact: true }).fill(admin.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

/** A passkey the browser holds itself, as a phone or a security key would. */
async function virtualAuthenticator(page: Page): Promise<CDPSession> {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });
  return cdp;
}

test("makes the first admin with the setup code", async ({ page }) => {
  const code = readFileSync(join(data ?? "", "state", "setup-code"), "utf8").trim();
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Welcome to Racksail" })).toBeVisible();
  await page.getByLabel("Setup code", { exact: true }).fill("WRONG-CODE0-00000-00000");
  await page.getByLabel("Username", { exact: true }).fill(admin.username);
  await page.getByLabel("Name", { exact: true }).fill(admin.name);
  await page.getByLabel("New password", { exact: true }).fill(admin.password);
  await page.getByRole("button", { name: "Make the admin and sign in" }).click();
  await expect(page.getByText("The setup code isn't right")).toBeVisible();
  expect(await violationsInEachMode(page), "the first admin's setup").toEqual([]);

  await page.getByLabel("Setup code", { exact: true }).fill(code);
  await page.getByRole("button", { name: "Make the admin and sign in" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  // The code works once, and the copy the console showed is gone.
  expect(() => readFileSync(join(data ?? "", "state", "setup-code"))).toThrow();
});

test("signs in with a password, and refuses a wrong one", async ({ page }) => {
  await signInWithPassword(page);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  await signOut(page);
  await page.goto("/");
  await page.getByLabel("Username", { exact: true }).fill(admin.username);
  await page.getByLabel("Password", { exact: true }).fill("not the password at all");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByText("The username or password isn't right.")).toBeVisible();
  expect(await violationsInEachMode(page), "the sign-in screen").toEqual([]);
});

test("adds an authenticator app, which signing in then asks for", async ({ page }) => {
  test.setTimeout(90_000);
  await signInWithPassword(page);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  await page.goto("/settings/security");
  await page.getByRole("button", { name: "Set up", exact: true }).first().click();
  await confirmIfAsked(page);
  const setup = page.getByRole("dialog");
  secret = (await setup.getByLabel("Secret", { exact: true }).inputValue()).replace(/\s/g, "");
  expect(secret).toMatch(/^[A-Z2-7]{16,}$/);
  await setup.getByLabel("Code", { exact: true }).fill(await freshCode(page));
  await setup.getByRole("button", { name: "Turn it on" }).click();
  await expect(setup.getByText("Your authenticator app is set up")).toBeVisible();
  await setup.getByRole("button", { name: "I've kept it" }).click();

  await signOut(page);
  await signInWithPassword(page);
  await expect(page.getByRole("heading", { name: "One more step" })).toBeVisible();
  await page.getByLabel("Code", { exact: true }).fill("000000");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("That code isn't right, or it was used already.")).toBeVisible();
  expect(await violationsInEachMode(page), "the second step of signing in").toEqual([]);
  await page.getByLabel("Code", { exact: true }).fill(await freshCode(page));
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("asks the person to confirm it's them before a sensitive change", async ({ page }) => {
  test.setTimeout(90_000);
  await signInWithPassword(page);
  await page.getByLabel("Code", { exact: true }).fill(await freshCode(page));
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  await page.goto("/settings/tokens");
  await page.getByRole("button", { name: "Make a token" }).first().click();
  const make = page.getByRole("dialog", { name: "Make an API token" });
  await make.getByLabel("Name", { exact: true }).fill("e2e");
  await make.getByRole("button", { name: "Make a token" }).click();
  // A session that hasn't confirmed anything yet is asked to, and the
  // token is made once it has.
  const confirm = page.getByRole("dialog", { name: "Confirm it's you" });
  await expect(confirm).toBeVisible();
  await confirm.getByRole("textbox", { name: "Password" }).fill("a wrong password here");
  await confirm.getByRole("button", { name: "Confirm" }).click();
  await expect(confirm.getByText("Your current password isn't right.")).toBeVisible();
  await confirm.getByRole("textbox", { name: "Password" }).fill(admin.password);
  await confirm.getByRole("button", { name: "Confirm" }).click();
  await expect(page.getByRole("dialog", { name: "Your token e2e" })).toBeVisible();
});

test("adds a passkey, which signs in on its own", async ({ page }) => {
  test.setTimeout(90_000);
  await virtualAuthenticator(page);
  await signInWithPassword(page);
  await page.getByLabel("Code", { exact: true }).fill(await freshCode(page));
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  await page.goto("/settings/security");
  await page.getByRole("button", { name: "Add a passkey" }).first().click();
  const add = page.getByRole("dialog", { name: "Add a passkey" });
  await add.getByLabel("Name", { exact: true }).fill("Virtual key");
  await add.getByRole("button", { name: "Add a passkey" }).click();
  // Adding a way to sign in needs the person to confirm it's them first.
  await confirmIfAsked(page);
  await expect(page.getByText("Virtual key")).toBeVisible();

  await signOut(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Sign in with a passkey" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.user.username).toBe(admin.username);
});

test("views the panel as an account's owner, under a banner every mode shows clearly", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await signInWithPassword(page);
  await page.getByLabel("Code", { exact: true }).fill(await freshCode(page));
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // An account with an owner to view the panel as. The owner is a new
  // user, so the admin confirms it's them first.
  expect((await page.request.post("/api/v1/session/step-up", { data: { password: admin.password } })).status()).toBe(200);
  const opened = await page.request.post("/api/v1/core/accounts/open", {
    data: { slug: "acme", name: "Acme", owner: { username: "owen", name: "Owen" } },
  });
  expect(opened.status()).toBe(202);
  const owner: string = (await opened.json()).owner.id;

  await page.goto(`/users/${owner}`);
  await page.getByRole("button", { name: "View as them" }).click();
  await page
    .getByRole("alertdialog", { name: "View the panel as owen?" })
    .getByRole("button", { name: "View as them" })
    .click();
  const banner = page.getByRole("region", { name: "Viewing the panel as someone else" });
  await expect(banner).toBeVisible();
  const stop = banner.getByRole("button", { name: "Stop viewing as Owen" });
  await stop.focus();
  // The banner takes the protected colors, the same whatever theme the
  // person chose, in light and dark and with high contrast: the warning
  // color, and its text color for the button. Less motion has the colors
  // change at once, rather than fade from the mode before.
  for (const [colorScheme, contrast, warning, warningText] of [
    ["light", "no-preference", "rgb(138, 90, 0)", "rgb(255, 255, 255)"],
    ["dark", "no-preference", "rgb(227, 163, 58)", "rgb(0, 20, 47)"],
    ["light", "more", "rgb(122, 79, 0)", "rgb(255, 255, 255)"],
    ["dark", "more", "rgb(255, 201, 77)", "rgb(0, 0, 0)"],
  ] as const) {
    await page.emulateMedia({ colorScheme, contrast, reducedMotion: "reduce" });
    await expect(banner).toHaveCSS("background-color", warning);
    await expect(stop).toHaveCSS("background-color", warningText);
    await expect(stop).toHaveCSS("color", warning);
    expect(
      await violations(page, "section[aria-label='Viewing the panel as someone else']"),
      `the banner in ${colorScheme} mode, contrast ${contrast}`,
    ).toEqual([]);
  }
  await stop.click();
  await expect(banner).toBeHidden();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.user.username).toBe(admin.username);
});

test("has nothing axe finds on the pages of your account and of people and access", async ({ page }) => {
  test.setTimeout(120_000);
  await signInWithPassword(page);
  await page.getByLabel("Code", { exact: true }).fill(await freshCode(page));
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // The account and its owner the test before opened.
  const accounts = await (await page.request.get("/api/v1/core/accounts")).json();
  const account: string = accounts.items.find((a: { spec: { slug: string } }) => a.spec.slug === "acme").id;
  const users = await (await page.request.get("/api/v1/core/users")).json();
  const owner: string = users.items.find((u: { spec: { username: string } }) => u.spec.username === "owen").id;
  for (const path of [
    "/settings/profile",
    "/settings/security",
    "/settings/sessions",
    "/settings/tokens",
    "/settings/ssh-keys",
    "/settings/sign-in",
    "/roles",
    "/users",
    `/users/${owner}`,
    "/accounts",
    `/accounts/${account}`,
  ]) {
    await test.step(path, async () => {
      await page.goto(path);
      await page.waitForLoadState("networkidle");
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      expect.soft(await violationsInEachMode(page), path).toEqual([]);
    });
  }
});
