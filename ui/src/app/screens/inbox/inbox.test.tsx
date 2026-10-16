import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createIntl } from "react-intl";
import { describe, expect, it } from "vitest";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, fakeLive, json, renderShell } from "@/test/render";
import { webhookKind } from "@/test/resources";
import type { InboxItem } from "./api";
import inboxCatalog from "./messages/en.json";
import { inboxMessage, inboxMessageId, inboxTitle } from "./text";

const now = new Date().toISOString();

function item(overrides: Partial<InboxItem>): InboxItem {
  return {
    id: "item",
    category: "job",
    severity: "error",
    source: "core",
    code: "job_failed",
    title: "Something failed",
    time: now,
    scope: { level: "server" },
    version: "1",
    actions: [],
    ...overrides,
  } as InboxItem;
}

const jobFailed = item({
  id: "job:job_1",
  code: "job_failed",
  params: { title: "Create a site", error: "The mirror isn't available right now." },
  title: "Create a site failed",
  message: "The mirror isn't available right now.",
  job: "job_1",
  changeSet: "chg_1",
  actions: [
    { type: "retry_job", title: "Retry", target: "job_1" },
    { type: "undo_changeset", title: "Undo the change", target: "chg_1" },
    { type: "open_job", title: "Show the log", target: "job_1" },
  ],
});

const updates = item({
  id: "updates:packages",
  category: "update",
  severity: "warning",
  code: "updates_available",
  params: { count: "3", security: "1", all: "false" },
  title: "3 package updates are available",
  message: "One of them fixes security problems.",
  actions: [{ type: "docs", title: "How updates work", target: "install/updates" }],
});

const certificate = item({
  id: "certificate:mail.example.com",
  category: "certificate",
  severity: "warning",
  code: "certificate_expiring",
  params: { name: "mail.example.com", date: "2026-10-17T00:00:00Z", days: "1" },
  title: "The certificate for mail.example.com expires tomorrow",
});

const degraded = item({
  id: "resource:whk_1",
  category: "resource",
  severity: "warning",
  code: "resource_degraded",
  params: {
    kind: "Webhook",
    name: "https://hooks.example.com",
    message: "The last 6 attempts failed.",
  },
  title: "Webhook https://hooks.example.com works only in part",
  message: "The last 6 attempts failed.",
  subject: "whk_01jq0000000000000000000001",
  kind: "core.webhook",
  actions: [
    { type: "open_resource", title: "Open", target: "whk_01jq0000000000000000000001" },
    { type: "open_events", title: "Show its events", target: "whk_01jq0000000000000000000001" },
  ],
});

const backups = item({
  id: "backups:failed",
  category: "job",
  severity: "error",
  source: "backups",
  code: "backup_failed",
  title: "Last night's backup failed",
});

type Route = (request: Request) => Response | Promise<Response>;

function panel(inbox: Route, routes: Record<string, Route> = {}) {
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
    "GET /api/v1/inbox": inbox,
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/health": () => json({ status: "ok", checks: [] }),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events": () => json({ events: [] }),
    ...routes,
  };
  return {
    api: fakeApi(Object.fromEntries(Object.entries(all).map(([k, r]) => [k, record(r)]))),
    sent,
  };
}

async function home(api: ReturnType<typeof panel>["api"], live = fakeLive()) {
  const result = renderShell({ modules: [], messages: shellMessages, api, live: live.live });
  const card = await screen.findByRole("region", { name: /Needs your attention/ });
  return { ...result, card, sources: live.sources };
}

describe("the attention inbox", () => {
  it("shows what needs attention in the person's words, with what can be done", async () => {
    const { api } = panel(() =>
      json({ items: [jobFailed, updates, certificate, degraded, backups], dismissed: 0 }),
    );
    const { card } = await home(api);
    expect(await within(card).findByText("Create a site failed")).toBeInTheDocument();
    expect(within(card).getByText("3 package updates are available")).toBeInTheDocument();
    expect(within(card).getByText("One of them fixes security problems.")).toBeInTheDocument();
    expect(
      within(card).getByText("The certificate for mail.example.com expires tomorrow"),
    ).toBeInTheDocument();
    expect(
      within(card).getByText("Webhook https://hooks.example.com works only in part"),
    ).toBeInTheDocument();
    // What a module found reads in its own words, or the panel's English.
    expect(within(card).getByText("Last night's backup failed")).toBeInTheDocument();
    expect(within(card).getByText("5 items")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Undo the change" })).toBeInTheDocument();
    expect(
      within(card).getByRole("button", { name: "How updates work (opens in a new tab)" }),
    ).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Open" })).toBeInTheDocument();
    expect(await axeViolations(card)).toEqual([]);
  });

  it("shows the first few and the rest when asked", async () => {
    const many = Array.from({ length: 7 }, (_, i) =>
      item({ id: `job:${i}`, params: { title: `Job ${i}`, error: "" }, title: `Job ${i} failed` }),
    );
    const { api } = panel(() => json({ items: many, dismissed: 0 }));
    const user = userEvent.setup();
    const { card } = await home(api);
    await within(card).findByText("Job 0 failed");
    expect(within(card).queryByText("Job 6 failed")).not.toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Show all 7" }));
    expect(within(card).getByText("Job 6 failed")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Show fewer" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("retries a failed job and shows it in the jobs drawer", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel(() => json({ items: [jobFailed], dismissed: 0 }), {
      "POST /api/v1/jobs/job_1/retry": () =>
        json({
          id: "job_2",
          type: "changeset.apply",
          title: "Create a site",
          state: "queued",
          progress: { done: 0, total: 0 },
          attempt: 1,
          createdAt: now,
        }),
    });
    const { card } = await home(api);
    await user.click(await within(card).findByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(sent.some((s) => s.method === "POST" && s.path === "/api/v1/jobs/job_1/retry")).toBe(
        true,
      ),
    );
    expect(await screen.findByRole("dialog", { name: "Jobs" })).toBeInTheDocument();
  });

  it("goes to a resource's events, and opens resources it's about", async () => {
    const user = userEvent.setup();
    const { api } = panel(() => json({ items: [degraded], dismissed: 0 }));
    const { card, router } = await home(api);
    await user.click(await within(card).findByRole("button", { name: "Show the events" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/events"));
    expect(router.state.location.search).toEqual({ subject: "whk_01jq0000000000000000000001" });
  });

  it("dismisses an item, offering to bring it back", async () => {
    const user = userEvent.setup();
    let dismissed = false;
    const { api, sent } = panel(
      () => json({ items: dismissed ? [] : [jobFailed], dismissed: dismissed ? 1 : 0 }),
      {
        "POST /api/v1/inbox/job%3Ajob_1/dismiss": () => {
          dismissed = true;
          return new Response(null, { status: 204 });
        },
        "DELETE /api/v1/inbox/job%3Ajob_1/dismiss": () => {
          const restored = dismissed;
          dismissed = false;
          return json({ restored });
        },
      },
    );
    const { card } = await home(api);
    await user.click(
      await within(card).findByRole("button", { name: "More for Create a site failed" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Dismiss" }));
    expect(await within(card).findByText("Nothing needs your attention")).toBeInTheDocument();
    expect(sent.find((s) => s.method === "POST")?.body).toBeUndefined();
    expect(await screen.findByText("Dismissed “Create a site failed”")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Undo" }));
    expect(await within(card).findByText("Create a site failed")).toBeInTheDocument();
    expect(sent.some((s) => s.method === "DELETE")).toBe(true);
  });

  it("snoozes an item for a day", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel(() => json({ items: [updates], dismissed: 0 }), {
      "POST /api/v1/inbox/updates%3Apackages/dismiss": () => new Response(null, { status: 204 }),
    });
    const { card } = await home(api);
    await user.click(
      await within(card).findByRole("button", { name: "More for 3 package updates are available" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "Snooze for a day" }));
    await waitFor(() => expect(sent.some((s) => s.method === "POST")).toBe(true));
    const body = sent.find((s) => s.method === "POST")?.body as { until?: string } | undefined;
    const until = Date.parse(body?.until ?? "");
    expect(until - Date.now()).toBeGreaterThan(23 * 3600_000);
    expect(until - Date.now()).toBeLessThanOrEqual(24 * 3600_000);
  });

  it("shows dismissed items when asked, to bring them back", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel(
      (request) =>
        new URL(request.url).searchParams.get("dismissed")
          ? json({
              items: [{ ...certificate, dismissal: { at: now, until: "2026-10-20T08:00:00Z" } }],
              dismissed: 1,
            })
          : json({ items: [], dismissed: 1 }),
      {
        "DELETE /api/v1/inbox/certificate%3Amail.example.com/dismiss": () =>
          json({ restored: true }),
      },
    );
    const { card } = await home(api);
    const toggle = await within(card).findByRole("button", { name: "Show dismissed (1)" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    expect(await within(card).findByText(/^Snoozed until/)).toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: /^More for The certificate/ }));
    await user.click(await screen.findByRole("menuitem", { name: "Bring back" }));
    await waitFor(() => expect(sent.some((s) => s.method === "DELETE")).toBe(true));
  });

  it("says what's being checked and what couldn't be", async () => {
    const { api } = panel(() =>
      json({
        items: [],
        dismissed: 0,
        checking: ["updates"],
        unavailable: [
          { source: "certificates", code: "edge.stopped", message: "The edge isn't running." },
        ],
      }),
    );
    const { card } = await home(api);
    expect(await within(card).findByText("Nothing needs your attention")).toBeInTheDocument();
    expect(within(card).getByRole("status")).toHaveTextContent("Checking updates…");
    expect(
      within(card).getByText("Couldn't check certificates: The edge isn't running."),
    ).toBeInTheDocument();
  });

  it("says why parts couldn't be checked in the person's words, when it has the values for them", async () => {
    const { api } = panel(() =>
      json({
        items: [],
        dismissed: 0,
        unavailable: [
          {
            source: "updates",
            code: "core.unavailable",
            message: "The package lists are down.",
            params: { what: "The package lists" },
          },
          // Without the value its message needs, the panel's English reads.
          {
            source: "certificates",
            code: "core.timeout",
            message: "Reading the certificates took too long.",
          },
        ],
      }),
    );
    const { card } = await home(api);
    expect(
      await within(card).findByText(
        "Couldn't check updates: The package lists isn't available right now. Try again in a moment.",
      ),
    ).toBeInTheDocument();
    expect(
      within(card).getByText(
        "Couldn't check certificates: Reading the certificates took too long.",
      ),
    ).toBeInTheDocument();
  });

  it("names the panel's own kinds in the person's words", async () => {
    // The panel sends its English word; the screens' catalog has the person's.
    const { api } = panel(() =>
      json({
        items: [{ ...degraded, params: { ...degraded.params, kind: "core.webhook" } }],
        dismissed: 0,
      }),
    );
    const { card } = await home(api);
    expect(
      await within(card).findByText("Webhook https://hooks.example.com works only in part"),
    ).toBeInTheDocument();
  });

  it("reads the inbox again when the panel says it changed", async () => {
    let items: InboxItem[] = [];
    const { api } = panel(() => json({ items, dismissed: 0 }));
    const { card, sources } = await home(api);
    await within(card).findByText("Nothing needs your attention");
    const stream = sources.find((s) => s.url === "/api/v1/inbox/stream");
    expect(stream).toBeDefined();
    items = [jobFailed];
    act(() => {
      stream?.open();
      stream?.emit("changed", { reason: "event" });
    });
    expect(await within(card).findByText("Create a site failed")).toBeInTheDocument();
  });
});

describe("inbox items' words", () => {
  const intl = createIntl({
    locale: "en",
    timeZone: "UTC",
    messages: { ...shellMessages, ...inboxCatalog },
  });

  it("come from the catalog of whoever found them", () => {
    expect(inboxMessageId(jobFailed)).toBe("inbox.job_failed");
    expect(inboxMessageId(backups)).toBe("backups.inbox.backup_failed");
    expect(inboxMessageId(updates, "message")).toBe("inbox.updates_available.message");
  });

  it("count with the numbers the panel sends as text", () => {
    expect(inboxTitle(intl, updates)).toBe("3 package updates are available");
    expect(
      inboxMessage(intl, { ...updates, params: { count: "2", security: "2", all: "true" } }),
    ).toBe("They all fix security problems.");
    expect(inboxTitle(intl, { ...certificate, params: { ...certificate.params, days: "0" } })).toBe(
      "The certificate for mail.example.com expires today",
    );
  });

  it("show moments in the person's format and kinds in their language", () => {
    const restart = item({
      code: "restart_scheduled",
      params: { reasons: "linux-image", time: "2026-10-18T03:00:00Z" },
      title: "The server restarts at 2026-10-18 03:00 UTC to finish installing updates",
    });
    expect(inboxTitle(intl, restart)).toBe(
      "The server restarts at Oct 18, 2026, 3:00 AM to finish installing updates",
    );
    const translated = createIntl({
      locale: "en",
      messages: { ...inboxCatalog, "core.kinds.webhook.title": "Hook" },
    });
    expect(inboxTitle(translated, degraded)).toBe(
      "Hook https://hooks.example.com works only in part",
    );
  });

  it("fall back on the panel's English when a value the message needs is missing", () => {
    expect(inboxTitle(intl, { ...jobFailed, params: {} })).toBe("Create a site failed");
    expect(inboxMessage(intl, { ...jobFailed, message: "" })).toBe("");
  });
});
