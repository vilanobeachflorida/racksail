import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useInsertionEffect,
  useMemo,
  useState,
} from "react";
import type { ThemeLayers } from "./layers";
import {
  applyPreferences,
  loadPreferences,
  type Preferences,
  preferencesKey,
  savePreferences,
} from "./prefs";
import type { Theme } from "./types";

export interface ThemeContextValue {
  /** The server and reseller themes on top of the default one. */
  layers: ThemeLayers;
  /** A theme shown on the whole panel for now, such as one being edited. */
  preview: Theme | null;
  setPreview: (theme: Theme | null) => void;
  preferences: Preferences;
  setPreferences: (change: Partial<Preferences>) => void;
  /** Light or dark, after following the system setting where the preference does. */
  mode: "light" | "dark";
  /**
   * Changes whenever token values may have changed, for code that reads
   * them, like charts drawn on a canvas.
   */
  tokensKey: string;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

// A short fingerprint of a string, to notice when the layer CSS changes.
function hash(text: string): string {
  let h = 5381;
  for (let i = 0; i < text.length; i++) {
    h = (Math.imul(h, 33) + text.charCodeAt(i)) | 0;
  }
  return (h >>> 0).toString(36);
}

function systemDark(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-color-scheme: dark)").matches
    : false;
}

function useSystemDark(): boolean {
  const [dark, setDark] = useState(systemDark);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") {
      return;
    }
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => setDark(query.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  return dark;
}

export interface ThemeProviderProps {
  /** The server and reseller themes, when the panel has them. */
  layers?: ThemeLayers;
  /** Where the panel serves a theme package's font files. */
  fontURL?: (path: string) => string;
  /** Called when the person changes a preference, to keep it with their account too. */
  onPreferencesChange?: (preferences: Preferences) => void;
  children: ReactNode;
}

/**
 * Applies the theme layers and personal preferences to the page: layers as
 * a style element that overrides the default theme's tokens, preferences as
 * attributes on the root element. The default theme is in the stylesheet
 * already, so the theme engine only loads when there's a layer to apply.
 */
export function ThemeProvider({
  layers,
  fontURL,
  onPreferencesChange,
  children,
}: ThemeProviderProps) {
  const [preferences, setPrefs] = useState<Preferences>(() => loadPreferences());
  const [preview, setPreview] = useState<Theme | null>(null);
  const [css, setCSS] = useState("");
  const systemIsDark = useSystemDark();
  const server = layers?.server ?? null;
  const reseller = layers?.reseller ?? null;
  const stableLayers = useMemo<ThemeLayers>(() => ({ server, reseller }), [server, reseller]);

  useEffect(() => {
    if (!preview && !server && !reseller) {
      setCSS("");
      return;
    }
    let cancelled = false;
    Promise.all([import("./css"), import("./layers")]).then(([{ renderLayerCSS }, { resolveLayers }]) => {
      if (!cancelled) {
        setCSS(renderLayerCSS(preview ?? resolveLayers({ server, reseller }), fontURL));
      }
    });
    return () => {
      cancelled = true;
    };
  }, [preview, server, reseller, fontURL]);

  useInsertionEffect(() => {
    if (css === "") {
      return;
    }
    const style = document.createElement("style");
    style.dataset.rsThemeLayer = "";
    style.textContent = css;
    document.head.append(style);
    return () => style.remove();
  }, [css]);

  useEffect(() => {
    applyPreferences(preferences);
  }, [preferences]);

  // Another tab changed the preferences.
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key === preferencesKey) {
        setPrefs(loadPreferences());
      }
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const mode: "light" | "dark" =
    preferences.mode === "system" ? (systemIsDark ? "dark" : "light") : preferences.mode;
  const tokensKey = `${mode}:${preferences.contrast}:${hash(css)}`;

  const setPreferences = useCallback(
    (change: Partial<Preferences>) => {
      setPrefs((current) => {
        const next = { ...current, ...change };
        savePreferences(next);
        onPreferencesChange?.(next);
        return next;
      });
    },
    [onPreferencesChange],
  );

  const value = useMemo<ThemeContextValue>(
    () => ({
      layers: stableLayers,
      preview,
      setPreview,
      preferences,
      setPreferences,
      mode,
      tokensKey,
    }),
    [stableLayers, preview, preferences, setPreferences, mode, tokensKey],
  );
  return <ThemeContext value={value}>{children}</ThemeContext>;
}

/** The theme layers and the person's preferences. */
export function useTheme(): ThemeContextValue {
  const value = useContext(ThemeContext);
  if (!value) {
    throw new Error("useTheme needs a ThemeProvider above it");
  }
  return value;
}

/** The theme context when there is one, for components that also work without it. */
export function useOptionalTheme(): ThemeContextValue | null {
  return useContext(ThemeContext);
}
