import type { QueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext, useEffect } from "react";
import { ApiError } from "@/lib/api/client";
import type { PanelSession } from "@/lib/api/session";

interface SessionValue {
  /** Who is signed in, or null when the panel runs without sessions. */
  session: PanelSession | null;
  /** Reads the session again, as after signing in or out or changing it. */
  refresh: () => Promise<void>;
}

const SessionContext = createContext<SessionValue>({
  session: null,
  refresh: async () => {},
});

/** Makes the signed-in session available to the panel. */
export function SessionProvider({ value, children }: { value: SessionValue; children: ReactNode }) {
  return <SessionContext value={value}>{children}</SessionContext>;
}

/** The signed-in session, and a way to read it again. */
export function useSession(): SessionValue {
  return useContext(SessionContext);
}

/**
 * Whether someone is viewing the panel as the signed-in person. How that
 * person signs in is out of their reach then, so screens leave out what
 * would change it rather than offer what the panel refuses.
 */
export function useViewing(): boolean {
  return Boolean(useContext(SessionContext).session?.impersonation);
}

/** Codes that say the person isn't signed in any more. */
const signedOutCodes = new Set([
  "core.unauthenticated",
  "auth.session_expired",
  "auth.token_invalid",
]);

/** Whether an error says the person isn't signed in any more. */
export function isSignedOut(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401 && signedOutCodes.has(error.code);
}

/**
 * Reads the session again when any request finds the person signed out,
 * as when their session ends while the panel is open, so the sign-in
 * screen takes over.
 */
export function useSignOutWatch(client: QueryClient, onSignedOut: () => void) {
  useEffect(() => {
    const queries = client.getQueryCache().subscribe((event) => {
      if (
        event.type === "updated" &&
        event.query.queryKey[0] !== "session" &&
        isSignedOut(event.action.type === "error" ? event.action.error : null)
      ) {
        onSignedOut();
      }
    });
    const mutations = client.getMutationCache().subscribe((event) => {
      if (
        event.type === "updated" &&
        event.action.type === "error" &&
        isSignedOut(event.action.error)
      ) {
        onSignedOut();
      }
    });
    return () => {
      queries();
      mutations();
    };
  }, [client, onSignedOut]);
}
