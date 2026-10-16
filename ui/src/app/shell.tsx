import { Outlet, useMatches, useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { useIntl } from "react-intl";
import { Sheet, SheetContent, SheetTitle } from "@/design/components/ui/dialog";
import { Skeleton } from "@/design/components/ui/skeleton";
import { Toaster } from "@/design/components/ui/toaster";
import { jobDone } from "@/lib/changes";
import { useMediaQuery } from "@/lib/media";
import { AssistantDock } from "./assistant-dock";
import { BrandMark, useBrandName } from "./brand";
import { RouteAnnouncer, useRouteFocus } from "./focus";
import { useJobs } from "./jobs";
import { messages } from "./messages";
import { moduleCommands, useActiveModules, usePermission } from "./modules";
import { NavLinks, Rail } from "./rail";
import { useShell } from "./shell-state";
import { type Binding, useShortcuts } from "./shortcuts";
import { ShortcutsHelp } from "./shortcuts-help";
import { TopBar } from "./topbar";

// The command bar and the jobs drawer load the first time they open, which
// keeps them out of what the browser loads before it can show the panel.
const CommandBar = lazy(() => import("./command-bar"));
const Jobs = lazy(() => import("./jobs-panel"));

/** Whether something has been open, so it stays mounted to close smoothly. */
function useOpenedOnce(open: boolean): boolean {
  const [opened, setOpened] = useState(open);
  if (open && !opened) {
    setOpened(true);
  }
  return opened || open;
}

/** Keeps the browser tab's title on the deepest route that names its page. */
function useDocumentTitle() {
  const intl = useIntl();
  const matches = useMatches();
  const title = matches.findLast((match) => match.staticData.title)?.staticData.title;
  const page = title ? intl.formatMessage(title) : "";
  const name = useBrandName();
  useEffect(() => {
    document.title = page ? intl.formatMessage(messages.documentTitle, { page, name }) : name;
  }, [intl, page, name]);
}

function SkipLinks() {
  const intl = useIntl();
  const shell = useShell();
  // On phones the navigation lives in the menu sheet, so the link goes to
  // the button that opens it.
  const wide = useMediaQuery("(min-width: 48rem)");
  const link =
    "sr-only focus:not-sr-only focus:absolute focus:start-2 focus:top-2 focus:z-50 focus:rounded-md focus:bg-card focus:px-3 focus:py-2 focus:shadow-md focus:focus-ring";
  return (
    <div>
      <a href="#content" className={link}>
        {intl.formatMessage(messages.skipToContent)}
      </a>
      <a href={wide ? "#navigation" : "#menu-button"} className={link}>
        {intl.formatMessage(messages.skipToNav)}
      </a>
      {shell.assistant && shell.docked && shell.assistantOpen ? (
        <a href="#assistant" className={link}>
          {intl.formatMessage(messages.skipToAssistant)}
        </a>
      ) : null}
    </div>
  );
}

function MobileNav() {
  const intl = useIntl();
  const shell = useShell();
  const name = useBrandName();
  return (
    <Sheet open={shell.navOpen} onOpenChange={shell.setNavOpen}>
      <SheetContent side="start" className="flex flex-col gap-4" aria-describedby={undefined}>
        <SheetTitle className="flex items-center gap-2">
          <BrandMark className="h-6" />
          {name}
        </SheetTitle>
        <nav
          aria-label={intl.formatMessage(messages.navLabel)}
          className="flex min-h-0 flex-1 flex-col"
        >
          <NavLinks wide onNavigate={() => shell.setNavOpen(false)} />
        </nav>
      </SheetContent>
    </Sheet>
  );
}

/**
 * The frame around every screen: skip links, the top bar, the icon rail,
 * the content, the assistant dock and the command bar, jobs drawer and
 * shortcut help they open.
 */
export function Shell() {
  const shell = useShell();
  const navigate = useNavigate();
  const modules = useActiveModules();
  const allowed = usePermission();
  const jobs = useJobs();
  const announcement = useRouteFocus();
  useDocumentTitle();

  const running = (jobs.data ?? []).filter((j) => !jobDone(j.state)).length;
  const commandShown = useOpenedOnce(shell.commandOpen);
  const jobsShown = useOpenedOnce(shell.jobsOpen);
  const bindings = useMemo<Binding[]>(
    () => [
      { keys: "mod+k", run: () => shell.setCommandOpen(!shell.commandOpen), global: true },
      { keys: "mod+j", run: () => shell.setJobsOpen(!shell.jobsOpen), global: true },
      {
        keys: "mod+.",
        run: () => {
          if (shell.assistant) {
            shell.setAssistantOpen(!shell.assistantOpen);
          }
        },
        global: true,
      },
      { keys: "shift+?", run: () => shell.setShortcutsOpen(true) },
      { keys: "g h", run: () => void navigate({ to: "/" }) },
      { keys: "g s", run: () => void navigate({ to: "/settings" }) },
      ...moduleCommands(modules, allowed)
        .filter((c) => c.shortcut)
        .map((c) => ({
          keys: c.shortcut ?? "",
          run: () => void c.run({ navigate: (to: string) => void navigate({ to }) }),
        })),
    ],
    [shell, navigate, modules, allowed],
  );
  useShortcuts(bindings);

  return (
    <div className="flex h-dvh flex-col bg-background text-foreground">
      <SkipLinks />
      <TopBar runningJobs={running} />
      <div className="flex min-h-0 flex-1">
        <Rail />
        <main
          id="content"
          tabIndex={-1}
          className="min-w-0 flex-1 overflow-y-auto px-4 py-6 outline-none sm:px-6"
        >
          <Outlet />
        </main>
        <AssistantDock />
      </div>
      <MobileNav />
      {commandShown ? (
        <Suspense fallback={null}>
          <CommandBar />
        </Suspense>
      ) : null}
      {jobsShown ? (
        <Suspense fallback={null}>
          <Jobs open={shell.jobsOpen} onOpenChange={shell.setJobsOpen} />
        </Suspense>
      ) : null}
      <ShortcutsHelp />
      <RouteAnnouncer text={announcement} />
      <Toaster />
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
      className="flex h-dvh flex-col bg-background"
    >
      <div className="flex h-14 items-center gap-3 border-border border-b bg-card px-4">
        <BrandMark className="h-7" />
        <Skeleton className="h-5 w-24" />
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="hidden w-20 flex-col items-center gap-3 border-border border-e bg-card p-2 pt-4 md:flex">
          <Skeleton className="size-8" />
          <Skeleton className="size-8" />
          <Skeleton className="size-8" />
        </div>
        <div className="flex flex-1 flex-col gap-4 p-6">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-32 w-full max-w-md" />
        </div>
      </div>
    </div>
  );
}
