import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { builtinTheme } from "@/design/theme/spec";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderApp, renderShell } from "@/test/render";
import {
  defaultGeneral,
  generalKey,
  knownTimeZone,
  loadGeneral,
  normalizeGeneral,
} from "../general";
import { recentKey } from "../screens/recent";

afterEach(() => {
  window.localStorage.clear();
});

const api = fakeApi({
  "GET /api/v1/jobs": () => json({ jobs: [] }),
  "GET /api/v1/events": () => json({ events: [] }),
});

const saved = () =>
  JSON.parse(window.localStorage.getItem(generalKey) ?? "{}") as Record<string, unknown>;

describe("the general settings", () => {
  it("choose the time zone times show in from every one the browser knows", async () => {
    const user = userEvent.setup();
    renderApp(api, "/settings/general");
    expect(await screen.findByRole("heading", { level: 1, name: "General" })).toBeInTheDocument();
    const picker = screen.getByRole("button", { name: /^Time zone/ });
    expect(picker).toHaveTextContent(/This browser's time zone/);
    await user.click(picker);
    await user.type(screen.getByPlaceholderText("Find a time zone"), "lisb");
    await user.click(await screen.findByRole("option", { name: "Europe/Lisbon" }));
    await waitFor(() => expect(saved().timeZone).toBe("Europe/Lisbon"));
    expect(screen.getByRole("button", { name: "Time zone Europe/Lisbon" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("choose a clock, showing what each looks like", async () => {
    const user = userEvent.setup();
    renderApp(api, "/settings/general");
    const clocks = await screen.findByRole("radiogroup", { name: "Clock" });
    expect(within(clocks).getByRole("radio", { name: "12-hour (3:45 PM)" })).toBeInTheDocument();
    await user.click(within(clocks).getByRole("radio", { name: "24-hour (15:45)" }));
    await waitFor(() => expect(saved().hourCycle).toBe("h23"));
    await user.click(within(clocks).getByRole("radio", { name: /As your language writes it/ }));
    await waitFor(() => expect(saved().hourCycle).toBe(""));
  });

  it("turn single-key shortcuts off", async () => {
    const user = userEvent.setup();
    renderApp(api, "/settings/general");
    await user.click(await screen.findByRole("switch", { name: "Use single-key shortcuts" }));
    await waitFor(() => expect(saved().letterShortcuts).toBe(false));
    // ? is a character key too, so the help opens from the command bar.
    await user.keyboard("?");
    expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).not.toBeInTheDocument();
    await user.keyboard("{Control>}k{/Control}");
    const bar = await screen.findByRole("dialog", { name: "Command bar" });
    await user.click(await within(bar).findByRole("option", { name: "Keyboard shortcuts" }));
    const help = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(
      within(help).getByText(
        "Shortcuts of letter keys alone are off. You can turn them back on in the general settings.",
      ),
    ).toBeInTheDocument();
  });

  it("forget what was opened, and stop remembering it", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem(
      recentKey,
      JSON.stringify([{ path: "/jobs", title: "Jobs", what: "Page", at: 1 }]),
    );
    renderApp(api, "/settings/general");
    await user.click(await screen.findByRole("button", { name: "Forget what I opened" }));
    expect(window.localStorage.getItem(recentKey)).toBeNull();
    expect(await screen.findByText("Forgotten")).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Remember what I open" }));
    await waitFor(() => expect(saved().rememberRecent).toBe(false));
  });

  it("are listed among the panel's settings", async () => {
    renderApp(api, "/settings");
    expect(await screen.findByRole("link", { name: /^General/ })).toHaveAttribute(
      "href",
      "/settings/general",
    );
  });
});

describe("the general preferences", () => {
  it("keep known settings with valid values and fill in the rest", () => {
    expect(
      normalizeGeneral({
        timeZone: "Mars/Olympus",
        hourCycle: "h24",
        letterShortcuts: "no",
        rememberRecent: false,
      }),
    ).toEqual({ ...defaultGeneral, rememberRecent: false });
    expect(normalizeGeneral({ timeZone: "Europe/Lisbon", hourCycle: "h12" })).toEqual({
      ...defaultGeneral,
      timeZone: "Europe/Lisbon",
      hourCycle: "h12",
    });
    expect(normalizeGeneral(null)).toEqual(defaultGeneral);
    expect(knownTimeZone("UTC")).toBe(true);
    expect(knownTimeZone("Mars/Olympus")).toBe(false);
  });

  it("come back from the browser, or start from the defaults", () => {
    window.localStorage.setItem(generalKey, JSON.stringify({ letterShortcuts: false }));
    expect(loadGeneral()).toEqual({ ...defaultGeneral, letterShortcuts: false });
    window.localStorage.setItem(generalKey, "{");
    expect(loadGeneral()).toEqual(defaultGeneral);
  });
});

describe("the appearance settings", () => {
  it("show the theme layers in effect, with a sample drawn the way the person sees it", async () => {
    const server = builtinTheme("compact");
    server.name = "Northwind";
    server.version = "2.1.0";
    server.author = "Northwind IT";
    const reseller = builtinTheme("racksail");
    reseller.name = "Acme";
    renderShell({
      modules: [],
      messages: shellMessages,
      path: "/settings/appearance",
      api,
      theme: { layers: { server, reseller } },
    });
    const card = await screen.findByRole("region", { name: "Your theme" });
    const layers = within(card).getByRole("list", { name: "Theme layers, from the bottom" });
    const items = within(layers)
      .getAllByRole("listitem")
      .map((li) => li.textContent);
    expect(items).toEqual([
      "Racksail's theme",
      "The server's theme: NorthwindVersion 2.1.0, by Northwind IT",
      expect.stringMatching(/^Your reseller's theme: AcmeVersion /),
      expect.stringMatching(/^Your choices/),
    ]);
    expect(
      within(card).getByRole("region", { name: /Example screen|Preview/i }),
    ).toBeInTheDocument();
  });

  it("say when only Racksail's theme is in effect", async () => {
    renderShell({ modules: [], messages: shellMessages, path: "/settings/appearance", api });
    expect(
      await screen.findByText("No server or reseller theme is set, so you see Racksail's own."),
    ).toBeInTheDocument();
  });
});
