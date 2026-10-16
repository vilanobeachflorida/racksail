import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { fakeApi, json, renderShell } from "@/test/render";
import { changeSet, webhook, webhookKind } from "@/test/resources";
import { recentKey } from "./screens/recent";

afterEach(() => {
  window.localStorage.clear();
});

type Route = (request: Request) => Response;

function panel(routes: Record<string, Route> = {}) {
  const sent: { method: string; path: string; search: string }[] = [];
  const all: Record<string, Route> = {
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/kinds/core.webhook": () => json(webhookKind),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events": () => json({ events: [] }),
    "GET /api/v1/core/webhooks": () => json({ items: [webhook()] }),
    "GET /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(webhook()),
    "GET /api/v1/operations": () =>
      json({
        operations: [
          {
            id: "listWebhooks",
            method: "GET",
            path: "/api/v1/core/webhooks",
            kind: "core.webhook",
            operation: "list",
            permission: "core.webhook.read",
            risk: "read",
            mutating: false,
            summary: "List webhooks",
          },
          {
            id: "createWebhook",
            method: "POST",
            path: "/api/v1/core/webhooks",
            kind: "core.webhook",
            operation: "create",
            permission: "core.webhook.create",
            risk: "medium",
            mutating: true,
            summary: "Create a webhook",
          },
        ],
      }),
    "GET /api/v1/search": () =>
      json({
        results: [
          {
            id: "whk_01jq0000000000000000000001",
            kind: "core.webhook",
            scope: { level: "server" },
            title: "https://hooks.example.com/racksail",
            phase: "ready",
            updatedAt: "2026-10-14T09:30:00Z",
          },
        ],
      }),
    ...routes,
  };
  const api = fakeApi(
    Object.fromEntries(
      Object.entries(all).map(([key, route]) => [
        key,
        (request: Request) => {
          const url = new URL(request.url);
          sent.push({ method: request.method, path: url.pathname, search: url.search });
          return route(request);
        },
      ]),
    ),
  );
  return { api, sent };
}

async function openBar(user: ReturnType<typeof userEvent.setup>) {
  await user.keyboard("{Control>}k{/Control}");
  return screen.findByRole("dialog", { name: "Command bar" });
}

describe("the command bar", () => {
  it("finds resources of any kind, and leads to every result", async () => {
    const user = userEvent.setup();
    const { api } = panel();
    const { router } = renderShell({ modules: [], messages: shellMessages, api });
    await screen.findByRole("navigation", { name: "Main" });
    const bar = await openBar(user);
    await user.type(within(bar).getByRole("combobox"), "hooks");
    const result = await within(bar).findByRole("option", {
      name: /https:\/\/hooks\.example\.com\/racksail/,
    });
    expect(
      within(bar).getByRole("option", { name: "See every result for “hooks”" }),
    ).toBeInTheDocument();
    await user.click(result);
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/core/webhooks/whk_01jq0000000000000000000001"),
    );
    const again = await openBar(user);
    await user.type(within(again).getByRole("combobox"), "hooks");
    await user.click(
      await within(again).findByRole("option", { name: "See every result for “hooks”" }),
    );
    await waitFor(() => expect(router.state.location.pathname).toBe("/search"));
    expect(router.state.location.search).toEqual({ q: "hooks" });
  });

  it("starts new resources and opens the lists of the kinds the person may use", async () => {
    const user = userEvent.setup();
    const { api } = panel();
    const { router } = renderShell({
      modules: [],
      messages: shellMessages,
      api,
      can: (p) => p !== "core.webhook.create",
    });
    await screen.findByRole("navigation", { name: "Main" });
    const bar = await openBar(user);
    expect(await within(bar).findByRole("option", { name: /^Webhooks/ })).toBeInTheDocument();
    expect(within(bar).queryByRole("option", { name: "New webhook" })).not.toBeInTheDocument();
    for (const page of ["All jobs", "Events", "Changes", "Resources", "Search"]) {
      expect(within(bar).getByRole("option", { name: page })).toBeInTheDocument();
    }
    await user.click(within(bar).getByRole("option", { name: /^Webhooks/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/core/webhooks"));
  });

  it("starts a new resource of a kind", async () => {
    const user = userEvent.setup();
    const { api } = panel();
    const { router } = renderShell({ modules: [], messages: shellMessages, api });
    await screen.findByRole("navigation", { name: "Main" });
    const bar = await openBar(user);
    await user.click(await within(bar).findByRole("option", { name: "New webhook" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/core/webhooks/new"));
  });

  it("offers what was opened recently", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem(
      recentKey,
      JSON.stringify([
        { path: "/jobs/job_1", title: "Create webhook", what: "Job", at: Date.now() },
      ]),
    );
    const { api } = panel();
    const { router } = renderShell({ modules: [], messages: shellMessages, api });
    await screen.findByRole("navigation", { name: "Main" });
    const bar = await openBar(user);
    const group = await within(bar).findByRole("group", { name: "Recently opened" });
    await user.click(within(group).getByRole("option", { name: /Create webhook/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/jobs/job_1"));
  });

  it("runs what the resource on screen offers", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({
      "POST /api/v1/core/webhooks/whk_01jq0000000000000000000001/actions/test": () =>
        json(changeSet({ id: "chg_test", title: "Send a test webhook", risk: "low" })),
    });
    renderShell({
      modules: [],
      messages: shellMessages,
      path: "/core/webhooks/whk_01jq0000000000000000000001",
      api,
    });
    await screen.findByRole("heading", { level: 1, name: "https://hooks.example.com/racksail" });
    const bar = await openBar(user);
    const group = await within(bar).findByRole("group", {
      name: "Actions for https://hooks.example.com/racksail",
    });
    expect(
      within(group).getByRole("option", { name: "Edit https://hooks.example.com/racksail" }),
    ).toBeInTheDocument();
    expect(
      within(group).getByRole("option", { name: "Delete https://hooks.example.com/racksail" }),
    ).toBeInTheDocument();
    await user.click(within(group).getByRole("option", { name: "Send a test" }));
    expect(await screen.findByRole("dialog", { name: "Send a test" })).toBeInTheDocument();
    await waitFor(() => expect(sent.some((s) => s.path.endsWith("/actions/test"))).toBe(true));
  });

  it("undoes the last change made here", async () => {
    const user = userEvent.setup();
    const applied = changeSet({ state: "applied", dryRun: false });
    const undo = changeSet({ id: "chg_undo", title: "Undo: Update webhook", undoOf: applied.id });
    const { api, sent } = panel({
      "PUT /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(changeSet()),
      [`POST /api/v1/changesets/${applied.id}/apply`]: () => json(applied),
      [`GET /api/v1/changesets/${applied.id}`]: () => json(applied),
      [`POST /api/v1/changesets/${applied.id}/undo`]: () => json(undo),
      "POST /api/v1/changesets/chg_undo/apply": () =>
        json({ ...undo, state: "applied", dryRun: false }),
      "GET /api/v1/changesets/chg_undo": () => json({ ...undo, state: "applied", dryRun: false }),
    });
    renderShell({
      modules: [],
      messages: shellMessages,
      path: "/core/webhooks/whk_01jq0000000000000000000001/edit",
      api,
    });
    await user.type(await screen.findByLabelText("Description"), "Ops");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    await user.click(
      await within(await screen.findByRole("dialog")).findByRole("button", { name: "Apply" }),
    );
    await screen.findByText("Applied: Update webhook");
    const bar = await openBar(user);
    await user.click(await within(bar).findByRole("option", { name: "Undo “Update webhook”" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_undo/apply")).toBe(true),
    );
  });
});
