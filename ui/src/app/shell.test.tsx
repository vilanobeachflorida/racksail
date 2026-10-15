import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Globe, Plus, Sparkles } from "lucide-react";
import { afterEach, describe, expect, it } from "vitest";
import { builtinTheme } from "@/design/theme/spec";
import { shellMessages } from "@/lib/i18n";
import type { AssistantPanelProps, ModuleUI } from "@/modules/types";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderShell } from "@/test/render";
import { dockQuery } from "./shell-state";

const realMatchMedia = window.matchMedia;
afterEach(() => {
  window.matchMedia = realMatchMedia;
});

/** Makes the given media queries match, as on a screen of that size. */
function matchMediaQueries(...matching: string[]) {
  window.matchMedia = (query: string) =>
    ({ ...realMatchMedia(query), matches: matching.includes(query) }) as MediaQueryList;
}
const desktop = ["(min-width: 48rem)", dockQuery];

function AssistantPanel({ question, questionId, onClose }: AssistantPanelProps) {
  return (
    <div className="flex flex-col gap-2 p-4">
      <h2>How can I help?</h2>
      {question ? <p>{`Asked: ${question} (${questionId})`}</p> : null}
      <button type="button" onClick={onClose}>
        Done
      </button>
    </div>
  );
}

const ai: ModuleUI = {
  id: "ai",
  name: { id: "ai.name" },
  description: { id: "ai.description" },
  icon: Sparkles,
  assistant: {
    panel: async () => ({ default: AssistantPanel }),
    slashCommands: [{ name: "explain", description: { id: "ai.explain" } }],
  },
};

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
      screen: async () => ({
        default: () => (
          <div>
            <h1>All sites</h1>
            <label>
              Site name
              <input />
            </label>
          </div>
        ),
      }),
    },
  ],
  commands: [
    {
      id: "new-site",
      label: { id: "web.newSite" },
      icon: Plus,
      shortcut: "mod+shift+n",
      run: ({ navigate }) => navigate("/web/sites"),
    },
  ],
};

const messages = {
  ...shellMessages,
  "ai.name": "Assistant",
  "ai.description": "Answers questions about the server.",
  "ai.explain": "Explain what's on the screen",
  "web.name": "Web hosting",
  "web.description": "Sites.",
  "web.sites": "Sites",
  "web.newSite": "Create a site",
};

const api = fakeApi({
  "GET /api/v1/health": () => json({ status: "ok", checks: [] }),
  "GET /api/v1/version": () =>
    json({
      version: "1.2.3",
      channel: "stable",
      goVersion: "go1.27.0",
      os: "linux",
      arch: "arm64",
    }),
  "GET /api/v1/jobs": () => json({ items: [] }),
});

async function shell(options: Partial<Parameters<typeof renderShell>[0]> = {}) {
  const result = renderShell({ modules: [web], messages, api, ...options });
  await screen.findByRole("navigation", { name: "Main" });
  return result;
}

describe("the command bar", () => {
  it("goes to pages and runs module commands", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("{Control>}k{/Control}");
    const bar = await screen.findByRole("dialog", { name: "Command bar" });
    await user.type(within(bar).getByPlaceholderText("Go to a page or run a command"), "sites");
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("heading", { name: "All sites" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Command bar" })).not.toBeInTheDocument();

    await user.click(
      screen.getAllByRole("button", { name: "Search or run a command" })[0] as HTMLElement,
    );
    await user.type(
      await screen.findByPlaceholderText("Go to a page or run a command"),
      "settings",
    );
    await user.click(screen.getByRole("option", { name: "Settings" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
  });

  it("changes the appearance", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("{Control>}k{/Control}");
    await user.click(await screen.findByRole("option", { name: "Use dark mode" }));
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    await user.keyboard("{Control>}k{/Control}");
    expect(await screen.findByRole("option", { name: "Use light mode" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Use dark mode" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "Turn high contrast on" }));
    expect(document.documentElement).toHaveAttribute("data-contrast", "high");
    expect(JSON.parse(window.localStorage.getItem("racksail.preferences") ?? "{}")).toMatchObject({
      mode: "dark",
      contrast: "high",
    });
  });

  it("says when nothing matches", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("{Control>}k{/Control}");
    await user.type(await screen.findByPlaceholderText("Go to a page or run a command"), "zzzz");
    expect(screen.getByText("No pages or commands match.")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});

describe("keyboard shortcuts", () => {
  it("go places with key sequences, but not while typing", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("gs");
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    await user.keyboard("gh");
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();

    await user.keyboard("{Control>}{Shift>}n{/Shift}{/Control}");
    expect(await screen.findByRole("heading", { name: "All sites" })).toBeInTheDocument();
    const field = screen.getByRole("textbox", { name: "Site name" });
    await user.type(field, "gs");
    expect(field).toHaveValue("gs");
    expect(screen.getByRole("heading", { name: "All sites" })).toBeInTheDocument();
  });

  it("are listed in the shortcut help, with the modules' own", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("?");
    const help = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(within(help).getByText("Open the command bar").nextElementSibling).toHaveTextContent(
      "CtrlK",
    );
    expect(within(help).getByText("Create a site").nextElementSibling).toHaveTextContent(
      "CtrlShiftN",
    );
    // The assistant's shortcut only shows while there is one.
    expect(within(help).queryByText("Open or close the assistant")).not.toBeInTheDocument();
  });

  it("open the jobs drawer", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("{Control>}j{/Control}");
    const jobs = await screen.findByRole("dialog", { name: "Jobs" });
    expect(await within(jobs).findByText("No jobs yet")).toBeInTheDocument();
  });
});

describe("the assistant dock", () => {
  it("sits beside the content on wide screens and can be resized and closed", async () => {
    matchMediaQueries(...desktop);
    const user = userEvent.setup();
    await shell({ modules: [web, ai] });
    const dock = screen.getByRole("complementary", { name: "Assistant" });
    expect(
      await within(dock).findByRole("heading", { name: "How can I help?" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Skip to the assistant" })).toHaveAttribute(
      "href",
      "#assistant",
    );

    const handle = within(dock).getByRole("separator", { name: "Resize the assistant" });
    expect(handle).toHaveAttribute("aria-valuenow", "33");
    handle.focus();
    await user.keyboard("{ArrowLeft}");
    expect(handle).toHaveAttribute("aria-valuenow", "34");
    await user.keyboard("{Shift>}{ArrowRight}{/Shift}");
    expect(handle).toHaveAttribute("aria-valuenow", "29");
    await user.keyboard("{End}");
    expect(handle).toHaveAttribute("aria-valuenow", "50");
    await user.keyboard("{Home}");
    expect(handle).toHaveAttribute("aria-valuenow", "20");
    expect(JSON.parse(window.localStorage.getItem("racksail.shell.dock") ?? "{}")).toEqual({
      open: true,
      width: 0.2,
    });

    const toggle = screen.getByRole("button", { name: "Close the assistant" });
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    await user.click(toggle);
    expect(screen.queryByRole("complementary", { name: "Assistant" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open the assistant" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    await user.keyboard("{Control>}.{/Control}");
    expect(await screen.findByRole("complementary", { name: "Assistant" })).toBeInTheDocument();
  });

  it("takes questions and slash commands from the command bar", async () => {
    matchMediaQueries(...desktop);
    const user = userEvent.setup();
    await shell({ modules: [web, ai] });
    await user.keyboard("{Control>}k{/Control}");
    const input = await screen.findByPlaceholderText(
      "Go to a page, run a command or ask a question",
    );
    await user.type(input, "why is mail slow");
    expect(
      screen.getByRole("option", { name: "Ask the assistant: why is mail slow" }),
    ).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    const dock = screen.getByRole("complementary", { name: "Assistant" });
    expect(await within(dock).findByText("Asked: why is mail slow (1)")).toBeInTheDocument();

    await user.keyboard("{Control>}k{/Control}");
    await user.type(await screen.findByPlaceholderText(/ask a question/), "/ex");
    expect(screen.queryByRole("option", { name: "Settings" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "/explain Explain what's on the screen" }));
    expect(await within(dock).findByText("Asked: /explain (2)")).toBeInTheDocument();
  });

  it("opens as a sheet on phones only when asked to", async () => {
    const user = userEvent.setup();
    await shell({ modules: [web, ai] });
    expect(screen.queryByRole("dialog", { name: "Assistant" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Skip to the assistant" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open the assistant" }));
    const sheet = await screen.findByRole("dialog", { name: "Assistant" });
    await user.click(await within(sheet).findByRole("button", { name: "Done" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Assistant" })).not.toBeInTheDocument(),
    );
    // Closing the sheet doesn't close the dock for wide screens.
    expect(window.localStorage.getItem("racksail.shell.dock")).toBeNull();
  });

  it("isn't there while the assistant module is off", async () => {
    matchMediaQueries(...desktop);
    const user = userEvent.setup();
    await shell({ modules: [web, ai], states: { ai: "off" } });
    expect(screen.queryByRole("complementary", { name: "Assistant" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /the assistant/ })).not.toBeInTheDocument();
    await user.keyboard("{Control>}.{/Control}");
    expect(screen.queryByRole("complementary", { name: "Assistant" })).not.toBeInTheDocument();
    await user.keyboard("{Control>}k{/Control}");
    expect(await screen.findByPlaceholderText("Go to a page or run a command")).toBeInTheDocument();
  });
});

describe("white-labeling", () => {
  it("shows a reseller's logo, name and favicon instead of Racksail's", async () => {
    const icon = document.createElement("link");
    icon.rel = "icon";
    icon.type = "image/svg+xml";
    icon.href = "/favicon.svg";
    document.head.append(icon);
    const reseller = builtinTheme("racksail");
    reseller.id = "acme";
    reseller.name = "Acme";
    reseller.assets = {
      logoLight: "logo.svg",
      logoDark: "logo-dark.svg",
      mark: "mark.svg",
      favicon: "icon.png",
    };
    const { unmount } = await shell({
      theme: {
        layers: { reseller },
        fileURL: (layer, path) => `/api/v1/themes/${layer}/files/${path}`,
        brandName: "Acme Hosting",
      },
    });
    const home = screen.getByRole("link", { name: "Acme Hosting" });
    const images = [...home.querySelectorAll("img")].map((img) => img.getAttribute("src"));
    expect(images).toEqual([
      "/api/v1/themes/reseller/files/mark.svg",
      "/api/v1/themes/reseller/files/logo.svg",
    ]);
    await screen.findByRole("heading", { level: 1, name: "Home" });
    await waitFor(() => expect(document.title).toBe("Home · Acme Hosting"));
    expect(icon.getAttribute("href")).toBe("/api/v1/themes/reseller/files/icon.png");
    expect(icon).not.toHaveAttribute("type");

    unmount();
    expect(icon.getAttribute("href")).toBe("/favicon.svg");
    expect(icon).toHaveAttribute("type", "image/svg+xml");
    icon.remove();
  });

  it("keeps Racksail's mark and name without a theme logo", async () => {
    await shell();
    const home = screen.getByRole("link", { name: "Racksail" });
    expect(home.querySelector("img")).toBeNull();
    expect(home).toHaveTextContent("Racksail");
  });

  it("loads the server's custom CSS while an admin has it on", async () => {
    const custom = () => document.head.querySelector<HTMLLinkElement>("link[data-rs-custom]");
    const { unmount } = await shell({ theme: { customCSSURL: "/api/v1/theme/custom.css" } });
    expect(custom()?.getAttribute("href")).toBe("/api/v1/theme/custom.css");
    expect(custom()?.rel).toBe("stylesheet");
    unmount();
    expect(custom()).toBeNull();
  });
});

describe("the menu on phones", () => {
  it("opens as a sheet and closes when a page is chosen", async () => {
    const user = userEvent.setup();
    await shell();
    const button = screen.getByRole("button", { name: "Open the menu" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    await user.click(button);
    const sheet = await screen.findByRole("dialog", { name: "Racksail" });
    await user.click(
      within(within(sheet).getByRole("navigation", { name: "Main" })).getByRole("link", {
        name: "Sites",
      }),
    );
    expect(await screen.findByRole("heading", { name: "All sites" })).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Racksail" })).not.toBeInTheDocument(),
    );
  });
});

describe("appearance settings", () => {
  it("apply to the page at once and stay in the browser", async () => {
    const user = userEvent.setup();
    await shell({ path: "/settings/appearance" });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Appearance" }),
    ).toBeInTheDocument();
    const root = document.documentElement;
    const choose = async (group: string, option: string) =>
      user.click(
        within(screen.getByRole("radiogroup", { name: group })).getByRole("radio", {
          name: option,
        }),
      );

    await choose("Light or dark", "Dark");
    expect(root).toHaveAttribute("data-theme", "dark");
    await choose("Contrast", "High");
    expect(root).toHaveAttribute("data-contrast", "high");
    await choose("Density", "Compact");
    expect(root).toHaveAttribute("data-density", "compact");
    await choose("Text size", "Larger");
    expect(root).toHaveAttribute("data-text-size", "larger");
    await choose("Motion", "Reduce motion");
    expect(root).toHaveAttribute("data-motion", "reduce");
    expect(JSON.parse(window.localStorage.getItem("racksail.preferences") ?? "{}")).toEqual({
      mode: "dark",
      contrast: "high",
      density: "compact",
      textSize: "larger",
      motion: "reduce",
    });

    await choose("Light or dark", "Match the system");
    expect(root).not.toHaveAttribute("data-theme");
  });
});

describe("the shell's accessibility", () => {
  it("has no problems axe can find on the home screen, with the dock open", async () => {
    matchMediaQueries(...desktop);
    await shell({ modules: [web, ai] });
    await screen.findByRole("heading", { name: "How can I help?" });
    expect(await axeViolations(document, { page: true })).toEqual([]);
  });

  it("has none in the command bar or the shortcut help", async () => {
    const user = userEvent.setup();
    await shell();
    await user.keyboard("{Control>}k{/Control}");
    await screen.findByRole("dialog", { name: "Command bar" });
    expect(await axeViolations()).toEqual([]);
    await user.keyboard("{Escape}");
    await user.keyboard("?");
    await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(await axeViolations()).toEqual([]);
  });

  it("has none on the settings and appearance pages", async () => {
    const { router } = await shell({ path: "/settings" });
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(await axeViolations(document, { page: true })).toEqual([]);
    await router.navigate({ to: "/settings/appearance" });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Appearance" }),
    ).toBeInTheDocument();
    expect(await axeViolations(document, { page: true })).toEqual([]);
  });
});
