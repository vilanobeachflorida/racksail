/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The address of `racksail dev`, which serves the API the dev server proxies to.
const devServer = process.env.RACKSAIL_DEV_ADDR ?? "127.0.0.1:7070";

export default defineConfig({
  plugins: [react(), tailwindcss()],
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
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}", "scripts/**/*.test.mjs"],
    css: false,
    restoreMocks: true,
  },
});
