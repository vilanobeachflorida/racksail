import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import type { ChangeSet } from "@/lib/changes";
import { fakeApi, json, Providers } from "@/test/render";
import { changeSet } from "@/test/resources";
import { type FlowOptions, useChangeFlow } from "./flow";

type Route = (request: Request) => Response | Promise<Response>;

const preview = changeSet({ id: "chg_preview" });
const applied = changeSet({ id: "chg_preview", state: "applying", dryRun: false, job: "job_1" });

const problem = (status: number, code: string, params?: Record<string, unknown>) =>
  json({ type: "about:blank", status, code, ...(params ? { params } : {}) }, status);

/** The flow against a panel that answers the given routes, recording the idempotency keys it's sent. */
function flowWith(routes: Record<string, Route>, options: FlowOptions = {}) {
  const keys: (string | null)[] = [];
  const api = fakeApi(
    Object.fromEntries(
      Object.entries(routes).map(([key, route]) => [
        key,
        (request: Request) => {
          keys.push(request.headers.get("Idempotency-Key"));
          return route(request);
        },
      ]),
    ),
  );
  const wrapper = ({ children }: { children: ReactNode }) => (
    <Providers api={api}>{children}</Providers>
  );
  const hook = renderHook(() => useChangeFlow(options), { wrapper });
  return { hook, keys };
}

describe("taking a change through its preview", () => {
  it("applies the preview by its ID and hands the change to the toasts", async () => {
    const onApplied = vi.fn();
    const { hook, keys } = flowWith(
      { "POST /api/v1/changesets/chg_preview/apply": () => json(applied) },
      { onApplied },
    );
    const send = vi.fn(async () => preview);
    await act(() => hook.result.current.preview({ send }));
    expect(send).toHaveBeenCalledWith({ dryRun: true });
    expect(hook.result.current.state).toEqual({ step: "preview", preview });
    let end: unknown;
    await act(async () => {
      end = await hook.result.current.apply();
    });
    expect(end).toEqual({ step: "tracked", applied });
    expect(hook.result.current.state).toEqual({ step: "tracked", applied });
    expect(onApplied).toHaveBeenCalledWith(applied);
    expect(keys[0]).toMatch(/.+/);
  });

  it("keeps a change someone waits for in the flow", async () => {
    const { hook } = flowWith(
      { "POST /api/v1/changesets/chg_preview/apply": () => json(applied) },
      { keep: true },
    );
    act(() => hook.result.current.show(preview));
    await act(async () => {
      await hook.result.current.apply();
    });
    expect(hook.result.current.state).toEqual({ step: "applied", applied });
  });

  it("sends a change that carried secret values again, with the same key", async () => {
    const { hook, keys } = flowWith({
      "POST /api/v1/changesets/chg_preview/apply": () =>
        problem(409, "changeset.preview_has_secrets", { title: "Update webhook" }),
    });
    const send = vi.fn(async (options: { dryRun?: boolean; idempotencyKey?: string }) =>
      options.dryRun ? preview : applied,
    );
    await act(() => hook.result.current.preview({ send }));
    await act(async () => {
      await hook.result.current.apply();
    });
    expect(send).toHaveBeenLastCalledWith({ idempotencyKey: keys[0] });
    expect(hook.result.current.state).toEqual({ step: "tracked", applied });
  });

  it("says a preview kept from before can't be applied when it carried secret values", async () => {
    const { hook } = flowWith({
      "POST /api/v1/changesets/chg_preview/apply": () =>
        problem(409, "changeset.preview_has_secrets", { title: "Update webhook" }),
    });
    act(() => hook.result.current.show(preview));
    let end: unknown;
    await act(async () => {
      end = await hook.result.current.apply();
    });
    expect(end).toMatchObject({ step: "error", preview });
    const state = hook.result.current.state;
    expect(state.step === "error" && state.error instanceof ApiError && state.error.code).toBe(
      "changeset.preview_has_secrets",
    );
  });

  it("shows what the change would do now when things changed since its preview", async () => {
    const fresh = changeSet({ id: "chg_fresh", title: "Update webhook" });
    const { hook } = flowWith({
      "POST /api/v1/changesets/chg_preview/apply": () =>
        problem(409, "changeset.preview_stale", { title: "Update webhook", preview: "chg_fresh" }),
      "GET /api/v1/changesets/chg_fresh": () => json(fresh),
      "POST /api/v1/changesets/chg_fresh/apply": () =>
        json({ ...fresh, state: "applying", dryRun: false }),
    });
    act(() => hook.result.current.show(preview));
    await act(async () => {
      await hook.result.current.apply();
    });
    expect(hook.result.current.state).toEqual({ step: "preview", preview: fresh, stale: true });
    // Applying again applies what it does now.
    await act(async () => {
      await hook.result.current.apply();
    });
    expect(hook.result.current.state).toMatchObject({
      step: "tracked",
      applied: { id: "chg_fresh", dryRun: false },
    });
  });

  it("says what happened when the fresh preview can't be read, or applying fails", async () => {
    const { hook } = flowWith({
      "POST /api/v1/changesets/chg_preview/apply": () =>
        problem(409, "changeset.preview_stale", { title: "Update webhook", preview: "chg_gone" }),
    });
    act(() => hook.result.current.show(preview));
    await act(async () => {
      await hook.result.current.apply();
    });
    const state = hook.result.current.state;
    expect(state.step === "error" && state.error instanceof ApiError && state.error.code).toBe(
      "changeset.preview_stale",
    );

    const broken = flowWith({
      "POST /api/v1/changesets/chg_preview/apply": () => {
        throw new TypeError("Failed to fetch");
      },
    });
    act(() => broken.hook.result.current.show(preview));
    let end: unknown;
    await act(async () => {
      end = await broken.hook.result.current.apply();
    });
    expect(end).toMatchObject({ step: "error", preview });
  });

  it("does nothing when there is no preview to apply", async () => {
    const { hook, keys } = flowWith({});
    let end: unknown;
    await act(async () => {
      end = await hook.result.current.apply();
    });
    expect(end).toEqual({ step: "idle" });
    expect(keys).toEqual([]);
    const failing = vi.fn(async (): Promise<ChangeSet> => {
      throw new ApiError(422, { type: "about:blank", status: 422, code: "core.invalid" });
    });
    await act(async () => {
      await expect(hook.result.current.preview({ send: failing })).rejects.toBeInstanceOf(ApiError);
    });
    expect(hook.result.current.state.step).toBe("error");
  });
});
