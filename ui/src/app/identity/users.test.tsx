import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createIntl } from "react-intl";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PanelSession } from "@/lib/api/session";
import { fakeApi, json, renderApp } from "@/test/render";
import { readLinkReturn } from "./account/security";
import { type AuditRecord, type UserResource, userView } from "./api";
import catalog from "./messages/en.json";
import { repeats, who } from "./people/audit";
import { resourceName } from "./preview";
import { formatTime } from "./util";

const shell = {
  "GET /api/v1/health": () => json({ status: "ok", checks: [] }),
  "GET /api/v1/version": () =>
    json({ version: "1.2.3", channel: "stable", goVersion: "go1.27", os: "linux", arch: "arm64" }),
  "GET /api/v1/jobs": () => json({ jobs: [] }),
  "GET /api/v1/events": () => json({ events: [] }),
};

const ada: PanelSession = {
  user: { id: "user_1", name: "Ada Lovelace", username: "ada", home: { level: "server" } },
  permissions: ["*"],
  modules: {},
  stepUpMethods: ["password"],
  steppedUpUntil: "2999-01-01T00:00:00Z",
  accounts: [],
};

const adaResource: UserResource = {
  id: "user_1",
  kind: "core.user",
  scope: { level: "server" },
  revision: 3,
  generation: 3,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-15T10:00:00Z",
  spec: {
    username: "ada",
    name: "Ada Lovelace",
    grants: [{ role: "admin", scope: { level: "server" } }],
    sshKeys: null,
  },
  status: {
    phase: "ready",
    passwordSet: true,
    twoFactor: true,
    lastSignInAt: "2026-10-15T09:00:00Z",
  },
};

/** The change set the panel answers a user's creation with. */
function created(id: string, spec: unknown, dryRun: boolean) {
  return {
    id: dryRun ? "chg_preview" : "chg_1",
    title: "Create user",
    state: dryRun ? "planned" : "applying",
    dryRun,
    risk: "high",
    createdAt: "2026-10-15T10:00:00Z",
    estimate: { downloadBytes: 0, diskBytes: 0, memoryBytes: 0 },
    changes: [
      {
        op: "create",
        kind: "core.user",
        id,
        scope: { level: "server" },
        after: spec,
        diff: [{ path: "username", op: "add", after: "bea" }],
        source: { type: "request" },
      },
    ],
    effects: [],
    ...(dryRun ? {} : { job: "job_1" }),
  };
}

beforeEach(() => {
  window.history.replaceState(null, "", "/");
});

describe("users", () => {
  it("shows a user as their resource and status say", () => {
    expect(userView(adaResource)).toMatchObject({
      id: "user_1",
      username: "ada",
      name: "Ada Lovelace",
      home: { level: "server" },
      grants: [{ role: "admin", scope: { level: "server" } }],
      allowFrom: [],
      disabled: false,
      passwordSet: true,
      twoFactor: true,
      lastSignInAt: "2026-10-15T09:00:00Z",
      revision: 3,
    });
    expect(userView({ ...adaResource, status: undefined })).toMatchObject({
      passwordSet: false,
      twoFactor: false,
    });
  });

  it("adds a user after a preview of the change, then makes their sign-in link", async () => {
    const sent: { dryRun: boolean; body: { id?: string; scope?: unknown; spec?: unknown } }[] = [];
    const api = fakeApi({
      ...shell,
      "GET /api/v1/session": () => json(ada),
      "GET /api/v1/core/users": () => json({ items: [adaResource], next: "" }),
      "GET /api/v1/core/accounts": () => json({ items: [], next: "" }),
      "POST /api/v1/core/users": async (r) => {
        const body = await r.json();
        const dryRun = new URL(r.url).searchParams.get("dryRun") === "true";
        sent.push({ dryRun, body });
        return json(created(body.id ?? "user_2", body.spec, dryRun), dryRun ? 200 : 202);
      },
      "POST /api/v1/core/users/user_2/link": () =>
        json({
          code: "inv_secret",
          path: "/activate#inv_secret",
          url: "https://panel.example.com/activate#inv_secret",
          purpose: "activate",
          expiresAt: "2026-10-22T10:00:00Z",
        }),
    });
    renderApp(api, "/users");
    expect(await screen.findByRole("link", { name: "ada" })).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("button", { name: "Add a user" })[0] as HTMLElement);
    const form = await screen.findByRole("dialog", { name: "Add a user" });
    await userEvent.type(within(form).getByLabelText("Username"), "Bea");
    await userEvent.click(within(form).getByRole("button", { name: "Preview" }));

    // The preview is the change set of a dry run; nothing is added yet.
    const preview = await screen.findByRole("dialog", { name: "Add a user" });
    expect(await within(preview).findByText("bea")).toBeInTheDocument();
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({
      dryRun: true,
      body: { scope: { level: "server" }, spec: { username: "Bea" } },
    });
    await userEvent.click(within(preview).getByRole("button", { name: "Apply" }));
    const confirm = await screen.findByRole("alertdialog");
    await userEvent.click(within(confirm).getByRole("button", { name: "Apply" }));

    // Applying adds the user the preview showed, who then gets a link.
    const link = await screen.findByRole("dialog", { name: "A sign-in link for Bea" });
    expect(
      within(link).getByDisplayValue("https://panel.example.com/activate#inv_secret"),
    ).toBeInTheDocument();
    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toMatchObject({
      dryRun: false,
      body: { id: "user_2", spec: { username: "Bea" } },
    });
  });

  it("names the account a user belongs to, as the list of users does", async () => {
    const owen: UserResource = {
      ...adaResource,
      id: "user_3",
      scope: { level: "account", id: "acct_1" },
      spec: { username: "owen", name: "Owen", grants: [], sshKeys: null },
    };
    const api = fakeApi({
      ...shell,
      "GET /api/v1/session": () => json(ada),
      "GET /api/v1/core/users": () => json({ items: [owen], next: "" }),
      "GET /api/v1/core/users/user_3": () => json(owen),
      "GET /api/v1/core/accounts": () =>
        json({
          items: [
            {
              id: "acct_1",
              kind: "core.account",
              scope: { level: "account", id: "acct_1" },
              revision: 1,
              generation: 1,
              createdAt: "2026-10-01T10:00:00Z",
              updatedAt: "2026-10-01T10:00:00Z",
              spec: { name: "Review Account", slug: "reviewacct" },
            },
          ],
          next: "",
        }),
    });
    renderApp(api, "/users/user_3");
    const home = await screen.findByRole("textbox", { name: /^Belongs to/ });
    await waitFor(() => expect(home).toHaveValue("Account Review Account (reviewacct)"));
  });

  it("offers no help back in while viewing the panel as someone", async () => {
    // Owen, an account's owner, viewed by an admin, opens one of his users.
    const owen: PanelSession = {
      user: {
        id: "user_2",
        name: "Owen",
        username: "owen",
        home: { level: "account", id: "acct_1" },
      },
      permissions: ["*"],
      modules: {},
      stepUpMethods: ["password"],
      accounts: [],
      impersonation: {
        by: "user_1",
        name: "Ada",
        startedAt: "2026-10-15T10:00:00Z",
        endsAt: "2026-10-15T11:00:00Z",
      },
    };
    const cleo: UserResource = {
      ...adaResource,
      id: "user_3",
      scope: { level: "account", id: "acct_1" },
      spec: {
        username: "cleo",
        name: "Cleo",
        grants: [{ role: "collaborator", scope: { level: "account", id: "acct_1" } }],
        sshKeys: null,
      },
    };
    const api = fakeApi({
      ...shell,
      "GET /api/v1/session": () => json(owen),
      "GET /api/v1/core/users/user_3": () => json(cleo),
      "GET /api/v1/core/users/user_3/sessions": () => json({ sessions: [] }),
      "GET /api/v1/core/users/user_3/ssh-keys": () => json({ keys: [] }),
      "GET /api/v1/core/users/user_3/tokens": () =>
        json({
          tokens: [
            {
              id: "tok_1",
              user: "user_3",
              name: "Backups",
              hint: "rsk_abcd",
              permissions: ["*.read"],
              accounts: null,
              allowFrom: null,
              createdAt: "2026-10-15T10:00:00Z",
            },
          ],
        }),
    });
    renderApp(api, "/users/user_3");
    expect(await screen.findByRole("heading", { name: "Cleo" })).toBeInTheDocument();
    for (const name of ["Help them back in", "Make a sign-in link", "View as them"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    await userEvent.click(screen.getByRole("tab", { name: "Signing in" }));
    expect(await screen.findByText("Backups")).toBeInTheDocument();
    for (const name of ["Set a password", "Revoke"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it("names users in previews that ask for a typed confirmation", () => {
    expect(resourceName({ id: "user_9", before: { username: "bea" } })).toBe("bea");
    expect(resourceName({ id: "acct_1", after: { slug: "acme" } })).toBe("acme");
    expect(resourceName({ id: "x_1" })).toBe("x_1");
  });
});

describe("the security settings", () => {
  it("read how linking at a provider's page went", () => {
    expect(readLinkReturn("#linked=signprov_1")).toEqual({ linked: "signprov_1" });
    expect(readLinkReturn("#error=auth.identity_taken&provider=signprov_1")).toEqual({
      error: "auth.identity_taken",
      provider: "signprov_1",
    });
    expect(readLinkReturn("")).toEqual({});
  });
});

describe("roles and permissions", () => {
  it("scroll the permission matrix in a region named after it, as on a phone", async () => {
    // A phone's screen is narrower than the matrix.
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(360);
    const api = fakeApi({
      ...shell,
      "GET /api/v1/session": () => json(ada),
      "GET /api/v1/roles": () =>
        json({
          roles: [
            {
              role: "admin",
              title: "Admin",
              description: "Does everything, everywhere.",
              levels: ["server"],
              patterns: ["*"],
            },
          ],
          matrix: [
            {
              permission: "web.site.read",
              description: "See sites",
              roles: { admin: "everywhere" },
            },
          ],
          self: [],
        }),
    });
    renderApp(api, "/roles");
    const region = await screen.findByRole("region", { name: "Permission matrix" });
    expect(region).toHaveAttribute("tabindex", "0");
    const matrix = within(region).getByRole("table", { name: "Permission matrix" });
    expect(within(matrix).getByText("web.site.read")).toBeInTheDocument();
  });
});

describe("the audit log", () => {
  const intl = createIntl({ locale: "en", messages: catalog });
  const record = (actor: AuditRecord["actor"]) => ({ actor }) as AuditRecord;
  it("names the person an API token acts for, and whoever viewed the panel as them", () => {
    expect(
      who(intl, record({ type: "token", name: "deploys", userName: "ada", source: "cli" })),
    ).toBe("ada's API token deploys, with the racksail command");
    expect(
      who(
        intl,
        record({
          type: "user",
          name: "bea",
          source: "ui",
          impersonator: "user_1",
          impersonatorName: "ada",
        }),
      ),
    ).toBe("bea, in the panel, viewed by ada");
    expect(
      who(intl, record({ type: "user", name: "bea", source: "ui", impersonator: "user_1" })),
    ).toBe("bea, in the panel, viewed by user_1");
  });

  it("says how often a refusal repeated, and when it last did", () => {
    const last = "2026-10-15T11:30:00Z";
    expect(repeats(intl, { repeats: 312, lastTime: last } as AuditRecord)).toBe(
      `312 more times, the last at ${formatTime(intl, last)}`,
    );
    expect(repeats(intl, { repeats: 1, lastTime: last } as AuditRecord)).toBe(
      `Once more, the last at ${formatTime(intl, last)}`,
    );
    expect(repeats(intl, { repeats: 4 } as AuditRecord)).toBe("Happened 4 more times");
  });

  it("lists the records in a table named after it", async () => {
    const api = fakeApi({
      ...shell,
      "GET /api/v1/session": () => json(ada),
      "GET /api/v1/core/users": () => json({ items: [adaResource], next: "" }),
      "GET /api/v1/audit": () =>
        json({
          records: [
            {
              id: "aud_1",
              time: "2026-10-15T09:00:00Z",
              action: "web.site.update",
              summary: "Changed shop.example.com",
              outcome: "success",
              scope: { level: "server" },
              actor: { type: "user", id: "user_1", name: "ada", source: "ui" },
            },
          ],
          next: "",
        }),
    });
    renderApp(api, "/audit");
    const table = await screen.findByRole("table", { name: "Audit log" });
    expect(within(table).getByText("Changed shop.example.com")).toBeInTheDocument();
  });

  it("shows on a refusal's record how often it repeated", async () => {
    const refused: AuditRecord = {
      id: "aud_1",
      time: "2026-10-15T10:00:00Z",
      actor: { type: "user", id: "user_2", name: "bea", source: "api" },
      action: "core.user.read",
      scope: { level: "server" },
      outcome: "denied",
      reason: "Bea may not read users.",
      summary: "Read the users",
      repeats: 312,
      lastTime: "2026-10-15T10:59:00Z",
    };
    const done: AuditRecord = {
      id: "aud_2",
      time: "2026-10-15T09:00:00Z",
      actor: { type: "user", id: "user_1", name: "ada", source: "ui" },
      action: "core.user.create",
      scope: { level: "server" },
      outcome: "success",
      summary: "Create the user bea",
    };
    const api = fakeApi({
      ...shell,
      "GET /api/v1/session": () => json(ada),
      "GET /api/v1/core/users": () => json({ items: [adaResource], next: "" }),
      "GET /api/v1/core/accounts": () => json({ items: [], next: "" }),
      "GET /api/v1/audit": () => json({ records: [refused, done], next: "" }),
    });
    renderApp(api, "/audit");
    const row = (await screen.findByRole("button", { name: "Read the users" })).closest("tr");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("Happened 312 more times")).toBeInTheDocument();
    const other = screen.getByRole("button", { name: "Create the user bea" }).closest("tr");
    expect(within(other as HTMLElement).queryByText(/more time/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Read the users" }));
    const details = await screen.findByRole("dialog", { name: "Read the users" });
    expect(within(details).getByText("Repeated")).toBeInTheDocument();
    expect(
      within(details).getByText(
        `312 more times, the last at ${formatTime(intl, refused.lastTime)}`,
      ),
    ).toBeInTheDocument();
  });
});
