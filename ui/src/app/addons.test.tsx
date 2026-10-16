import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { QueryClient } from "@tanstack/react-query";
import { type AnyRoute, createMemoryHistory } from "@tanstack/react-router";
import { renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, Providers, renderShell } from "@/test/render";
import { webhook, webhookKind } from "@/test/resources";
import { addonIcon, addonIcons } from "./addon-icons";
import type { AddonEntry } from "./addons";
import { createAppRouter } from "./router";
import { columnBatch, columnText, useAddonColumns } from "./screens/resources/addon-columns";

type Route = (request: Request) => Response | Promise<Response>;

const from = "site-guard 1.4.0";
const entry = (point: AddonEntry["point"], ui: AddonEntry["ui"]): AddonEntry => ({
  addon: "site-guard",
  label: from,
  point,
  ui,
});

const entries: AddonEntry[] = [
  entry("ui.nav", { type: "nav", label: "Site guard", icon: "shield", path: "/overview" }),
  entry("ui.card", { type: "card", label: "Blocked today", path: "card" }),
  entry("ui.settings", { type: "settings", label: "Guard rules", path: "rules" }),
  entry("ui.command", { type: "command", label: "Review blocked visitors", path: "review" }),
  entry("ui.tab", { type: "tab", label: "Guard", kind: "core.webhook", path: "tab" }),
  entry("ui.tab", { type: "tab", label: "Elsewhere", kind: "web.site", path: "site" }),
  entry("ui.column", { type: "column", label: "Blocked", kind: "core.webhook", path: "blocked" }),
];

const second = webhook({
  id: "whk_01jq0000000000000000000002",
  spec: { url: "https://b.example.com/hook" },
});

/** A panel with one add-on running, recording what the panel is asked. */
function panel(routes: Record<string, Route> = {}) {
  const sent: { path: string; search: string }[] = [];
  const all: Record<string, Route> = {
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/kinds/core.webhook": () => json(webhookKind),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events": () => json({ events: [] }),
    "GET /api/v1/changesets": () => json({ changeSets: [] }),
    "GET /api/v1/core/webhooks": () => json({ items: [webhook(), second] }),
    "GET /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(webhook()),
    "GET /api/v1/core/webhooks/whk_01jq0000000000000000000001/history": () =>
      json({ versions: [] }),
    "GET /api/v1/addons/entries": () => json({ entries }),
    // The add-on's column route: text for the second webhook, and a value
    // the panel doesn't show for the first.
    "GET /api/v1/addons/site-guard/blocked": (request) => {
      const ids = new URL(request.url).searchParams.get("ids")?.split(",") ?? [];
      return json({
        values: Object.fromEntries(ids.map((id) => [id, id === second.id ? "12 visitors" : 3])),
      });
    },
    ...routes,
  };
  const api = fakeApi(
    Object.fromEntries(
      Object.entries(all).map(([key, route]) => [
        key,
        (request: Request) => {
          const url = new URL(request.url);
          sent.push({ path: url.pathname, search: url.search });
          return route(request);
        },
      ]),
    ),
  );
  return { api, sent };
}

function render(path: string, api = panel().api) {
  return renderShell({ modules: [], messages: shellMessages, path, api });
}

describe("what add-ons add to the panel", () => {
  it("lists their navigation entries, which open their pages in a sandboxed frame", async () => {
    const user = userEvent.setup();
    const { router } = render("/");
    const nav = await screen.findByRole("navigation", { name: "Main" });
    const link = await within(nav).findByRole("link", { name: "Site guard" });
    expect(link).toHaveAttribute("href", "/addons/site-guard/overview");
    expect(link.querySelector("svg.lucide-shield")).not.toBeNull();
    await user.click(link);
    await waitFor(() => expect(router.state.location.pathname).toBe("/addons/site-guard/overview"));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Site guard" }),
    ).toBeInTheDocument();
    expect(screen.getByText(`From ${from}`)).toBeInTheDocument();
    const frame = screen.getByTitle("Site guard");
    expect(frame).toHaveAttribute("src", "/api/v1/addons/site-guard/ui/overview");
    // Scripts run, but never with the panel's origin, so the page can't
    // read the panel's session.
    expect(frame.getAttribute("sandbox")?.split(" ")).not.toContain("allow-same-origin");
    expect(frame).toHaveAttribute("referrerpolicy", "no-referrer");
    await waitFor(() => expect(document.title).toBe("Site guard · Racksail"));
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("shows their cards on the home screen", async () => {
    render("/");
    const card = await screen.findByRole("region", { name: "Blocked today" });
    expect(within(card).getByTitle("Blocked today")).toHaveAttribute(
      "src",
      "/api/v1/addons/site-guard/ui/card",
    );
  });

  it("lists their settings pages", async () => {
    render("/settings");
    expect(await screen.findByRole("heading", { level: 2, name: "Add-ons" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Guard rules/ })).toHaveAttribute(
      "href",
      "/addons/site-guard/rules",
    );
    expect(screen.getByText(`From ${from}`)).toBeInTheDocument();
  });

  it("offers their commands in the command bar", async () => {
    const user = userEvent.setup();
    const { router } = render("/");
    await screen.findByRole("navigation", { name: "Main" });
    await user.keyboard("{Control>}k{/Control}");
    const bar = await screen.findByRole("dialog", { name: "Command bar" });
    const group = await within(bar).findByRole("group", { name: "Add-ons" });
    await user.click(within(group).getByRole("option", { name: /^Review blocked visitors/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/addons/site-guard/review"));
  });

  it("adds their tabs to the screens of the kinds they name, about the resource shown", async () => {
    const user = userEvent.setup();
    render("/core/webhooks/whk_01jq0000000000000000000001");
    const tab = await screen.findByRole("tab", { name: "Guard" });
    expect(screen.queryByRole("tab", { name: "Elsewhere" })).not.toBeInTheDocument();
    await user.click(tab);
    expect(await screen.findByTitle("Guard")).toHaveAttribute(
      "src",
      "/api/v1/addons/site-guard/ui/tab?resource=whk_01jq0000000000000000000001",
    );
  });

  it("adds their columns to the lists of the kinds they name, with the text they answer", async () => {
    const { api, sent } = panel();
    render("/core/webhooks", api);
    const table = await screen.findByRole("table", { name: "Webhooks" });
    expect(await within(table).findByRole("columnheader", { name: /Blocked/ })).toBeInTheDocument();
    expect(await within(table).findByText("12 visitors")).toBeInTheDocument();
    const asked = sent.filter((s) => s.path === "/api/v1/addons/site-guard/blocked");
    expect(asked.map((s) => new URLSearchParams(s.search).get("ids"))).toContain(
      `${webhook().id},${second.id}`,
    );
  });

  it("adds nothing when the panel lists no add-ons the person may use", async () => {
    render(
      "/settings",
      panel({
        "GET /api/v1/addons/entries": () =>
          json({ type: "about:blank", status: 403, code: "core.forbidden" }, 403),
      }).api,
    );
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).queryByRole("link", { name: "Site guard" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 2, name: "Add-ons" })).not.toBeInTheDocument();
  });
});

describe("an add-on's column", () => {
  it("asks for the values of a limited number of resources at a time", async () => {
    const ids = Array.from(
      { length: columnBatch * 2 + 5 },
      (_, i) => `whk_${String(i).padStart(4, "0")}`,
    );
    const { api, sent } = panel({
      "GET /api/v1/addons/site-guard/blocked": (request) => {
        const asked = new URL(request.url).searchParams.get("ids")?.split(",") ?? [];
        return json({ values: Object.fromEntries(asked.map((id) => [id, `seen ${id}`])) });
      },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <Providers api={api}>{children}</Providers>
    );
    const { result } = renderHook(() => useAddonColumns("core.webhook", ids), { wrapper });
    await waitFor(() =>
      expect(Object.keys(result.current[0]?.values ?? {})).toHaveLength(ids.length),
    );
    expect(result.current[0]?.values[ids.at(-1) ?? ""]).toBe(`seen ${ids.at(-1)}`);
    const sizes = sent
      .filter((s) => s.path === "/api/v1/addons/site-guard/blocked")
      .map((s) => new URLSearchParams(s.search).get("ids")?.split(",").length);
    expect(sizes).toEqual([columnBatch, columnBatch, 5]);
  });

  it("has no columns for kinds no add-on names", async () => {
    const { api, sent } = panel();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <Providers api={api}>{children}</Providers>
    );
    const { result } = renderHook(() => useAddonColumns("web.site", ["sit_1"]), { wrapper });
    await waitFor(() => expect(sent.some((s) => s.path === "/api/v1/addons/entries")).toBe(true));
    expect(result.current).toEqual([]);
    expect(sent.some((s) => s.path === "/api/v1/addons/site-guard/blocked")).toBe(false);
  });

  it("keeps only the text an answer gives for each resource", () => {
    expect(columnText({ values: { a: "one", b: 2, c: null, d: "four" } })).toEqual({
      a: "one",
      d: "four",
    });
    expect(columnText({ values: ["one"] })).toEqual({});
    expect(columnText({ values: "one" })).toEqual({});
    expect(columnText(null)).toEqual({});
    expect(columnText("values")).toEqual({});
  });
});

describe("an add-on entry's icon", () => {
  it("is the icon it names, among those the panel bundles, or the add-on icon", () => {
    expect(addonIcon("shield")).toBe(addonIcons.shield);
    expect(addonIcon("chart-line")).toBe(addonIcons["chart-line"]);
    for (const name of [undefined, "", "no-such-icon", "constructor", "toString"]) {
      expect(addonIcon(name)).toBe(addonIcons.puzzle);
    }
  });
});

describe("the kinds add-ons add", () => {
  it("keep out of where the panel's own screens are", () => {
    // The panel keeps these names, as internal/hooks/addon.go lists them,
    // from the namespaces of add-ons' kinds, whose screens are at
    // /<namespace>/<plural>.
    const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
    const source = readFileSync(join(repo, "internal/hooks/addon.go"), "utf8");
    const list = source.match(/var ReservedNamespaces = \[\]string\{([\s\S]*?)\n\}/)?.[1] ?? "";
    const reserved = [...list.matchAll(/"([a-z0-9-]+)"/g)].map((m) => m[1]);
    expect(reserved).toContain("core");
    const router = createAppRouter([], new QueryClient(), createMemoryHistory());
    const children = (router.routeTree.children ?? []) as AnyRoute[] | Record<string, AnyRoute>;
    const firsts = Object.values(children)
      .map((route) => route.path.replace(/^\/+/, "").split("/")[0] ?? "")
      .filter((first) => first !== "" && !first.startsWith("$"));
    expect(firsts.length).toBeGreaterThan(5);
    expect(firsts.filter((first) => !reserved.includes(first))).toEqual([]);
  });
});
