// Holds the shell to its bundle budget (docs/architecture/conventions.md):
// what the browser downloads before it can show the panel, compressed. That
// is the entry chunk, everything it imports and its styles, and the chunks
// the shell loads lazily but before it renders, such as each module's entry
// file and the English messages. Screens, dialogs and other languages load
// later and don't count. Neither do fonts, which are compressed already and
// load as text appears.

import { gzipSync } from "node:zlib";

/**
 * The parts of Rolldown's output the budget reads.
 * @typedef {{ type: "chunk", fileName: string, code: string, imports: string[], isEntry: boolean,
 *   isDynamicEntry: boolean, facadeModuleId: string | null, viteMetadata?: { importedCss: Set<string> } }} Chunk
 * @typedef {{ type: "asset", fileName: string, source: string | Uint8Array }} Asset
 * @typedef {Record<string, Chunk | Asset>} Bundle
 */

/**
 * The files of the first load: the entry chunks and the lazily loaded
 * chunks the shell loads before it renders, everything they import, and
 * their styles.
 * @param {Bundle} bundle
 * @param {readonly RegExp[]} atStart the modules, by path, that the shell loads lazily before it renders
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
    const module = item.facadeModuleId?.split("\\").join("/") ?? "";
    if (item.isEntry || (item.isDynamicEntry && atStart.some((pattern) => pattern.test(module)))) {
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
 * Fails the build when the first load is over budget, and says what it
 * weighs.
 * @param {{ budget: number, atStart: readonly RegExp[] }} options budget in bytes
 * @returns {import("vite").Plugin}
 */
export function bundleBudget({ budget, atStart }) {
  return {
    name: "racksail:bundle-budget",
    apply: "build",
    /** @param {unknown} _options @param {Bundle} bundle */
    writeBundle(_options, bundle) {
      const sizes = firstLoad(bundle, atStart).map((file) => {
        const item = /** @type {Chunk | Asset} */ (bundle[file]);
        return { file, size: compressedSize(item.type === "chunk" ? item.code : item.source) };
      });
      const total = sizes.reduce((sum, { size }) => sum + size, 0);
      const summary = `the shell's first load is ${kb(total)} compressed, of a ${kb(budget)} budget`;
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
