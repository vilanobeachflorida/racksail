import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Globe, Plus } from "lucide-react";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { pseudoMessage } from "@/lib/pseudo";
import type { ModuleUI } from "@/modules/types";
import { untranslatedText } from "@/test/pseudo";
import { fakeApi, json, renderApp, renderShell } from "@/test/render";

const version = {
  version: "1.2.3",
  channel: "stable",
  goVersion: "go1.27.0",
  os: "linux",
  arch: "arm64",
};

function panel(health: () => Response | Promise<Response>) {
  return fakeApi({
    "GET /api/v1/health": health,
    "GET /api/v1/version": () => json(version),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events": () => json({ events: [] }),
  });
}

describe("the shell", () => {
  it("shows the mark, the name and the panel's status", async () => {
    renderApp(panel(() => json({ status: "ok", checks: [{ name: "agent", status: "ok" }] })));
    expect(await screen.findByRole("link", { name: "Racksail" })).toHaveAttribute("href", "/");
    expect(screen.getByRole("navigation", { name: "Main" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
    expect(await screen.findByText("Everything is working.")).toBeInTheDocument();
    expect(screen.getByText("agent: working")).toBeInTheDocument();
    expect(await screen.findByText("Racksail 1.2.3, stable channel")).toBeInTheDocument();
    expect(screen.getByText("Built with go1.27.0 for linux/arm64")).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("Home · Racksail"));
    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
  });

  it("lists failing health checks", async () => {
    renderApp(
      panel(() =>
        json(
          {
            status: "degraded",
            checks: [{ name: "agent", status: "failing", message: "the socket is gone" }],
          },
          503,
        ),
      ),
    );
    expect(await screen.findByText("Something needs attention.")).toBeInTheDocument();
    expect(screen.getByText("agent: the socket is gone")).toBeInTheDocument();
  });

  it("says so when the panel doesn't answer", async () => {
    renderApp(
      panel(() => {
        throw new TypeError("Failed to fetch");
      }),
    );
    expect(await screen.findByText(/The panel isn't answering/)).toBeInTheDocument();
  });

  it("has a page for addresses that lead nowhere", async () => {
    renderApp(
      panel(() => json({ status: "ok", checks: [] })),
      "/nowhere",
    );
    expect(
      await screen.findByRole("heading", { name: "This page doesn't exist" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Go to the home screen" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
  });

  it("lets keyboard users skip to the content and the navigation", async () => {
    renderApp(panel(() => json({ status: "ok", checks: [] })));
    await screen.findByRole("heading", { level: 1, name: "Home" });
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Skip to content" })).toHaveFocus();
    expect(screen.getByRole("main")).toHaveAttribute("id", "content");
    await userEvent.tab();
    // Narrow screens keep the navigation in the menu sheet.
    expect(screen.getByRole("link", { name: "Skip to navigation" })).toHaveAttribute(
      "href",
      "#menu-button",
    );
    expect(document.getElementById("menu-button")).toHaveAccessibleName("Open the menu");
  });
});

describe("module contributions", () => {
  const web: ModuleUI = {
    id: "web",
    name: { id: "web.name" },
    description: { id: "web.description" },
    icon: Globe,
    nav: [{ id: "sites", label: { id: "web.sites" }, icon: Globe, to: "/sites" }],
    routes: [
      {
        path: "/sites",
        title: { id: "web.sites" },
        screen: async () => ({ default: () => <h1>All sites</h1> }),
      },
    ],
    homeCards: [
      {
        id: "summary",
        title: { id: "web.summary" },
        card: async () => ({ default: () => <p>Three sites are running.</p> }),
      },
    ],
    settings: [
      {
        id: "php",
        title: { id: "web.php" },
        page: async () => ({ default: () => <h1>PHP defaults</h1> }),
      },
    ],
    commands: [
      {
        id: "new-site",
        label: { id: "web.newSite" },
        icon: Plus,
        permission: "web.site.create",
        run: ({ navigate }) => navigate("/web/sites"),
      },
    ],
  };
  const messages = {
    ...shellMessages,
    "web.name": "Web hosting",
    "web.description": "Sites.",
    "web.sites": "Sites",
    "web.summary": "Sites at a glance",
    "web.php": "PHP",
    "web.newSite": "Create a site",
  };
  const api = panel(() => json({ status: "ok", checks: [] }));

  it("puts navigation entries in the rail and cards on the home screen", async () => {
    renderShell({ modules: [web], messages, api });
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Sites" })).toHaveAttribute("href", "/web/sites");
    expect(within(nav).getByRole("link", { name: "Settings" })).toHaveAttribute(
      "href",
      "/settings",
    );
    expect(await screen.findByText("Three sites are running.")).toBeInTheDocument();
  });

  it("mounts screens under the module's path, with a breadcrumb", async () => {
    renderShell({ modules: [web], messages, api });
    const nav = await screen.findByRole("navigation", { name: "Main" });
    await userEvent.click(within(nav).getByRole("link", { name: "Sites" }));
    expect(await screen.findByRole("heading", { name: "All sites" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Sites" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    await waitFor(() => expect(document.title).toBe("Sites · Racksail"));
    const trail = screen.getByRole("navigation", { name: "Where you are" });
    expect(within(trail).getByRole("link", { name: "Web hosting" })).toHaveAttribute(
      "href",
      "/web",
    );
    expect(within(trail).getByText("Sites")).toHaveAttribute("aria-current", "page");
  });

  it("mounts settings pages under /settings and lists them", async () => {
    const { router } = renderShell({ modules: [web], messages, path: "/settings/web/php", api });
    expect(await screen.findByRole("heading", { name: "PHP defaults" })).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("PHP · Racksail"));
    await router.navigate({ to: "/settings" });
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /PHP/ })).toHaveAttribute("href", "/settings/web/php");
    expect(screen.getByRole("link", { name: /^Appearance/ })).toHaveAttribute(
      "href",
      "/settings/appearance",
    );
  });

  it("leaves out modules that are off and entries the person may not use", async () => {
    renderShell({ modules: [web], messages, api, states: { web: "off" } });
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).queryByRole("link", { name: "Sites" })).not.toBeInTheDocument();
    expect(screen.queryByText("Three sites are running.")).not.toBeInTheDocument();
  });

  it("offers module commands in the command bar to people allowed to run them", async () => {
    const user = userEvent.setup();
    renderShell({ modules: [web], messages, api, can: (p) => p !== "web.site.create" });
    await screen.findByRole("navigation", { name: "Main" });
    await user.keyboard("{Control>}k{/Control}");
    expect(await screen.findByRole("dialog", { name: "Command bar" })).toBeInTheDocument();
    expect(screen.queryByText("Create a site")).not.toBeInTheDocument();
  });
});

describe("pseudo-locales", () => {
  const api = panel(() => json({ status: "ok", checks: [] }));

  it("show the whole panel accented and longer, with every message intact", async () => {
    renderApp(api, "/", { locale: "en-XA" });
    expect(
      await screen.findByRole("heading", { level: 1, name: pseudoMessage("Home", "en-XA") }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("navigation", { name: pseudoMessage("Main", "en-XA") }),
    ).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en-XA");
    expect(document.documentElement.dir).toBe("ltr");
  });

  it("leave nothing on the panel's own screens outside the catalogs", async () => {
    const user = userEvent.setup();
    const x = (text: string) => pseudoMessage(text, "en-XA");
    const greek = fakeApi({
      "GET /api/v1/health": () =>
        json(
          {
            status: "degraded",
            checks: [{ name: "πράκτορας", status: "failing", message: "χάθηκε" }],
          },
          503,
        ),
      "GET /api/v1/version": () => json(version),
      "GET /api/v1/jobs": () =>
        json({
          jobs: [
            {
              id: "job_1",
              type: "changeset.apply",
              title: "Εφαρμογή",
              state: "running",
              progress: { done: 1, total: 3, message: "Εγκατάσταση" },
              attempt: 1,
              createdAt: "2026-10-15T12:00:00Z",
            },
          ],
        }),
      "GET /api/v1/events": () =>
        json({
          events: [
            {
              id: "evt_1",
              type: "δοκιμή.συνέβη",
              time: "2026-10-15T12:00:00Z",
              severity: "warning",
              scope: { level: "server" },
              message: "Κάτι συνέβη",
              actor: { type: "user", name: "Άννα", source: "ui" },
            },
          ],
        }),
    });
    const { unmount } = renderApp(greek, "/", { locale: "en-XA" });
    await screen.findByRole("heading", { level: 1, name: x("Home") });
    await screen.findByText(/χάθηκε/);
    await screen.findByText(/Κάτι συνέβη/);
    expect(untranslatedText()).toEqual([]);

    await user.keyboard("{Control>}j{/Control}");
    await screen.findByText("Εφαρμογή");
    expect(untranslatedText()).toEqual([]);
    await user.keyboard("{Escape}");

    await user.keyboard("{Control>}k{/Control}");
    await screen.findByRole("dialog", { name: x("Command bar") });
    expect(untranslatedText()).toEqual([]);
    await user.keyboard("{Escape}");

    await user.keyboard("?");
    await screen.findByRole("dialog", { name: x("Keyboard shortcuts") });
    expect(untranslatedText()).toEqual([]);
    unmount();

    for (const path of ["/settings", "/settings/general", "/settings/appearance"]) {
      const { unmount: done } = renderApp(greek, path, { locale: "en-XA" });
      await screen.findByRole("heading", { level: 1 });
      expect(untranslatedText(), path).toEqual([]);
      done();
    }

    renderApp(greek, "/settings/appearance/theme", { locale: "en-XA" });
    await screen.findByRole("heading", { level: 1, name: x("Theme editor") });
    // A problem of every kind, so each message shows.
    const text = screen.getByRole("textbox", { name: x("Text") });
    await user.clear(text);
    await user.type(text, "#FFFFFF");
    await user.click(screen.getByRole("combobox", { name: x("Start from") }));
    await screen.findByRole("listbox");
    expect(untranslatedText()).toEqual([]);
    await user.keyboard("{Escape}");
    const tabs = within(screen.getAllByRole("tablist")[0] as HTMLElement).getAllByRole("tab");
    for (const tab of tabs) {
      await user.click(tab);
      expect(untranslatedText(), tab.textContent ?? "").toEqual([]);
    }
    const checker = screen.getByRole("region", { name: x("Contrast checker") });
    for (const tab of within(checker).getAllByRole("tab")) {
      await user.click(tab);
      expect(untranslatedText(), tab.textContent ?? "").toEqual([]);
    }
  });

  it("lay the panel out right to left", async () => {
    renderApp(api, "/settings", { locale: "ar-XB" });
    expect(
      await screen.findByRole("heading", { level: 1, name: pseudoMessage("Settings", "ar-XB") }),
    ).toBeInTheDocument();
    expect(document.documentElement.dir).toBe("rtl");
  });

  it("can be chosen in the general settings and are remembered", async () => {
    const user = userEvent.setup();
    renderApp(api, "/settings/general", { locale: undefined });
    await screen.findByRole("heading", { level: 1, name: "General" });
    await user.click(screen.getByRole("combobox", { name: "Language" }));
    await user.click(await screen.findByRole("option", { name: "en-XA" }));
    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: pseudoMessage("General", "en-XA"),
      }),
    ).toBeInTheDocument();
    expect(window.localStorage.getItem("racksail.locale")).toBe("en-XA");
    expect(document.documentElement.lang).toBe("en-XA");
  });
});
