// The suites leave out tests in hidden directories below their own, but
// still run when the checkout itself sits in a hidden directory, as a
// worktree inside the repository does.
//
//   node --test hidden.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import { hiddenBelow } from "./hidden.ts";

test("hidden directories below the tests hold none of them", () => {
  const ignore = hiddenBelow("/home/dev/racksail/.worktrees/one/test/e2e/specs");
  for (const file of ["smoke.spec.ts", "admin/users.spec.ts"]) {
    assert.ok(!ignore.test(`/home/dev/racksail/.worktrees/one/test/e2e/specs/${file}`), file);
  }
  for (const file of [".wip/smoke.spec.ts", "admin/.other/users.spec.ts", ".scratch.spec.ts"]) {
    assert.ok(ignore.test(`/home/dev/racksail/.worktrees/one/test/e2e/specs/${file}`), file);
  }
  assert.ok(!ignore.test("/home/dev/racksail/test/e2e/specs/.wip/smoke.spec.ts"), "another checkout's");
  assert.ok(!hiddenBelow("/a.b/specs").test("/aXb/specs/.x.spec.ts"), "dots in the path are literal");
});
