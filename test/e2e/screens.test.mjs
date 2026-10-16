// The browser suites name each screen they check the same way on every
// panel. A panel makes its IDs afresh each time it starts, as CI's does on
// every run, so a visual baseline named after one would never be found
// again.
//
//   node --test screens.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import { screenList, screenshotName } from "./screens.ts";

// Two panels started apart, whose developer users got different IDs.
const linked = ["/", "/core/webhooks", "/settings", "/users"];
const one = screenList(linked, "user_01m52fx5ynftkvem36pntmqk6h");
const two = screenList(linked, "user_01m52g6e9gfnm9qwe8cce0rt46");

test("every screen has the same name and baseline on every panel", () => {
  assert.notDeepEqual(
    one.map((s) => s.path),
    two.map((s) => s.path),
    "the panels' users have pages of their own",
  );
  assert.deepEqual(
    one.map((s) => s.name),
    two.map((s) => s.name),
  );
  assert.deepEqual(one.map(screenshotName), two.map(screenshotName));
  for (const screen of one) {
    assert.doesNotMatch(screen.name, /_[0-9a-z]{26}/, `${screen.path} is named after an ID`);
  }
});

test("the page of the user the suites act as is checked when there's a session", () => {
  const own = one.find((s) => s.path === "/users/user_01m52fx5ynftkvem36pntmqk6h");
  assert.deepEqual(own, { path: "/users/user_01m52fx5ynftkvem36pntmqk6h", name: "/users/<your id>" });
  assert.equal(screenshotName(own), "users-your-id.png");
  assert.ok(!screenList(linked).some((s) => s.path.startsWith("/users/")), "a panel without a session");
});

test("every screen is checked once, under a baseline of its own", () => {
  const paths = one.map((s) => s.path);
  assert.deepEqual(paths, [...new Set(paths)]);
  assert.equal(paths.filter((p) => p === "/").length, 1, "the navigation's link to the first screen");
  const files = one.map(screenshotName);
  assert.deepEqual(files, [...new Set(files)]);
  for (const file of files) {
    assert.match(file, /^[a-z0-9]+(?:-[a-z0-9]+)*\.png$/);
  }
  assert.equal(screenshotName({ path: "/", name: "/" }), "home.png");
  assert.equal(screenshotName({ path: "/core/webhooks/new", name: "/core/webhooks/new" }), "core-webhooks-new.png");
});
