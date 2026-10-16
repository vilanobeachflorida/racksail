import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  type Dispatch,
  lazy,
  type ReactNode,
  type SetStateAction,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { type IntlShape, useIntl } from "react-intl";
import type { ApiClient } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { ChangeSet } from "@/lib/changes";
import { type LiveClient, useLiveClient } from "@/lib/live";
import { usePermission } from "../modules";
import { ScreenMessages } from "../screens/catalog";
import { type ShellState, useShell } from "../shell-state";

// The dialog for undos that need a look first loads when one does, and what
// follows changes loads with the first one.
const UndoDialog = lazy(() => import("../screens/changes/undo-dialog"));
const loadFollow = () => import("./follow");

export interface TrackOptions {
  /** Says nothing while a dialog follows the change itself; tracking it again without it hands over. */
  quiet?: boolean;
}

export interface ChangeTracker {
  /**
   * Follows an applied change set's job and says how it ended, offering to
   * undo it, in a toast that shows its progress meanwhile.
   */
  track: (changeSet: ChangeSet, options?: TrackOptions) => void;
  /**
   * Undoes a change set: at once when the undo is safe, and after showing
   * its preview when it removes data or is risky.
   */
  undo: (changeSetId: string) => Promise<void>;
  /** The latest change applied here that can still be undone, for the command bar. */
  last: ChangeSet | null;
}

const TrackerContext = createContext<ChangeTracker>({
  track: () => {},
  undo: async () => {},
  last: null,
});

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? {}) === JSON.stringify(b ?? {});

/**
 * The permissions undoing a change set takes, as the panel works the undo
 * out: deleting what it created, putting back the settings and labels it
 * changed, and creating again what it deleted. An action that changed no
 * settings needs nothing, since there is nothing of it to put back.
 */
export function undoPermissions(cs: Pick<ChangeSet, "changes">): string[] {
  const changes = cs.changes ?? [];
  const created = new Set(changes.filter((c) => c.op === "create").map((c) => c.id));
  const needed = new Set<string>();
  for (const c of changes) {
    if (c.op === "create") {
      needed.add(`${c.kind}.delete`);
    } else if (c.op === "delete") {
      needed.add(`${c.kind}.create`);
    } else if (!created.has(c.id) && !(same(c.before, c.after) && same(c.labels, c.labelsBefore))) {
      needed.add(`${c.kind}.update`);
    }
  }
  return [...needed];
}

/** Whether a change set changed something undo can put back; actions that changed no settings can't be undone. */
export function undoable(cs: Pick<ChangeSet, "changes" | "state">): boolean {
  return (cs.state === "applied" || cs.state === "failed") && undoPermissions(cs).length > 0;
}

/** Whether someone with these permissions may undo a change set. */
export function mayUndo(
  cs: Pick<ChangeSet, "changes" | "state">,
  allowed: (permission: string) => boolean,
): boolean {
  return undoable(cs) && undoPermissions(cs).every(allowed);
}

/** A change being followed, and how. */
export interface Followed {
  quiet: boolean;
  stop: (() => void) | null;
  done: boolean;
}

/** What following changes needs from the page it happens on. */
export interface TrackerHost {
  api: ApiClient;
  live: LiveClient;
  queryClient: QueryClient;
  followed: Map<string, Followed>;
  /** The latest of what toasts use, since they outlive renders. */
  current: () => {
    intl: IntlShape;
    shell: ShellState;
    allowed: (permission: string) => boolean;
  };
  setLast: Dispatch<SetStateAction<ChangeSet | null>>;
  /** Shows an undo's preview before it's applied. */
  confirm: (preview: ChangeSet, of: string) => void;
  track: ChangeTracker["track"];
  undo: ChangeTracker["undo"];
}

/**
 * Follows the changes people apply, wherever they applied them, and keeps
 * the queries that show what they changed current once they're done.
 */
export function ChangeTrackerProvider({ children }: { children: ReactNode }) {
  const intl = useIntl();
  const api = useApi();
  const live = useLiveClient();
  const queryClient = useQueryClient();
  const shell = useShell();
  const allowed = usePermission();
  const followed = useRef(new Map<string, Followed>());
  const [confirming, setConfirming] = useState<{ preview: ChangeSet; of: string } | null>(null);
  const [last, setLast] = useState<ChangeSet | null>(null);
  const latest = useRef({ intl, shell, allowed });
  useEffect(() => {
    latest.current = { intl, shell, allowed };
  });

  const host = useMemo<TrackerHost>(() => {
    const self: TrackerHost = {
      api,
      live,
      queryClient,
      followed: followed.current,
      current: () => latest.current,
      setLast,
      confirm: (preview, of) => setConfirming({ preview, of }),
      track: (cs, options) => {
        void loadFollow().then((follow) => follow.track(self, cs, options));
      },
      undo: async (id) => {
        const follow = await loadFollow();
        await follow.undo(self, id);
      },
    };
    return self;
  }, [api, live, queryClient]);

  const track = useCallback<ChangeTracker["track"]>(
    (cs, options) => host.track(cs, options),
    [host],
  );
  const undo = useCallback<ChangeTracker["undo"]>((id) => host.undo(id), [host]);
  const value = useMemo(() => ({ track, undo, last }), [track, undo, last]);
  return (
    <TrackerContext value={value}>
      {children}
      {confirming ? (
        <Suspense fallback={null}>
          <ScreenMessages fallback={null}>
            <UndoDialog
              key={confirming.preview.id}
              preview={confirming.preview}
              of={confirming.of}
              onClose={() => setConfirming(null)}
            />
          </ScreenMessages>
        </Suspense>
      ) : null}
    </TrackerContext>
  );
}

/** Follows applied changes and undoes them. */
export function useChangeTracker(): ChangeTracker {
  return useContext(TrackerContext);
}
