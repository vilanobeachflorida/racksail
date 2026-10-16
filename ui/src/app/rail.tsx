import { Link } from "@tanstack/react-router";
import { Building2, House, type LucideIcon, ScrollText, Settings, Users } from "lucide-react";
import { useIntl } from "react-intl";
import { cn } from "@/lib/utils";
import { messages } from "./messages";
import { navEntries, useActiveModules, usePermission } from "./modules";

function RailLink({
  to,
  icon: Icon,
  label,
  exact,
  onNavigate,
  wide,
}: {
  to: string;
  icon: LucideIcon;
  label: string;
  exact?: boolean;
  onNavigate?: () => void;
  wide?: boolean;
}) {
  return (
    <Link
      to={to}
      activeOptions={{ exact: exact ?? false }}
      onClick={onNavigate}
      className={cn(
        "flex items-center rounded-md text-muted-foreground text-xs hover:bg-accent hover:text-accent-foreground focus-visible:focus-ring-inset data-[status=active]:bg-primary-subtle data-[status=active]:font-medium data-[status=active]:text-foreground",
        wide ? "h-control-lg gap-3 px-3 text-sm" : "flex-col gap-1 px-1 py-2 text-center",
      )}
    >
      <Icon className="size-5 shrink-0" aria-hidden="true" />
      <span className={cn(!wide && "line-clamp-2 break-words")}>{label}</span>
    </Link>
  );
}

/** The panel's own screens in the navigation, with what they need. */
const panelEntries = [
  {
    to: "/accounts",
    icon: Building2,
    label: messages.navAccounts,
    permission: "core.account.read",
  },
  { to: "/users", icon: Users, label: messages.navUsers, permission: "core.user.read" },
  { to: "/audit", icon: ScrollText, label: messages.navAudit, permission: "core.audit.read" },
];

/**
 * The navigation: Home, every module's entries the person may see, the
 * accounts, users and audit log, and Settings. It is the icon rail on wide screens and the menu sheet on
 * phones, where wide lays the entries out in rows.
 */
export function NavLinks({ wide, onNavigate }: { wide?: boolean; onNavigate?: () => void }) {
  const intl = useIntl();
  const modules = useActiveModules();
  const allowed = usePermission();
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-1">
      <ul className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto">
        <li>
          <RailLink
            to="/"
            exact
            icon={House}
            label={intl.formatMessage(messages.navHome)}
            onNavigate={onNavigate}
            wide={wide}
          />
        </li>
        {navEntries(modules, intl, allowed).map((entry) => (
          <li key={`${entry.module.id}.${entry.id}`}>
            <RailLink
              to={entry.href}
              icon={entry.icon}
              label={intl.formatMessage(entry.label)}
              onNavigate={onNavigate}
              wide={wide}
            />
          </li>
        ))}
        {panelEntries
          .filter((entry) => allowed(entry.permission))
          .map((entry) => (
            <li key={entry.to}>
              <RailLink
                to={entry.to}
                icon={entry.icon}
                label={intl.formatMessage(entry.label)}
                onNavigate={onNavigate}
                wide={wide}
              />
            </li>
          ))}
      </ul>
      <div className="border-border border-t pt-1">
        <RailLink
          to="/settings"
          icon={Settings}
          label={intl.formatMessage(messages.navSettings)}
          onNavigate={onNavigate}
          wide={wide}
        />
      </div>
    </div>
  );
}

/** The icon rail on the start side of every screen, from md width up. */
export function Rail() {
  const intl = useIntl();
  return (
    <nav
      id="navigation"
      tabIndex={-1}
      aria-label={intl.formatMessage(messages.navLabel)}
      className="hidden w-20 flex-col border-border border-e bg-card p-2 outline-none md:flex"
    >
      <NavLinks />
    </nav>
  );
}
