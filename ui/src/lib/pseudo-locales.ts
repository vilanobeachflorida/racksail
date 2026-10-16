/**
 * Pseudo-locales make translation problems visible without a translation:
 * en-XA accents every letter and makes text about 40% longer, so strings
 * that skip the catalogs or layouts that can't grow stand out, and ar-XB
 * mirrors the layout right to left with text marked to read that way.
 * pseudo.ts makes their messages.
 */
export const pseudoLocales = ["en-XA", "ar-XB"] as const;
export type PseudoLocale = (typeof pseudoLocales)[number];

export function isPseudoLocale(locale: string): locale is PseudoLocale {
  return (pseudoLocales as readonly string[]).includes(locale);
}
