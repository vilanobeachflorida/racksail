import { type MessageFormatElement, parse, TYPE } from "@formatjs/icu-messageformat-parser";
import { printAST } from "@formatjs/icu-messageformat-parser/printer.js";
import type { Messages } from "./messages";
import type { PseudoLocale } from "./pseudo-locales";

// Making pseudo-locale messages takes the message printer, so this module
// loads only when a pseudo-locale is chosen.

export { isPseudoLocale, type PseudoLocale, pseudoLocales } from "./pseudo-locales";

const accents: Record<string, string> = {
  a: "á",
  b: "ƀ",
  c: "ç",
  d: "ð",
  e: "é",
  f: "ƒ",
  g: "ĝ",
  h: "ĥ",
  i: "í",
  j: "ĵ",
  k: "ķ",
  l: "ļ",
  m: "ɱ",
  n: "ñ",
  o: "ó",
  p: "þ",
  q: "ǫ",
  r: "ŕ",
  s: "š",
  t: "ţ",
  u: "ú",
  v: "ṽ",
  w: "ŵ",
  x: "ẋ",
  y: "ý",
  z: "ž",
  A: "Á",
  B: "Ɓ",
  C: "Ç",
  D: "Ð",
  E: "É",
  F: "Ƒ",
  G: "Ĝ",
  H: "Ĥ",
  I: "Í",
  J: "Ĵ",
  K: "Ķ",
  L: "Ļ",
  M: "Ṁ",
  N: "Ñ",
  O: "Ó",
  P: "Þ",
  Q: "Ǫ",
  R: "Ŕ",
  S: "Š",
  T: "Ţ",
  U: "Ú",
  V: "Ṽ",
  W: "Ŵ",
  X: "Ẋ",
  Y: "Ý",
  Z: "Ž",
};

const vowels = new Set("aeiouAEIOU");

function accentText(text: string): string {
  let out = "";
  for (const ch of text) {
    out += accents[ch] ?? ch;
    // Doubling vowels lengthens text the way translations often do.
    if (vowels.has(ch)) {
      out += accents[ch] ?? ch;
    }
  }
  return out;
}

// Right-to-left override and pop: Latin text shows reversed, as words in
// a right-to-left script would sit.
const rlo = "\u202E";
const pdf = "\u202C";

function transform(
  elements: MessageFormatElement[],
  text: (s: string) => string,
): MessageFormatElement[] {
  return elements.map((el) => {
    switch (el.type) {
      case TYPE.literal:
        return { ...el, value: text(el.value) };
      case TYPE.plural:
      case TYPE.select:
        return {
          ...el,
          options: Object.fromEntries(
            Object.entries(el.options).map(([key, option]) => [
              key,
              { ...option, value: transform(option.value, text) },
            ]),
          ),
        } as MessageFormatElement;
      case TYPE.tag:
        return { ...el, children: transform(el.children, text) };
      default:
        return el;
    }
  });
}

/** One message in a pseudo-locale; placeholders, plurals and tags keep working. */
export function pseudoMessage(message: string, locale: PseudoLocale): string {
  let ast: MessageFormatElement[];
  try {
    ast = parse(message, { ignoreTag: false });
  } catch {
    return message;
  }
  if (locale === "ar-XB") {
    return `${rlo}${printAST(transform(ast, (s) => s))}${pdf}`;
  }
  return `[${printAST(transform(ast, accentText))}]`;
}

/** A whole catalog in a pseudo-locale. */
export function pseudoMessages(messages: Messages, locale: PseudoLocale): Messages {
  return Object.fromEntries(
    Object.entries(messages).map(([id, text]) => [id, pseudoMessage(text, locale)]),
  );
}
