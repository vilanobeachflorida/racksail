/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { configDefaults } from "vitest/config";
import { bundleBudget } from "./scripts/bundle-budget.mjs";

// The address of `racksail dev`, which serves the API the dev server proxies to.
const devServer = process.env.RACKSAIL_DEV_ADDR ?? "127.0.0.1:7070";

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // The shell's budget from docs/architecture/conventions.md. Before it
    // renders, the shell also loads every module's entry file and English
    // messages, and the theme engine when the server or a reseller has a
    // theme.
    bundleBudget({
      budget: 250_000,
      atStart: [
        /\/src\/modules\/[^/]+\/index\.tsx?$/,
        /\/src\/modules\/[^/]+\/messages\/en\.json$/,
        /\/src\/design\/theme\/(?:css|layers)\.ts$/,
      ],
    }),
  ],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/api": { target: `http://${devServer}`, ws: true },
    },
  },
  build: {
    // The Go binary embeds whatever is here; see internal/webui.
    outDir: "../internal/webui/dist/app",
    emptyOutDir: true,
    target: "es2023",
    // The panel's Content-Security-Policy allows fonts only from 'self', so
    // nothing may be inlined as a data: URL.
    assetsInlineLimit: 0,
    // The bundle budget measures what matters, the compressed first load;
    // the entry chunk, React included, is larger than Vite's default for
    // one chunk before compression.
    chunkSizeWarningLimit: 600,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}", "scripts/**/*.test.mjs"],
    // Other checkouts and worktrees can sit inside the repository in hidden
    // directories; their tests aren't this one's.
    exclude: [...configDefaults.exclude, "**/.*/**"],
    css: false,
    restoreMocks: true,
  },
});
