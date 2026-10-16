// Builds the web UI with the pseudo-locales next to English and serves it,
// with the API proxied to racksail dev at RACKSAIL_DEV_ADDR, for the
// pseudo-locale suite:
//
//   RACKSAIL_DEV_ADDR=127.0.0.1:7270 node pseudo/serve.mjs --port 7271
//
// The UI's sources are copied into build/pseudo/ui, where every English
// catalog gets an en-XA and an ar-XB one beside it, so ui/ stays as it is.
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { pseudoCatalog, pseudoLocales } from "./pseudo.mjs";

const { values } = parseArgs({ options: { port: { type: "string", default: "7271" } } });
const repo = path.resolve(import.meta.dirname, "../../..");
const ui = path.join(repo, "ui");
const out = path.join(repo, "build", "pseudo");
const copy = path.join(out, "ui");
const dist = path.join(out, "dist");
const vite = path.join(ui, "node_modules", "vite", "bin", "vite.js");

if (!existsSync(vite)) {
  console.error("serve.mjs: the UI's packages aren't installed; run npm ci in ui first");
  process.exit(1);
}

rmSync(out, { recursive: true, force: true });
// The UI's files that git tracks, never its packages or output, so add a
// new file before building with it. Other checkouts and worktrees can sit
// inside the repository, untracked and in hidden directories, so neither
// counts; a file deleted but not yet committed doesn't either.
const files = execFileSync("git", ["ls-files", "--cached", "-z", "--", "."], {
  cwd: ui,
  encoding: "utf8",
})
  .split("\0")
  .filter((file) => file && !file.split("/").some((part) => part.startsWith(".")))
  .filter((file) => existsSync(path.join(ui, file)));
for (const file of files) {
  mkdirSync(path.dirname(path.join(copy, file)), { recursive: true });
  cpSync(path.join(ui, file), path.join(copy, file));
}
symlinkSync(path.join(ui, "node_modules"), path.join(copy, "node_modules"), "dir");

const catalogs = files.filter((file) => /(^|\/)messages\/en\.json$/.test(file));
for (const file of catalogs) {
  const english = JSON.parse(readFileSync(path.join(ui, file), "utf8"));
  for (const locale of Object.keys(pseudoLocales)) {
    const pseudo = pseudoCatalog(english, locale);
    writeFileSync(path.join(copy, path.dirname(file), `${locale}.json`), `${JSON.stringify(pseudo, null, 2)}\n`);
  }
}
console.log(`serve.mjs: added ${Object.keys(pseudoLocales).join(" and ")} to ${catalogs.length} catalogs`);

const build = spawnSync(process.execPath, [vite, "build", "--outDir", dist, "--emptyOutDir", "--logLevel", "warn"], {
  cwd: copy,
  stdio: "inherit",
});
if (build.status !== 0) {
  process.exit(build.status ?? 1);
}

// vite preview proxies the API the way the UI's development server does.
const preview = spawn(process.execPath, [vite, "preview", "--outDir", dist, "--host", "127.0.0.1", "--port", values.port, "--strictPort"], {
  cwd: copy,
  stdio: "inherit",
});
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => preview.kill(signal));
}
preview.on("exit", (code, signal) => process.exit(code ?? (signal ? 1 : 0)));
