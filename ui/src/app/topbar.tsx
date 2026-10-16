import { Link, useMatches } from "@tanstack/react-router";
import {
  ChevronRight,
  CircleHelp,
  ListChecks,
  Menu,
  Monitor,
  Moon,
  PanelRight,
  Search,
  Sun,
} from "lucide-react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Badge } from "@/design/components/ui/display";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Tooltip,
} from "@/design/components/ui/overlay";
import type { Preferences } from "@/design/theme/prefs";
import { useTheme } from "@/design/theme/provider";
import { cn } from "@/lib/utils";
import { BrandLogo, useBrandName } from "./brand";
import { UserMenu } from "./identity/user-menu";
import { messages } from "./messages";
import { usePageTitles } from "./page-title";
import { useShell } from "./shell-state";
import { ariaKeyShortcuts, ShortcutKeys } from "./shortcuts";

/** The documentation site; the panel links to it but never loads anything from it. */
export const docsURL = "https://vilanobeachflorida.github.io/racksail/";

/**
 * Where the page is: Home, then each route above it that names itself,
 * with a message or, for pages such as a resource's, through its screen.
 */
export function Breadcrumbs() {
  const intl = useIntl();
  const matches = useMatches();
  const titles = usePageTitles();
  const trail = matches
    .map((match) => ({
      match,
      title:
        titles[match.id]?.crumb ??
        (match.staticData.title ? intl.formatMessage(match.staticData.title) : ""),
    }))
    .filter(({ match, title }) => title !== "" && match.pathname !== "/")
    .filter(({ match }, i, all) => all.findIndex((m) => m.match.pathname === match.pathname) === i);
  return (
    <nav aria-label={intl.formatMessage(messages.breadcrumb)} className="min-w-0">
      <ol className="flex min-w-0 items-center gap-1 text-sm">
        <li className="hidden shrink-0 sm:block">
          <Link
            to="/"
            className="rounded-sm text-muted-foreground hover:text-foreground focus-visible:focus-ring"
          >
            {intl.formatMessage(messages.navHome)}
          </Link>
        </li>
        {trail.map(({ match, title }, i) => {
          const last = i === trail.length - 1;
          return (
            // Phones only have room for the page itself.
            <li
              key={match.id}
              className={cn("min-w-0 items-center gap-1", last ? "flex" : "hidden sm:flex")}
            >
              <ChevronRight
                className="hidden size-4 shrink-0 text-muted-foreground sm:block rtl:rotate-180"
                aria-hidden="true"
              />
              {last ? (
                <span aria-current="page" className="truncate font-medium">
                  {title}
                </span>
              ) : (
                <Link
                  to={match.pathname}
                  className="truncate rounded-sm text-muted-foreground hover:text-foreground focus-visible:focus-ring"
                >
                  {title}
                </Link>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

function AppearanceMenu() {
  const intl = useIntl();
  const { preferences, setPreferences, mode } = useTheme();
  const Icon = preferences.mode === "system" ? Monitor : mode === "dark" ? Moon : Sun;
  return (
    <DropdownMenu>
      <Tooltip content={intl.formatMessage(messages.appearanceMenu)}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={intl.formatMessage(messages.appearanceMenu)}
          >
            <Icon aria-hidden="true" />
          </Button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>{intl.formatMessage(messages.appearanceMode)}</DropdownMenuLabel>
        <DropdownMenuRadioGroup
          value={preferences.mode}
          onValueChange={(v) => setPreferences({ mode: v as Preferences["mode"] })}
        >
          <DropdownMenuRadioItem value="system">
            {intl.formatMessage(messages.modeSystem)}
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="light">
            {intl.formatMessage(messages.modeLight)}
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="dark">
            {intl.formatMessage(messages.modeDark)}
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/settings/appearance">{intl.formatMessage(messages.appearanceMore)}</Link>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * The bar above every screen: the menu on phones, where the page is, the
 * command bar, jobs, the assistant and the appearance settings.
 */
export function TopBar({ runningJobs }: { runningJobs: number }) {
  const intl = useIntl();
  const shell = useShell();
  const brandName = useBrandName();
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-border border-b bg-card px-3 sm:px-4">
      <Button
        id="menu-button"
        variant="ghost"
        size="icon"
        className="md:hidden"
        aria-label={intl.formatMessage(messages.navOpen)}
        aria-expanded={shell.navOpen}
        onClick={() => shell.setNavOpen(true)}
      >
        <Menu aria-hidden="true" />
      </Button>
      <Link
        to="/"
        className="flex shrink-0 items-center gap-2 rounded-md font-semibold text-lg focus-visible:focus-ring"
        aria-label={brandName}
      >
        <BrandLogo />
      </Link>
      <div className="mx-2 hidden h-6 w-px bg-border sm:block" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <Breadcrumbs />
      </div>
      <Button
        variant="outline"
        className="hidden w-64 justify-between gap-3 text-muted-foreground md:inline-flex lg:w-72"
        onClick={() => shell.setCommandOpen(true)}
        aria-keyshortcuts={ariaKeyShortcuts("mod+k")}
      >
        <span className="flex min-w-0 items-center gap-2">
          <Search aria-hidden="true" />
          <span className="truncate">{intl.formatMessage(messages.commandOpen)}</span>
        </span>
        {/* The button names its shortcut with aria-keyshortcuts. */}
        <span aria-hidden="true" className="shrink-0">
          <ShortcutKeys keys="mod+k" />
        </span>
      </Button>
      <Button
        variant="ghost"
        size="icon"
        className="md:hidden"
        aria-label={intl.formatMessage(messages.commandOpen)}
        onClick={() => shell.setCommandOpen(true)}
      >
        <Search aria-hidden="true" />
      </Button>
      <Tooltip content={intl.formatMessage(messages.jobsOpen)}>
        <Button
          variant="ghost"
          size="icon"
          className="relative"
          aria-label={
            runningJobs > 0
              ? intl.formatMessage(messages.jobsOpenRunning, { count: runningJobs })
              : intl.formatMessage(messages.jobsOpen)
          }
          onClick={() => shell.setJobsOpen(true)}
        >
          <ListChecks aria-hidden="true" />
          {runningJobs > 0 ? (
            <Badge
              variant="accent"
              className="absolute -end-1.5 -top-1.5 h-5 min-w-5 justify-center px-1 leading-none"
            >
              {intl.formatNumber(runningJobs)}
            </Badge>
          ) : null}
        </Button>
      </Tooltip>
      {shell.assistant ? (
        <Tooltip
          content={intl.formatMessage(
            shell.assistantOpen ? messages.assistantClose : messages.assistantOpen,
          )}
        >
          <Button
            variant="ghost"
            size="icon"
            aria-label={intl.formatMessage(
              shell.assistantOpen ? messages.assistantClose : messages.assistantOpen,
            )}
            aria-pressed={shell.assistantOpen}
            onClick={() => shell.setAssistantOpen(!shell.assistantOpen)}
          >
            <PanelRight className="rtl:-scale-x-100" aria-hidden="true" />
          </Button>
        </Tooltip>
      ) : null}
      <AppearanceMenu />
      <DropdownMenu>
        <Tooltip content={intl.formatMessage(messages.help)}>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label={intl.formatMessage(messages.help)}>
              <CircleHelp aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
        </Tooltip>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => shell.setShortcutsOpen(true)}>
            {intl.formatMessage(messages.shortcutsTitle)}
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <a href={docsURL} target="_blank" rel="noreferrer">
              {intl.formatMessage(messages.helpDocs)}
            </a>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <UserMenu />
    </header>
  );
}
