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
import { fontLayer, layerAssetURLs, type ThemeAssetURLs, type ThemeFileURL } from "./files";
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
  /** Where the server's or reseller's logos, mark, favicon and login background are served. */
  assets: ThemeAssetURLs;
  /** The name a server's or reseller's logo stands for, when it has one. */
  brandName: string | null;
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

/** Adds a stylesheet to the page while there's one to add. */
function useStylesheet(href: string | undefined, marker: string) {
  useEffect(() => {
    if (!href) {
      return;
    }
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = href;
    link.dataset[marker] = "";
    document.head.append(link);
    return () => link.remove();
  }, [href, marker]);
}

/** Shows the theme's favicon in the browser tab instead of Racksail's, while there is one. */
function useFavicon(href: string | undefined) {
  useEffect(() => {
    if (!href) {
      return;
    }
    const links = [...document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')];
    const before = links.map((link) => ({
      link,
      href: link.getAttribute("href"),
      type: link.getAttribute("type"),
      sizes: link.getAttribute("sizes"),
    }));
    for (const link of links) {
      // The theme's file may be any image type, so the browser works it out.
      link.setAttribute("href", href);
      link.removeAttribute("type");
      link.removeAttribute("sizes");
    }
    return () => {
      for (const { link, ...attrs } of before) {
        for (const [name, value] of Object.entries(attrs)) {
          if (value === null) {
            link.removeAttribute(name);
          } else {
            link.setAttribute(name, value);
          }
        }
      }
    };
  }, [href]);
}

export interface ThemeProviderProps {
  /** The server and reseller themes, when the panel has them. */
  layers?: ThemeLayers;
  /** Where the panel serves the files of the server's and reseller's theme packages. */
  fileURL?: ThemeFileURL;
  /** The name the server's or reseller's logo stands for, such as a hosting company's. */
  brandName?: string;
  /**
   * Where the server's custom CSS is served, when an admin has turned it on.
   * The panel checks it and wraps it in its cascade layer, below personal
   * preferences and the protected palette (internal/theme.WrapCustomCSS).
   */
  customCSSURL?: string;
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
  fileURL,
  brandName,
  customCSSURL,
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
    // A theme being previewed has no package to serve fonts from.
    const fontURL =
      fileURL && !preview
        ? (path: string, font: "sans" | "mono") => {
            const layer = fontLayer({ server, reseller }, font);
            return layer ? fileURL(layer, path) : path;
          }
        : undefined;
    Promise.all([import("./css"), import("./layers")]).then(
      ([{ renderLayerCSS }, { resolveLayers }]) => {
        if (!cancelled) {
          setCSS(renderLayerCSS(preview ?? resolveLayers({ server, reseller }), fontURL));
        }
      },
    );
    return () => {
      cancelled = true;
    };
  }, [preview, server, reseller, fileURL]);

  const assets = useMemo(
    () => (fileURL ? layerAssetURLs({ server, reseller }, fileURL) : {}),
    [server, reseller, fileURL],
  );
  useFavicon(assets.favicon);
  useStylesheet(customCSSURL, "rsCustom");

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
      assets,
      brandName: brandName ?? null,
    }),
    [stableLayers, preview, preferences, setPreferences, mode, tokensKey, assets, brandName],
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
