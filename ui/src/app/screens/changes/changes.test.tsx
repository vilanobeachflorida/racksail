import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, json, renderShell } from "@/test/render";
import { changeSet, webhook, webhookKind } from "@/test/resources";

const applied = changeSet({
  id: "chg_applied",
  title: "Update webhook",
  state: "applied",
  dryRun: false,
  job: "job_1",
  appliedAt: "2026-10-15T12:00:05Z",
});

const created = changeSet({
  id: "chg_created",
  title: "Create webhook",
  state: "applied",
  dryRun: false,
  changes: [
    {
      op: "create",
      kind: "core.webhook",
      id: webhook().id,
      scope: { level: "server" },
      source: { type: "request" },
      after: webhook().spec,
    },
  ],
});

const planned = changeSet({
  id: "chg_planned",
  title: "Update webhook",
  state: "planned",
  dryRun: true,
});

type Route = (request: Request) => Response;

function panel(routes: Record<string, Route> = {}) {
  const sent: { method: string; path: string; search: string }[] = [];
  const all: Record<string, Route> = {
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events": () => json({ events: [] }),
    "GET /api/v1/changesets": (request) =>
      json({
        changeSets:
          new URL(request.url).searchParams.get("previews") === "true"
            ? [planned]
            : [applied, created],
      }),
    "GET /api/v1/changesets/chg_applied": () => json(applied),
    "GET /api/v1/changesets/chg_planned": () => json(planned),
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

describe("the changes screens", () => {
  it("list every change with its state and risk, and undo one at once when that's safe", async () => {
    const user = userEvent.setup();
    const undo = changeSet({
      id: "chg_undo",
      title: "Undo: Update webhook",
      undoOf: "chg_applied",
    });
    const { api, sent } = panel({
      "POST /api/v1/changesets/chg_applied/undo": () => json(undo),
      "POST /api/v1/changesets/chg_undo/apply": () =>
        json({ ...undo, state: "applied", dryRun: false }),
      "GET /api/v1/changesets/chg_undo": () => json({ ...undo, state: "applied", dryRun: false }),
    });
    renderShell({ modules: [], messages: shellMessages, path: "/changes", api });
    expect(await screen.findByRole("heading", { level: 1, name: "Changes" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Changes" });
    const row = (await within(table).findByRole("link", { name: "Update webhook" })).closest(
      '[role="row"]',
    );
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("Medium risk")).toBeInTheDocument();
    expect(within(row as HTMLElement).getByText("developer")).toBeInTheDocument();
    await user.click(within(row as HTMLElement).getByRole("button", { name: "Undo" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_undo/apply")).toBe(true),
    );
    expect(sent.find((s) => s.path === "/api/v1/changesets/chg_applied/undo")?.search).toBe(
      "?dryRun=true",
    );
    expect(await screen.findByText("Applied: Undo: Update webhook")).toBeInTheDocument();
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("show what undoing would delete before doing it", async () => {
    const user = userEvent.setup();
    const undo = changeSet({
      id: "chg_undo_create",
      title: "Undo: Create webhook",
      undoOf: "chg_created",
      changes: [
        {
          op: "delete",
          kind: "core.webhook",
          id: webhook().id,
          scope: { level: "server" },
          source: { type: "request" },
          before: webhook().spec,
        },
      ],
    });
    const { api, sent } = panel({
      "POST /api/v1/changesets/chg_created/undo": () => json(undo),
      "POST /api/v1/changesets/chg_undo_create/apply": () =>
        json({ ...undo, state: "applied", dryRun: false }),
      "GET /api/v1/changesets/chg_undo_create": () =>
        json({ ...undo, state: "applied", dryRun: false }),
    });
    renderShell({ modules: [], messages: shellMessages, path: "/changes", api });
    const table = await screen.findByRole("table", { name: "Changes" });
    const row = (await within(table).findByRole("link", { name: "Create webhook" })).closest(
      '[role="row"]',
    );
    await user.click(within(row as HTMLElement).getByRole("button", { name: "Undo" }));
    const dialog = await screen.findByRole("dialog", { name: "Undo this change?" });
    expect(sent.some((s) => s.path.endsWith("/apply"))).toBe(false);
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_undo_create/apply")).toBe(true),
    );
  });

  it("show an undo that replaces later changes before doing it", async () => {
    const user = userEvent.setup();
    // The same undo as the one applied at once, but the webhook changed
    // after the change set, so undoing puts back what it had before that
    // and replaces the later change too.
    const undo = changeSet({
      id: "chg_undo_later",
      title: "Undo: Update webhook",
      undoOf: "chg_applied",
      changedSince: [webhook().id],
    });
    const { api, sent } = panel({
      "POST /api/v1/changesets/chg_applied/undo": () => json(undo),
      "POST /api/v1/changesets/chg_undo_later/apply": () =>
        json({ ...undo, state: "applied", dryRun: false }),
      "GET /api/v1/changesets/chg_undo_later": () =>
        json({ ...undo, state: "applied", dryRun: false }),
    });
    renderShell({ modules: [], messages: shellMessages, path: "/changes", api });
    const table = await screen.findByRole("table", { name: "Changes" });
    const row = (await within(table).findByRole("link", { name: "Update webhook" })).closest(
      '[role="row"]',
    );
    await user.click(within(row as HTMLElement).getByRole("button", { name: "Undo" }));
    const dialog = await screen.findByRole("dialog", { name: "Undo this change?" });
    expect(sent.find((s) => s.path === "/api/v1/changesets/chg_applied/undo")?.search).toBe(
      "?dryRun=true",
    );
    expect(sent.some((s) => s.path.endsWith("/apply"))).toBe(false);
    await user.click(await within(dialog).findByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_undo_later/apply")).toBe(true),
    );
  });

  it("list the previews that weren't applied when asked", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/changes", api });
    await screen.findByRole("table", { name: "Changes" });
    await user.click(screen.getByRole("switch", { name: "Show previews that weren't applied" }));
    await waitFor(() => expect(sent.some((s) => s.search.includes("previews=true"))).toBe(true));
    expect(await screen.findByText("Not applied yet")).toBeInTheDocument();
  });

  it("show one change with who made it, its job, and apply it when it's a preview", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel({
      "POST /api/v1/changesets/chg_planned/apply": () =>
        json({ ...planned, state: "applied", dryRun: false }),
    });
    renderShell({ modules: [], messages: shellMessages, path: "/changes/chg_planned", api });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Update webhook" }),
    ).toBeInTheDocument();
    expect(screen.getByText("developer, in the panel")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: webhook().id })).toHaveAttribute(
      "href",
      `/core/webhooks/${webhook().id}`,
    );
    await user.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_planned/apply")).toBe(true),
    );
  });

  it("show what a kept preview would do now, to apply that instead", async () => {
    const user = userEvent.setup();
    const fresh = changeSet({ id: "chg_fresh", title: "Update webhook", state: "planned" });
    let current = fresh;
    const { api, sent } = panel({
      "POST /api/v1/changesets/chg_planned/apply": () =>
        json(
          {
            type: "about:blank",
            status: 409,
            code: "changeset.preview_stale",
            params: { title: "Update webhook", preview: "chg_fresh" },
          },
          409,
        ),
      "GET /api/v1/changesets/chg_fresh": () => json(current),
      "POST /api/v1/changesets/chg_fresh/apply": () => {
        current = { ...fresh, state: "applying", dryRun: false, job: "job_2" };
        return json(current);
      },
    });
    const { router } = renderShell({
      modules: [],
      messages: shellMessages,
      path: "/changes/chg_planned",
      api,
    });
    await screen.findByRole("heading", { level: 1, name: "Update webhook" });
    await user.click(screen.getByRole("button", { name: "Apply" }));
    const dialog = await screen.findByRole("dialog", { name: "Update webhook" });
    expect(within(dialog).getByText(/Something changed since the preview was made/)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(sent.some((s) => s.path === "/api/v1/changesets/chg_fresh/apply")).toBe(true),
    );
    // The fresh preview became the change, whose page follows it.
    await waitFor(() => expect(router.state.location.pathname).toBe("/changes/chg_fresh"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Apply" })).not.toBeInTheDocument();
  });

  it("say why a kept preview can't be applied, and where to make the change again", async () => {
    const user = userEvent.setup();
    const { api } = panel({
      "POST /api/v1/changesets/chg_planned/apply": () =>
        json(
          {
            type: "about:blank",
            status: 409,
            code: "changeset.preview_has_secrets",
            params: { title: "Update webhook" },
          },
          409,
        ),
    });
    const { router } = renderShell({
      modules: [],
      messages: shellMessages,
      path: "/changes/chg_planned",
      api,
    });
    await screen.findByRole("heading", { level: 1, name: "Update webhook" });
    await user.click(screen.getByRole("button", { name: "Apply" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The change couldn't be applied");
    expect(alert).toHaveTextContent(
      "Update webhook carried secret values, which previews don't keep. Send the change again to apply it.",
    );
    await user.click(within(alert).getByRole("link", { name: "Make the change again" }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(`/core/webhooks/${webhook().id}/edit`),
    );
  });

  it("only offer undo to those who may undo what it takes", async () => {
    const { api } = panel();
    // Undoing the creation deletes the webhook, which the person may not do.
    renderShell({
      modules: [],
      messages: shellMessages,
      path: "/changes",
      api,
      can: (p) => p !== "core.webhook.delete",
    });
    const table = await screen.findByRole("table", { name: "Changes" });
    const row = (name: string) =>
      within(table).getByRole("link", { name }).closest('[role="row"]') as HTMLElement;
    await within(table).findByRole("link", { name: "Create webhook" });
    expect(within(row("Create webhook")).queryByRole("button", { name: "Undo" })).toBeNull();
    expect(within(row("Update webhook")).getByRole("button", { name: "Undo" })).toBeVisible();
  });

  it("show the job a change ran as", async () => {
    const { api } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/changes/chg_applied", api });
    expect(await screen.findByRole("link", { name: "job_1" })).toHaveAttribute(
      "href",
      "/jobs/job_1",
    );
    await waitFor(() => expect(document.title).toBe("Update webhook · Racksail"));
  });
});
