import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { safeStorage } from "@/design/theme/prefs";
import type { SessionPreferences } from "@/lib/api/session";

/**
 * How the panel works for the person: where times show, which clock they
 * use, whether letter keys act as shortcuts and whether the command bar
 * remembers what they opened. They're kept in the browser and, once the
 * person is signed in, with their account, so they follow them.
 */
export interface GeneralPreferences {
  /** The time zone times show in, such as Europe/Lisbon or UTC; empty follows the browser. */
  timeZone: string;
  /** A 12-hour or 24-hour clock; empty follows the language. */
  hourCycle: "" | "h12" | "h23";
  /**
   * Shortcuts made of letter keys alone, such as G then H, which get in the
   * way of speech input and some screen readers, so people can turn them off.
   */
  letterShortcuts: boolean;
  /** Whether the command bar offers what was opened recently. */
  rememberRecent: boolean;
}

export const defaultGeneral: GeneralPreferences = {
  timeZone: "",
  hourCycle: "",
  letterShortcuts: true,
  rememberRecent: true,
};

export const generalKey = "racksail.general";

/** Whether a time zone is one the browser knows. */
export function knownTimeZone(zone: string): boolean {
  try {
    new Intl.DateTimeFormat("en", { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

/** Keeps known preferences with valid values and fills in the rest with defaults. */
export function normalizeGeneral(value: unknown): GeneralPreferences {
  const out = { ...defaultGeneral };
  if (typeof value !== "object" || value === null) {
    return out;
  }
  const v = value as Record<string, unknown>;
  if (typeof v.timeZone === "string" && (v.timeZone === "" || knownTimeZone(v.timeZone))) {
    out.timeZone = v.timeZone;
  }
  if (v.hourCycle === "" || v.hourCycle === "h12" || v.hourCycle === "h23") {
    out.hourCycle = v.hourCycle;
  }
  if (typeof v.letterShortcuts === "boolean") {
    out.letterShortcuts = v.letterShortcuts;
  }
  if (typeof v.rememberRecent === "boolean") {
    out.rememberRecent = v.rememberRecent;
  }
  return out;
}

/**
 * The general preferences among those an account keeps, or null when it
 * keeps none of them, as an account whose preferences were kept before the
 * panel kept these doesn't. The ones it leaves out are the defaults.
 */
export function accountGeneral(
  account: SessionPreferences | null | undefined,
): GeneralPreferences | null {
  if (
    !account ||
    (account.timeZone === undefined &&
      account.hourCycle === undefined &&
      account.letterShortcuts === undefined &&
      account.rememberRecent === undefined)
  ) {
    return null;
  }
  return normalizeGeneral({
    timeZone: account.timeZone ?? "",
    hourCycle: account.hourCycle ?? "",
    letterShortcuts: account.letterShortcuts ?? true,
    rememberRecent: account.rememberRecent ?? true,
  });
}

/** The general preferences as an account keeps them, without the ones that follow the browser. */
export function generalForAccount(prefs: GeneralPreferences): SessionPreferences {
  return {
    ...(prefs.timeZone ? { timeZone: prefs.timeZone } : {}),
    ...(prefs.hourCycle ? { hourCycle: prefs.hourCycle } : {}),
    letterShortcuts: prefs.letterShortcuts,
    rememberRecent: prefs.rememberRecent,
  };
}

export function loadGeneral(storage: Storage | null = safeStorage()): GeneralPreferences {
  try {
    const raw = storage?.getItem(generalKey);
    return normalizeGeneral(raw ? JSON.parse(raw) : null);
  } catch {
    return { ...defaultGeneral };
  }
}

function saveGeneral(prefs: GeneralPreferences, storage: Storage | null = safeStorage()) {
  try {
    storage?.setItem(generalKey, JSON.stringify(prefs));
  } catch {
    // Without storage the preferences last until the page is closed.
  }
}

interface GeneralValue {
  preferences: GeneralPreferences;
  setPreferences: (change: Partial<GeneralPreferences>) => void;
}

const GeneralContext = createContext<GeneralValue>({
  preferences: defaultGeneral,
  setPreferences: () => {},
});

export interface GeneralPreferencesProviderProps {
  /**
   * The preferences kept with the person's account. Its general ones
   * replace those this browser remembered once they arrive, so every
   * browser works the same.
   */
  account?: SessionPreferences | null;
  /** Called when the person changes a preference, to keep it with their account too. */
  onPreferencesChange?: (preferences: GeneralPreferences) => void;
  children: ReactNode;
}

/**
 * Keeps the general preferences in this browser, in step with its other
 * tabs, and with the account's when there is one.
 */
export function GeneralPreferencesProvider({
  account,
  onPreferencesChange,
  children,
}: GeneralPreferencesProviderProps) {
  const [preferences, setState] = useState<GeneralPreferences>(() => loadGeneral());
  // The preferences as last set, for changes made before React renders again.
  const current = useRef(preferences);
  const onChange = useRef(onPreferencesChange);
  useEffect(() => {
    onChange.current = onPreferencesChange;
  });
  const adopt = useCallback((next: GeneralPreferences) => {
    current.current = next;
    setState(next);
  }, []);
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key === generalKey) {
        adopt(loadGeneral());
      }
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [adopt]);
  // The account's preferences arrived or changed in another browser.
  const kept = accountGeneral(account);
  const keptKey = kept ? JSON.stringify(kept) : "";
  useEffect(() => {
    if (keptKey === "") {
      return;
    }
    const next = JSON.parse(keptKey) as GeneralPreferences;
    adopt(next);
    saveGeneral(next);
  }, [keptKey, adopt]);
  // Saving and telling the account happen here rather than in a state
  // updater, which React may call twice.
  const setPreferences = useCallback(
    (change: Partial<GeneralPreferences>) => {
      const next = normalizeGeneral({ ...current.current, ...change });
      adopt(next);
      saveGeneral(next);
      onChange.current?.(next);
    },
    [adopt],
  );
  const value = useMemo(() => ({ preferences, setPreferences }), [preferences, setPreferences]);
  return <GeneralContext value={value}>{children}</GeneralContext>;
}

/** The person's general preferences and a way to change them. */
export function useGeneralPreferences(): GeneralValue {
  return useContext(GeneralContext);
}
