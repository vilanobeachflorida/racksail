/**
 * Color math, computed exactly as internal/theme does it, so the editor's
 * contrast checker agrees with the server that accepts or refuses a theme.
 */

export interface RGB {
  r: number;
  g: number;
  b: number;
  a: number;
}

const hex = /^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;

/** Whether a string is a color themes accept: #RRGGBB or #RRGGBBAA. */
export function isColor(value: unknown): value is string {
  return typeof value === "string" && hex.test(value);
}

/** Reads #RRGGBB or #RRGGBBAA; null for anything else. */
export function parseColor(color: string): RGB | null {
  if (!isColor(color)) {
    return null;
  }
  const n = (i: number) => Number.parseInt(color.slice(i, i + 2), 16);
  return { r: n(1), g: n(3), b: n(5), a: color.length === 9 ? n(7) / 255 : 1 };
}

function channel(v: number): string {
  return Math.round(Math.max(0, Math.min(255, v)))
    .toString(16)
    .toUpperCase()
    .padStart(2, "0");
}

/** Formats a color as #RRGGBB, ignoring alpha. */
export function toHex({ r, g, b }: RGB): string {
  return `#${channel(r)}${channel(g)}${channel(b)}`;
}

/** A share of color a over b, as CSS color-mix in srgb does. */
export function mix(a: string, b: string, share: number): string | null {
  const ca = parseColor(a);
  const cb = parseColor(b);
  if (!ca || !cb) {
    return null;
  }
  const m = (x: number, y: number) => x * share + y * (1 - share);
  return toHex({ r: m(ca.r, cb.r), g: m(ca.g, cb.g), b: m(ca.b, cb.b), a: 1 });
}

/** Relative luminance as WCAG defines it, from 0 for black to 1 for white. */
export function luminance(color: string): number | null {
  const c = parseColor(color);
  if (!c) {
    return null;
  }
  const lin = (v: number) => {
    const x = v / 255;
    return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b);
}

/** The WCAG contrast ratio of two colors, from 1 to 21; null when either isn't a color. */
export function contrast(a: string, b: string): number | null {
  const la = luminance(a);
  const lb = luminance(b);
  if (la === null || lb === null) {
    return null;
  }
  const [hi, lo] = la > lb ? [la, lb] : [lb, la];
  return (hi + 0.05) / (lo + 0.05);
}

/** A color's hue in degrees and its saturation from 0 to 1, as in HSL. */
export function hue(color: string): { hue: number; saturation: number } | null {
  const c = parseColor(color);
  if (!c) {
    return null;
  }
  const r = c.r / 255;
  const g = c.g / 255;
  const b = c.b / 255;
  const hi = Math.max(r, g, b);
  const lo = Math.min(r, g, b);
  const d = hi - lo;
  if (d === 0) {
    return { hue: 0, saturation: 0 };
  }
  const l = (hi + lo) / 2;
  const saturation = d / (1 - Math.abs(2 * l - 1));
  let h: number;
  if (hi === r) {
    h = ((g - b) / d) % 6;
  } else if (hi === g) {
    h = (b - r) / d + 2;
  } else {
    h = (r - g) / d + 4;
  }
  h *= 60;
  return { hue: h < 0 ? h + 360 : h, saturation };
}
