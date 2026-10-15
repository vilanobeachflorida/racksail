import { useRouterState } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

/**
 * Moves focus to the new page after navigating, as a full page load would,
 * and announces its title: to the page's main heading when it has one, to
 * the content area otherwise. The first page keeps the browser's own focus.
 */
export function useRouteFocus(): string {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const status = useRouterState({ select: (s) => s.status });
  const shown = useRef<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  useEffect(() => {
    if (status !== "idle" || shown.current === pathname) {
      return;
    }
    const first = shown.current === null;
    shown.current = pathname;
    if (first) {
      return;
    }
    // Give the screen a moment to render its heading.
    const timer = window.setTimeout(() => {
      const main = document.getElementById("content");
      const heading = main?.querySelector<HTMLElement>("h1");
      const target = heading ?? main;
      if (target) {
        if (heading && !heading.hasAttribute("tabindex")) {
          heading.setAttribute("tabindex", "-1");
        }
        target.focus({ preventScroll: false });
      }
      setAnnouncement(document.title);
    }, 50);
    return () => window.clearTimeout(timer);
  }, [pathname, status]);
  return announcement;
}

/** A polite live region that reads out page changes to screen readers. */
export function RouteAnnouncer({ text }: { text: string }) {
  return (
    <p aria-live="polite" aria-atomic="true" className="sr-only">
      {text}
    </p>
  );
}
