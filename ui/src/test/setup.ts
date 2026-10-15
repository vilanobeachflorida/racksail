import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// jsdom leaves out browser features the UI uses. These stand-ins do
// nothing, or as little as the components need, so tests can render them.
// Tests of Node scripts have no window.
if (typeof window !== "undefined") {
  // jsdom has no layout, so it can't scroll; the router restores the scroll
  // position on every navigation.
  window.scrollTo = () => {};
  Element.prototype.scrollIntoView = () => {};

  // No media query matches, as on a narrow light-mode screen without
  // preferences; tests that need one replace this.
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;

  window.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };

  // Radix reads pointer capture on its controls.
  Element.prototype.hasPointerCapture ??= () => false;
  Element.prototype.setPointerCapture ??= () => {};
  Element.prototype.releasePointerCapture ??= () => {};

  // Charts draw on a canvas; a context that accepts every call lets them
  // render without one.
  const context = new Proxy(
    {},
    {
      get: (_target, key) => {
        if (key === "measureText") {
          return () => ({ width: 10 });
        }
        if (key === "canvas") {
          return document.createElement("canvas");
        }
        return () => {};
      },
      set: () => true,
    },
  );
  HTMLCanvasElement.prototype.getContext = (() => context) as unknown as typeof HTMLCanvasElement.prototype.getContext;

  window.URL.createObjectURL ??= () => "blob:test";
  window.URL.revokeObjectURL ??= () => {};
}

afterEach(() => {
  cleanup();
  if (typeof window !== "undefined") {
    window.localStorage.clear();
    for (const attr of ["data-theme", "data-contrast", "data-density", "data-text-size", "data-motion"]) {
      document.documentElement.removeAttribute(attr);
    }
  }
});
