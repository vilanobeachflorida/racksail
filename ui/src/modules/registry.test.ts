import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { isModuleId, loadModules, moduleIds, moduleRegistry } from "./registry";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

/**
 * The modules' directories among files, by their paths in the repository:
 * every directory under modules/ but all, which imports them. Hidden
 * directories, such as another checkout's, aren't modules.
 */
function moduleDirectories(files: readonly string[]): string[] {
  const dirs = new Set<string>();
  for (const file of files) {
    const [top, dir, ...rest] = file.split("/");
    if (top === "modules" && dir && rest.length > 0 && dir !== "all" && !dir.startsWith(".")) {
      dirs.add(dir);
    }
  }
  return [...dirs].sort();
}

/** The files git tracks under modules/, so directories it doesn't track don't count either. */
function trackedModuleFiles(): string[] {
  return execFileSync("git", ["ls-files", "-z", "--cached", "--", "modules"], {
    cwd: repo,
    encoding: "utf8",
  })
    .split("\0")
    .filter((file) => file !== "");
}

function catalog(id: string): Record<string, string> {
  return JSON.parse(readFileSync(join(repo, "ui/src/modules", id, "messages/en.json"), "utf8"));
}

function manifestName(id: string): string {
  const manifest = readFileSync(join(repo, "modules", id, "manifest.yaml"), "utf8");
  return manifest.match(/^name: (.+)$/m)?.[1] ?? "";
}

describe("module registry", () => {
  it("lists the Go modules in the same order", () => {
    const allGo = readFileSync(join(repo, "modules/all/all.go"), "utf8");
    const goIds = [
      ...(allGo.match(/var IDs = \[\]string\{([^}]*)\}/)?.[1] ?? "").matchAll(/"([a-z]+)"/g),
    ].map((m) => m[1]);
    expect(moduleIds).toEqual(goIds);
    expect([...moduleIds].sort()).toEqual(moduleDirectories(trackedModuleFiles()));
    expect(Object.keys(moduleRegistry)).toEqual([...moduleIds]);
  });

  it("counts the directories git tracks under modules/, not hidden or stray ones", () => {
    expect(
      moduleDirectories([
        "modules/web/manifest.yaml",
        "modules/web/web.go",
        "modules/dns/manifest.yaml",
        "modules/all/all.go",
        "modules/README.md",
        "modules/.wip/manifest.yaml",
        "ui/src/modules/web/index.ts",
      ]),
    ).toEqual(["dns", "web"]);
  });

  it("knows which ids are modules", () => {
    expect(isModuleId("web")).toBe(true);
    expect(isModuleId("toString")).toBe(false);
    expect(isModuleId("all")).toBe(false);
  });

  it("loads every entry, each describing its own module", async () => {
    const modules = await loadModules();
    expect(modules.map((m) => m.id)).toEqual([...moduleIds]);
    for (const module of modules) {
      const messages = catalog(module.id);
      expect(Object.hasOwn(messages, module.name.id as string), `${module.id} name`).toBe(true);
      expect(Object.hasOwn(messages, module.description.id as string)).toBe(true);
      expect(messages[`${module.id}.name`]).toBe(manifestName(module.id));
      expect(module.icon).toBeTruthy();
    }
  });

  it("loads only the modules asked for", async () => {
    const modules = await loadModules(["dns", "web"]);
    expect(modules.map((m) => m.id)).toEqual(["dns", "web"]);
  });

  it("keeps each module's messages under its own id", () => {
    for (const id of moduleIds) {
      for (const key of Object.keys(catalog(id))) {
        expect(key.startsWith(`${id}.`) || key.startsWith(`error.${id}.`), `${key} in ${id}`).toBe(
          true,
        );
      }
    }
  });
});
