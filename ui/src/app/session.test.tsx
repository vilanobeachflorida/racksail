import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Globe, Plus, Signpost, Sparkles } from "lucide-react";
import { describe, expect, it, vi } from "vitest";
import { builtinTheme } from "@/design/theme/spec";
import type { PanelSession } from "@/lib/api/session";
import { permits, sessionModuleStates, themeFileURL } from "@/lib/api/session";
import type { ModuleUI } from "@/modules/types";
import { fakeApi, json, renderApp } from "@/test/render";

// The bundled modules don't add navigation yet, so these stand in for
// them, with the bundled modules' own messages.
const testModules: ModuleUI[] = [
  {
    id: "web",
    name: { id: "web.name" },
    description: { id: "web.description" },
    icon: Globe,
    nav: [
      { id: "sites", label: { id: "web.name" }, icon: Globe, to: "/", permission: "web.site.read" },
    ],
    commands: [
      {
        id: "new-site",
        label: { id: "web.description" },
        icon: Plus,
        permission: "web.site.create",
        run: () => {},
      },
    ],
  },
  {
    id: "dns",
    name: { id: "dns.name" },
    description: { id: "dns.description" },
    icon: Signpost,
    nav: [{ id: "zones", label: { id: "dns.name" }, icon: Signpost, to: "/" }],
  },
  {
    id: "ai",
    name: { id: "ai.name" },
    description: { id: "ai.description" },
    icon: Sparkles,
    assistant: { panel: async () => ({ default: () => <p>Ask away</p> }) },
  },
];

vi.mock("@/modules/registry", async (original) => ({
  ...(await original<typeof import("@/modules/registry")>()),
  loadModules: async () => testModules,
}));

const server = builtinTheme("racksail");
server.id = "acme";
server.name = "Acme";
server.tokens.light.brand = "#5B21B6";
server.assets = { logoLight: "logo light.svg" };

const session: PanelSession = {
  user: { id: "user_1", name: "Ana" },
  permissions: ["*.read"],
  modules: { web: "on", dns: "off", ai: "off" },
  theme: {
    server,
    files: { server: "/api/v1/themes/acme/files/" },
    brandName: "Acme Hosting",
    customCSS: "/api/v1/themes/custom.css",
  },
  preferences: { mode: "dark", density: "compact" },
};

function panel(answers: { session?: () => Response; preferences?: (r: Request) => Response }) {
  return fakeApi({
    "GET /api/v1/health": () => json({ status: "ok", checks: [] }),
    "GET /api/v1/version": () =>
      json({
        version: "1.2.3",
        channel: "stable",
        goVersion: "go1.27",
        os: "linux",
        arch: "arm64",
      }),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events": () => json({ events: [] }),
    ...(answers.session ? { "GET /api/v1/session": answers.session } : {}),
    ...(answers.preferences ? { "PUT /api/v1/session/preferences": answers.preferences } : {}),
  });
}

describe("the session", () => {
  it("decides which modules show, what the person may do and how the panel looks", async () => {
    const saved: unknown[] = [];
    renderApp(
      panel({
        session: () => json(session),
        preferences: (r) => {
          void r.json().then((body) => saved.push(body));
          return new Response(null, { status: 204 });
        },
      }),
    );
    const nav = await screen.findByRole("navigation", { name: "Main" });
    // web is on and readable; dns is off.
    expect(within(nav).getByRole("link", { name: "Web hosting" })).toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "DNS" })).not.toBeInTheDocument();
    // The assistant module is off, so there's no assistant.
    expect(screen.queryByRole("button", { name: /assistant/i })).not.toBeInTheDocument();
    // Reading doesn't allow creating, so the module's command isn't offered.
    const actions = await screen.findByRole("region", { name: "Quick actions" });
    expect(within(actions).queryByText(/Sites, domains on sites/)).not.toBeInTheDocument();

    // The server's theme, logo, name and custom CSS apply.
    const home = screen.getByRole("link", { name: "Acme Hosting" });
    expect(home.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/themes/acme/files/logo%20light.svg",
    );
    await waitFor(() =>
      expect(document.head.querySelector("style[data-rs-theme-layer]")?.textContent).toContain(
        "--rs-brand: #5B21B6;",
      ),
    );
    expect(document.head.querySelector("link[data-rs-custom]")?.getAttribute("href")).toBe(
      "/api/v1/themes/custom.css",
    );

    // The account's preferences replace the browser's, and changes go back.
    await waitFor(() => expect(document.documentElement).toHaveAttribute("data-theme", "dark"));
    expect(document.documentElement).toHaveAttribute("data-density", "compact");
    await userEvent.click(screen.getByRole("button", { name: "Appearance" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Light" }));
    await waitFor(() => expect(saved).toEqual([expect.objectContaining({ mode: "light" })]));
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
  });

  it("says when preferences couldn't be kept with the account", async () => {
    renderApp(
      panel({
        session: () => json({ ...session, preferences: null }),
        preferences: () =>
          json(
            {
              type: "about:blank",
              status: 503,
              code: "core.unavailable",
              params: { what: "The panel" },
            },
            503,
          ),
      }),
    );
    await userEvent.click(await screen.findByRole("button", { name: "Appearance" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Dark" }));
    expect(
      await screen.findByText(/apply in this browser, but they couldn't be kept with your account/),
    ).toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });

  it("runs without one when the panel doesn't serve sessions", async () => {
    renderApp(panel({}));
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "DNS" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Racksail" })).toBeInTheDocument();
  });

  it("asks to sign in when the person isn't", async () => {
    renderApp(
      panel({
        session: () =>
          json({ type: "about:blank", status: 401, code: "core.unauthenticated" }, 401),
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("Sign in to continue.");
  });
});

describe("session helpers", () => {
  it("match permissions as roles grant them", () => {
    expect(permits(["*"], "web.site.delete")).toBe(true);
    expect(permits(["*.read"], "web.site.read")).toBe(true);
    expect(permits(["*.read"], "web.site.create")).toBe(false);
    expect(permits(["web.site.*"], "web.site.deploy")).toBe(true);
    expect(permits(["web.site.read"], "web.site.reader")).toBe(false);
    expect(permits(["*"], "core.impersonate")).toBe(false);
    expect(permits(["*", "core.impersonate"], "core.impersonate")).toBe(true);
    expect(permits([], "web.site.read")).toBe(false);
  });

  it("keep only bundled modules' states and build file URLs", () => {
    expect(sessionModuleStates({ ...session, modules: { web: "active", nope: "on" } })).toEqual({
      web: "active",
    });
    expect(themeFileURL("/files", "fonts/a b.woff2")).toBe("/files/fonts/a%20b.woff2");
    expect(themeFileURL("/files/", "logo.svg")).toBe("/files/logo.svg");
  });
});
