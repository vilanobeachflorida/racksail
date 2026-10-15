import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Jobs } from "@/app/jobs";
import type { Job, LogLine } from "@/lib/changes";
import { fakeApi, fakeLive, json, renderWithProviders } from "@/test/render";
import { JobLog, JobsDrawer } from "./jobs-drawer";

const job = (id: string, state: Job["state"], extra: Partial<Job> = {}): Job => ({
  id,
  type: "changeset.apply",
  title: `Apply ${id}`,
  state,
  progress: { done: 2, total: 5, message: "Installing php8.4-fpm" },
  attempt: 1,
  createdAt: "2026-10-15T12:00:00Z",
  ...extra,
});

const line = (message: string, level = "info"): LogLine => ({
  time: "2026-10-15T12:00:01Z",
  level,
  message,
});

describe("the jobs drawer", () => {
  it("lists running jobs first, with progress and actions", async () => {
    const onCancel = vi.fn();
    const onRetry = vi.fn();
    renderWithProviders(
      <JobsDrawer
        open
        onOpenChange={() => {}}
        jobs={[
          job("job_1", "succeeded"),
          job("job_2", "running"),
          job("job_3", "failed", {
            attempt: 2,
            error: { code: "agent.operation_failed", message: "apt failed" },
          }),
        ]}
        renderLog={(j) => <JobLog lines={[line(`log of ${j.id}`)]} label={j.title} />}
        onCancel={onCancel}
        onRetry={onRetry}
      />,
    );
    const drawer = await screen.findByRole("dialog", { name: "Jobs" });
    const running = within(drawer).getByRole("region", { name: "Running" });
    expect(within(running).getByText("Apply job_2")).toBeInTheDocument();
    expect(
      within(running).getByRole("progressbar", { name: "Progress of Apply job_2" }),
    ).toHaveAttribute("aria-valuenow", "40");
    expect(within(running).getByText(/2 of 5/)).toHaveTextContent("Installing php8.4-fpm");
    // The running job shows its log at once.
    expect(within(running).getByRole("log", { name: "Apply job_2" })).toHaveTextContent(
      "log of job_2",
    );
    await userEvent.click(within(running).getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledWith(expect.objectContaining({ id: "job_2" }));

    const finished = within(drawer).getByRole("region", { name: "Finished" });
    expect(within(finished).getByText("apt failed")).toBeInTheDocument();
    expect(within(finished).getByText("Attempt 2")).toBeInTheDocument();
    await userEvent.click(within(finished).getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledWith(expect.objectContaining({ id: "job_3" }));
  });

  it("explains an empty list and a failed load", async () => {
    const { unmount } = renderWithProviders(
      <JobsDrawer open onOpenChange={() => {}} jobs={[]} renderLog={() => null} />,
    );
    expect(await screen.findByText("No jobs yet")).toBeInTheDocument();
    unmount();
    const onRetryLoad = vi.fn();
    renderWithProviders(
      <JobsDrawer
        open
        onOpenChange={() => {}}
        jobs={[]}
        error={new Error("boom")}
        onRetryLoad={onRetryLoad}
        renderLog={() => null}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(onRetryLoad).toHaveBeenCalled();
  });

  it("follows new log lines until turned off", async () => {
    const { rerender } = renderWithProviders(<JobLog lines={[line("one")]} label="Log" />);
    const follow = screen.getByRole("switch", { name: "Follow new lines" });
    expect(follow).toBeChecked();
    await userEvent.click(follow);
    expect(follow).not.toBeChecked();
    rerender(<JobLog lines={[line("one"), line("two", "error")]} label="Log" />);
    expect(screen.getByRole("log")).toHaveTextContent("two");
    expect(screen.getByText("error")).toHaveClass("text-danger");
  });

  it("loads jobs from the panel and keeps them and their logs current", async () => {
    const { live, sources } = fakeLive();
    const cancel = vi.fn(() => json(job("job_9", "canceled")));
    const api = fakeApi({
      "GET /api/v1/jobs": () => json({ items: [job("job_9", "running")] }),
      "POST /api/v1/jobs/job_9/actions/cancel": cancel,
    });
    renderWithProviders(<Jobs open onOpenChange={() => {}} />, { api, live });
    expect(await screen.findByText("Apply job_9")).toBeInTheDocument();
    const jobsStream = sources.find((s) => s.url === "/api/v1/jobs/events");
    const logStream = sources.find((s) => s.url === "/api/v1/jobs/job_9/events");
    expect(jobsStream).toBeDefined();
    expect(logStream).toBeDefined();
    act(() => logStream?.emit("log", line("Unpacking")));
    expect(await screen.findByText("Unpacking")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).toHaveBeenCalled();
    act(() => jobsStream?.emit("job", job("job_9", "canceled")));
    await waitFor(() =>
      expect(screen.getByRole("region", { name: "Finished" })).toHaveTextContent("Apply job_9"),
    );
  });
});
