import { defaultTheme } from "./spec";
import type { Theme } from "./types";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// The same merge as internal/theme's Overlay: a layer's empty strings,
// zeros, empty lists and missing fields keep the value below.
function mergeInto(dst: Record<string, unknown>, src: Record<string, unknown>): void {
  for (const [key, value] of Object.entries(src)) {
    if (isRecord(value) && isRecord(dst[key])) {
      mergeInto(dst[key] as Record<string, unknown>, value);
      continue;
    }
    if (
      value === undefined ||
      value === null ||
      value === "" ||
      value === 0 ||
      (Array.isArray(value) && value.length === 0)
    ) {
      continue;
    }
    dst[key] = structuredClone(value);
  }
}

/** A theme with every token a layer sets replacing the base's. */
export function overlay(base: Theme, layer: Partial<Theme>): Theme {
  const merged = structuredClone(base) as unknown as Record<string, unknown>;
  mergeInto(merged, layer as unknown as Record<string, unknown>);
  return merged as unknown as Theme;
}

/**
 * The themes someone sees on top of the default theme: the server theme an
 * admin set and the theme of their reseller. Either may be missing.
 */
export interface ThemeLayers {
  server?: Theme | null;
  reseller?: Theme | null;
}

/** The theme in effect for the given layers, before personal preferences. */
export function resolveLayers(layers: ThemeLayers, base: Theme = defaultTheme): Theme {
  let theme = base;
  for (const layer of [layers.server, layers.reseller]) {
    if (layer) {
      theme = overlay(theme, layer);
    }
  }
  return theme;
}
