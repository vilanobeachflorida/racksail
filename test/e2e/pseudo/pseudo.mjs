// Pseudo-locales: English messages rewritten so a panel shown in them tells
// what a translation would break, without anyone translating anything.
//
//   en-XA accents every letter, brackets each message and makes it a third
//         longer, so text that isn't from a catalog stands out, cut-off
//         text loses its closing bracket, and layouts get the length most
//         translations add.
//   ar-XB has every word read right to left, and since the UI treats it as
//         Arabic, the panel lays itself out right to left too.
//
// Only the text of a message changes: its arguments, plural and select
// branches and tags stay as they are, so the result formats like the
// English message.
import { parse, TYPE } from "@formatjs/icu-messageformat-parser";
import { printAST } from "@formatjs/icu-messageformat-parser/printer.js";

const plain = Array.from("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ");
const accented = Array.from("àƀçđéƒĝĥîĵķĺɱñöþǫŕšţûṽŵẋýžÀƁÇĐÉƑĜĤÎĴĶĹṀÑÖÞǪŔŠŢÛṼŴẊÝŽ");
const accents = new Map(plain.map((c, i) => [c, accented[i]]));

// Right-to-left mark, right-to-left override and pop directional
// formatting, as in Android's and Chrome's ar-XB.
const RLM = "‏";
const RLO = "‮";
const PDF = "‬";

// Padding that makes a message a third longer than its English.
export const padding = "·";

function eachLiteral(elements, fn) {
  for (const el of elements) {
    switch (el.type) {
      case TYPE.literal:
        fn(el);
        break;
      case TYPE.plural:
      case TYPE.select:
        for (const option of Object.values(el.options)) {
          eachLiteral(option.value, fn);
        }
        break;
      case TYPE.tag:
        eachLiteral(el.children, fn);
        break;
    }
  }
}

function accent(elements) {
  let letters = 0;
  eachLiteral(elements, (el) => {
    el.value = Array.from(el.value, (c) => {
      const a = accents.get(c);
      if (a) {
        letters++;
        return a;
      }
      return c;
    }).join("");
  });
  if (letters === 0) {
    return elements;
  }
  const longer = padding.repeat(Math.ceil(letters / 3));
  return [literal("["), ...elements, literal(`${longer}]`)];
}

function reverseWords(elements) {
  eachLiteral(elements, (el) => {
    el.value = el.value.replace(/[A-Za-z]+/g, (word) => `${RLM}${RLO}${word}${PDF}${RLM}`);
  });
  return elements;
}

function literal(value) {
  return { type: TYPE.literal, value };
}

/** The pseudo-locales and how each rewrites a message's elements. */
export const pseudoLocales = Object.freeze({ "en-XA": accent, "ar-XB": reverseWords });

/** Rewrites one ICU message for a pseudo-locale. */
export function pseudoMessage(message, locale) {
  const rewrite = pseudoLocales[locale];
  if (!rewrite) {
    throw new Error(`${locale} isn't a pseudo-locale; they are ${Object.keys(pseudoLocales).join(" and ")}`);
  }
  return printAST(rewrite(parse(message)));
}

/** Rewrites every message of a catalog, naming the one that doesn't parse. */
export function pseudoCatalog(catalog, locale) {
  const out = {};
  for (const [id, message] of Object.entries(catalog)) {
    try {
      out[id] = pseudoMessage(message, locale);
    } catch (error) {
      throw new Error(`message ${id}: ${error.message}`);
    }
  }
  return out;
}
