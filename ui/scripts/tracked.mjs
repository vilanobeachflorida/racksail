// The files the UI's tools look at: those git tracks. Other checkouts and
// worktrees can sit inside the repository, untracked and in hidden
// directories, so neither counts; a file deleted but not yet committed
// doesn't either.

import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";

/**
 * Lists the files git tracks under dir, leaving out hidden files and
 * directories, as sorted paths relative to dir with forward slashes. It
 * needs a git checkout.
 * @param {string} dir
 * @returns {string[]}
 */
export function trackedFiles(dir) {
  // git's complaint, such as not being in a checkout, goes in the error.
  return execFileSync("git", ["ls-files", "-z", "--cached", "--", "."], {
    cwd: dir,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  })
    .split("\0")
    .filter((rel) => rel !== "" && !rel.split("/").some((part) => part.startsWith(".")))
    .filter((rel) => existsSync(join(dir, ...rel.split("/"))))
    .sort();
}
