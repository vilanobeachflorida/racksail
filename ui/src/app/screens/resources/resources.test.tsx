import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderShell } from "@/test/render";
import { changeSet, hiddenKind, webhook, webhookKind } from "@/test/resources";

const second = webhook({
  id: "whk_01jq0000000000000000000002",
  revision: 3,
  spec: { url: "https://b.example.com/hook", events: ["*"] },
  status: { phase: "failed", message: "The receiver answered 500.", observedGeneration: 2 },
  labels: {},
});

type Route = (request: Request) => Response | Promise<Response>;

/** A panel that describes the webhook kind and keeps the given webhooks, recording what it's sent. */
function panel(routes: Record<string, Route> = {}) {
  const sent: { method: string; path: string; search: string; body: unknown }[] = [];
  const record =
    (route: Route): Route =>
    async (request) => {
      const url = new URL(request.url);
      const text = request.method === "GET" ? "" : await request.clone().text();
      sent.push({
        method: request.method,
        path: url.pathname,
        search: url.search,
        body: text ? JSON.parse(text) : undefined,
      });
      return route(request);
    };
  const all: Record<string, Route> = {
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind, hiddenKind] }),
    "GET /api/v1/kinds/core.webhook": () => json(webhookKind),
    "GET /api/v1/core/webhooks": () => json({ items: [webhook(), second] }),
    "GET /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(webhook()),
    "GET /api/v1/core/webhooks/whk_01jq0000000000000000000001/history": () =>
      json({
        versions: [
          {
            generation: 1,
            time: "2026-10-01T10:00:00Z",
            actor: "developer",
            changeSet: "chg_created",
            spec: { url: "https://hooks.example.com/racksail", events: ["web.site.*"] },
          },
          {
            generation: 2,
            time: "2026-10-14T09:30:00Z",
            actor: "developer",
            changeSet: "chg_updated",
            spec: { url: "https://hooks.example.com/racksail", events: ["web.site.*", "*.failed"] },
          },
        ],
      }),
    "GET /api/v1/changesets": () =>
      json({
        changeSets: [
          changeSet({
            id: "chg_updated",
            state: "applied",
            dryRun: false,
            title: "Update webhook",
          }),
          changeSet({
            id: "chg_created",
            state: "applied",
            dryRun: false,
            title: "Create webhook",
          }),
        ],
      }),
    "GET /api/v1/events": () => json({ events: [] }),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
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
    ...routes,
  };
  const api = fakeApi(
    Object.fromEntries(Object.entries(all).map(([key, route]) => [key, record(route)])),
  );
  return { api, sent };
}

function render(path: string, api = panel().api, can?: (permission: string) => boolean) {
  return renderShell({ modules: [], messages: shellMessages, path, api, ...(can ? { can } : {}) });
}

describe("a kind's list", () => {
  it("shows the columns the kind's hints name, with links, badges and states", async () => {
    render("/core/webhooks");
    expect(await screen.findByRole("heading", { level: 1, name: "Webhooks" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Webhooks" });
    expect(
      await within(table).findByRole("link", { name: "https://b.example.com/hook" }),
    ).toHaveAttribute("href", "/core/webhooks/whk_01jq0000000000000000000002");
    expect(within(table).getByText("web.site.*")).toBeInTheDocument();
    expect(within(table).getByText("Failed")).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: /Last delivery/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New webhook" })).toHaveAttribute(
      "href",
      "/core/webhooks/new",
    );
    const trail = screen.getByRole("navigation", { name: "Where you are" });
    expect(within(trail).getByRole("link", { name: "Panel" })).toHaveAttribute("href", "/core");
    await waitFor(() => expect(document.title).toBe("Webhooks · Racksail"));
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("deletes the rows chosen as one change, through its preview", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({
      "POST /api/v1/changesets": () =>
        json(
          changeSet({
            id: "chg_delete",
            title: "Delete 2 webhooks",
            changes: [
              {
                op: "delete",
                kind: "core.webhook",
                id: webhook().id,
                scope: { level: "server" },
                source: { type: "request" },
                before: webhook().spec,
              },
              {
                op: "delete",
                kind: "core.webhook",
                id: second.id,
                scope: { level: "server" },
                source: { type: "request" },
                before: second.spec,
              },
            ] as never,
          }),
        ),
    });
    render("/core/webhooks", api);
    const table = await screen.findByRole("table", { name: "Webhooks" });
    await within(table).findByText("https://b.example.com/hook");
    for (const box of within(table)
      .getAllByRole("checkbox", { name: /Select/ })
      .slice(1)) {
      await user.click(box);
    }
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete 2 webhooks?" });
    expect(await within(dialog).findByText("Delete 2 webhooks")).toBeInTheDocument();
    const request = sent.find((s) => s.method === "POST" && s.path === "/api/v1/changesets");
    expect(request?.search).toBe("?dryRun=true");
    expect(request?.body).toEqual({
      changes: [
        { op: "delete", kind: "core.webhook", id: webhook().id, revision: 7 },
        { op: "delete", kind: "core.webhook", id: second.id, revision: 3 },
      ],
    });
  });

  it("isn't there for kinds the panel keeps to itself", async () => {
    render("/core/sessions");
    expect(
      await screen.findByRole("heading", { level: 1, name: "This page doesn't exist" }),
    ).toBeInTheDocument();
  });
});

describe("a resource's page", () => {
  it("shows its settings, state, conditions, history and actions", async () => {
    const user = userEvent.setup();
    render("/core/webhooks/whk_01jq0000000000000000000001");
    expect(
      await screen.findByRole("heading", { level: 1, name: "https://hooks.example.com/racksail" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Ready", { selector: "span" })).toBeInTheDocument();
    expect(await screen.findByText("Set, kept hidden")).toBeInTheDocument();
    const facts = screen.getByRole("region", { name: "Details" });
    expect(within(facts).getByText("The server")).toBeInTheDocument();
    expect(within(facts).getByText("team=ops")).toBeInTheDocument();
    await waitFor(() =>
      expect(document.title).toBe("https://hooks.example.com/racksail · Webhook · Racksail"),
    );

    await user.click(screen.getByRole("tab", { name: "Status" }));
    expect(await screen.findByText("The last event was delivered.")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Conditions" })).toBeInTheDocument();
    expect(screen.getByText("Last delivery")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "History" }));
    const history = await screen.findByRole("list", {
      name: "Versions of the settings, newest first",
    });
    expect(within(history).getByText("Version 2")).toBeInTheDocument();
    expect(within(history).getByText("Created, version 1")).toBeInTheDocument();
    expect(within(history).getAllByRole("button", { name: "Undo" })).toHaveLength(2);

    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(await screen.findByRole("menuitem", { name: "Send a test" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Send again" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("leaves out what the person may not do", async () => {
    render("/core/webhooks/whk_01jq0000000000000000000001", panel().api, (p) =>
      p.endsWith(".read"),
    );
    await screen.findByRole("heading", { level: 1, name: "https://hooks.example.com/racksail" });
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Actions" })).not.toBeInTheDocument();
  });

  it("runs the action the address asks for through its preview", async () => {
    const { api, sent } = panel({
      "POST /api/v1/core/webhooks/whk_01jq0000000000000000000001/actions/test": () =>
        json(
          changeSet({
            id: "chg_test",
            title: "Send a test webhook",
            risk: "low",
            changes: [
              {
                op: "action",
                action: "test",
                kind: "core.webhook",
                id: webhook().id,
                scope: { level: "server" },
                source: { type: "request" },
              },
            ] as never,
          }),
        ),
    });
    render("/core/webhooks/whk_01jq0000000000000000000001?action=test", api);
    const dialog = await screen.findByRole("dialog", { name: "Send a test" });
    expect(await within(dialog).findByRole("button", { name: "Apply" })).toBeInTheDocument();
    expect(sent.find((s) => s.path.endsWith("/actions/test"))?.search).toBe("?dryRun=true");
  });

  it("says when the resource doesn't exist", async () => {
    render("/core/webhooks/whk_01jq0000000000000000000009");
    expect(
      await screen.findByRole("heading", { level: 1, name: "This webhook doesn't exist" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to webhooks" })).toHaveAttribute(
      "href",
      "/core/webhooks",
    );
  });
});

describe("creating and editing", () => {
  it("previews a new resource, applies it with its secret and opens it", async () => {
    const user = userEvent.setup();
    const created = changeSet({
      id: "chg_create",
      title: "Create webhook",
      changes: [
        {
          op: "create",
          kind: "core.webhook",
          id: webhook().id,
          scope: { level: "server" },
          source: { type: "request" },
          after: { url: "https://hooks.example.com/racksail", events: ["core.*"], secret: "ref" },
        },
      ] as never,
    });
    const { api, sent } = panel({
      "POST /api/v1/core/webhooks": (request) =>
        new URL(request.url).searchParams.get("dryRun")
          ? json(created)
          : json({
              ...created,
              id: "chg_create_applied",
              state: "applied",
              dryRun: false,
              appliedAt: "2026-10-15T12:01:00Z",
            }),
      "GET /api/v1/changesets/chg_create_applied": () =>
        json({ ...created, id: "chg_create_applied", state: "applied", dryRun: false }),
    });
    const { router } = render("/core/webhooks/new", api);
    expect(
      await screen.findByRole("heading", { level: 1, name: "New webhook" }),
    ).toBeInTheDocument();
    await user.type(await screen.findByLabelText(/URL/), "https://hooks.example.com/racksail");
    await user.click(screen.getByRole("button", { name: "Add to Events" }));
    await user.type(screen.getByRole("textbox", { name: "Events 1" }), "core.*");
    await user.type(screen.getByLabelText("Signing secret"), "swordfish");
    await user.click(screen.getByRole("button", { name: "Review the change" }));

    const dialog = await screen.findByRole("dialog", { name: "Create the webhook" });
    expect(await within(dialog).findByText("Create webhook")).toBeInTheDocument();
    const preview = sent.find((s) => s.method === "POST" && s.path === "/api/v1/core/webhooks");
    expect(preview?.search).toBe("?dryRun=true");
    expect(preview?.body).toEqual({
      spec: { url: "https://hooks.example.com/racksail", events: ["core.*"] },
      secrets: { secret: "swordfish" },
    });

    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    // The secret's value isn't kept with the preview, so the change is sent again.
    await waitFor(() =>
      expect(
        sent.filter((s) => s.method === "POST" && s.path === "/api/v1/core/webhooks"),
      ).toHaveLength(2),
    );
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/core/webhooks/whk_01jq0000000000000000000001"),
    );
    expect(await screen.findByText("Applied: Create webhook")).toBeInTheDocument();
  });

  it("shows the server's problems on the fields they're about", async () => {
    const user = userEvent.setup();
    const { api } = panel({
      "POST /api/v1/core/webhooks": () =>
        json(
          {
            code: "core.invalid",
            title: "Some values aren't valid",
            status: 422,
            errors: [
              {
                location: "body.spec.url",
                code: "core.bad_value",
                message: "use an http or https URL",
                params: { reason: "use an http or https URL" },
              },
            ],
          },
          422,
        ),
    });
    render("/core/webhooks/new", api);
    await screen.findByRole("heading", { level: 1, name: "New webhook" });
    await user.type(await screen.findByLabelText(/URL/), "ftp://example.com/hook");
    await user.click(screen.getByRole("button", { name: "Add to Events" }));
    await user.type(screen.getByRole("textbox", { name: "Events 1" }), "*");
    await user.type(screen.getByLabelText("Signing secret"), "swordfish");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    expect(await screen.findByText(/use an http or https URL/)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("leaves an optional section nobody filled in out of the change", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({ "POST /api/v1/core/webhooks": () => json(changeSet()) });
    render("/core/webhooks/new", api);
    await screen.findByRole("heading", { level: 1, name: "New webhook" });
    await user.click(await screen.findByRole("button", { name: "Advanced" }));
    await user.type(await screen.findByLabelText(/URL/), "https://hooks.example.com/racksail");
    await user.click(screen.getByRole("button", { name: "Add to Events" }));
    await user.type(screen.getByRole("textbox", { name: "Events 1" }), "*");
    await user.type(screen.getByLabelText("Signing secret"), "swordfish");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    await screen.findByRole("dialog");
    const body = sent.find((s) => s.method === "POST")?.body as { spec: Record<string, unknown> };
    expect(body.spec).not.toHaveProperty("owner");
  });

  it("previews new settings and applies the preview as it was shown", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({
      "PUT /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(changeSet()),
      "POST /api/v1/changesets/chg_01jq0000000000000000000001/apply": () =>
        json(changeSet({ state: "applied", dryRun: false })),
      "GET /api/v1/changesets/chg_01jq0000000000000000000001": () =>
        json(changeSet({ state: "applied", dryRun: false })),
    });
    render("/core/webhooks/whk_01jq0000000000000000000001/edit", api);
    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "Edit https://hooks.example.com/racksail",
      }),
    ).toBeInTheDocument();
    expect(await screen.findByLabelText("Signing secret")).toHaveAttribute(
      "placeholder",
      expect.stringMatching(/.+/),
    );
    await user.type(screen.getByLabelText("Description"), "Ops");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Change https://hooks.example.com/racksail",
    });
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(
        sent.some((s) => s.path === "/api/v1/changesets/chg_01jq0000000000000000000001/apply"),
      ).toBe(true),
    );
    const update = sent.find((s) => s.method === "PUT");
    expect(update?.body).toEqual({
      spec: {
        url: "https://hooks.example.com/racksail",
        events: ["web.site.*", "*.failed"],
        secret: "ref",
        description: "Ops",
      },
      labels: { team: "ops" },
      revision: 7,
    });
    expect(await screen.findByText("Applied: Update webhook")).toBeInTheDocument();
  });
});
