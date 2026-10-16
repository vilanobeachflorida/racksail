import { sep } from "node:path";

/**
 * Matches files in hidden directories below dir, an absolute path, such as
 * another checkout's or worktree's inside the repository, whose tests
 * aren't this one's. Playwright matches against absolute paths, and the
 * checkout may itself sit in a hidden directory, so only what's below dir
 * counts.
 */
export function hiddenBelow(dir: string): RegExp {
  const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const slash = escape(sep);
  return new RegExp(`^${escape(dir)}${slash}(?:[^${slash}]+${slash})*\\.`);
}
