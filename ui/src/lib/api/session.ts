import type { Preferences } from "@/design/theme/prefs";
import type { Theme } from "@/design/theme/types";
import { isModuleId, type ModuleId } from "@/modules/registry";

/** Where a module is in its life, as the module manager reports it. */
export type ModuleState = "off" | "on" | "active" | "stopping" | "removing";

/** Display preferences as the account keeps them; ones it leaves out are the defaults. */
export type SessionPreferences = Partial<Preferences>;

/**
 * What the panel tells the web UI when it starts: who is signed in, what
 * they may do, which modules are on, and how the panel looks for them.
 */
export interface PanelSession {
  user: {
    id: string;
    name: string;
    username?: string;
    email?: string;
    /** Where the person belongs: the server, a reseller or an account. */
    home?: { level: "server" | "reseller" | "account"; id?: string };
  };
  /**
   * What the person may do, as their roles grant it: permission names such
   * as web.site.read, or patterns such as * and *.read.
   */
  permissions: string[];
  /** Each module's state; modules it leaves out are off. */
  modules: Record<string, ModuleState>;
  theme?: {
    /** The server's theme, set by an admin. */
    server?: Theme | null;
    /** The theme of the reseller whose customer the person is. */
    reseller?: Theme | null;
    /** Where each theme package's files are served from, ending in a slash. */
    files?: { server?: string; reseller?: string };
    /** The name the server's or reseller's logo stands for. */
    brandName?: string;
    /** Where the admin's custom CSS is served, when it's on. */
    customCSS?: string;
  };
  /** The person's display preferences, kept with their account. */
  preferences?: SessionPreferences | null;
  /** The API token the panel was called with, rather than a session. */
  token?: { id: string; name: string };
  /** Who is viewing the panel as the person, and until when. */
  impersonation?: SessionImpersonation;
  /** What the person must do before anything else. */
  pending?: "two-factor" | "password";
  /** Until when sensitive actions go ahead without confirming it's them again. */
  steppedUpUntil?: string;
  /** How the person can confirm it's them: password, totp or passkey. */
  stepUpMethods?: string[];
  /** When the session ends at the latest. */
  expiresAt?: string;
  /** The accounts the person's grants name. */
  accounts?: { id: string; name: string; slug: string }[];
  /** Whether this is racksail dev, which signs everyone in as its developer. */
  dev?: boolean;
}

/** An admin or reseller viewing the panel as someone, to help them. */
export interface SessionImpersonation {
  /** The user viewing. */
  by: string;
  name: string;
  startedAt: string;
  endsAt: string;
}

/** The session as the panel sends it; lists it leaves empty may come as null. */
type SessionAnswer = Omit<PanelSession, "permissions" | "stepUpMethods" | "accounts"> & {
  permissions: string[] | null;
  stepUpMethods?: string[] | null;
  accounts?: { id: string; name: string; slug: string }[] | null;
};

/** The session the panel sent, with its empty lists as lists. */
export function normalizeSession(answer: SessionAnswer): PanelSession {
  return {
    ...answer,
    permissions: answer.permissions ?? [],
    stepUpMethods: answer.stepUpMethods ?? [],
    accounts: answer.accounts ?? [],
  };
}

// Wildcards in roles never cover these; they're granted by name.
const special = new Set(["core.override", "secrets.read", "core.impersonate"]);

function globToRegExp(pattern: string): RegExp {
  const source = pattern
    .split("")
    .map((c) => (c === "*" ? ".*" : c === "?" ? "." : c.replace(/[.+^${}()|[\]\\]/g, "\\$&")))
    .join("");
  return new RegExp(`^${source}$`);
}

/**
 * Whether permission patterns grant a permission. Patterns match as the
 * panel's roles do: * stands for any run of characters and ? for one, and
 * special permissions only match by name.
 */
export function permits(patterns: readonly string[], permission: string): boolean {
  return patterns.some((pattern) => {
    if (pattern === permission) {
      return true;
    }
    if (special.has(permission) || !/[*?]/.test(pattern)) {
      return false;
    }
    return globToRegExp(pattern).test(permission);
  });
}

/** The URL of a file in a theme package, given where the package's files are served. */
export function themeFileURL(base: string, path: string): string {
  const root = base.endsWith("/") ? base : `${base}/`;
  return root + path.split("/").map(encodeURIComponent).join("/");
}

/** The session's module states for the bundled modules; ones it leaves out are off. */
export function sessionModuleStates(session: PanelSession): Partial<Record<ModuleId, ModuleState>> {
  const states: Partial<Record<ModuleId, ModuleState>> = {};
  for (const [id, state] of Object.entries(session.modules ?? {})) {
    if (isModuleId(id)) {
      states[id] = state;
    }
  }
  return states;
}
