import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Kind } from "@/app/screens/resources/kinds";
import { pseudoMessage } from "@/lib/pseudo";
import { untranslatedText } from "@/test/pseudo";
import { fakeApi, json, renderApp } from "@/test/render";
import { changeSet, webhook, webhookKind } from "@/test/resources";

/*
 * Every screen in the en-XA pseudo-locale, with data written in Greek, so
 * any Latin letter outside a message's brackets is text written into the
 * code rather than taken from a catalog.
 */

const x = (text: string) => pseudoMessage(text, "en-XA");

/** The data's own words in Greek: schema descriptions are the panel's English in the field. */
function greek(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(greek);
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([k, v]) => [
        k,
        k === "description" && typeof v === "string" ? "Περιγραφή" : greek(v),
      ]),
    );
  }
  return value;
}

const kind = {
  ...(greek(webhookKind) as Kind),
  prefix: "ψκ",
  // The schema's examples and patterns are the field's, not the page's.
  actions: webhookKind.actions?.map((a) => ({
    ...a,
    title: "Δοκιμή",
    summary: "Δοκιμή",
    description: "Δοκιμή",
  })),
} as Kind;

const id = "ψκ_01";
const resource = webhook({
  id,
  spec: { url: "παράδειγμα.ελ/άγκιστρο", events: ["δοκιμή.όλα"], secret: "ψ" },
  labels: { ομάδα: "λειτουργίες" },
  status: {
    phase: "degraded",
    message: "Αποτυχία",
    observedGeneration: 2,
    lastDelivery: "2026-10-14T09:31:00Z",
    conditions: [
      {
        type: "Έτοιμο",
        status: "false",
        reason: "Αποτυχία",
        message: "Αποτυχία",
        lastTransitionTime: "2026-10-14T09:31:00Z",
      },
    ],
  },
});

const applied = changeSet({
  id: "αλλ_01",
  title: "Αλλαγή",
  state: "applied",
  dryRun: false,
  job: "εργ_01",
  actor: { type: "user", name: "Άννα", source: "ui" },
  changes: [
    {
      op: "update",
      kind: "core.webhook",
      id,
      scope: { level: "server" },
      source: { type: "request" },
      before: { διεύθυνση: "παράδειγμα.ελ" },
      after: { διεύθυνση: "παράδειγμα.ελ", ομάδα: "Ομάδα" },
      diff: [{ path: "ομάδα", op: "add", after: "Ομάδα" }],
    },
  ] as never,
});

const job = {
  id: "εργ_01",
  type: "αλλαγή.εφαρμογή",
  title: "Αλλαγή",
  state: "failed",
  progress: { done: 1, total: 2 },
  attempt: 2,
  actor: { type: "user", name: "Άννα", source: "ui" },
  changeSet: "αλλ_01",
  resource: id,
  error: { code: "δοκιμή.λάθος", message: "Αποτυχία" },
  createdAt: "2026-10-15T12:00:00Z",
  startedAt: "2026-10-15T12:00:01Z",
  finishedAt: "2026-10-15T12:00:31Z",
};

const event = {
  id: "γεγ_01",
  type: "δοκιμή.συνέβη",
  time: "2026-10-15T12:00:00Z",
  severity: "warning",
  scope: { level: "server" },
  message: "Κάτι συνέβη",
  actor: { type: "user", name: "Άννα", source: "ui" },
  subject: id,
  kind: "core.webhook",
  changeSet: "αλλ_01",
  job: "εργ_01",
};

const api = fakeApi({
  "GET /api/v1/health": () => json({ status: "ok", checks: [] }),
  "GET /api/v1/kinds": () => json({ kinds: [kind] }),
  "GET /api/v1/kinds/core.webhook": () => json(kind),
  "GET /api/v1/core/webhooks": () => json({ items: [resource] }),
  [`GET /api/v1/core/webhooks/${encodeURIComponent(id)}`]: () => json(resource),
  [`GET /api/v1/core/webhooks/${encodeURIComponent(id)}/history`]: () =>
    json({
      versions: [
        // A diff names the fields it touches by their place in the data.
        {
          generation: 1,
          time: "2026-10-01T10:00:00Z",
          actor: "Άννα",
          changeSet: "αλλ_00",
          spec: { διεύθυνση: "παράδειγμα.ελ" },
        },
        {
          generation: 2,
          time: "2026-10-14T09:30:00Z",
          actor: "Άννα",
          changeSet: "αλλ_01",
          spec: { διεύθυνση: "παράδειγμα.ελ", ομάδα: "λειτουργίες" },
        },
      ],
    }),
  "GET /api/v1/changesets": () => json({ changeSets: [applied] }),
  [`GET /api/v1/changesets/${encodeURIComponent("αλλ_01")}`]: () => json(applied),
  "GET /api/v1/jobs": () => json({ jobs: [job] }),
  [`GET /api/v1/jobs/${encodeURIComponent("εργ_01")}`]: () => json(job),
  [`GET /api/v1/jobs/${encodeURIComponent("εργ_01")}/logs`]: () =>
    json({
      lines: [{ seq: 1, time: "2026-10-15T12:00:02Z", level: "ERROR", message: "Αποτυχία" }],
    }),
  "GET /api/v1/events": () => json({ events: [event] }),
  "GET /api/v1/events/types": () => json({ types: ["δοκιμή.συνέβη"] }),
  "GET /api/v1/search": () =>
    json({
      results: [
        {
          id,
          kind: "core.webhook",
          scope: { level: "server" },
          title: "παράδειγμα.ελ/άγκιστρο",
          phase: "ready",
          updatedAt: "2026-10-14T09:30:00Z",
        },
      ],
      more: ["core.webhook"],
    }),
  "GET /api/v1/operations": () =>
    json({
      operations: [
        {
          id: "δημιουργία",
          method: "POST",
          path: "/api/v1/core/webhooks",
          kind: "core.webhook",
          operation: "create",
          permission: "core.webhook.create",
          risk: "medium",
          mutating: true,
          summary: "Δημιουργία",
        },
      ],
    }),
  "GET /api/v1/inbox": () =>
    json({
      items: [
        {
          id: "εργ",
          category: "job",
          severity: "error",
          source: "core",
          code: "job_failed",
          params: { title: "Αλλαγή", error: "Αποτυχία" },
          title: "Αλλαγή",
          message: "Αποτυχία",
          time: "2026-10-15T12:00:31Z",
          scope: { level: "server" },
          version: "1",
          actions: [
            { type: "retry_job", title: "Retry", target: "εργ_01" },
            { type: "open_job", title: "Show the log", target: "εργ_01" },
          ],
        },
        {
          id: "ενημερώσεις",
          category: "update",
          severity: "warning",
          source: "core",
          code: "updates_available",
          params: { count: "3", security: "1", all: "false" },
          title: "Ενημερώσεις",
          message: "Ασφάλεια",
          time: "2026-10-15T12:00:31Z",
          scope: { level: "server" },
          version: "2",
          actions: [{ type: "docs", title: "How updates work", target: "install/updates" }],
        },
      ],
      dismissed: 2,
      checking: ["certificates"],
    }),
});

async function visit(path: string, ready: () => Promise<unknown>) {
  const result = renderApp(api, path, { locale: "en-XA" });
  await screen.findByRole("heading", { level: 1 });
  await ready();
  return result;
}

describe("the screens in a pseudo-locale", () => {
  it("take every word on the home screen's inbox from the catalogs", async () => {
    const { unmount } = await visit("/", () => screen.findByText(/Αποτυχία/));
    await screen.findByText(x("How updates work"));
    expect(untranslatedText()).toEqual([]);
    unmount();
  });

  it("take every word of a kind's list, form and resource from the catalogs", async () => {
    const user = userEvent.setup();
    let page = await visit("/core/webhooks", () => screen.findByText("παράδειγμα.ελ/άγκιστρο"));
    expect(untranslatedText()).toEqual([]);
    page.unmount();

    page = await visit(`/core/webhooks/${id}`, () => screen.findByText("Αποτυχία"));
    expect(untranslatedText()).toEqual([]);
    for (const tab of ["Status", "History", "Events"]) {
      await user.click(screen.getByRole("tab", { name: x(tab) }));
      await waitFor(() =>
        expect(screen.getByRole("tab", { name: x(tab) })).toHaveAttribute("aria-selected", "true"),
      );
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(untranslatedText(), tab).toEqual([]);
    }
    page.unmount();

    page = await visit("/core/webhooks/new", () => screen.findByText(x("Review the change")));
    expect(untranslatedText()).toEqual([]);
    page.unmount();

    page = await visit(`/core/webhooks/${id}/edit`, () =>
      screen.findByText(x("Review the change")),
    );
    expect(untranslatedText()).toEqual([]);
    page.unmount();
  });

  it("take every word of the panel's own screens from the catalogs", async () => {
    const screens: [string, string | RegExp][] = [
      ["/jobs", "Αλλαγή"],
      ["/jobs/εργ_01", "Αποτυχία"],
      ["/events", "Κάτι συνέβη"],
      ["/changes", "Αλλαγή"],
      ["/changes/αλλ_01", "Ομάδα"],
      ["/search?q=παράδειγμα", "παράδειγμα.ελ/άγκιστρο"],
      ["/resources", x("Webhooks")],
      ["/core", x("Webhooks")],
      ["/settings/general", x("Clock")],
      ["/settings/appearance", x("Your theme")],
    ];
    for (const [path, text] of screens) {
      const { unmount } = await visit(path, () => screen.findAllByText(text));
      expect(untranslatedText(), path).toEqual([]);
      unmount();
    }
  });
});
