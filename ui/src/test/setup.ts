import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// jsdom has no layout, so it can't scroll; the router restores the scroll
// position on every navigation. Tests of Node scripts have no window.
if (typeof window !== "undefined") {
  window.scrollTo = () => {};
}

afterEach(() => {
  cleanup();
});
