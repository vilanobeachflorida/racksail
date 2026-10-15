import { contrast, hue, isColor, mix } from "./color";
import { themeSpec } from "./spec";
import type { Pair, Palette, Theme, ThemeProblem } from "./types";
import { paletteKeys } from "./types";

/** Contrast minimums from WCAG 2.2 AA. */
export const textContrast = 4.5;
export const nonTextContrast = 3;

/**
 * Every color token of a palette by name: the ones themes set, chart1 to
 * chartN and the derived ones, as internal/theme computes them.
 */
export function paletteColors(palette: Palette): Record<string, string> {
  const colors: Record<string, string> = {};
  for (const key of paletteKeys) {
    const value = palette[key];
    if (typeof value === "string") {
      colors[key] = value;
    }
  }
  (palette.chart ?? []).forEach((c, i) => {
    colors[`chart${i + 1}`] = c;
  });
  for (const d of themeSpec.derivations) {
    const from = colors[d.from];
    const over = colors[d.over];
    const mixed = from && over ? mix(from, over, d.share) : null;
    if (mixed) {
      colors[d.name] = mixed;
    }
  }
  return colors;
}

/** The pairs checked for a palette: the fixed ones and one per chart color. */
export function pairsFor(palette: Palette): Pair[] {
  return [
    ...themeSpec.pairs,
    ...(palette.chart ?? []).map((_, i) => ({
      foreground: `chart${i + 1}`,
      background: "surfaceRaised",
      minimum: nonTextContrast,
      purpose: `chart series ${i + 1}`,
    })),
  ];
}

const round2 = (v: number) => Math.round(v * 100) / 100;

function checkPalette(mode: "light" | "dark", palette: Palette): ThemeProblem[] {
  const problems: ThemeProblem[] = [];
  const colors = paletteColors(palette);
  for (const name of Object.keys(colors).sort()) {
    const value = colors[name];
    if (value !== undefined && value !== "" && !isColor(value)) {
      problems.push({ mode, kind: "color", foreground: name, purpose: value });
    }
  }
  for (const pair of pairsFor(palette)) {
    const fg = colors[pair.foreground];
    const bg = colors[pair.background];
    const ratio = fg && bg ? contrast(fg, bg) : null;
    if (ratio === null || ratio + 1e-9 < pair.minimum) {
      problems.push({
        mode,
        kind: "contrast",
        foreground: pair.foreground,
        background: pair.background,
        ratio: ratio === null ? 0 : round2(ratio),
        minimum: pair.minimum,
        purpose: pair.purpose,
      });
    }
  }
  for (const meaning of themeSpec.meanings) {
    const value = colors[meaning.token];
    const h = value ? hue(value) : null;
    if (!h) {
      continue;
    }
    const ok =
      h.saturation >= 0.25 && meaning.hues.some(([from, to]) => h.hue >= from && h.hue <= to);
    if (!ok) {
      problems.push({ mode, kind: "meaning", foreground: meaning.token, purpose: meaning.name });
    }
  }
  return problems;
}

/**
 * Everything wrong with a theme's palettes: contrasts below WCAG AA,
 * colors that aren't colors and status colors that lost their meaning. The
 * server refuses a theme with any of these.
 */
export function checkTheme(theme: Theme): ThemeProblem[] {
  return [...checkPalette("light", theme.tokens.light), ...checkPalette("dark", theme.tokens.dark)];
}

/** One row of the contrast checker. */
export interface ContrastRow extends Pair {
  mode: "light" | "dark";
  ratio: number | null;
  pass: boolean;
}

/** Every checked pair of both modes with its ratio, for the editor's contrast checker. */
export function contrastTable(theme: Theme): ContrastRow[] {
  const rows: ContrastRow[] = [];
  for (const mode of ["light", "dark"] as const) {
    const palette = theme.tokens[mode];
    const colors = paletteColors(palette);
    for (const pair of pairsFor(palette)) {
      const fg = colors[pair.foreground];
      const bg = colors[pair.background];
      const ratio = fg && bg ? contrast(fg, bg) : null;
      rows.push({
        ...pair,
        mode,
        ratio: ratio === null ? null : round2(ratio),
        pass: ratio !== null && ratio + 1e-9 >= pair.minimum,
      });
    }
  }
  return rows;
}
