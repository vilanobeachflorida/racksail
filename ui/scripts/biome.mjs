// Runs Biome on the UI's files that git tracks, for npm run lint and npm run
// format, so another checkout or worktree inside the repository, or a file
// nobody has added, is neither checked nor rewritten. biome.json still
// decides which of them Biome reads.
//
// Usage: node scripts/biome.mjs <command> [options]   (such as check, or format --write)

import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { trackedFiles } from "./tracked.mjs";

const launcher = createRequire(import.meta.url).resolve("@biomejs/biome/bin/biome");

/**
 * Runs Biome with args, a command and its options, on the files git tracks
 * in dir. It runs in dir, where Biome finds its configuration.
 * @param {string[]} args
 * @param {string} dir
 * @param {import("node:child_process").StdioOptions} [stdio]
 * @returns {import("node:child_process").SpawnSyncReturns<string>}
 */
export function runBiome(args, dir, stdio = "inherit") {
  const files = trackedFiles(dir);
  if (files.length === 0) {
    // Without paths Biome would read the whole directory.
    throw new Error(`git tracks no files in ${dir}`);
  }
  return spawnSync(process.execPath, [launcher, ...args, "--", ...files], {
    cwd: dir,
    stdio,
    encoding: "utf8",
  });
}

function main() {
  const ui = fileURLToPath(new URL("..", import.meta.url));
  /** @type {import("node:child_process").SpawnSyncReturns<string>} */
  let result;
  try {
    result = runBiome(process.argv.slice(2), ui);
  } catch (error) {
    console.error(`biome: couldn't list the files to give Biome: ${error}`);
    process.exitCode = 1;
    return;
  }
  if (result.error) {
    console.error(`biome: couldn't run Biome: ${result.error}`);
  }
  process.exitCode = result.status ?? 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
