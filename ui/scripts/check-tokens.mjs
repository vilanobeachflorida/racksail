// Reports colors and type sizes written into UI code instead of taken from
// theme tokens. Themes can only restyle what goes through tokens, so a
// hard-coded color is a bug even when it looks right in the default theme,
// and a size off the type scale ignores the theme's scale and can fall
// below the smallest step.
//
// Usage: node scripts/check-tokens.mjs [dir]   (default: src)
//
// It checks the files git tracks there, so it needs a git checkout.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const palette =
  "slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose";
const utilities =
  "bg|text|border(?:-[xytrblse])?|ring(?:-offset)?|outline|fill|stroke|from|via|to|decoration|divide|accent|caret|shadow|inset-shadow|placeholder";
const namedColors =
  "aliceblue|antiquewhite|aqua|aquamarine|azure|beige|bisque|black|blanchedalmond|blue|blueviolet|brown|burlywood|cadetblue|chartreuse|chocolate|coral|cornflowerblue|cornsilk|crimson|cyan|darkblue|darkcyan|darkgoldenrod|darkgray|darkgreen|darkgrey|darkkhaki|darkmagenta|darkolivegreen|darkorange|darkorchid|darkred|darksalmon|darkseagreen|darkslateblue|darkslategray|darkslategrey|darkturquoise|darkviolet|deeppink|deepskyblue|dimgray|dimgrey|dodgerblue|firebrick|floralwhite|forestgreen|fuchsia|gainsboro|ghostwhite|gold|goldenrod|gray|green|greenyellow|grey|honeydew|hotpink|indianred|indigo|ivory|khaki|lavender|lavenderblush|lawngreen|lemonchiffon|lightblue|lightcoral|lightcyan|lightgoldenrodyellow|lightgray|lightgreen|lightgrey|lightpink|lightsalmon|lightseagreen|lightskyblue|lightslategray|lightslategrey|lightsteelblue|lightyellow|lime|limegreen|linen|magenta|maroon|mediumaquamarine|mediumblue|mediumorchid|mediumpurple|mediumseagreen|mediumslateblue|mediumspringgreen|mediumturquoise|mediumvioletred|midnightblue|mintcream|mistyrose|moccasin|navajowhite|navy|oldlace|olive|olivedrab|orange|orangered|orchid|palegoldenrod|palegreen|paleturquoise|palevioletred|papayawhip|peachpuff|peru|pink|plum|powderblue|purple|rebeccapurple|red|rosybrown|royalblue|saddlebrown|salmon|sandybrown|seagreen|seashell|sienna|silver|skyblue|slateblue|slategray|slategrey|snow|springgreen|steelblue|tan|teal|thistle|tomato|turquoise|violet|wheat|white|whitesmoke|yellow|yellowgreen";
const colorProperties =
  "color|background|background-color|border|border-color|border-(?:top|right|bottom|left|block|inline|block-start|block-end|inline-start|inline-end)(?:-color)?|outline|outline-color|fill|stroke|box-shadow|text-shadow|caret-color|accent-color|text-decoration-color|column-rule-color|stop-color|flood-color|lighting-color";
const styleProperties =
  "color|background|backgroundColor|borderColor|outlineColor|fill|stroke|caretColor|accentColor|textDecorationColor|stopColor";

/** @type {{ name: string, pattern: RegExp, files: RegExp }[]} */
const rules = [
  {
    name: "hex color",
    pattern: /(?<![\w&])#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})(?![\w-])/g,
    files: /\.(?:css|tsx?|mjs)$/,
  },
  {
    name: "color function",
    pattern: /(?<![\w-])(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(/g,
    files: /\.(?:css|tsx?|mjs)$/,
  },
  {
    name: "Tailwind palette color",
    pattern: new RegExp(
      `(?<![\\w-])(?:${utilities})-(?:(?:${palette})-\\d{2,3}|white|black)(?:/\\d+)?(?![\\w-])`,
      "g",
    ),
    files: /\.(?:css|tsx?|mjs)$/,
  },
  {
    name: "named color",
    pattern: new RegExp(
      `(?<![\\w-])(?:${colorProperties})\\s*:[^;{}]*?\\b(?:${namedColors})\\b`,
      "gi",
    ),
    files: /\.css$/,
  },
  {
    name: "named color",
    pattern: new RegExp(`\\b(?:${styleProperties})\\s*:\\s*["'\`](?:${namedColors})\\b`, "g"),
    files: /\.(?:tsx?|mjs)$/,
  },
  {
    name: "arbitrary text size",
    pattern: /(?<![\w-])text-\[(?:length:)?[\d.]+(?:px|r?em|pt)\]/g,
    files: /\.(?:tsx?|mjs)$/,
  },
];

/** Generated from the theme by `racksail theme css`, so it holds the actual colors. */
const generated = ["design/theme.css", "lib/api/schema.d.ts"];

/**
 * Tests check color values themselves, such as contrast math; they style nothing.
 * @param {string} name
 */
const isTest = (name) => /\.test\.(?:tsx?|mjs)$/.test(name);

/**
 * @typedef {{ file: string, line: number, column: number, rule: string, text: string }} Problem
 */

/**
 * Finds hard-coded colors in one file's text.
 * @param {string} text
 * @param {string} file the file's path; its extension picks the rules
 * @returns {Problem[]}
 */
export function findHardCodedColors(text, file) {
  // CSS comments may mention colors; code comments are left alone because
  // stripping them reliably needs a parser.
  const source = file.endsWith(".css")
    ? text.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, " "))
    : text;
  /** @type {Problem[]} */
  const problems = [];
  for (const rule of rules) {
    if (!rule.files.test(file)) {
      continue;
    }
    for (const match of source.matchAll(rule.pattern)) {
      const before = source.slice(0, match.index);
      const line = before.split("\n").length;
      const column = match.index - before.lastIndexOf("\n");
      problems.push({ file, line, column, rule: rule.name, text: match[0].trim() });
    }
  }
  return problems.sort((a, b) => a.line - b.line || a.column - b.column);
}

/**
 * Lists the files to check under dir: those git tracks, leaving out hidden
 * directories, generated files and tests. Other checkouts and worktrees can
 * sit inside the repository, untracked and in hidden directories, so
 * neither counts; a file deleted but not yet committed doesn't either.
 * @param {string} dir
 * @returns {string[]}
 */
export function filesToCheck(dir) {
  const tracked = execFileSync("git", ["ls-files", "-z", "--cached", "--", "."], {
    cwd: dir,
    encoding: "utf8",
  }).split("\0");
  return tracked
    .filter((rel) => {
      const parts = rel.split("/");
      const name = parts.at(-1) ?? "";
      return (
        /\.(?:css|tsx?|mjs)$/.test(name) &&
        !parts.some((part) => part.startsWith(".")) &&
        !generated.includes(rel) &&
        !isTest(name)
      );
    })
    .map((rel) => join(dir, ...rel.split("/")))
    .filter((path) => existsSync(path))
    .sort();
}

function main() {
  const ui = fileURLToPath(new URL("..", import.meta.url));
  const dir = process.argv[2] ?? join(ui, "src");
  /** @type {string[]} */
  let files;
  try {
    files = filesToCheck(dir);
  } catch (error) {
    console.error(`check-tokens: git couldn't list the files to check in ${dir}: ${error}`);
    process.exitCode = 1;
    return;
  }
  const problems = files.flatMap((file) =>
    findHardCodedColors(readFileSync(file, "utf8"), relative(ui, file)),
  );
  for (const p of problems) {
    console.error(
      `${p.file}:${p.line}:${p.column}: ${p.rule} ${p.text}; use a theme token instead`,
    );
  }
  if (problems.length > 0) {
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
