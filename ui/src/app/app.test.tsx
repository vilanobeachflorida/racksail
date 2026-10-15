import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Globe, Plus } from "lucide-react";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import type { ModuleUI } from "@/modules/types";
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
    renderApp(panel(() => json({ status: "ok", checks: [] })), "/nowhere");
    expect(await screen.findByRole("heading", { name: "This page doesn't exist" })).toBeInTheDocument();
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
    expect(screen.getByRole("link", { name: "Skip to navigation" })).toHaveAttribute("href", "#menu-button");
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
    expect(within(nav).getByRole("link", { name: "Settings" })).toHaveAttribute("href", "/settings");
    expect(await screen.findByText("Three sites are running.")).toBeInTheDocument();
  });

  it("mounts screens under the module's path, with a breadcrumb", async () => {
    renderShell({ modules: [web], messages, api });
    const nav = await screen.findByRole("navigation", { name: "Main" });
    await userEvent.click(within(nav).getByRole("link", { name: "Sites" }));
    expect(await screen.findByRole("heading", { name: "All sites" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Sites" })).toHaveAttribute("aria-current", "page");
    await waitFor(() => expect(document.title).toBe("Sites · Racksail"));
    const trail = screen.getByRole("navigation", { name: "Where you are" });
    expect(within(trail).getByRole("link", { name: "Web hosting" })).toHaveAttribute("href", "/web");
    expect(within(trail).getByText("Sites")).toHaveAttribute("aria-current", "page");
  });

  it("mounts settings pages under /settings and lists them", async () => {
    const { router } = renderShell({ modules: [web], messages, path: "/settings/web/php", api });
    expect(await screen.findByRole("heading", { name: "PHP defaults" })).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("PHP · Racksail"));
    await router.navigate({ to: "/settings" });
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /PHP/ })).toHaveAttribute("href", "/settings/web/php");
    expect(screen.getByRole("link", { name: /^Appearance/ })).toHaveAttribute("href", "/settings/appearance");
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
