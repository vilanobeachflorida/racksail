// Holds the first screen to its bundle budget (docs/architecture/conventions.md):
// what the browser downloads before it can show the home screen, compressed.
// That is the shell, the entry chunk, everything it imports and its styles,
// and the chunks the shell loads lazily but before it renders, such as each
// module's entry file and the English messages, and then what the home screen
// loads as it shows. Other screens, dialogs and languages load later and
// don't count. Neither do fonts, which are compressed already and load as
// text appears.

import { gzipSync } from "node:zlib";

/**
 * The parts of Rolldown's output the budget reads.
 * @typedef {{ type: "chunk", fileName: string, code: string, imports: string[], isEntry: boolean,
 *   isDynamicEntry: boolean, facadeModuleId: string | null, moduleIds?: string[],
 *   viteMetadata?: { importedCss: Set<string> } }} Chunk
 * @typedef {{ type: "asset", fileName: string, source: string | Uint8Array }} Asset
 * @typedef {Record<string, Chunk | Asset>} Bundle
 */

/**
 * The modules a chunk holds, by path with forward slashes. A chunk the build
 * put several lazily loaded modules in has no facade module, so it's known
 * by what it holds.
 * @param {Chunk} chunk
 * @returns {string[]}
 */
function modulesOf(chunk) {
  const ids = chunk.moduleIds ?? (chunk.facadeModuleId ? [chunk.facadeModuleId] : []);
  return ids.map((id) => id.split("\\").join("/"));
}

/**
 * The files of a first load: the entry chunks and the chunks that hold
 * modules loaded lazily before it's done, everything they import, and their
 * styles.
 * @param {Bundle} bundle
 * @param {readonly RegExp[]} atStart the modules, by path, that load lazily before it's done
 * @returns {string[]}
 */
export function firstLoad(bundle, atStart) {
  /** @type {Set<string>} */
  const files = new Set();
  /** @param {string} file */
  const add = (file) => {
    const item = bundle[file];
    if (files.has(file) || !item) {
      return;
    }
    files.add(file);
    if (item.type === "chunk") {
      item.imports.forEach(add);
      for (const css of item.viteMetadata?.importedCss ?? []) {
        add(css);
      }
    }
  };
  for (const item of Object.values(bundle)) {
    if (item.type !== "chunk") {
      continue;
    }
    if (item.isEntry || modulesOf(item).some((id) => atStart.some((pattern) => pattern.test(id)))) {
      add(item.fileName);
    }
  }
  return [...files].sort();
}

/**
 * How large a file is compressed, as the panel serves it.
 * @param {string | Uint8Array} source
 */
export function compressedSize(source) {
  return gzipSync(source, { level: 9 }).length;
}

/** @param {number} bytes */
const kb = (bytes) => `${(bytes / 1000).toFixed(1)} KB`;

/**
 * Fails the build when the first screen is over budget, and says what it
 * and the shell weigh.
 * @param {{ budget: number, atStart: readonly RegExp[], firstScreen?: readonly RegExp[] }} options
 *   budget in bytes; atStart, the modules the shell loads lazily before it
 *   renders; firstScreen, those the home screen then loads as it shows
 * @returns {import("vite").Plugin}
 */
export function bundleBudget({ budget, atStart, firstScreen = [] }) {
  return {
    name: "racksail:bundle-budget",
    apply: "build",
    /** @param {unknown} _options @param {Bundle} bundle */
    writeBundle(_options, bundle) {
      const shell = new Set(firstLoad(bundle, atStart));
      const sizes = firstLoad(bundle, [...atStart, ...firstScreen]).map((file) => {
        const item = /** @type {Chunk | Asset} */ (bundle[file]);
        return { file, size: compressedSize(item.type === "chunk" ? item.code : item.source) };
      });
      const total = sizes.reduce((sum, { size }) => sum + size, 0);
      const shellTotal = sizes.reduce(
        (sum, { file, size }) => sum + (shell.has(file) ? size : 0),
        0,
      );
      const summary =
        `the first screen loads ${kb(total)} compressed, of a ${kb(budget)} budget: ` +
        `${kb(shellTotal)} for the shell and ${kb(total - shellTotal)} for the home screen`;
      if (total > budget) {
        const largest = sizes
          .sort((a, b) => b.size - a.size)
          .slice(0, 10)
          .map(({ file, size }) => `  ${kb(size).padStart(9)}  ${file}`)
          .join("\n");
        this.error(
          `${summary}. Load more of it lazily, the way screens are; the largest files:\n${largest}`,
        );
      }
      console.log(`${summary}.`);
    },
  };
}
