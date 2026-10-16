import { useEffect } from "react";
import { useGeneralPreferences } from "@/app/general";
import { safeStorage } from "@/design/theme/prefs";

/** Something opened recently, which the command bar offers again. */
export interface RecentItem {
  /** Where it is in the panel. */
  path: string;
  title: string;
  /** What it is, such as a kind's name. */
  what: string;
  /** When it was last opened, in milliseconds since the epoch. */
  at: number;
}

export const recentKey = "racksail.recent";
const kept = 12;

/** What was opened recently in this browser, the latest first. */
export function loadRecent(storage: Storage | null = safeStorage()): RecentItem[] {
  try {
    const raw = storage?.getItem(recentKey);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed
      .filter(
        (item): item is RecentItem =>
          typeof item === "object" &&
          item !== null &&
          typeof item.path === "string" &&
          item.path.startsWith("/") &&
          typeof item.title === "string" &&
          typeof item.what === "string" &&
          typeof item.at === "number",
      )
      .slice(0, kept);
  } catch {
    return [];
  }
}

/** Remembers that something was opened, keeping the latest few. */
export function recordRecent(
  item: Omit<RecentItem, "at">,
  storage: Storage | null = safeStorage(),
) {
  const next = [
    { ...item, at: Date.now() },
    ...loadRecent(storage).filter((r) => r.path !== item.path),
  ];
  try {
    storage?.setItem(recentKey, JSON.stringify(next.slice(0, kept)));
  } catch {
    // Without storage nothing is remembered, which only costs the shortcut.
  }
}

/** Forgets what was opened. */
export function clearRecent(storage: Storage | null = safeStorage()) {
  try {
    storage?.removeItem(recentKey);
  } catch {
    // Nothing was kept to forget.
  }
}

/** Remembers a screen as opened, unless the person turned that off. */
export function useRecordRecent(item: Omit<RecentItem, "at"> | null) {
  const { preferences } = useGeneralPreferences();
  const remember = preferences.rememberRecent;
  const path = item?.path;
  const title = item?.title;
  const what = item?.what;
  useEffect(() => {
    if (remember && path && title && what) {
      recordRecent({ path, title, what });
    }
  }, [remember, path, title, what]);
}
