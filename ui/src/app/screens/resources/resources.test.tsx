import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ChangeSet } from "@/lib/changes";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, fakeLive, json, renderShell } from "@/test/render";
import {
  accountKind,
  changeSet,
  databaseKind,
  hiddenKind,
  ruleKind,
  siteKind,
  webhook,
  webhookKind,
} from "@/test/resources";
import type { Resource } from "./kinds";

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

function render(
  path: string,
  api = panel().api,
  can?: (permission: string) => boolean,
  live = fakeLive().live,
) {
  return renderShell({
    modules: [],
    messages: shellMessages,
    path,
    api,
    live,
    ...(can ? { can } : {}),
  });
}

/** A problem the panel answers with. */
function refusal(status: number, code: string, extra: Record<string, unknown> = {}) {
  return json({ type: "about:blank", status, code, title: code, ...extra }, status);
}

/** The change set a preview becomes once it's applied. */
const appliedOf = (cs: ChangeSet): ChangeSet => ({
  ...cs,
  state: "applied",
  dryRun: false,
  appliedAt: "2026-10-15T12:01:00Z",
});

const removal = changeSet({
  id: "chg_remove",
  title: "Delete webhook",
  changes: [
    {
      op: "delete",
      kind: "core.webhook",
      id: webhook().id,
      scope: { level: "server" },
      source: { type: "request" },
      before: webhook().spec,
    },
  ] as never,
});

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

  it("closes the dialog once the change several rows make is applied", async () => {
    const user = userEvent.setup();
    const both = changeSet({ id: "chg_delete", title: "Delete 2 webhooks" });
    const { api, sent } = panel({
      "POST /api/v1/changesets": () => json(both),
      "POST /api/v1/changesets/chg_delete/apply": () => json(appliedOf(both)),
      "GET /api/v1/changesets/chg_delete": () => json(appliedOf(both)),
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
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_delete/apply")).toBe(true),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Applied: Delete 2 webhooks")).toBeInTheDocument();
  });

  it("deletes a row from its menu, and closes the dialog once that's applied", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({
      "DELETE /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(removal),
      "POST /api/v1/changesets/chg_remove/apply": () => json(appliedOf(removal)),
      "GET /api/v1/changesets/chg_remove": () => json(appliedOf(removal)),
    });
    render("/core/webhooks", api);
    const table = await screen.findByRole("table", { name: "Webhooks" });
    await user.click(
      await within(table).findByRole("button", {
        name: "Actions for https://hooks.example.com/racksail",
      }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Delete the webhook https://hooks.example.com/racksail?",
    });
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Applied: Delete webhook")).toBeInTheDocument();
    expect(sent.find((s) => s.method === "DELETE")?.search).toBe("?dryRun=true&revision=7");
  });

  it("runs an action that takes input on several rows, keeping what was typed when refused", async () => {
    const user = userEvent.setup();
    let refuse = true;
    const resend = changeSet({ id: "chg_resend", title: "Send again: 2 webhooks", risk: "low" });
    const { api, sent } = panel({
      "POST /api/v1/changesets": (request) => {
        if (new URL(request.url).searchParams.get("dryRun") !== "true") {
          return json(appliedOf(resend));
        }
        if (refuse) {
          refuse = false;
          // Each change has the same problem with the input they share.
          return refusal(422, "core.invalid", {
            errors: [0, 1].map((i) => ({
              location: `changes[${i}].input.delivery`,
              code: "core.bad_value",
              message: "there's no such delivery",
              params: { reason: "there's no such delivery" },
            })),
          });
        }
        return json(resend);
      },
      "POST /api/v1/changesets/chg_resend/apply": () => json(appliedOf(resend)),
      "GET /api/v1/changesets/chg_resend": () => json(appliedOf(resend)),
    });
    render("/core/webhooks", api);
    const table = await screen.findByRole("table", { name: "Webhooks" });
    await within(table).findByText("https://b.example.com/hook");
    for (const box of within(table)
      .getAllByRole("checkbox", { name: /Select/ })
      .slice(1)) {
      await user.click(box);
    }
    await user.click(screen.getByRole("button", { name: "Send again" }));
    const dialog = await screen.findByRole("dialog", { name: "Send again: 2 resources" });
    await user.type(await within(dialog).findByRole("textbox", { name: /Delivery/ }), "dlv_7");
    await user.click(within(dialog).getByRole("button", { name: "Preview" }));
    // The problem shows under the field, which keeps what was typed.
    const field = await within(dialog).findByRole("textbox", { name: /Delivery/ });
    await waitFor(() => expect(field).toHaveAccessibleDescription(/there's no such delivery/));
    expect(field).toHaveValue("dlv_7");
    await user.click(within(dialog).getByRole("button", { name: "Preview" }));
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const previews = sent.filter((s) => s.path === "/api/v1/changesets");
    expect(previews.at(-1)?.body).toEqual({
      changes: [webhook().id, second.id].map((id) => ({
        op: "action",
        kind: "core.webhook",
        id,
        action: "redeliver",
        input: { delivery: "dlv_7" },
      })),
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

  it("goes back to an action's input as it was typed when the preview fails", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({
      "POST /api/v1/core/webhooks/whk_01jq0000000000000000000001/actions/redeliver": () =>
        refusal(409, "core.conflict", { params: { what: "The delivery dlv_9" } }),
    });
    render("/core/webhooks/whk_01jq0000000000000000000001?action=redeliver", api);
    const dialog = await screen.findByRole("dialog", { name: "Send again" });
    await user.type(await within(dialog).findByRole("textbox", { name: /Delivery/ }), "dlv_9");
    await user.click(within(dialog).getByRole("button", { name: "Preview" }));
    expect(await within(dialog).findByText("The change couldn't be previewed")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Back" }));
    expect(await within(dialog).findByRole("textbox", { name: /Delivery/ })).toHaveValue("dlv_9");
    expect(sent.find((s) => s.path.endsWith("/redeliver"))?.body).toEqual({ delivery: "dlv_9" });
  });

  it("follows an action someone waits for, and undoing it leaves its own toast alone", async () => {
    const user = userEvent.setup();
    const tested = changeSet({
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
          // The action noted when it last tested, which undo puts back.
          before: webhook().spec,
          after: { ...(webhook().spec as Record<string, unknown>), description: "Tested" },
        },
      ] as never,
    });
    const undo = changeSet({
      id: "chg_undo",
      title: "Undo: Send a test webhook",
      undoOf: "chg_test",
    });
    const job = {
      id: "job_t",
      type: "changeset.apply",
      title: "Send a test webhook",
      state: "succeeded",
      progress: { done: 1, total: 1 },
      attempt: 1,
      createdAt: "2026-10-15T12:00:00Z",
    };
    const { api, sent } = panel({
      "POST /api/v1/core/webhooks/whk_01jq0000000000000000000001/actions/test": () => json(tested),
      "POST /api/v1/changesets/chg_test/apply": () => json({ ...appliedOf(tested), job: "job_t" }),
      "GET /api/v1/changesets/chg_test": () => json({ ...appliedOf(tested), job: "job_t" }),
      "GET /api/v1/jobs/job_t": () => json(job),
      "GET /api/v1/jobs/job_t/logs": () => json({ lines: [] }),
      "POST /api/v1/changesets/chg_test/undo": () => json(undo),
      "POST /api/v1/changesets/chg_undo/apply": () => json(appliedOf(undo)),
      "GET /api/v1/changesets/chg_undo": () => json(appliedOf(undo)),
    });
    render("/core/webhooks/whk_01jq0000000000000000000001?action=test", api);
    const dialog = await screen.findByRole("dialog", { name: "Send a test" });
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    expect(await within(dialog).findByText("Done")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Applied: Undo: Send a test webhook")).toBeInTheDocument();
    expect(sent.some((s) => s.path === "/api/v1/changesets/chg_undo/apply")).toBe(true);
    expect(screen.queryByText("Applied: Send a test webhook")).not.toBeInTheDocument();
  });

  it("only offers undo in the history to those who may undo what it takes", async () => {
    const user = userEvent.setup();
    const created = changeSet({
      id: "chg_created",
      state: "applied",
      dryRun: false,
      title: "Create webhook",
      changes: [
        {
          op: "create",
          kind: "core.webhook",
          id: webhook().id,
          scope: { level: "server" },
          source: { type: "request" },
          after: webhook().spec,
        },
      ] as never,
    });
    const updated = changeSet({ id: "chg_updated", state: "applied", dryRun: false });
    const { api } = panel({
      "GET /api/v1/changesets": () => json({ changeSets: [updated, created] }),
    });
    const history = async (can: (permission: string) => boolean) => {
      const shown = render("/core/webhooks/whk_01jq0000000000000000000001", api, can);
      await user.click(await screen.findByRole("tab", { name: "History" }));
      const list = await screen.findByRole("list", {
        name: "Versions of the settings, newest first",
      });
      const version = (name: string) => {
        const item = within(list).getByText(name).closest("li") as HTMLElement;
        return within(item).queryByRole("button", { name: "Undo" });
      };
      await within(list).findByText("Version 2");
      const found = { update: version("Version 2"), create: version("Created, version 1") };
      shown.unmount();
      return found;
    };
    // Undoing the creation deletes the webhook; undoing the update changes it.
    const noDelete = await history((p) => p !== "core.webhook.delete");
    expect(noDelete.update).not.toBeNull();
    expect(noDelete.create).toBeNull();
    const noUpdate = await history((p) => p !== "core.webhook.update");
    expect(noUpdate.update).toBeNull();
    expect(noUpdate.create).not.toBeNull();
  });

  it("says when the ID can't be copied", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(
      new DOMException("Write permission denied.", "NotAllowedError"),
    );
    render("/core/webhooks/whk_01jq0000000000000000000001");
    await user.click(await screen.findByRole("button", { name: "Copy the ID" }));
    expect(
      await screen.findByText("The ID couldn't be copied. Select it and copy it from there."),
    ).toBeInTheDocument();
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

  it("makes new settings against what the form opened, and says when someone else changed it", async () => {
    const user = userEvent.setup();
    const live = fakeLive();
    let current = webhook();
    const { api, sent } = panel({
      "GET /api/v1/core/webhooks/whk_01jq0000000000000000000001": () => json(current),
      "PUT /api/v1/core/webhooks/whk_01jq0000000000000000000001": async (request) => {
        const body = (await request.json()) as { revision?: number };
        return body.revision === current.revision
          ? json(changeSet())
          : refusal(409, "core.conflict", { params: { what: current.id } });
      },
    });
    render("/core/webhooks/whk_01jq0000000000000000000001/edit", api, undefined, live.live);
    await screen.findByRole("heading", {
      level: 1,
      name: "Edit https://hooks.example.com/racksail",
    });
    await user.type(await screen.findByLabelText("Description"), "Mine");

    // Someone else sends the webhook one more event meanwhile.
    current = webhook({
      revision: 8,
      spec: { events: ["web.site.*", "*.failed", "web.site.deleted"] },
    });
    const stream = live.sources.find((s) => s.url === "/api/v1/resources/stream");
    act(() => {
      stream?.open();
      stream?.emit("notice", {
        op: "updated",
        id: current.id,
        kind: "core.webhook",
        scope: { level: "server" },
        generation: 3,
        revision: 8,
        phase: "ready",
      });
    });
    const title = await screen.findByText("Someone else changed this webhook since you opened it");
    const alert = title.closest('[role="alert"]') as HTMLElement;
    expect(within(alert).getByRole("list", { name: "What they changed" })).toHaveTextContent(
      "web.site.deleted",
    );
    // What was typed stays, and applying it would be refused rather than
    // undo the other change.
    expect(screen.getByLabelText("Description")).toHaveValue("Mine");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    await waitFor(() => expect(sent.some((s) => s.method === "PUT")).toBe(true));
    expect(sent.find((s) => s.method === "PUT")?.body).toMatchObject({
      revision: 7,
      spec: { events: ["web.site.*", "*.failed"], description: "Mine" },
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await user.click(within(alert).getByRole("button", { name: "Load the latest settings" }));
    await waitFor(() =>
      expect(
        screen.queryByText("Someone else changed this webhook since you opened it"),
      ).not.toBeInTheDocument(),
    );
    const description = await screen.findByLabelText("Description");
    expect(description).toHaveValue("");
    await user.type(description, "Mine");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    await screen.findByRole("dialog", { name: "Change https://hooks.example.com/racksail" });
    expect(sent.filter((s) => s.method === "PUT").at(-1)?.body).toMatchObject({
      revision: 8,
      spec: { events: ["web.site.*", "*.failed", "web.site.deleted"], description: "Mine" },
    });
  });

  it("says when the panel refuses new settings because the resource changed meanwhile", async () => {
    const user = userEvent.setup();
    const { api } = panel({
      "PUT /api/v1/core/webhooks/whk_01jq0000000000000000000001": () =>
        refusal(409, "core.conflict", { params: { what: webhook().id } }),
    });
    render("/core/webhooks/whk_01jq0000000000000000000001/edit", api);
    await user.type(await screen.findByLabelText("Description"), "Mine");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    // The panel knew before its stream said so.
    expect(
      await screen.findByText("Someone else changed this webhook since you opened it"),
    ).toBeInTheDocument();
    expect(screen.queryByText("These settings can't be used yet")).not.toBeInTheDocument();
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

describe("resources that belong to accounts", () => {
  const accounts = [
    {
      ...webhook(),
      id: "acct_01jq0000000000000000000001",
      kind: "core.account",
      scope: { level: "account", id: "acct_01jq0000000000000000000001" },
      spec: { name: "Shop" },
    },
    {
      ...webhook(),
      id: "acct_01jq0000000000000000000002",
      kind: "core.account",
      scope: { level: "account", id: "acct_01jq0000000000000000000002" },
      spec: { name: "Blog" },
    },
  ];
  const database = {
    ...webhook(),
    id: "db_01jq0000000000000000000001",
    kind: "databases.database",
    scope: { level: "account", id: "acct_01jq0000000000000000000001" },
    spec: { name: "shop" },
  };

  /** A panel with accounts, databases and sites, recording what it's sent. */
  function accountsPanel(routes: Record<string, Route> = {}) {
    return panel({
      "GET /api/v1/kinds": () =>
        json({ kinds: [webhookKind, accountKind, databaseKind, siteKind] }),
      "GET /api/v1/kinds/web.site": () => json(siteKind),
      "GET /api/v1/kinds/core.account": () => json(accountKind),
      "GET /api/v1/core/accounts": () => json({ items: accounts }),
      "GET /api/v1/databases/databases": () => json({ items: [database] }),
      "POST /api/v1/web/sites": () => json(changeSet({ title: "Create site" })),
      "POST /api/v1/core/accounts": () => json(changeSet({ title: "Create account" })),
      ...routes,
    });
  }

  it("ask who a new one belongs to, and offer the resources its fields refer to", async () => {
    const user = userEvent.setup();
    const { api, sent } = accountsPanel();
    render("/web/sites/new", api);
    await screen.findByRole("heading", { level: 1, name: "New site" });
    await user.click(await screen.findByRole("combobox", { name: /Account/ }));
    await user.click(await screen.findByRole("option", { name: "Shop" }));
    await user.type(screen.getByRole("textbox", { name: /Domain/ }), "shop.example.com");
    await user.click(screen.getByRole("combobox", { name: /Database/ }));
    await user.click(await screen.findByRole("option", { name: "shop" }));
    // Without a kind to list, a field naming a resource takes its ID.
    await user.type(
      screen.getByRole("textbox", { name: /Reseller/ }),
      "rsl_01jq0000000000000000000001",
    );
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    await screen.findByRole("dialog", { name: "Create the site" });
    expect(sent.find((s) => s.method === "POST")?.body).toEqual({
      spec: {
        domain: "shop.example.com",
        database: "db_01jq0000000000000000000001",
        reseller: "rsl_01jq0000000000000000000001",
      },
      scope: { level: "account", id: "acct_01jq0000000000000000000001" },
    });
  });

  it("say what's wrong with the owner the panel was given", async () => {
    const user = userEvent.setup();
    const { api } = accountsPanel({
      "POST /api/v1/web/sites": () =>
        refusal(422, "core.invalid", {
          errors: [
            {
              location: "body.scope.id",
              code: "core.not_found",
              message: "That account doesn't exist.",
              params: { what: "That account" },
            },
          ],
        }),
    });
    render("/web/sites/new", api);
    await user.click(await screen.findByRole("combobox", { name: /Account/ }));
    await user.click(await screen.findByRole("option", { name: "Blog" }));
    await user.type(screen.getByRole("textbox", { name: /Domain/ }), "blog.example.com");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    expect(await screen.findByText("That account doesn't exist.")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("ask nothing of an account, which belongs to itself, and name it up front", async () => {
    const user = userEvent.setup();
    const { api, sent } = accountsPanel();
    render("/core/accounts/new", api);
    await screen.findByRole("heading", { level: 1, name: "New account" });
    expect(screen.queryByRole("combobox", { name: /Account/ })).not.toBeInTheDocument();
    await user.type(await screen.findByRole("textbox", { name: /Name/ }), "Studio");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    await screen.findByRole("dialog", { name: "Create the account" });
    const body = sent.find((s) => s.method === "POST")?.body as {
      id: string;
      scope: { level: string; id: string };
    };
    expect(body.id).toMatch(/^acct_[0-7][0-9a-hjkmnp-tv-z]{25}$/);
    expect(body.scope).toEqual({ level: "account", id: body.id });
  });
});

describe("the home screen's quick actions", () => {
  it("start a new resource of the kinds no module starts itself", async () => {
    const user = userEvent.setup();
    const { router } = render("/");
    const card = await screen.findByRole("region", { name: "Quick actions" });
    await user.click(await within(card).findByRole("button", { name: "New webhook" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/core/webhooks/new"));
  });

  it("leave out kinds the person may not create", async () => {
    const { api } = panel({
      "GET /api/v1/kinds": () => json({ kinds: [webhookKind, accountKind] }),
      "GET /api/v1/operations": () =>
        json({
          operations: ["webhook", "account"].map((name) => ({
            id: `create${name}`,
            method: "POST",
            path: `/api/v1/core/${name}s`,
            kind: `core.${name}`,
            operation: "create",
            permission: `core.${name}.create`,
            risk: "medium",
            mutating: true,
            summary: `Create a ${name}`,
          })),
        }),
    });
    render("/", api, (p) => p !== "core.webhook.create");
    const card = await screen.findByRole("region", { name: "Quick actions" });
    expect(await within(card).findByRole("button", { name: "New account" })).toBeVisible();
    expect(within(card).queryByRole("button", { name: "New webhook" })).not.toBeInTheDocument();
  });
});

describe("kinds add-ons add", () => {
  const rule = {
    id: "grule_01jq0000000000000000000001",
    kind: "guard.rule",
    scope: { level: "server" },
    generation: 1,
    revision: 4,
    createdAt: "2026-10-15T10:00:00Z",
    updatedAt: "2026-10-15T10:00:00Z",
    labels: {},
    spec: { pattern: "wp-admin" },
    status: { phase: "ready", watching: "wp-admin", observedGeneration: 1 },
  } as unknown as Resource;
  const rules = ruleKind.path;

  /** A panel with an add-on's kind, which no operation of the API names. */
  function withRules(routes: Record<string, Route> = {}) {
    return panel({
      "GET /api/v1/kinds": () => json({ kinds: [webhookKind, ruleKind] }),
      "GET /api/v1/kinds/guard.rule": () => json(ruleKind),
      [`GET ${rules}`]: () => json({ items: [rule] }),
      [`GET ${rules}/${rule.id}`]: () => json(rule),
      [`GET ${rules}/${rule.id}/history`]: () => json({ versions: [] }),
      ...routes,
    });
  }

  it("are offered to those their own permissions allow", async () => {
    const user = userEvent.setup();
    const { router } = render("/", withRules().api, (p) => p !== "core.webhook.create");
    const card = await screen.findByRole("region", { name: "Quick actions" });
    expect(within(card).queryByRole("button", { name: "New webhook" })).not.toBeInTheDocument();
    await user.click(await within(card).findByRole("button", { name: "New guard rule" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/guard/rules/new"));
  });

  it("are left out for those their permissions don't allow", async () => {
    render("/", withRules().api, (p) => p !== "guard.rule.create");
    const card = await screen.findByRole("region", { name: "Quick actions" });
    expect(await within(card).findByRole("button", { name: "New webhook" })).toBeVisible();
    expect(within(card).queryByRole("button", { name: "New guard rule" })).not.toBeInTheDocument();
  });

  it("list their resources from the path the kind's description gives", async () => {
    const { api, sent } = withRules();
    render("/guard/rules", api);
    expect(await screen.findByRole("heading", { level: 1, name: "Rules" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Rules" });
    expect(await within(table).findByRole("link", { name: rule.id })).toHaveAttribute(
      "href",
      `/guard/rules/${rule.id}`,
    );
    expect(sent.some((s) => s.method === "GET" && s.path === rules)).toBe(true);
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("show a resource's settings and the status its add-on reported", async () => {
    const { api, sent } = withRules();
    render(`/guard/rules/${rule.id}`, api);
    expect(await screen.findByRole("heading", { level: 1, name: rule.id })).toBeInTheDocument();
    expect((await screen.findAllByText("wp-admin")).length).toBeGreaterThan(0);
    expect(sent.some((s) => s.method === "GET" && s.path === `${rules}/${rule.id}`)).toBe(true);
  });

  it("create a resource through its preview at that path", async () => {
    const user = userEvent.setup();
    const created = changeSet({
      id: "chg_rule",
      title: "Create guard rule",
      changes: [
        {
          op: "create",
          kind: "guard.rule",
          id: rule.id,
          scope: { level: "server" },
          source: { type: "request" },
          after: { pattern: "xmlrpc" },
        },
      ] as never,
    });
    const { api, sent } = withRules({ [`POST ${rules}`]: () => json(created) });
    render("/guard/rules/new", api);
    expect(
      await screen.findByRole("heading", { level: 1, name: "New guard rule" }),
    ).toBeInTheDocument();
    await user.type(await screen.findByRole("textbox", { name: /Pattern/ }), "xmlrpc");
    await user.click(screen.getByRole("button", { name: "Review the change" }));
    expect(await screen.findByRole("dialog", { name: "Create the guard rule" })).toBeVisible();
    const preview = sent.find((s) => s.method === "POST" && s.path === rules);
    expect(preview?.search).toBe("?dryRun=true");
    expect(preview?.body).toMatchObject({ spec: { pattern: "xmlrpc" } });
  });
});
