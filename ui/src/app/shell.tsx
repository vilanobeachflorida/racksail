import { Link, Outlet, useMatches } from "@tanstack/react-router";
import { House, type LucideIcon } from "lucide-react";
import { useEffect } from "react";
import { useIntl } from "react-intl";
import { Mark } from "@/design/components/mark";
import { Skeleton } from "@/design/components/ui/skeleton";
import { messages } from "./messages";
import { navEntries, useModules } from "./modules";

function RailLink({ to, icon: Icon, label }: { to: string; icon: LucideIcon; label: string }) {
  return (
    <Link
      to={to}
      activeOptions={{ exact: to === "/" }}
      className="flex flex-col items-center gap-1 rounded-md px-1 py-2 text-center text-muted-foreground text-xs hover:bg-accent hover:text-accent-foreground data-[status=active]:bg-accent data-[status=active]:font-medium data-[status=active]:text-foreground"
    >
      <Icon className="size-5" aria-hidden="true" />
      {label}
    </Link>
  );
}

/** Keeps the browser tab's title on the deepest route that names its page. */
function useDocumentTitle() {
  const intl = useIntl();
  const matches = useMatches();
  const title = matches.findLast((match) => match.staticData.title)?.staticData.title;
  const page = title ? intl.formatMessage(title) : "";
  const name = intl.formatMessage(messages.name);
  useEffect(() => {
    document.title = page ? intl.formatMessage(messages.documentTitle, { page }) : name;
  }, [intl, page, name]);
}

/** The frame around every screen: the top bar, the icon rail and the content. */
export function Shell() {
  const intl = useIntl();
  const modules = useModules();
  useDocumentTitle();

  return (
    <div className="grid min-h-dvh grid-cols-[auto_minmax(0,1fr)] grid-rows-[auto_1fr] bg-background text-foreground">
      <a
        href="#content"
        className="sr-only focus:not-sr-only focus:absolute focus:start-2 focus:top-2 focus:z-50 focus:rounded-md focus:bg-card focus:px-3 focus:py-2 focus:shadow-md"
      >
        {intl.formatMessage(messages.skipToContent)}
      </a>
      <header className="col-span-2 flex h-14 items-center gap-3 border-border border-b bg-card px-4">
        <Link to="/" className="flex items-center gap-2 rounded-md font-semibold text-lg">
          <Mark className="h-7" />
          {intl.formatMessage(messages.name)}
        </Link>
      </header>
      <nav
        aria-label={intl.formatMessage(messages.navLabel)}
        className="flex w-20 flex-col gap-1 border-border border-e bg-card p-2"
      >
        <RailLink to="/" icon={House} label={intl.formatMessage(messages.navHome)} />
        {navEntries(modules, intl).map((entry) => (
          <RailLink
            key={`${entry.module.id}.${entry.id}`}
            to={entry.href}
            icon={entry.icon}
            label={intl.formatMessage(entry.label)}
          />
        ))}
      </nav>
      <main id="content" tabIndex={-1} className="min-w-0 p-6 outline-none">
        <Outlet />
      </main>
    </div>
  );
}

/** The shell's shape, shown while the panel loads. */
export function ShellSkeleton() {
  const intl = useIntl();
  return (
    <div
      role="status"
      aria-label={intl.formatMessage(messages.loading)}
      className="grid min-h-dvh grid-cols-[auto_minmax(0,1fr)] grid-rows-[auto_1fr] bg-background"
    >
      <div className="col-span-2 flex h-14 items-center gap-3 border-border border-b bg-card px-4">
        <Mark className="h-7" />
        <Skeleton className="h-5 w-24" />
      </div>
      <div className="flex w-20 flex-col items-center gap-3 border-border border-e bg-card p-2 pt-4">
        <Skeleton className="size-8" />
        <Skeleton className="size-8" />
        <Skeleton className="size-8" />
      </div>
      <div className="flex flex-col gap-4 p-6">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full max-w-md" />
      </div>
    </div>
  );
}
