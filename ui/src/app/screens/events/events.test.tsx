import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TimelineEvent } from "@/lib/events";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, fakeLive, json, renderShell } from "@/test/render";
import { webhookKind } from "@/test/resources";
import { eventMatches, matchType } from "./list";

const updated: TimelineEvent = {
  id: "evt_1",
  type: "core.webhook.updated",
  time: "2026-10-15T12:00:00Z",
  severity: "info",
  scope: { level: "server" },
  message: "Update webhook",
  actor: { type: "user", name: "developer", source: "ui" },
  subject: "whk_01jq0000000000000000000001",
  kind: "core.webhook",
  changeSet: "chg_1",
  job: "job_1",
};

const failed: TimelineEvent = {
  id: "evt_2",
  type: "core.webhook.failed",
  time: "2026-10-14T08:00:00Z",
  severity: "warning",
  scope: { level: "server" },
  message: "The webhook's deliveries are failing",
  subject: "whk_01jq0000000000000000000001",
  kind: "core.webhook",
};

function panel() {
  const asked: URLSearchParams[] = [];
  const api = fakeApi({
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/jobs": () => json({ jobs: [] }),
    "GET /api/v1/events/types": () =>
      json({ types: ["core.webhook.updated", "core.webhook.failed"] }),
    "GET /api/v1/events": (request) => {
      const query = new URL(request.url).searchParams;
      asked.push(query);
      const severity = query.get("minSeverity");
      return json({ events: severity === "warning" ? [failed] : [updated, failed] });
    },
  });
  return { api, asked };
}

describe("the events screen", () => {
  it("shows what happened by day, with who did it and links to what it touched", async () => {
    const { api } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/events", api });
    expect(await screen.findByRole("heading", { level: 1, name: "Events" })).toBeInTheDocument();
    expect(await screen.findByText("Update webhook")).toBeInTheDocument();
    expect(screen.getByText("By developer, in the panel")).toBeInTheDocument();
    expect(screen.getByText("The webhook's deliveries are failing")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Show the change" })[0]).toHaveAttribute(
      "href",
      "/changes/chg_1",
    );
    expect(screen.getAllByRole("link", { name: "Show the job" })[0]).toHaveAttribute(
      "href",
      "/jobs/job_1",
    );
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("keeps its filters in the address and asks the panel for what they let through", async () => {
    const user = userEvent.setup();
    const { api, asked } = panel();
    const { router } = renderShell({ modules: [], messages: shellMessages, path: "/events", api });
    await screen.findByText("Update webhook");
    await user.click(screen.getByRole("combobox", { name: "How urgent" }));
    await user.click(await screen.findByRole("option", { name: "Warning or worse" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ severity: "warning" }));
    await waitFor(() => expect(asked.at(-1)?.get("minSeverity")).toBe("warning"));
    await waitFor(() => expect(screen.queryByText("Update webhook")).not.toBeInTheDocument());
  });

  it("shows only what's about one resource when the address names it", async () => {
    const user = userEvent.setup();
    const { api, asked } = panel();
    const { router } = renderShell({
      modules: [],
      messages: shellMessages,
      path: "/events?subject=whk_01jq0000000000000000000001",
      api,
    });
    expect(await screen.findByText("Only events about")).toBeInTheDocument();
    await waitFor(() =>
      expect(asked.at(-1)?.get("subject")).toBe("whk_01jq0000000000000000000001"),
    );
    await user.click(screen.getByRole("button", { name: "Show all" }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it("puts new events at the top as they happen", async () => {
    const { api } = panel();
    const { live, sources } = fakeLive();
    renderShell({ modules: [], messages: shellMessages, path: "/events", api, live });
    const list = await screen.findByText("Update webhook");
    expect(list).toBeInTheDocument();
    const stream = await waitFor(() => {
      const found = sources.find((s) => s.url === "/api/v1/events/stream");
      expect(found).toBeDefined();
      return found;
    });
    act(() => {
      stream?.open();
      stream?.emit("event", {
        ...updated,
        id: "evt_3",
        message: "Send a test webhook",
        time: new Date().toISOString(),
      });
    });
    expect(await screen.findByText("Send a test webhook")).toBeInTheDocument();
    const items = screen.getAllByRole("listitem").map((li) => li.textContent ?? "");
    expect(items.findIndex((t) => t.includes("Send a test webhook"))).toBeLessThan(
      items.findIndex((t) => t.includes("Update webhook")),
    );
  });
});

describe("event filters", () => {
  it("match types as the panel does", () => {
    expect(matchType("web.site.*", "web.site.created")).toBe(true);
    expect(matchType("*.failed", "core.webhook.failed")).toBe(true);
    expect(matchType("web.*", "web.site.deploy.failed")).toBe(true);
    expect(matchType("web.site.?", "web.site.created")).toBe(false);
    expect(matchType("core.webhook.updated", "core.webhook.updated")).toBe(true);
  });

  it("let through only events at least as urgent, about the subject and in the range", () => {
    expect(eventMatches(failed, { minSeverity: "warning" })).toBe(true);
    expect(eventMatches(updated, { minSeverity: "warning" })).toBe(false);
    expect(eventMatches(updated, { subject: "whk_other" })).toBe(false);
    expect(eventMatches(updated, { since: "2026-10-15T00:00:00Z" })).toBe(true);
    expect(eventMatches(failed, { since: "2026-10-15T00:00:00Z" })).toBe(false);
  });
});
