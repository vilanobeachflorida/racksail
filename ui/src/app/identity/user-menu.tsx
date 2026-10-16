import { Link } from "@tanstack/react-router";
import { KeyRound, LogOut, ShieldCheck, UserRound } from "lucide-react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Tooltip,
} from "@/design/components/ui/overlay";
import { toast } from "@/design/components/ui/toaster";
import { unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { messages } from "../messages";
import { page } from "./page";
import { useSession } from "./session";

/** The first letters of a name, for the menu's button. */
export function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  const letters = words.length > 1 ? [words[0], words[words.length - 1]] : words;
  return letters
    .map((w) => [...(w ?? "")][0] ?? "")
    .join("")
    .toUpperCase();
}

/**
 * The signed-in person's menu: who they are, their account and security
 * settings, and signing out.
 */
export function UserMenu() {
  const intl = useIntl();
  const api = useApi();
  const { session } = useSession();
  if (!session) {
    return null;
  }
  const { user } = session;
  // Only a session ends by signing out: racksail dev signs its developer
  // in on every request, and API tokens are revoked instead.
  const canSignOut = !session.token && (!session.dev || session.expiresAt !== undefined);
  const leave = async () => {
    try {
      await unwrap(api.POST("/api/v1/auth/sign-out"));
      // Nothing of the session should stay in memory.
      page.load("/");
    } catch (error) {
      toast.error(
        intl.formatMessage(messages.userSignOutFailed, { reason: errorMessage(intl, error) }),
      );
    }
  };
  return (
    <DropdownMenu>
      <Tooltip content={intl.formatMessage(messages.userMenu)}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={intl.formatMessage(messages.userMenuFor, { name: user.name })}
          >
            <span
              aria-hidden="true"
              className="flex size-7 items-center justify-center rounded-full bg-primary-subtle font-semibold text-foreground text-xs"
            >
              {initials(user.name) || <UserRound className="size-4" />}
            </span>
          </Button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent align="end" className="min-w-56">
        <DropdownMenuLabel className="flex flex-col gap-0.5">
          <span className="truncate font-semibold">{user.name}</span>
          {user.username && user.username !== user.name ? (
            <span className="truncate font-normal text-muted-foreground text-xs">
              {user.username}
            </span>
          ) : null}
          {session.token ? (
            <span className="truncate font-normal text-muted-foreground text-xs">
              {intl.formatMessage(messages.userToken, { name: session.token.name })}
            </span>
          ) : null}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/settings/profile">
            <UserRound aria-hidden="true" />
            {intl.formatMessage(messages.userProfile)}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to="/settings/security">
            <ShieldCheck aria-hidden="true" />
            {intl.formatMessage(messages.userSecurity)}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to="/settings/tokens">
            <KeyRound aria-hidden="true" />
            {intl.formatMessage(messages.userTokens)}
          </Link>
        </DropdownMenuItem>
        {canSignOut ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void leave()}>
              <LogOut aria-hidden="true" />
              {intl.formatMessage(messages.userSignOut)}
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
