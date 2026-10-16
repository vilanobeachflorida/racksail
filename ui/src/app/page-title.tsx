import { useMatch } from "@tanstack/react-router";
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

/** What a route's screen says it is: its crumb, and the browser tab's title when that says more. */
export interface PageTitle {
  crumb: string;
  document?: string;
}

interface PageTitles {
  titles: Readonly<Record<string, PageTitle>>;
  set: (matchId: string, title: PageTitle | null) => void;
}

const PageTitleContext = createContext<PageTitles>({ titles: {}, set: () => {} });

/**
 * Keeps the titles screens give their routes, for routes whose title comes
 * from what they show, such as a resource's name, rather than from a
 * message.
 */
export function PageTitleProvider({ children }: { children: ReactNode }) {
  const [titles, setTitles] = useState<Readonly<Record<string, PageTitle>>>({});
  const set = useCallback((matchId: string, title: PageTitle | null) => {
    setTitles((current) => {
      const before = current[matchId];
      if (
        title === null
          ? before === undefined
          : before?.crumb === title.crumb && before?.document === title.document
      ) {
        return current;
      }
      const next = { ...current };
      if (title === null) {
        delete next[matchId];
      } else {
        next[matchId] = title;
      }
      return next;
    });
  }, []);
  const value = useMemo<PageTitles>(() => ({ titles, set }), [titles, set]);
  return <PageTitleContext value={value}>{children}</PageTitleContext>;
}

/** The titles screens gave their routes, by route match. */
export function usePageTitles(): Readonly<Record<string, PageTitle>> {
  return useContext(PageTitleContext).titles;
}

/**
 * Names the route this screen renders, for the breadcrumb and the browser
 * tab, while the screen shows. Undefined, as while it loads, names
 * nothing yet.
 */
export function usePageTitle(crumb: string | undefined, document?: string): void {
  const { set } = useContext(PageTitleContext);
  const matchId = useMatch({ strict: false, select: (m) => m.id });
  useEffect(() => {
    if (crumb === undefined || crumb === "") {
      return;
    }
    set(matchId, { crumb, ...(document ? { document } : {}) });
    return () => set(matchId, null);
  }, [set, matchId, crumb, document]);
}
