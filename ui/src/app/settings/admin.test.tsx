import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderShell } from "@/test/render";

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
      "POST /api/v1/core/modules/mod_notes/actions/turn_on": (request) => {
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
    const api = fakeApi({
      ...shell,
      "GET /api/v1/notifications/preferences": () => json({ email: false }),
      "PUT /api/v1/notifications/preferences": async (request) => {
        saved = (await request.json()) as Record<string, unknown>;
        return json({ ...saved, updatedAt: "2026-10-15T12:00:00Z" });
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

    await user.click(screen.getByRole("switch", { name: "Email me" }));
    const [events] = screen.getAllByRole("textbox", { name: "Events" });
    if (!events) {
      throw new Error("no events field");
    }
    await user.type(events, "*.failed, security.*");
    await user.click(screen.getByRole("switch", { name: "Quiet hours" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved?.email).toBe(true);
    expect(saved?.events).toEqual(["*.failed", "security.*"]);
    expect(saved?.quietHours).toMatchObject({ start: "22:00", end: "07:00" });
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

describe("the settings index", () => {
  it("links the server's settings", async () => {
    renderShell({ modules: [], messages: shellMessages, api: fakeApi(shell), path: "/settings" });
    for (const name of ["Modules", "Rules", "Updates", "Notifications"]) {
      expect(await screen.findByRole("link", { name: new RegExp(`^${name}`) })).toBeInTheDocument();
    }
  });
});
