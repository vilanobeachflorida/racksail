import { useNavigate, useRouterState, useSearch } from "@tanstack/react-router";
import { useEffect, useRef } from "react";

/**
 * Opens a screen's form for adding something when a link asks for it with
 * ?new=1, as the command bar's "Add a user" does, and takes the parameter
 * off the address, so going back or reloading doesn't open it again.
 */
export function useAskedToAdd(open: () => void): void {
  const search = useSearch({ strict: false }) as { new?: unknown };
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const navigate = useNavigate();
  const latest = useRef(open);
  latest.current = open;
  const asked = Boolean(search.new);
  useEffect(() => {
    if (!asked) {
      return;
    }
    latest.current();
    void navigate({ to: pathname, replace: true });
  }, [asked, navigate, pathname]);
}
