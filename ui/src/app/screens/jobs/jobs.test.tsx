import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Job } from "@/lib/changes";
import { shellMessages } from "@/lib/i18n";
import { axeViolations } from "@/test/axe";
import { fakeApi, fakeLive, json, renderShell } from "@/test/render";
import { webhookKind } from "@/test/resources";

const failed: Job = {
  id: "job_failed",
  type: "changeset.apply",
  title: "Create webhook",
  state: "failed",
  progress: { done: 1, total: 2 },
  attempt: 1,
  actor: { type: "user", name: "developer", source: "ui" },
  changeSet: "chg_1",
  resource: "whk_01jq0000000000000000000001",
  error: { code: "core.unavailable", message: "The mirror isn't available right now." },
  createdAt: "2026-10-15T12:00:00Z",
  startedAt: "2026-10-15T12:00:01Z",
  finishedAt: "2026-10-15T12:00:31Z",
};

const running: Job = {
  id: "job_running",
  type: "changeset.apply",
  title: "Update webhook",
  state: "running",
  progress: { done: 1, total: 3, message: "Writing settings" },
  attempt: 1,
  createdAt: "2026-10-15T12:01:00Z",
  startedAt: "2026-10-15T12:01:01Z",
};

function panel(extra: Record<string, (request: Request) => Response> = {}) {
  const sent: string[] = [];
  const routes: Record<string, (request: Request) => Response> = {
    "GET /api/v1/kinds": () => json({ kinds: [webhookKind] }),
    "GET /api/v1/jobs": () => json({ jobs: [running, failed] }),
    "GET /api/v1/jobs/job_failed": () => json(failed),
    "GET /api/v1/jobs/job_failed/logs": () =>
      json({
        lines: [
          { seq: 1, time: "2026-10-15T12:00:02Z", level: "INFO", message: "Downloading packages" },
          {
            seq: 2,
            time: "2026-10-15T12:00:30Z",
            level: "ERROR",
            message: "The mirror didn't answer",
          },
        ],
      }),
    "GET /api/v1/jobs/job_running": () => json(running),
    "GET /api/v1/events": () => json({ events: [] }),
    "POST /api/v1/jobs/job_failed/retry": () =>
      json({ ...failed, id: "job_retry", state: "queued", retryOf: "job_failed" }),
    ...extra,
  };
  const api = fakeApi(
    Object.fromEntries(
      Object.entries(routes).map(([key, route]) => [
        key,
        (request: Request) => {
          sent.push(`${request.method} ${new URL(request.url).pathname}`);
          return route(request);
        },
      ]),
    ),
  );
  return { api, sent };
}

describe("the jobs screens", () => {
  it("list every job with its state, and run failed ones again", async () => {
    const user = userEvent.setup();
    const { api, sent } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/jobs", api });
    expect(await screen.findByRole("heading", { level: 1, name: "Jobs" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Jobs" });
    expect(await within(table).findByRole("link", { name: "Create webhook" })).toHaveAttribute(
      "href",
      "/jobs/job_failed",
    );
    expect(within(table).getByText("Failed")).toBeInTheDocument();
    expect(within(table).getByText("Running")).toBeInTheDocument();
    await user.click(within(table).getByRole("button", { name: /Retry/ }));
    await waitFor(() => expect(sent).toContain("POST /api/v1/jobs/job_failed/retry"));
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("show why a job failed, what it was for and its whole log", async () => {
    const { api } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/jobs/job_failed", api });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Create webhook" }),
    ).toBeInTheDocument();
    expect(screen.getByText("The mirror isn't available right now.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "chg_1" })).toHaveAttribute("href", "/changes/chg_1");
    expect(screen.getByRole("link", { name: "whk_01jq0000000000000000000001" })).toHaveAttribute(
      "href",
      "/core/webhooks/whk_01jq0000000000000000000001",
    );
    expect(screen.getByText("30 sec")).toBeInTheDocument();
    expect(await screen.findByText("The mirror didn't answer")).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("Create webhook · Racksail"));
  });

  it("follow a running job's progress and log as they come", async () => {
    const { api } = panel();
    const { live, sources } = fakeLive();
    renderShell({ modules: [], messages: shellMessages, path: "/jobs/job_running", api, live });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Update webhook" }),
    ).toBeInTheDocument();
    const stream = await waitFor(() => {
      const found = sources.find((s) => s.url === "/api/v1/jobs/job_running/stream");
      expect(found).toBeDefined();
      return found;
    });
    act(() => {
      stream?.open();
      stream?.emit("log", {
        seq: 1,
        time: "2026-10-15T12:01:02Z",
        level: "INFO",
        message: "Wrote the settings",
      });
      stream?.emit("job", {
        ...running,
        state: "succeeded",
        progress: { done: 3, total: 3 },
        finishedAt: "2026-10-15T12:01:05Z",
      });
    });
    expect(await screen.findByText("Wrote the settings")).toBeInTheDocument();
    expect(await screen.findByText("Done")).toBeInTheDocument();
  });

  it("say when a job doesn't exist", async () => {
    const { api } = panel();
    renderShell({ modules: [], messages: shellMessages, path: "/jobs/job_gone", api });
    expect(await screen.findByText("This job doesn't exist")).toBeInTheDocument();
  });
});
