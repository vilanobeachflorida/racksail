/**
 * Finds text that didn't come from the message catalogs. Under the en-XA
 * pseudo-locale every message is bracketed and its letters accented, so
 * with test data written in another script (these tests use Greek), Latin
 * letters or separators outside brackets were written into the code.
 */

// What data and formatting may put outside a message: Greek test data,
// digits, spaces, the punctuation of numbers, paths and addresses, and
// symbols drawn in place of icons, such as a diff's + and ~ or a close
// button's ×. Words, and separators such as · or →, belong in messages,
// where translators can change them.
const allowedOutside = /^[\s\d.,:/_\-+%#@()=$✓✗!~−×›⌄\p{Script=Greek}]*$/u;

// A key cap on its own, such as the K of Ctrl K.
const keyCap = /^\s*[A-Z0-9⌘⇧⌥?.]\s*$/;

// Values that are data in attributes, such as color swatches' titles.
const dataAttribute = /^(#[0-9A-Fa-f]{6,8}|[\s\d.,:/_\-+%#@()=\p{Script=Greek}]*)$/u;

const textAttributes = [
  "aria-label",
  "aria-description",
  "aria-roledescription",
  "aria-valuetext",
  "title",
  "placeholder",
  "alt",
];

// Dates and times come from the browser's Intl, in the locale's words, and
// a text area's text is the value being edited.
const skipped = "style, script, noscript, template, time, textarea";

function outsideBrackets(text: string, depth: number): { outside: string; depth: number } {
  let outside = "";
  let d = depth;
  for (const ch of text) {
    if (ch === "[") {
      d++;
    } else if (ch === "]") {
      d = Math.max(0, d - 1);
    } else if (d === 0) {
      outside += ch;
    }
  }
  return { outside, depth: d };
}

/**
 * Text and labels under root that aren't from a message, one entry each.
 * formatted lists words the browser's Intl writes in the locale, such as
 * unit names, which aren't the catalogs' to translate.
 */
export function untranslatedText(
  root: Element = document.body,
  formatted: readonly string[] = [],
): string[] {
  const problems: string[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (node) =>
      node.parentElement?.closest(skipped) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
  });
  let depth = 0;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.textContent ?? "";
    const { outside, depth: next } = outsideBrackets(text, depth);
    depth = next;
    if (!allowedOutside.test(outside) && !keyCap.test(text) && !formatted.includes(text.trim())) {
      problems.push(
        `text ${JSON.stringify(outside.trim())} in <${node.parentElement?.tagName.toLowerCase()}>`,
      );
    }
  }
  for (const element of root.querySelectorAll("*")) {
    if (element.closest(skipped)) {
      continue;
    }
    for (const name of textAttributes) {
      const value = element.getAttribute(name);
      if (value === null || dataAttribute.test(value)) {
        continue;
      }
      const { outside } = outsideBrackets(value, 0);
      if (!allowedOutside.test(outside)) {
        problems.push(`${name}=${JSON.stringify(value)} on <${element.tagName.toLowerCase()}>`);
      }
    }
  }
  return problems;
}
