import type { ThemeLayers } from "./layers";
import type { Theme } from "./types";

// Where a theme's files come from. It's apart from the theme engine so the
// panel can show a server's logo without loading the engine.

/** The brand files, which a layer replaces together. */
export const brandAssets = ["logoLight", "logoDark", "mark", "favicon"] as const;
export type BrandAsset = (typeof brandAssets)[number];

/** Whether a theme has any of the brand files. */
export function hasBrand(theme: Partial<Theme>): boolean {
  return brandAssets.some((name) => Boolean(theme.assets?.[name]));
}

/** The layers whose theme packages hold files. */
export type ThemeLayer = "server" | "reseller";

/** Where the panel serves a file of the server's or the reseller's theme package. */
export type ThemeFileURL = (layer: ThemeLayer, path: string) => string;

// Top layer first.
const fromTop = ["reseller", "server"] as const;

/** The layer whose package a font's files are in: the top one that names the font. */
export function fontLayer(layers: ThemeLayers, font: "sans" | "mono"): ThemeLayer | null {
  return fromTop.find((layer) => layers[layer]?.tokens.typography?.[font]?.family) ?? null;
}

/** The URLs of the brand files and the login background in effect. */
export type ThemeAssetURLs = Partial<Record<BrandAsset | "loginBackground", string>>;

/**
 * Where the logos, mark, favicon and login background in effect are
 * served. The brand files all come from the top layer that has any; the
 * login background from the top layer that has one.
 */
export function layerAssetURLs(layers: ThemeLayers, fileURL: ThemeFileURL): ThemeAssetURLs {
  const urls: ThemeAssetURLs = {};
  const brand = fromTop.find((layer) => {
    const theme = layers[layer];
    return theme ? hasBrand(theme) : false;
  });
  if (brand) {
    for (const name of brandAssets) {
      const path = layers[brand]?.assets?.[name];
      if (path) {
        urls[name] = fileURL(brand, path);
      }
    }
  }
  const background = fromTop.find((layer) => layers[layer]?.assets?.loginBackground);
  const path = background ? layers[background]?.assets?.loginBackground : undefined;
  if (background && path) {
    urls.loginBackground = fileURL(background, path);
  }
  return urls;
}
