/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { configDefaults } from "vitest/config";
import { bundleBudget } from "./scripts/bundle-budget.mjs";

// The address of `racksail dev`, which serves the API the dev server proxies to.
const devServer = process.env.RACKSAIL_DEV_ADDR ?? "127.0.0.1:7070";

// Every module's entry file and English messages, which the shell loads
// before it renders, whatever state the module is in.
const moduleEntries = /[\\/]src[\\/]modules[\\/][^\\/]+[\\/]index\.tsx?$/;
const moduleMessages = /[\\/]src[\\/]modules[\\/][^\\/]+[\\/]messages[\\/]en\.json$/;

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // The first screen's budget from docs/architecture/conventions.md.
    // Before it renders, the shell also loads every module's entry file and
    // English messages, and the theme engine when the server or a reseller
    // has a theme; the home screen then loads the attention inbox, its words
    // and the core's kinds', and the quick actions.
    bundleBudget({
      budget: 250_000,
      atStart: [moduleEntries, moduleMessages, /\/src\/design\/theme\/(?:css|layers)\.ts$/],
      firstScreen: [
        /\/src\/app\/screens\/inbox\/card\.tsx$/,
        /\/src\/app\/screens\/(?:inbox|kinds)\/messages\/en\.json$/,
        /\/src\/app\/screens\/resources\/(?:kinds|quick)\.tsx?$/,
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
    // the entry chunk, which holds everything the shell imports, React
    // included, is larger than Vite's default for one chunk before
    // compression.
    chunkSizeWarningLimit: 800,
    rolldownOptions: {
      output: {
        // What the shell loads before it renders goes in as few files as it
        // can, since small files compress poorly on their own: the entry
        // chunk holds everything the entry imports, every module's entry
        // file is in one file with what only they use, and their English
        // messages are in another. Screens still load one by one.
        codeSplitting: {
          groups: [
            { name: "index", tags: ["$initial"], priority: 2 },
            { name: "module-entries", test: moduleEntries, priority: 1 },
            { name: "module-messages", test: moduleMessages, priority: 1 },
          ],
        },
      },
    },
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
