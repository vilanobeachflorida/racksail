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
  | { step: "applied"; applied: ChangeSet }
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
  /** Shows a preview made elsewhere, such as an undo's. */
  show: (preview: ChangeSet, request: ChangeRequest) => void;
  /** Applies the change the preview shows. */
  apply: () => Promise<void>;
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
  const set = useCallback((next: FlowState) => {
    current.current = next;
    setState(next);
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
    (cs: ChangeSet, next: ChangeRequest) => {
      request.current = next;
      set({ step: "preview", preview: cs });
    },
    [set],
  );

  const apply = useCallback(async () => {
    const state = current.current;
    const req = request.current;
    if (state.step !== "preview" || !req) {
      return;
    }
    set({ step: "applying", preview: state.preview });
    const key = newIdempotencyKey();
    try {
      let applied: ChangeSet;
      try {
        applied = req.carriesSecrets
          ? await req.send({ idempotencyKey: key })
          : await applyPreview(api, state.preview.id, key);
      } catch (error) {
        // A preview that carried secret values can only be sent again.
        if (error instanceof ApiError && error.code === "changeset.preview_has_secrets") {
          applied = await req.send({ idempotencyKey: key });
        } else {
          throw error;
        }
      }
      tracker.track(applied, { quiet: keep });
      set(keep ? { step: "applied", applied } : { step: "idle" });
      appliedRef.current?.(applied);
    } catch (error) {
      // The change would now do something else: show what, to apply that.
      if (error instanceof ApiError && error.code === "changeset.preview_stale") {
        const fresh = error.problem.params?.preview;
        if (typeof fresh === "string") {
          try {
            set({ step: "preview", preview: await getChangeSet(api, fresh), stale: true });
            return;
          } catch {
            // Fall through to say what happened.
          }
        }
      }
      set({ step: "error", error, preview: state.preview });
    }
  }, [api, keep, set, tracker]);

  const reset = useCallback(() => {
    request.current = null;
    set({ step: "idle" });
  }, [set]);

  return useMemo(
    () => ({ state, preview, show, apply, reset }),
    [state, preview, show, apply, reset],
  );
}
