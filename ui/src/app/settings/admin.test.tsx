import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, fakeLive, json, renderShell } from "@/test/render";

afterEach(() => {
  vi.restoreAllMocks();
});

const shell = {
  "GET /api/v1/jobs": () => json({ jobs: [] }),
  "GET /api/v1/events": () => json({ events: [] }),
};

const preview = (title: string, extra: Record<string, unknown> = {}) => ({
  id: "chg_preview",
  title,
  state: "planned",
  risk: "medium",
  dryRun: true,
  createdAt: "2026-10-15T12:00:00Z",
  estimate: { downloadBytes: 4_000_000, diskBytes: 12_000_000, memoryBytes: 40 * 1024 * 1024 },
  changes: [],
  effects: [
    { type: "package", action: "install", target: "webd", downloadBytes: 4_000_000 },
    { type: "port", action: "open", target: "443/tcp" },
  ],
  ...extra,
});

const manifest = (id: string, extra: Record<string, unknown> = {}) => ({
  id,
  name: id === "net" ? "Network" : "Notes",
  description: `The ${id} module.`,
  version: "0.1.0",
  license: "AGPL-3.0-or-later",
  category: "operations",
  dependencies: {},
  profiles: ["all-in-one"],
  hardware: {},
  cost: { idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10 },
  upstream: [],
  ...extra,
});

describe("the modules page", () => {
  it("lists modules and previews turning one on before applying it", async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    const api = fakeApi({
      ...shell,
      "GET /api/v1/modules": () =>
        json({
          modules: [
            {
              manifest: manifest("net", { required: true }),
              enabled: true,
              state: "on",
              usage: { resources: 3 },
              resource: "mod_net",
            },
            {
              manifest: manifest("notes"),
              enabled: false,
              state: "off",
              usage: { resources: 0 },
              resource: "mod_notes",
              needs: ["net"],
            },
          ],
        }),
      "GET /api/v1/modules/profiles": () =>
        json({
          profiles: [
            {
              id: "all-in-one",
              name: "All-in-one",
              description: "Everything.",
              modules: ["net", "notes"],
            },
          ],
        }),
      "POST /api/v1/modules/notes/actions/turn_on": (request) => {
        calls.push(`turn_on ${new URL(request.url).search}`);
        return json(preview("Turn on Notes"));
      },
      "POST /api/v1/changesets/chg_preview/apply": () => {
        calls.push("apply");
        return json({ ...preview("Turn on Notes"), dryRun: false, state: "applying" }, 202);
      },
    });
    const { container } = renderShell({
      modules: [],
      messages: shellMessages,
      api,
      path: "/settings/modules",
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Modules" })).toBeInTheDocument();
    const notes = await screen.findByRole("region", { name: "Notes" });
    expect(within(notes).getByText("Off")).toBeInTheDocument();
    expect(within(notes).getByText("Needs net, which turn on with it.")).toBeInTheDocument();
    const net = screen.getByRole("region", { name: "Network" });
    expect(within(net).getByText("Always on")).toBeInTheDocument();
    expect(within(net).getByRole("button", { name: "Turn off" })).toBeDisabled();

    await user.click(within(notes).getByRole("button", { name: "Turn on" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Turn on Notes")).toBeInTheDocument();
    expect(calls).toEqual(["turn_on ?dryRun=true"]);
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(calls).toEqual(["turn_on ?dryRun=true", "apply"]));
    expect(await axeViolations(container)).toEqual([]);
  });

  it("asks for the module's name, and offers its resources, before removing it", async () => {
    const user = userEvent.setup();
    let sent: Record<string, unknown> | undefined;
    let exported = 0;
    const save = vi.fn();
    vi.spyOn(URL, "createObjectURL").mockImplementation(() => {
      save();
      return "blob:export";
    });
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const api = fakeApi({
      ...shell,
      "GET /api/v1/modules": () =>
        json({
          modules: [
            {
              manifest: manifest("notes", {
                upstream: [
                  {
                    id: "notesd",
                    name: "Notesd",
                    license: "MIT",
                    source: { type: "debian", packages: ["notesd"] },
                    install: "With the first note.",
                  },
                ],
              }),
              enabled: false,
              state: "off",
              usage: { resources: 2 },
              installed: ["notesd"],
              resource: "mod_notes",
              exportable: true,
            },
          ],
        }),
      "GET /api/v1/modules/profiles": () => json({ profiles: [] }),
      "GET /api/v1/modules/notes/software": () =>
        json({
          software: [
            {
              id: "notesd",
              name: "Notesd",
              license: "MIT",
              source: { type: "debian", packages: ["notesd"] },
              install: "With the first note.",
              installed: true,
              downloadBytes: 3_000_000,
              diskBytes: 9_000_000,
              packages: [{ name: "notesd", version: "1.2-1" }],
            },
          ],
        }),
      "GET /api/v1/modules/notes/export": () => {
        exported++;
        return json({
          module: "notes",
          version: "0.1.0",
          exported: "2026-10-16T08:00:00Z",
          resources: [],
        });
      },
      "POST /api/v1/modules/notes/actions/remove": async (request) => {
        sent = (await request.json()) as Record<string, unknown>;
        return json(preview("Remove Notes", { risk: "high" }));
      },
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/modules" });
    const notes = await screen.findByRole("region", { name: "Notes" });

    // What it installs, as the server says.
    await user.click(
      within(notes).getByRole("button", { name: "Installs once something needs it: Notesd" }),
    );
    expect(await within(notes).findByText("notesd 1.2-1")).toBeInTheDocument();
    expect(within(notes).getByText("Installed")).toBeInTheDocument();

    await user.click(within(notes).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("alertdialog");
    const confirm = within(dialog).getByRole("button", { name: "Preview removing it" });
    expect(confirm).toBeDisabled();
    // Without its data, its resources are offered first.
    expect(within(dialog).queryByRole("button", { name: "Download its resources" })).toBeNull();
    await user.click(within(dialog).getByRole("checkbox", { name: "Keep its data" }));
    await user.click(within(dialog).getByRole("button", { name: "Download its resources" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(exported).toBe(1);
    // The name has to be typed as it is.
    const typed = within(dialog).getByRole("textbox");
    await user.type(typed, "notes");
    expect(confirm).toBeDisabled();
    await user.clear(typed);
    await user.type(typed, "Notes");
    await user.click(confirm);
    expect(await screen.findByText("Remove Notes")).toBeInTheDocument();
    expect(sent).toEqual({ confirm: "Notes", keepData: false });
  });

  it("removes a module that can't keep its resources while it is off, with them", async () => {
    const user = userEvent.setup();
    let sent: Record<string, unknown> | undefined;
    const api = fakeApi({
      ...shell,
      "GET /api/v1/modules": () =>
        json({
          modules: [
            {
              manifest: manifest("notes"),
              enabled: true,
              state: "active",
              usage: { resources: 2 },
              resource: "mod_notes",
              exportable: true,
            },
          ],
        }),
      "GET /api/v1/modules/profiles": () => json({ profiles: [] }),
      "POST /api/v1/modules/notes/actions/remove": async (request) => {
        sent = (await request.json()) as Record<string, unknown>;
        return json(preview("Remove Notes", { risk: "high" }));
      },
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/modules" });
    // Its category's heading is translated, not its id.
    expect(
      await screen.findByRole("heading", { level: 2, name: "Operations" }),
    ).toBeInTheDocument();
    const notes = screen.getByRole("region", { name: "Notes" });
    const off = within(notes).getByRole("button", { name: "Turn off" });
    expect(off).toBeDisabled();
    expect(off).toHaveAccessibleDescription(
      "Notes can't keep its 2 resources while it is off. Delete them first, or remove it with its data.",
    );

    await user.click(within(notes).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("alertdialog");
    const keep = within(dialog).getByRole("checkbox", { name: "Keep its data" });
    expect(keep).toBeDisabled();
    expect(keep).not.toBeChecked();
    expect(keep).toHaveAccessibleDescription(
      "Notes can't keep its 2 resources while it is off, so removing it deletes them.",
    );
    expect(
      within(dialog).getByRole("button", { name: "Download its resources" }),
    ).toBeInTheDocument();
    await user.type(within(dialog).getByRole("textbox"), "Notes");
    await user.click(within(dialog).getByRole("button", { name: "Preview removing it" }));
    expect(await screen.findByText("Remove Notes")).toBeInTheDocument();
    expect(sent).toEqual({ confirm: "Notes", keepData: false });
  });

  it("follows modules as their states change", async () => {
    const { live, sources } = fakeLive();
    let state = "on";
    const api = fakeApi({
      ...shell,
      "GET /api/v1/modules": () =>
        json({
          modules: [
            {
              manifest: manifest("notes"),
              enabled: true,
              state,
              usage: { resources: state === "active" ? 1 : 0 },
              resource: "mod_notes",
            },
          ],
        }),
      "GET /api/v1/modules/profiles": () => json({ profiles: [] }),
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/modules", live });
    const notes = await screen.findByRole("region", { name: "Notes" });
    expect(within(notes).getByText("On")).toBeInTheDocument();
    const stream = await waitFor(() => {
      const found = sources.find((s) => s.url.startsWith("/api/v1/resources/stream"));
      expect(found).toBeDefined();
      return found;
    });
    expect(stream?.url).toBe("/api/v1/resources/stream?kind=core.module");
    state = "active";
    stream?.emit("notice", { op: "status", id: "mod_notes", kind: "core.module", phase: "ready" });
    expect(await within(notes).findByText("Active")).toBeInTheDocument();
  });
});

describe("the rules page", () => {
  it("tries a rule on a change and shows what it decides", async () => {
    const user = userEvent.setup();
    let sent: Record<string, unknown> | undefined;
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/policies": () =>
        json({
          items: [
            {
              id: "pol_1",
              kind: "core.policy",
              revision: 1,
              generation: 1,
              scope: { level: "server" },
              createdAt: "2026-10-15T12:00:00Z",
              updatedAt: "2026-10-15T12:00:00Z",
              spec: {
                name: "No test domains",
                type: "validate",
                kinds: ["web.site"],
                enabled: true,
              },
            },
          ],
        }),
      "POST /api/v1/rules/test": async (request) => {
        sent = (await request.json()) as Record<string, unknown>;
        return json({
          applies: true,
          refused: true,
          message: "Test domains aren't allowed.",
          cost: 12,
          costLimit: 100000,
          points: ["web.site.create.validate"],
        });
      },
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/rules" });
    expect(await screen.findByRole("heading", { level: 1, name: "Rules" })).toBeInTheDocument();
    expect(await screen.findByText("No test domains")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Server rules" })).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Name" }), "Try");
    await user.type(screen.getByRole("textbox", { name: "Kind" }), "web.site");
    await user.type(
      screen.getByRole("textbox", { name: "Expression" }),
      'resource.spec.domain.endsWith(".test")',
    );
    await user.click(screen.getByRole("button", { name: "Try it" }));
    expect(await screen.findByText("The rule refuses the change")).toBeInTheDocument();
    expect(screen.getByText("It cost 12 of the 100,000 an evaluation may.")).toBeInTheDocument();
    expect(sent?.kind).toBe("web.site");
    expect((sent?.rule as Record<string, unknown> | undefined)?.type).toBe("validate");

    // Settings that aren't JSON are caught before anything is sent.
    sent = undefined;
    const spec = screen.getByRole("textbox", { name: "Settings after the change, as JSON" });
    await user.clear(spec);
    await user.type(spec, "not json");
    await user.click(screen.getByRole("button", { name: "Try it" }));
    expect(await screen.findByText("The settings aren't valid JSON.")).toBeInTheDocument();
    expect(sent).toBeUndefined();
  });

  it("lists hooks and points, switches a hook back on and turns safe mode on and off", async () => {
    const user = userEvent.setup();
    let breakerOff = true;
    let enabled: unknown;
    let safe: Record<string, unknown> = { active: false };
    const guard = () => ({
      owner: "addon:site-guard",
      label: "Site guard 1.2.0",
      kind: "validate",
      point: "web.site.deploy.validate",
      priority: 100,
      failures: breakerOff ? 5 : 0,
      disabled: breakerOff,
    });
    const caddy = {
      owner: "module:web",
      kind: "system",
      point: "system.caddy.config",
      priority: 0,
    };
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/policies": () => json({ items: [] }),
      "GET /api/v1/hooks": () => json({ hooks: [caddy, guard()] }),
      "POST /api/v1/hooks/enable": async (request) => {
        enabled = await request.json();
        breakerOff = false;
        return new Response(null, { status: 204 });
      },
      "GET /api/v1/hooks/points": (request) => {
        const prefix = new URL(request.url).searchParams.get("prefix") ?? "";
        const points = [
          {
            point: "system.caddy.config",
            description: "Caddy's configuration",
            config: true,
            checked: true,
            invariants: ["admin.listen"],
            hooks: [caddy],
          },
          {
            point: "web.site.deploy.validate",
            kind: "web.site",
            operation: "deploy",
            stage: "validate",
            hooks: [guard()],
          },
          { point: "web.site.delete.validate", kind: "web.site", operation: "delete" },
        ];
        return json({ points: points.filter((p) => p.point.startsWith(prefix)) });
      },
      "GET /api/v1/safe-mode": () => json(safe),
      "POST /api/v1/safe-mode": async (request) => {
        const { reason } = (await request.json()) as { reason: string };
        safe = {
          active: true,
          sources: [{ kind: "console", reason, since: "2026-10-15T12:00:00Z" }],
        };
        return json(safe);
      },
      "DELETE /api/v1/safe-mode": () => {
        safe = { active: false };
        return json(safe);
      },
    });
    const { container } = renderShell({
      modules: [],
      messages: shellMessages,
      api,
      path: "/settings/rules",
    });

    const hooks = await screen.findByRole("region", { name: "Hooks" });
    expect(await within(hooks).findByText("Site guard 1.2.0")).toBeInTheDocument();
    expect(within(hooks).getByRole("table", { name: "Hooks" })).toBeInTheDocument();
    expect(within(hooks).getByText("Patches configuration")).toBeInTheDocument();
    expect(within(hooks).getByText("Switched off")).toBeInTheDocument();
    await user.click(
      within(hooks).getByRole("button", {
        name: "Switch Site guard 1.2.0 back on at web.site.deploy.validate",
      }),
    );
    await waitFor(() => expect(within(hooks).queryByText("Switched off")).not.toBeInTheDocument());
    expect(enabled).toEqual({ owner: "addon:site-guard", point: "web.site.deploy.validate" });

    const points = await screen.findByRole("region", { name: "Hook points" });
    expect(await within(points).findByText("system.caddy.config")).toBeInTheDocument();
    expect(
      within(points).getByText("Configuration, checked before it is kept"),
    ).toBeInTheDocument();
    expect(
      within(points).getByText("Hooks can only make these stricter: admin.listen"),
    ).toBeInTheDocument();
    expect(within(points).getByText("Nothing attached")).toBeInTheDocument();
    await user.type(
      within(points).getByRole("textbox", { name: "Points starting with" }),
      "web.site.de",
    );
    await waitFor(() =>
      expect(within(points).queryByText("system.caddy.config")).not.toBeInTheDocument(),
    );
    expect(within(points).getByText("web.site.deploy.validate")).toBeInTheDocument();
    expect(within(points).getByText("web.site.delete.validate")).toBeInTheDocument();

    const safeMode = screen.getByRole("region", { name: "Safe mode" });
    expect(within(safeMode).getByText("Safe mode is off, and add-ons run.")).toBeInTheDocument();
    const turnOn = within(safeMode).getByRole("button", { name: "Turn on safe mode" });
    expect(turnOn).toBeDisabled();
    await user.type(
      within(safeMode).getByRole("textbox", { name: "Why" }),
      "An add-on keeps failing",
    );
    await user.click(turnOn);
    expect(
      await within(safeMode).findByText("Safe mode is on, so no add-on runs"),
    ).toBeInTheDocument();
    expect(
      within(safeMode).getByText(
        "Turned on from the console or this panel: An add-on keeps failing",
        { exact: false },
      ),
    ).toBeInTheDocument();
    expect(await axeViolations(container)).toEqual([]);
    await user.click(within(safeMode).getByRole("button", { name: "Turn off safe mode" }));
    expect(
      await within(safeMode).findByText("Safe mode is off, and add-ons run."),
    ).toBeInTheDocument();
  });

  it("keeps and tries the rules of a reseller or an account", async () => {
    const user = userEvent.setup();
    let listed = "";
    let sent: Record<string, unknown> | undefined;
    let created: Record<string, unknown> | undefined;
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/policies": () => json({ items: [] }),
      "GET /api/v1/core/account-policies": (request) => {
        listed = new URL(request.url).searchParams.get("scope") ?? "";
        return json({
          items: [
            {
              id: "apol_1",
              kind: "core.account_policy",
              revision: 1,
              generation: 1,
              scope: { level: "account", id: "acct_1" },
              createdAt: "2026-10-15T12:00:00Z",
              updatedAt: "2026-10-15T12:00:00Z",
              spec: { name: "Small mailboxes", type: "default", enabled: true },
            },
          ],
        });
      },
      "POST /api/v1/rules/test": async (request) => {
        sent = (await request.json()) as Record<string, unknown>;
        return json({ applies: false, cost: 0, costLimit: 100000 });
      },
      "POST /api/v1/core/account-policies": async (request) => {
        created = (await request.json()) as Record<string, unknown>;
        return json(preview("Create account rule"));
      },
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/rules" });
    await screen.findByRole("heading", { level: 2, name: "Server rules" });
    const [owner] = screen.getAllByRole("combobox", { name: "Rules of" });
    if (!owner) {
      throw new Error("no owner field");
    }
    await user.click(owner);
    await user.click(await screen.findByRole("option", { name: "An account" }));
    expect(screen.getByText("Give the account's ID to see its rules.")).toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "The account's ID" }), "acct_1");
    expect(await screen.findByText("Small mailboxes")).toBeInTheDocument();
    expect(listed).toBe("account:acct_1");
    expect(
      screen.getByRole("heading", { level: 2, name: "The account's rules" }),
    ).toBeInTheDocument();

    await user.type(screen.getByRole("textbox", { name: "Name" }), "Quota");
    await user.type(screen.getByRole("textbox", { name: "Kind" }), "mail.mailbox");
    await user.click(screen.getByRole("button", { name: "Try it" }));
    expect(await screen.findByText("The rule doesn't apply to this change.")).toBeInTheDocument();
    expect(sent?.owner).toEqual({ level: "account", id: "acct_1" });
    expect(sent?.scope).toEqual({ level: "account", id: "acct_1" });
    await user.click(screen.getByRole("button", { name: "Save as the account's rule" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(created?.scope).toEqual({ level: "account", id: "acct_1" });
  });

  it("marks the hooks that hold core.override", async () => {
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/policies": () => json({ items: [] }),
      "GET /api/v1/hooks": () =>
        json({
          hooks: [
            {
              owner: "addon:cdn",
              label: "CDN 2.0.0",
              kind: "system",
              point: "system.caddy.config",
              priority: 0,
              override: true,
            },
            { owner: "module:web", kind: "system", point: "system.caddy.config", priority: 0 },
          ],
        }),
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/rules" });
    const hooks = await screen.findByRole("region", { name: "Hooks" });
    expect(await within(hooks).findByText("CDN 2.0.0")).toBeInTheDocument();
    expect(within(hooks).getAllByText("Holds core.override")).toHaveLength(1);
  });

  it("says safe mode from the boot option lasts until the server starts without it", async () => {
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/policies": () => json({ items: [] }),
      "GET /api/v1/safe-mode": () =>
        json({
          active: true,
          sources: [{ kind: "boot", reason: "The server was started with the boot option." }],
        }),
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/rules" });
    const safeMode = await screen.findByRole("region", { name: "Safe mode" });
    expect(
      within(safeMode).getByText("The server was started with the racksail.safe_mode boot option."),
    ).toBeInTheDocument();
    expect(
      within(safeMode).getByText(
        "The boot option keeps safe mode on until the server starts without it.",
      ),
    ).toBeInTheDocument();
    expect(within(safeMode).queryByRole("button", { name: "Turn off safe mode" })).toBeNull();
    // Without core.hook.read the hooks and their points stay out of sight.
    expect(screen.queryByRole("region", { name: "Hooks" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Hook points" })).toBeNull();
  });
});

describe("the notifications page", () => {
  it("saves your preferences and lists the server's channels", async () => {
    const user = userEvent.setup();
    let saved: Record<string, unknown> | undefined;
    const puts: string[] = [];
    const api = fakeApi({
      ...shell,
      "GET /api/v1/notifications/preferences": () => json({ email: false }),
      "PUT /api/v1/notifications/preferences": async (request) => {
        const dryRun = new URL(request.url).searchParams.get("dryRun") === "true";
        puts.push(dryRun ? "preview" : "apply");
        saved = (await request.json()) as Record<string, unknown>;
        return dryRun
          ? json(preview("Change notification preferences", { risk: "low" }))
          : json({ ...preview("Change notification preferences"), dryRun: false }, 202);
      },
      "GET /api/v1/core/notification-channels": () =>
        json({
          items: [
            {
              id: "nch_1",
              kind: "core.notification_channel",
              revision: 1,
              generation: 1,
              scope: { level: "server" },
              createdAt: "2026-10-15T12:00:00Z",
              updatedAt: "2026-10-15T12:00:00Z",
              spec: { name: "Ops on ntfy", type: "ntfy", events: ["*.failed"] },
              status: { lastError: "the server answered 401 Unauthorized" },
            },
          ],
        }),
      "GET /api/v1/core/mail-relays": () => json({ items: [] }),
      "GET /api/v1/notifications": () => json({ notifications: [] }),
      "GET /api/v1/notifications/types": () => json({ types: [] }),
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/notifications" });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Notifications" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("Ops on ntfy")).toBeInTheDocument();
    expect(screen.getByText("the server answered 401 Unauthorized")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Channels" })).toBeInTheDocument();

    await user.click(screen.getByRole("switch", { name: "Email me" }));
    const [events] = screen.getAllByRole("textbox", { name: "Events" });
    if (!events) {
      throw new Error("no events field");
    }
    await user.type(events, "*.failed, security.*");
    await user.click(screen.getByRole("switch", { name: "Quiet hours" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    // Like every change, it is previewed first, then sent again to apply.
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Change notification preferences")).toBeInTheDocument();
    expect(saved?.email).toBe(true);
    expect(saved?.events).toEqual(["*.failed", "security.*"]);
    expect(saved?.quietHours).toMatchObject({ start: "22:00", end: "07:00" });
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(puts).toEqual(["preview", "apply"]));
  });

  it("says when personal notifications aren't available, and hides what you may not see", async () => {
    const forbidden = () =>
      json(
        { type: "about:blank", status: 403, code: "notify.not_a_person", title: "Forbidden" },
        403,
      );
    const api = fakeApi({
      ...shell,
      "GET /api/v1/notifications/preferences": forbidden,
      "GET /api/v1/core/notification-channels": () =>
        json(
          { type: "about:blank", status: 403, code: "core.forbidden", params: { action: "read" } },
          403,
        ),
      "GET /api/v1/core/mail-relays": () =>
        json(
          { type: "about:blank", status: 403, code: "core.forbidden", params: { action: "read" } },
          403,
        ),
      "GET /api/v1/notifications": () => json({ notifications: [] }),
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/notifications" });
    expect(
      await screen.findByText("Personal notifications aren't available here"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Channels" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Mail relay" })).not.toBeInTheDocument();
  });
});

describe("the updates page", () => {
  it("shows the release found and previews installing it", async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/update-settings": () =>
        json({
          items: [
            {
              id: "upd_1",
              kind: "core.update_settings",
              revision: 2,
              generation: 1,
              scope: { level: "server" },
              createdAt: "2026-10-15T12:00:00Z",
              updatedAt: "2026-10-15T12:00:00Z",
              spec: { channel: "stable" },
              status: {
                running: "1.2.0",
                available: {
                  tag: "v1.3.0",
                  version: "1.3.0",
                  channel: "stable",
                  published: "2026-10-14T09:00:00Z",
                  notesUrl: "https://github.com/vilanobeachflorida/racksail/releases/tag/v1.3.0",
                  asset: {
                    name: "racksail-linux-amd64",
                    url: "https://example.com/r",
                    size: 1,
                    sha256: "a",
                  },
                },
                os: { codename: "trixie", version: "13", next: "forky" },
              },
            },
          ],
        }),
      "POST /api/v1/core/update-settings/upd_1/actions/install": async (request) => {
        calls.push(
          `install ${new URL(request.url).search} ${JSON.stringify(await request.json())}`,
        );
        return json(preview("Install Racksail 1.3.0", { risk: "high" }));
      },
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/updates" });
    expect(await screen.findByRole("heading", { level: 1, name: "Updates" })).toBeInTheDocument();
    expect(await screen.findByText("Racksail 1.3.0 is available")).toBeInTheDocument();
    expect(screen.getByText("This server runs Racksail 1.2.0.")).toBeInTheDocument();
    expect(screen.getByText("This server runs Debian 13 (trixie).")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview upgrading to forky" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Preview installing it" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(calls).toEqual(['install ?dryRun=true {"version":"1.3.0"}']);
  });

  it("says how Debian's packages are kept up to date and saves the settings", async () => {
    const user = userEvent.setup();
    let saved: Record<string, unknown> | undefined;
    const calls: string[] = [];
    const api = fakeApi({
      ...shell,
      "GET /api/v1/core/update-settings": () =>
        json({
          items: [
            {
              id: "upd_1",
              kind: "core.update_settings",
              revision: 2,
              generation: 1,
              scope: { level: "server" },
              createdAt: "2026-10-15T12:00:00Z",
              updatedAt: "2026-10-15T12:00:00Z",
              spec: { channel: "stable", packages: { security: true } },
              status: {
                running: "1.2.0",
                os: {
                  codename: "trixie",
                  version: "13",
                  rebootRequired: true,
                  rebootReasons: ["kernel 6.12.48+deb13-amd64 is installed"],
                  rebootAt: "2026-10-16T04:00:00Z",
                },
                packages: { at: "2026-10-15T03:10:00Z", upgraded: 3 },
              },
            },
          ],
        }),
      "PUT /api/v1/core/update-settings/upd_1": async (request) => {
        saved = (await request.json()) as Record<string, unknown>;
        return json(preview("Change the update settings"));
      },
      "POST /api/v1/core/update-settings/upd_1/actions/upgrade_packages": (request) => {
        calls.push(`upgrade_packages ${new URL(request.url).search}`);
        return json(preview("Upgrade the server's packages"));
      },
    });
    renderShell({ modules: [], messages: shellMessages, api, path: "/settings/updates" });
    const debian = await screen.findByRole("region", { name: "Debian" });
    expect(
      within(debian).getByText("The server needs a reboot to finish an upgrade."),
    ).toBeInTheDocument();
    expect(within(debian).getByText("kernel 6.12.48+deb13-amd64 is installed")).toBeInTheDocument();
    expect(within(debian).getByText(/The server reboots by itself/)).toBeInTheDocument();
    expect(
      within(debian).getByText(/The last upgrade of the packages, .*, installed 3 updates\./),
    ).toBeInTheDocument();
    await user.click(
      within(debian).getByRole("button", { name: "Preview upgrading the packages" }),
    );
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(calls).toEqual(["upgrade_packages ?dryRun=true"]);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    const security = screen.getByRole("switch", {
      name: "Install Debian's security updates as they come out",
    });
    expect(security).toBeChecked();
    // The window shows once updates install on a schedule.
    expect(screen.queryByLabelText("From")).toBeNull();
    await user.click(
      screen.getByRole("switch", {
        name: "Install every other update once a day, after snapshots of the system",
      }),
    );
    expect(screen.getByLabelText("From")).toBeInTheDocument();
    await user.click(
      screen.getByRole("switch", { name: "Reboot by itself when an update needs it" }),
    );
    // Time fields take a whole value at once, as a browser's picker gives it.
    fireEvent.change(screen.getByLabelText("Reboot at (UTC)"), { target: { value: "02:30" } });
    await user.click(screen.getByRole("button", { name: "Preview the change" }));
    await waitFor(() => expect(saved).toBeDefined());
    const spec = (saved as { spec: Record<string, unknown> }).spec;
    expect(spec.packages).toEqual({
      security: true,
      scheduled: true,
      reboot: true,
      rebootTime: "02:30",
    });
  });
});

describe("the notifications page, live", () => {
  it("follows the relay's status without losing what you're typing", async () => {
    const user = userEvent.setup();
    const { live, sources } = fakeLive();
    let lastSent: string | undefined;
    let reads = 0;
    const api = fakeApi({
      ...shell,
      "GET /api/v1/notifications/preferences": () => json({ email: false }),
      "GET /api/v1/core/notification-channels": () => json({ items: [] }),
      "GET /api/v1/core/mail-relays": () => {
        reads++;
        return json({
          items: [
            {
              id: "smtp_1",
              kind: "core.smtp_relay",
              revision: 1,
              generation: 1,
              scope: { level: "server" },
              createdAt: "2026-10-15T12:00:00Z",
              updatedAt: "2026-10-15T12:00:00Z",
              spec: { host: "smtp.example.com", from: "panel@example.com", security: "starttls" },
              status: lastSent ? { lastSent } : {},
            },
          ],
        });
      },
      "GET /api/v1/notifications": () => json({ notifications: [] }),
    });
    renderShell({
      modules: [],
      messages: shellMessages,
      api,
      path: "/settings/notifications",
      live,
    });
    const host = await screen.findByRole("textbox", { name: "Server" });
    await waitFor(() => expect(host).toHaveValue("smtp.example.com"));
    await user.clear(host);
    await user.type(host, "relay.example.net");
    const stream = await waitFor(() => {
      const found = sources.find((s) => s.url.includes("kind=core.smtp_relay"));
      expect(found).toBeDefined();
      return found;
    });
    // An email went out: the relay's status changed, its settings didn't.
    const before = reads;
    lastSent = "2026-10-16T08:00:00Z";
    stream?.emit("notice", { op: "status", id: "smtp_1", kind: "core.smtp_relay", phase: "ready" });
    expect(await screen.findByText(/The last email went out/)).toBeInTheDocument();
    expect(reads).toBeGreaterThan(before);
    expect(screen.getByRole("textbox", { name: "Server" })).toHaveValue("relay.example.net");
  });

  it("reads the delivery log again as events arrive and waiting deliveries come due", async () => {
    const { live, sources } = fakeLive();
    const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
    const delivery = (id: string, title: string, state: string, extra = {}) => ({
      id,
      title,
      state,
      severity: "warning",
      attemptCount: 0,
      person: "usr_1",
      createdAt: ago(60_000),
      ...extra,
    });
    // Held for quiet hours, which have just ended.
    let log = [delivery("ntf_1", "Backup failed", "held", { nextAttempt: ago(500) })];
    let reads = 0;
    const api = fakeApi({
      ...shell,
      "GET /api/v1/notifications/preferences": () => json({ email: true }),
      "GET /api/v1/core/notification-channels": () => json({ items: [] }),
      "GET /api/v1/core/mail-relays": () => json({ items: [] }),
      "GET /api/v1/notifications": () => {
        reads++;
        return json({ notifications: log });
      },
    });
    renderShell({
      modules: [],
      messages: shellMessages,
      api,
      path: "/settings/notifications",
      live,
    });
    expect(await screen.findByText("Held for quiet hours")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Sent" })).toBeInTheDocument();
    log = [delivery("ntf_1", "Backup failed", "merged", { finishedAt: ago(0) })];
    expect(await screen.findByText("In a summary", {}, { timeout: 3000 })).toBeInTheDocument();

    // Events turn into deliveries; however many arrive at once, the log
    // reads again once.
    const events = sources.find((s) => s.url.includes("/api/v1/events/stream"));
    expect(events).toBeDefined();
    const before = reads;
    log = [delivery("ntf_2", "The disk is almost full", "sent"), ...log];
    for (const id of ["evt_1", "evt_2", "evt_3"]) {
      events?.emit("event", {
        id,
        type: "storage.disk.filling",
        time: ago(0),
        severity: "warning",
        scope: { level: "server" },
        message: "The disk is almost full.",
      });
    }
    expect(
      await screen.findByText("The disk is almost full", {}, { timeout: 4000 }),
    ).toBeInTheDocument();
    expect(reads).toBe(before + 1);
  });
});

describe("the settings index", () => {
  it("links the server's settings", async () => {
    renderShell({ modules: [], messages: shellMessages, api: fakeApi(shell), path: "/settings" });
    for (const name of ["Modules", "Rules", "Updates", "Notifications"]) {
      expect(await screen.findByRole("link", { name: new RegExp(`^${name}`) })).toBeInTheDocument();
    }
  });
});
