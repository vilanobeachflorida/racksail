import rawSpec from "../themes.json";
import type { Theme, ThemeSpec } from "./types";

/**
 * The built-in themes and the rules themes are checked against, generated
 * from internal/theme by `racksail theme spec` so both sides agree.
 */
export const themeSpec = rawSpec as unknown as ThemeSpec;

/** The default Racksail theme. */
export const defaultTheme: Theme = builtinTheme("racksail");

/** A built-in theme by id: racksail, high-contrast or compact. */
export function builtinTheme(id: string): Theme {
  const theme = themeSpec.themes.find((t) => t.id === id);
  if (!theme) {
    throw new Error(`there is no built-in theme called ${id}`);
  }
  return structuredClone(theme);
}

/** The built-in themes, the default first. */
export function builtinThemes(): Theme[] {
  return themeSpec.themes.map((t) => structuredClone(t));
}
