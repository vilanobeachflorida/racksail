import { useNavigate } from "@tanstack/react-router";
import {
  Building2,
  Eye,
  KeyRound,
  Laptop,
  LogIn,
  LogOut,
  type LucideIcon,
  Plus,
  ScrollText,
  ShieldCheck,
  Terminal,
  UserPlus,
  UserRound,
  Users,
  UsersRound,
} from "lucide-react";
import type { MessageDescriptor } from "react-intl";
import { useIntl } from "react-intl";
import { CommandGroup, CommandItem } from "@/design/components/ui/command";
import { toast } from "@/design/components/ui/toaster";
import { unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { messages } from "../messages";
import { usePermission } from "../modules";
import { peopleMessages as pm } from "../screens/messages";
import { page } from "./page";
import { useSession } from "./session";

interface Entry {
  id: string;
  label: MessageDescriptor;
  icon: LucideIcon;
  /** Words people may type for it besides its label. */
  keywords: string[];
  /** Where it goes; with add, the screen opens its form to add one. */
  to: string;
  add?: boolean;
  /** What the person needs to see it; nothing for what everyone has. */
  permission?: string;
}

/**
 * The kinds the screens about people and access are for. The command bar
 * offers them only here, so their generic screens aren't offered as well.
 */
export const peopleKinds: ReadonlySet<string> = new Set([
  "core.user",
  "core.account",
  "core.signin_policy",
  "core.signin_provider",
]);

/** The screens about people and access, with what each needs. */
const people: Entry[] = [
  {
    id: "users",
    label: messages.navUsers,
    icon: Users,
    keywords: ["people", "user", "members"],
    to: "/users",
    permission: "core.user.read",
  },
  {
    id: "accounts",
    label: messages.navAccounts,
    icon: Building2,
    keywords: ["customers", "tenants", "account"],
    to: "/accounts",
    permission: "core.account.read",
  },
  {
    id: "roles",
    label: messages.routeRoles,
    icon: UsersRound,
    keywords: ["permissions", "matrix", "role"],
    to: "/roles",
    permission: "core.user.read",
  },
  {
    id: "audit",
    label: messages.navAudit,
    icon: ScrollText,
    keywords: ["audit", "log", "who did what", "history"],
    to: "/audit",
    permission: "core.audit.read",
  },
  {
    id: "sign-in",
    label: pm.commandSignInSettings,
    icon: LogIn,
    keywords: [
      "sign-in policy",
      "lockouts",
      "second step",
      "allowed addresses",
      "providers",
      "ldap",
      "oidc",
    ],
    to: "/settings/sign-in",
    permission: "core.signin_policy.read",
  },
];

/** The person's own screens. */
const yours: Entry[] = [
  {
    id: "profile",
    label: messages.routeProfile,
    icon: UserRound,
    keywords: ["name", "email", "language", "password"],
    to: "/settings/profile",
  },
  {
    id: "security",
    label: messages.routeSecurity,
    icon: ShieldCheck,
    keywords: ["security", "two-factor", "authenticator", "passkeys", "recovery codes", "alerts"],
    to: "/settings/security",
  },
  {
    id: "sessions",
    label: messages.routeSessions,
    icon: Laptop,
    keywords: ["sessions", "devices", "sign out"],
    to: "/settings/sessions",
  },
  {
    id: "tokens",
    label: messages.routeTokens,
    icon: KeyRound,
    keywords: ["token", "api", "racksail command"],
    to: "/settings/tokens",
  },
  {
    id: "ssh-keys",
    label: messages.routeSSHKeys,
    icon: Terminal,
    keywords: ["ssh", "sftp", "public key"],
    to: "/settings/ssh-keys",
  },
];

/** What the person may add from the command bar. */
const additions: (Entry & { mine?: boolean })[] = [
  {
    id: "add-user",
    label: pm.commandAddUser,
    icon: UserPlus,
    keywords: ["new user", "invite", "people"],
    to: "/users",
    add: true,
    permission: "core.user.create",
  },
  {
    id: "open-account",
    label: pm.commandOpenAccount,
    icon: Plus,
    keywords: ["new account", "customer", "tenant"],
    to: "/accounts",
    add: true,
    permission: "core.account.create",
  },
  {
    id: "make-token",
    label: pm.commandMakeToken,
    icon: KeyRound,
    keywords: ["new token", "api", "racksail command"],
    to: "/settings/tokens",
    add: true,
    mine: true,
  },
  {
    id: "add-ssh-key",
    label: pm.commandAddSSHKey,
    icon: Terminal,
    keywords: ["new key", "ssh", "sftp"],
    to: "/settings/ssh-keys",
    add: true,
    mine: true,
  },
];

/**
 * The command bar's entries for people and access: the screens about
 * yourself, the users, accounts, roles, audit log and sign-in settings the
 * person may see, and what they may do from there, such as adding a user,
 * signing out or no longer viewing the panel as someone else.
 */
export function IdentityCommands({ close }: { close: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const navigate = useNavigate();
  const allowed = usePermission();
  const { session } = useSession();
  const go = (entry: Entry) => {
    close();
    void navigate({ to: entry.to, search: entry.add ? { new: 1 } : {} });
  };
  const leave = async (run: () => Promise<unknown>, failed: (reason: string) => string) => {
    close();
    try {
      await run();
      // Nothing of the session should stay in memory.
      page.load("/");
    } catch (error) {
      toast.error(failed(errorMessage(intl, error)));
    }
  };
  const item = (entry: Entry, key: string) => (
    <CommandItem
      key={key}
      value={`${key} ${intl.formatMessage(entry.label)}`}
      keywords={entry.keywords}
      onSelect={() => go(entry)}
    >
      <entry.icon aria-hidden="true" />
      {intl.formatMessage(entry.label)}
    </CommandItem>
  );
  // A person changes their own sign-in only in their own session: not with
  // an API token, nor while viewing the panel as someone else.
  const own = Boolean(session && !session.token && !session.impersonation);
  const viewing = session?.impersonation;
  // Only a session ends by signing out, as in the user menu.
  const canSignOut =
    Boolean(session) && !session?.token && (!session?.dev || session.expiresAt !== undefined);
  const shownPeople = [...people, ...additions.filter((e) => !e.mine)].filter((e) =>
    allowed(e.permission),
  );
  const ownAdditions = own ? additions.filter((e) => e.mine) : [];
  return (
    <>
      {shownPeople.length > 0 ? (
        <CommandGroup heading={intl.formatMessage(pm.settingsPeople)}>
          {shownPeople.map((e) => item(e, `people ${e.id}`))}
        </CommandGroup>
      ) : null}
      {session ? (
        <CommandGroup heading={intl.formatMessage(messages.userMenu)}>
          {[...yours, ...ownAdditions].map((e) => item(e, `yours ${e.id}`))}
          {viewing ? (
            <CommandItem
              value={`stop viewing ${intl.formatMessage(messages.impersonationStop, { user: session.user.name })}`}
              keywords={["impersonation", "view as", "back to my account"]}
              onSelect={() =>
                void leave(
                  () => unwrap(api.DELETE("/api/v1/session/impersonation")),
                  (reason) =>
                    intl.formatMessage(messages.impersonationStopFailed, {
                      user: session.user.name,
                      reason,
                    }),
                )
              }
            >
              <Eye aria-hidden="true" />
              {intl.formatMessage(messages.impersonationStop, { user: session.user.name })}
            </CommandItem>
          ) : null}
          {canSignOut ? (
            <CommandItem
              value={`sign out ${intl.formatMessage(messages.userSignOut)}`}
              keywords={["log out", "leave"]}
              onSelect={() =>
                void leave(
                  () => unwrap(api.POST("/api/v1/auth/sign-out")),
                  (reason) => intl.formatMessage(messages.userSignOutFailed, { reason }),
                )
              }
            >
              <LogOut aria-hidden="true" />
              {intl.formatMessage(messages.userSignOut)}
            </CommandItem>
          ) : null}
        </CommandGroup>
      ) : null}
    </>
  );
}
