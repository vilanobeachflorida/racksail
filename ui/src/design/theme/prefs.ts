/**
 * Personal display preferences, the top theme layer. They live in the
 * browser, and with the person's account once they're signed in, and apply
 * as attributes on the root element, which theme.css turns into tokens;
 * the server never has to render anything for them.
 */
export interface Preferences {
  mode: "system" | "light" | "dark";
  contrast: "system" | "standard" | "high";
  density: "theme" | "comfortable" | "compact";
  textSize: "default" | "small" | "large" | "larger" | "largest";
  motion: "system" | "reduce" | "full";
}

/** The values each preference takes; the first is the default. */
export const preferenceOptions = {
  mode: ["system", "light", "dark"],
  contrast: ["system", "standard", "high"],
  density: ["theme", "comfortable", "compact"],
  textSize: ["default", "small", "large", "larger", "largest"],
  motion: ["system", "reduce", "full"],
} as const satisfies { [K in keyof Preferences]: readonly Preferences[K][] };

export const defaultPreferences: Preferences = {
  mode: "system",
  contrast: "system",
  density: "theme",
  textSize: "default",
  motion: "system",
};

/** The attribute on the root element each preference sets. */
const attributes: Record<keyof Preferences, string> = {
  mode: "data-theme",
  contrast: "data-contrast",
  density: "data-density",
  textSize: "data-text-size",
  motion: "data-motion",
};

/** Keeps known preferences with valid values and fills in the rest with defaults. */
export function normalizePreferences(value: unknown): Preferences {
  const out = { ...defaultPreferences };
  if (typeof value !== "object" || value === null) {
    return out;
  }
  const record = value as Record<string, unknown>;
  for (const key of Object.keys(preferenceOptions) as (keyof Preferences)[]) {
    const v = record[key];
    if ((preferenceOptions[key] as readonly unknown[]).includes(v)) {
      (out as Record<string, unknown>)[key] = v;
    }
  }
  return out;
}

/** The root element's attributes for the preferences; defaults set none. */
export function preferenceAttributes(prefs: Preferences): Record<string, string> {
  const out: Record<string, string> = {};
  for (const key of Object.keys(attributes) as (keyof Preferences)[]) {
    if (prefs[key] !== preferenceOptions[key][0]) {
      out[attributes[key]] = prefs[key];
    }
  }
  return out;
}

/** Sets the root element's attributes for the preferences, removing the defaults' ones. */
export function applyPreferences(prefs: Preferences, root: HTMLElement = document.documentElement) {
  const wanted = preferenceAttributes(prefs);
  for (const attr of Object.values(attributes)) {
    const value = wanted[attr];
    if (value === undefined) {
      root.removeAttribute(attr);
    } else {
      root.setAttribute(attr, value);
    }
  }
}

export const preferencesKey = "racksail.preferences";

/** The preferences saved in this browser, or the defaults. */
export function loadPreferences(storage: Storage | null = safeStorage()): Preferences {
  try {
    const raw = storage?.getItem(preferencesKey);
    return normalizePreferences(raw ? JSON.parse(raw) : null);
  } catch {
    return { ...defaultPreferences };
  }
}

/** Saves the preferences in this browser; private windows may refuse, which is fine. */
export function savePreferences(prefs: Preferences, storage: Storage | null = safeStorage()) {
  try {
    storage?.setItem(preferencesKey, JSON.stringify(prefs));
  } catch {
    // Without storage the preferences last until the page is closed.
  }
}

/** localStorage when the browser allows it. */
export function safeStorage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

/**
 * Applies the saved preferences before anything renders, so the page never
 * flashes in the wrong mode or size.
 */
export function applyStoredPreferences(): Preferences {
  const prefs = loadPreferences();
  applyPreferences(prefs);
  return prefs;
}
