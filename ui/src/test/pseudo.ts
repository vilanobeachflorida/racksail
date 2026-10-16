/**
 * Finds text that didn't come from the message catalogs. Under the en-XA
 * pseudo-locale every message is bracketed and its letters accented, so
 * with test data written in another script (these tests use Greek), Latin
 * letters or separators outside brackets, or punctuation that joins two
 * messages, were written into the code.
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

// Punctuation that joins two messages, as in "[Jobs], [2 running]". The
// punctuation of numbers is allowed outside messages, so a separator
// written into the code shows only as what stands between two of them.
const separator = /[,:;·|–—]/;
const onlySeparators = /^[\s,:;·|–—]*$/;

interface Reading {
  depth: number;
  // What's outside messages since the last one ended, while it may still
  // be joining it to the next, and where it is.
  gap: string | null;
  where: string;
}

interface Join {
  text: string;
  where: string;
}

function outsideBrackets(
  text: string,
  reading: Reading,
  where: string,
): { outside: string; joins: Join[]; reading: Reading } {
  let outside = "";
  const joins: Join[] = [];
  let { depth, gap, where: gapWhere } = reading;
  for (const ch of text) {
    if (ch === "[") {
      if (depth === 0 && gap !== null && separator.test(gap)) {
        joins.push({ text: gap.trim(), where: gapWhere });
      }
      depth++;
      gap = null;
    } else if (ch === "]") {
      depth = Math.max(0, depth - 1);
      if (depth === 0) {
        gap = "";
      }
    } else if (depth === 0) {
      outside += ch;
      if (gap === "") {
        gapWhere = where;
      }
      gap = gap !== null && onlySeparators.test(gap + ch) ? gap + ch : null;
    }
  }
  return { outside, joins, reading: { depth, gap, where: gapWhere } };
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
  let reading: Reading = { depth: 0, gap: null, where: "" };
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.textContent ?? "";
    const where = `<${node.parentElement?.tagName.toLowerCase()}>`;
    const result = outsideBrackets(text, reading, where);
    reading = result.reading;
    for (const join of result.joins) {
      problems.push(`${JSON.stringify(join.text)} joins two messages in ${join.where}`);
    }
    if (
      !allowedOutside.test(result.outside) &&
      !keyCap.test(text) &&
      !formatted.includes(text.trim())
    ) {
      problems.push(`text ${JSON.stringify(result.outside.trim())} in ${where}`);
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
      const { outside, joins } = outsideBrackets(value, { depth: 0, gap: null, where: "" }, "");
      if (!allowedOutside.test(outside) || joins.length > 0) {
        problems.push(`${name}=${JSON.stringify(value)} on <${element.tagName.toLowerCase()}>`);
      }
    }
  }
  return problems;
}
