// Run with: node --test pseudo/
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { parse, TYPE } from "@formatjs/icu-messageformat-parser";
import { padding, pseudoCatalog, pseudoLocales, pseudoMessage } from "./pseudo.mjs";

const plain = Array.from("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ");
const accented = Array.from("àƀçđéƒĝĥîĵķĺɱñöþǫŕšţûṽŵẋýžÀƁÇĐÉƑĜĤÎĴĶĹṀÑÖÞǪŔŠŢÛṼŴẊÝŽ");

// shape writes a parsed message back with its literals as they read, so
// two messages compare equal when they format the same way.
function shape(elements) {
  return elements
    .map((el) => {
      switch (el.type) {
        case TYPE.literal:
          return el.value;
        case TYPE.argument:
          return `{${el.value}}`;
        case TYPE.number:
        case TYPE.date:
        case TYPE.time:
          return `{${el.value}, ${el.type}}`;
        case TYPE.pound:
          return "{#}";
        case TYPE.tag:
          return `<${el.value}>${shape(el.children)}</${el.value}>`;
        case TYPE.plural:
        case TYPE.select: {
          // Options are matched by key, so their order doesn't count.
          const options = Object.entries(el.options)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([key, option]) => `${key} {${shape(option.value)}}`);
          return `{${el.value}, ${el.type === TYPE.plural ? "plural" : "select"}, ${el.offset ? `offset:${el.offset} ` : ""}${options.join(" ")}}`;
        }
        default:
          throw new Error(`unexpected element ${el.type}`);
      }
    })
    .join("");
}

// undo takes a pseudo-locale's changes back out of a shape.
const undo = {
  "en-XA": (s) => {
    const m = s.match(new RegExp(`^\\[([\\s\\S]*?)${padding}+\\]$`));
    const inner = m ? m[1] : s;
    return Array.from(inner, (c) => {
      const i = accented.indexOf(c);
      return i >= 0 ? plain[i] : c;
    }).join("");
  },
  "ar-XB": (s) => s.replace(/[‏‮‬]/g, ""),
};

const examples = [
  "Skip to content",
  "{page} · Racksail",
  "The panel isn't answering. Check that racksail is running, then reload this page.",
  "Racksail {version}, {channel, select, stable {stable channel} beta {beta channel} nightly {nightly channel} other {development build}}",
  "{count, plural, one {# site} other {# sites}} on {host}",
  "Read <link>the guide</link> first, or '{'braces'}' stay literal",
  "Used {used, number, ::percent} of {total, number} on {day, date, medium}",
  "It's {name}'s server",
  "{name}",
];

test("pseudo-locales keep every message's structure", () => {
  for (const locale of Object.keys(pseudoLocales)) {
    for (const message of examples) {
      const pseudo = pseudoMessage(message, locale);
      assert.equal(undo[locale](shape(parse(pseudo))), shape(parse(message)), `${locale}: ${message} became ${pseudo}`);
    }
  }
});

test("en-XA accents, brackets and lengthens text", () => {
  assert.equal(pseudoMessage("Skip to content", "en-XA"), "[Šķîþ ţö çöñţéñţ·····]");
  assert.equal(pseudoMessage("{name}", "en-XA"), "{name}");
  const pseudo = pseudoMessage("{count, plural, one {# site} other {# sites}}", "en-XA");
  assert.match(pseudo, /^\[\{count,\s*plural,\s*one\s*\{# šîţé\}\s*other\s*\{# šîţéš\}\}·+\]$/);
  assert.doesNotMatch(pseudoMessage(examples[2], "en-XA"), /[A-Za-z]{2}.*\]$/s);
});

test("ar-XB reverses each word", () => {
  assert.equal(pseudoMessage("Go home", "ar-XB"), "‏‮Go‬‏ ‏‮home‬‏");
});

test("a message that doesn't parse is named", () => {
  assert.throws(() => pseudoCatalog({ "broken.message": "{unclosed" }, "en-XA"), /message broken\.message/);
  assert.throws(() => pseudoMessage("x", "fr-XX"), /isn't a pseudo-locale/);
});

// The pseudo build rewrites every catalog the UI has, so each must survive.
test("every catalog in the UI converts", () => {
  const repo = path.resolve(import.meta.dirname, "../../..");
  const files = execFileSync("git", ["ls-files", "-z", "--", "ui/src/**/messages/en.json"], { cwd: repo, encoding: "utf8" })
    .split("\0")
    .filter(Boolean);
  assert.ok(files.length > 0, "the UI has no catalogs");
  for (const file of files) {
    const english = JSON.parse(readFileSync(path.join(repo, file), "utf8"));
    for (const locale of Object.keys(pseudoLocales)) {
      const pseudo = pseudoCatalog(english, locale);
      assert.deepEqual(Object.keys(pseudo), Object.keys(english));
      for (const [id, message] of Object.entries(pseudo)) {
        assert.equal(undo[locale](shape(parse(message))), shape(parse(english[id])), `${file} ${id} in ${locale}`);
      }
    }
  }
});
