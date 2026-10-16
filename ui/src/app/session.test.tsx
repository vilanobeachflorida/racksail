import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Globe, Plus, Signpost, Sparkles } from "lucide-react";
import { describe, expect, it, vi } from "vitest";
import { builtinTheme } from "@/design/theme/spec";
import type { PanelSession } from "@/lib/api/session";
import { permits, sessionModuleStates, themeFileURL } from "@/lib/api/session";
import { pseudoMessage } from "@/lib/pseudo";
import type { ModuleUI } from "@/modules/types";
import { fakeApi, json, renderApp } from "@/test/render";
import { defaultGeneral, generalKey } from "./general";

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

function panel(answers: {
  session?: () => Response;
  preferences?: (r: Request) => Response;
  signInOptions?: () => Response;
}) {
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
    ...(answers.signInOptions ? { "GET /api/v1/auth/options": answers.signInOptions } : {}),
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
        signInOptions: () => json({ setupRequired: false, passkeys: false, providers: [] }),
      }),
    );
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(await screen.findByLabelText("Username")).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Main" })).not.toBeInTheDocument();
  });
});

describe("preferences kept with the account", () => {
  /** A panel that keeps what's sent as the person's preferences. */
  function keeping(preferences: PanelSession["preferences"], user = session.user) {
    const saved: unknown[] = [];
    const api = panel({
      session: () => json({ ...session, user, preferences }),
      preferences: (r) => {
        void r.json().then((body) => saved.push(body));
        return new Response(null, { status: 204 });
      },
    });
    return { api, saved };
  }
  const display = {
    mode: "dark",
    contrast: "system",
    density: "theme",
    textSize: "default",
    motion: "system",
  };

  it("bring the general ones to this browser, and keep changes to them", async () => {
    window.localStorage.setItem(
      generalKey,
      JSON.stringify({ ...defaultGeneral, timeZone: "Asia/Tokyo" }),
    );
    const { api, saved } = keeping({
      mode: "dark",
      timeZone: "Europe/Lisbon",
      hourCycle: "h23",
      letterShortcuts: false,
      rememberRecent: true,
    });
    renderApp(api, "/settings/general");
    expect(
      await screen.findByRole("button", { name: "Time zone Europe/Lisbon" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "24-hour (15:45)" })).toBeChecked();
    const letters = screen.getByRole("switch", { name: "Use single-key shortcuts" });
    expect(letters).not.toBeChecked();
    expect(
      screen.getByText("How the panel works for you, in every browser you sign in on."),
    ).toBeInTheDocument();
    // This browser remembers them for the next visit, before the session comes.
    expect(JSON.parse(window.localStorage.getItem(generalKey) ?? "{}")).toMatchObject({
      timeZone: "Europe/Lisbon",
      letterShortcuts: false,
    });

    // A change keeps every preference, the appearance's included, as the
    // account keeps them as one.
    await userEvent.click(letters);
    await waitFor(() =>
      expect(saved).toEqual([
        {
          ...display,
          timeZone: "Europe/Lisbon",
          hourCycle: "h23",
          letterShortcuts: true,
          rememberRecent: true,
        },
      ]),
    );
  });

  it("keep the general ones as they are when the appearance changes", async () => {
    const { api, saved } = keeping({ mode: "dark", letterShortcuts: false, rememberRecent: true });
    renderApp(api, "/");
    await userEvent.click(await screen.findByRole("button", { name: "Appearance" }));
    await userEvent.click(await screen.findByRole("menuitemradio", { name: "Light" }));
    await waitFor(() =>
      expect(saved).toEqual([
        { ...display, mode: "light", letterShortcuts: false, rememberRecent: true },
      ]),
    );
    // Following the browser's time zone and the language's clock is the
    // same as not keeping them.
    expect(saved[0]).not.toHaveProperty("timeZone");
    expect(saved[0]).not.toHaveProperty("hourCycle");
  });

  it("leave this browser's general ones alone while the account has none", async () => {
    window.localStorage.setItem(
      generalKey,
      JSON.stringify({ ...defaultGeneral, timeZone: "Asia/Tokyo" }),
    );
    const { api } = keeping({ mode: "dark" });
    renderApp(api, "/settings/general");
    expect(await screen.findByRole("button", { name: "Time zone Asia/Tokyo" })).toBeInTheDocument();
    await waitFor(() => expect(document.documentElement).toHaveAttribute("data-theme", "dark"));
    expect(screen.getByRole("button", { name: "Time zone Asia/Tokyo" })).toBeInTheDocument();
  });

  it("say where the appearance settings stay", async () => {
    const { api } = keeping({ mode: "dark" });
    const { unmount } = renderApp(api, "/settings/appearance");
    expect(
      await screen.findByText(
        "How the panel looks for you. These settings follow you to every browser you sign in on.",
      ),
    ).toBeInTheDocument();
    unmount();
    renderApp(panel({}), "/settings/appearance");
    expect(
      await screen.findByText(
        "How the panel looks on this device. These settings stay in this browser.",
      ),
    ).toBeInTheDocument();
  });

  it("show the profile's language unless this browser chose one of its own", async () => {
    const { api } = keeping(null, { ...session.user, locale: "en-XA" });
    const { unmount } = renderApp(api, "/settings/general", { locale: undefined });
    expect(
      await screen.findByRole("heading", { level: 1, name: pseudoMessage("General", "en-XA") }),
    ).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en-XA");
    unmount();

    window.localStorage.setItem("racksail.locale", "en");
    renderApp(api, "/settings/general", { locale: undefined });
    expect(await screen.findByRole("heading", { level: 1, name: "General" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
  });

  it("offer the profile's language where a browser would otherwise follow its own", async () => {
    const user = userEvent.setup();
    const { api } = keeping(null, { ...session.user, locale: "en-GB" });
    renderApp(api, "/settings/general", { locale: undefined });
    const language = await screen.findByRole("combobox", { name: "Language" });
    expect(language).toHaveTextContent("As in your profile (English)");
    await user.click(language);
    expect(
      await screen.findByRole("option", { name: "As in your profile (English)" }),
    ).toBeInTheDocument();
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
