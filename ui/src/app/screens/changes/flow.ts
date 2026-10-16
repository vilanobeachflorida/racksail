import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useChangeTracker } from "@/app/changes/tracker";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { ChangeSet } from "@/lib/changes";
import type { ChangeOptions } from "../resources/api";
import { applyPreview, getChangeSet, newIdempotencyKey } from "./api";

/** A change to preview and apply. */
export interface ChangeRequest {
  /** Sends the change: as a dry run for its preview, or applied. */
  send: (options: ChangeOptions) => Promise<ChangeSet>;
  /**
   * Whether the change carries secret values, which previews don't keep:
   * it's then sent again to apply it, rather than applied from its preview.
   */
  carriesSecrets?: boolean;
}

export type FlowState =
  | { step: "idle" }
  | { step: "previewing" }
  | { step: "preview"; preview: ChangeSet; stale?: boolean }
  | { step: "applying"; preview: ChangeSet }
  /** Applied and followed here until its job ends, as for actions whose result someone waits for. */
  | { step: "applied"; applied: ChangeSet }
  /** Applied and handed to the change tracker, whose toast follows it. */
  | { step: "tracked"; applied: ChangeSet }
  | { step: "error"; error: unknown; preview?: ChangeSet };

export interface FlowOptions {
  /** Called once the change is applied, with its change set. */
  onApplied?: (applied: ChangeSet) => void;
  /**
   * Keeps the applied change in the flow, as for actions whose result
   * someone waits for, rather than handing it to the toasts.
   */
  keep?: boolean;
}

export interface ChangeFlow {
  state: FlowState;
  /** Previews a change; the promise fails with what kept it from planning, such as fields that aren't valid. */
  preview: (request: ChangeRequest) => Promise<ChangeSet>;
  /**
   * Shows a preview made elsewhere, such as an undo's or one kept on the
   * changes screen. Without the request that made it, a preview that
   * carried secret values can't be applied: only sending the change again
   * could, and its values are gone.
   */
  show: (preview: ChangeSet, request?: ChangeRequest) => void;
  /**
   * Applies the change the preview shows. It never fails: it resolves with
   * the state it left the flow in, which says how it went.
   */
  apply: () => Promise<FlowState>;
  reset: () => void;
}

/**
 * Takes a change from its preview to applying it: the preview is a dry
 * run, and applying applies what it showed, or a fresh preview when the
 * change would now do something else. Applied changes go to the change
 * tracker, which follows their job and offers to undo them.
 */
export function useChangeFlow({ onApplied, keep }: FlowOptions = {}): ChangeFlow {
  const api = useApi();
  const tracker = useChangeTracker();
  const [state, setState] = useState<FlowState>({ step: "idle" });
  const request = useRef<ChangeRequest | null>(null);
  // The state as last set, for apply, which runs from the preview's button.
  const current = useRef<FlowState>(state);
  const set = useCallback((next: FlowState): FlowState => {
    current.current = next;
    setState(next);
    return next;
  }, []);
  const appliedRef = useRef(onApplied);
  useEffect(() => {
    appliedRef.current = onApplied;
  });

  const preview = useCallback(
    async (next: ChangeRequest) => {
      request.current = next;
      set({ step: "previewing" });
      try {
        const cs = await next.send({ dryRun: true });
        set({ step: "preview", preview: cs });
        return cs;
      } catch (error) {
        set({ step: "error", error });
        throw error;
      }
    },
    [set],
  );

  const show = useCallback(
    (cs: ChangeSet, next?: ChangeRequest) => {
      request.current = next ?? null;
      set({ step: "preview", preview: cs });
    },
    [set],
  );

  /** What applying a preview ran into: a fresh preview when the change would now do something else. */
  const failed = useCallback(
    async (error: unknown, preview: ChangeSet): Promise<FlowState> => {
      if (error instanceof ApiError && error.code === "changeset.preview_stale") {
        const fresh = error.problem.params?.preview;
        if (typeof fresh === "string") {
          try {
            return set({ step: "preview", preview: await getChangeSet(api, fresh), stale: true });
          } catch {
            // The refusal itself says what happened.
          }
        }
      }
      return set({ step: "error", error, preview });
    },
    [api, set],
  );

  const apply = useCallback(async (): Promise<FlowState> => {
    const state = current.current;
    const req = request.current;
    if (state.step !== "preview") {
      return state;
    }
    set({ step: "applying", preview: state.preview });
    const key = newIdempotencyKey();
    let applied: ChangeSet;
    try {
      applied = req?.carriesSecrets
        ? await req.send({ idempotencyKey: key })
        : await applyPreview(api, state.preview.id, key);
    } catch (error) {
      // A preview that carried secret values can only be sent again, which
      // takes the request that made it.
      if (!(req && error instanceof ApiError && error.code === "changeset.preview_has_secrets")) {
        return failed(error, state.preview);
      }
      try {
        applied = await req.send({ idempotencyKey: key });
      } catch (again) {
        return failed(again, state.preview);
      }
    }
    tracker.track(applied, { quiet: keep });
    const next = set(keep ? { step: "applied", applied } : { step: "tracked", applied });
    appliedRef.current?.(applied);
    return next;
  }, [api, failed, keep, set, tracker]);

  const reset = useCallback(() => {
    request.current = null;
    set({ step: "idle" });
  }, [set]);

  return useMemo(
    () => ({ state, preview, show, apply, reset }),
    [state, preview, show, apply, reset],
  );
}
