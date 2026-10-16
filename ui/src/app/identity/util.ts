import type { IntlShape } from "react-intl";
import { errorMessage } from "@/lib/errors";
import { t } from "./messages";
import { PasskeyError } from "./webauthn";

/** What went wrong, for people: a passkey prompt's outcome, or the panel's problem. */
export function failureMessage(intl: IntlShape, error: unknown): string {
  if (error instanceof PasskeyError) {
    switch (error.reason) {
      case "exists":
        return intl.formatMessage(t.passkeyExists);
      case "unavailable":
        return intl.formatMessage(t.passkeyUnavailable);
      case "unsupported":
        return intl.formatMessage(t.passkeyUnsupported);
      default:
        return intl.formatMessage(t.passkeyCancelled);
    }
  }
  return errorMessage(intl, error);
}

/** A moment, written for people in their language: "15 Oct 2026, 14:05". */
export function formatTime(intl: IntlShape, value: string | undefined | null): string {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) {
    return "";
  }
  return intl.formatDate(date, { dateStyle: "medium", timeStyle: "short" });
}

/** How long ago a moment was, or how long until it comes: "3 minutes ago", "in 2 days". */
export function formatRelative(
  intl: IntlShape,
  value: string | undefined | null,
  now: number = Date.now(),
): string {
  if (!value) {
    return "";
  }
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) {
    return "";
  }
  const seconds = Math.round((time - now) / 1000);
  const abs = Math.abs(seconds);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 365 * 86400],
    ["month", 30 * 86400],
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60],
  ];
  for (const [unit, size] of units) {
    if (abs >= size) {
      return intl.formatRelativeTime(Math.round(seconds / size), unit, { numeric: "auto" });
    }
  }
  return intl.formatRelativeTime(0, "second", { numeric: "auto" });
}

/** An owner as people read it: the whole server, or the account or reseller's ID. */
export function scopeLabel(
  intl: IntlShape,
  scope: { level: string; id?: string } | undefined,
  names?: ReadonlyMap<string, string>,
): string {
  if (!scope || scope.level === "server") {
    return intl.formatMessage(t.scopeServer);
  }
  const name = scope.id ? (names?.get(scope.id) ?? scope.id) : "";
  return intl.formatMessage(scope.level === "reseller" ? t.scopeReseller : t.scopeAccount, {
    name,
  });
}

/** An owner on its own, as a label: The whole server, Account Acme (acme). */
export function ownerLabel(
  intl: IntlShape,
  scope: { level: string; id?: string } | undefined,
  names?: ReadonlyMap<string, string>,
): string {
  if (!scope || scope.level === "server") {
    return intl.formatMessage(t.ownerServer);
  }
  const name = scope.id ? (names?.get(scope.id) ?? scope.id) : "";
  return intl.formatMessage(scope.level === "reseller" ? t.ownerReseller : t.ownerAccount, {
    name,
  });
}

/** Turns an owner typed by people, server, account:ID, reseller:ID or an account's ID, into one. */
export function parseScope(
  text: string,
): { level: "server" | "reseller" | "account"; id?: string } | null {
  const value = text.trim();
  if (value === "" || value === "server") {
    return { level: "server" };
  }
  const [level, id] = value.split(":", 2);
  if ((level === "account" || level === "reseller") && id) {
    return { level, id };
  }
  if (value.startsWith("acct_")) {
    return { level: "account", id: value };
  }
  return null;
}

/** Copies text to the clipboard; false when the browser wouldn't. */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

const units = ["K", "M", "G", "T"];

/**
 * Reads a size as people write it, as systemd does: bytes, or a number with
 * K, M, G or T, counting in 1024s. Null when it isn't one.
 */
export function parseSize(text: string): number | null {
  const match = /^\s*(\d+(?:\.\d+)?)\s*([KMGT])?(?:i?B)?\s*$/i.exec(text);
  if (!match) {
    return null;
  }
  const exponent = match[2] ? units.indexOf(match[2].toUpperCase()) + 1 : 0;
  return Math.round(Number(match[1]) * 1024 ** exponent);
}

/** Writes a size the way parseSize reads it, in the largest whole unit. */
export function sizeText(bytes: number | undefined): string {
  if (!bytes) {
    return "";
  }
  for (let i = units.length; i > 0; i--) {
    const size = 1024 ** i;
    if (bytes % size === 0) {
      return `${bytes / size}${units[i - 1]}`;
    }
  }
  return String(bytes);
}
