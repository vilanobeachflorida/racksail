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

/**
 * How the panel works for the person, in this browser: where times show,
 * which clock they use, whether letter keys act as shortcuts and whether
 * the command bar remembers what they opened.
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

/** Keeps the general preferences, in this browser and in step with its other tabs. */
export function GeneralPreferencesProvider({ children }: { children: ReactNode }) {
  const [preferences, setState] = useState<GeneralPreferences>(() => loadGeneral());
  // The preferences as last set, for changes made before React renders again.
  const current = useRef(preferences);
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key === generalKey) {
        current.current = loadGeneral();
        setState(current.current);
      }
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);
  // Saving happens here rather than in a state updater, which React may
  // call twice.
  const setPreferences = useCallback((change: Partial<GeneralPreferences>) => {
    const next = normalizeGeneral({ ...current.current, ...change });
    current.current = next;
    saveGeneral(next);
    setState(next);
  }, []);
  const value = useMemo(() => ({ preferences, setPreferences }), [preferences, setPreferences]);
  return <GeneralContext value={value}>{children}</GeneralContext>;
}

/** The person's general preferences and a way to change them. */
export function useGeneralPreferences(): GeneralValue {
  return useContext(GeneralContext);
}
