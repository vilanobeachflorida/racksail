import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import Jobs from "@/app/jobs-panel";
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

let seq = 0;
const line = (message: string, level: LogLine["level"] = "INFO"): LogLine => ({
  seq: ++seq,
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
    rerender(<JobLog lines={[line("one"), line("two", "ERROR")]} label="Log" />);
    expect(screen.getByRole("log")).toHaveTextContent("two");
    // The level is in the person's language; capitals come from the style.
    expect(screen.getByText("Error")).toHaveClass("text-danger", "uppercase");
  });

  it("loads jobs from the panel and keeps them and their logs current", async () => {
    const { live, sources } = fakeLive();
    const cancel = vi.fn(() => json(job("job_9", "canceled")));
    const list = vi.fn(() =>
      json({ jobs: [job("job_9", "running"), job("job_8", "failed", { attempt: 2 })] }),
    );
    const api = fakeApi({
      "GET /api/v1/jobs": list,
      "GET /api/v1/jobs/job_8/logs": () => json({ lines: [line("apt failed", "ERROR")] }),
      "POST /api/v1/jobs/job_9/cancel": cancel,
    });
    renderWithProviders(<Jobs open onOpenChange={() => {}} />, { api, live });
    expect(await screen.findByText("Apply job_9")).toBeInTheDocument();
    const timeline = sources.find((s) => s.url === "/api/v1/events/stream");
    const jobStream = sources.find((s) => s.url === "/api/v1/jobs/job_9/stream");
    expect(timeline).toBeDefined();
    expect(jobStream).toBeDefined();
    // The finished job's log is read once, not streamed.
    expect(sources.find((s) => s.url === "/api/v1/jobs/job_8/stream")).toBeUndefined();

    // A stream that starts over sends lines already shown; they show once.
    const first = line("Unpacking");
    act(() => jobStream?.emit("log", first, String(first.seq)));
    act(() => jobStream?.emit("log", first, String(first.seq)));
    expect(await screen.findAllByText("Unpacking")).toHaveLength(1);
    act(() => jobStream?.emit("job", job("job_9", "running", { progress: { done: 4, total: 5 } })));
    expect(
      await screen.findByRole("progressbar", { name: "Progress of Apply job_9" }),
    ).toHaveAttribute("aria-valuenow", "80");

    const finished = screen.getByRole("region", { name: "Finished" });
    await userEvent.click(within(finished).getByRole("button", { name: "Log" }));
    expect(await within(finished).findByText("apt failed")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.getByRole("region", { name: "Finished" })).toHaveTextContent("Apply job_9"),
    );

    // Work starting or ending elsewhere shows up through the timeline.
    list.mockImplementation(() => json({ jobs: [job("job_10", "running")] }));
    act(() =>
      timeline?.emit("event", {
        id: "evt_1",
        type: "core.changeset.applied",
        time: "2026-10-15T12:01:00Z",
        severity: "info",
        scope: { level: "server" },
        message: "Applied Create example.com",
        job: "job_10",
      }),
    );
    expect(await screen.findByText("Apply job_10")).toBeInTheDocument();
  });

  it("says why a cancel or retry failed, and what to do", async () => {
    let answer: () => Response | Promise<Response> = () =>
      json(
        {
          type: "about:blank",
          status: 403,
          code: "core.forbidden",
          params: { action: "cancel jobs" },
        },
        403,
      );
    const cancel = vi.fn(() => answer());
    const api = fakeApi({
      "GET /api/v1/jobs": () => json({ jobs: [job("job_9", "running")] }),
      "POST /api/v1/jobs/job_9/cancel": cancel,
    });
    renderWithProviders(<Jobs open onOpenChange={() => {}} />, { api });
    const button = await screen.findByRole("button", { name: "Cancel" });
    await userEvent.click(button);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The job wasn't canceled: You don't have permission to cancel jobs.",
    );
    // The button works again, and a second try clears the message.
    let release = () => {};
    answer = () =>
      new Promise<Response>((resolve) => {
        release = () => resolve(json(job("job_9", "canceled")));
      });
    await userEvent.click(button);
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    act(() => release());
    await waitFor(() =>
      expect(screen.getByRole("region", { name: "Finished" })).toHaveTextContent("Apply job_9"),
    );
    expect(cancel).toHaveBeenCalledTimes(2);
  });

  it("gives every log's switch its own label, even for jobs with the same title", async () => {
    renderWithProviders(
      <>
        <JobLog lines={[]} label="Apply changes" />
        <JobLog lines={[]} label="Apply changes" />
      </>,
    );
    const [first, second] = screen.getAllByRole("switch", { name: "Follow new lines" });
    expect(first?.id).not.toBe(second?.id);
    await userEvent.click(screen.getAllByText("Follow new lines")[1] as HTMLElement);
    expect(first).toBeChecked();
    expect(second).not.toBeChecked();
  });
});
