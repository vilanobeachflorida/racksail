import type { ModuleUI } from "./types";

/**
 * Every bundled module, in the order of modules/all in Go, which is the order
 * the panel lists them in. Both lists name all modules from the start, so
 * work on one module never touches another module's files.
 */
export const moduleIds = [
  "network",
  "web",
  "hosting",
  "databases",
  "dns",
  "mail",
  "containers",
  "vms",
  "storage",
  "shares",
  "home",
  "marketplace",
  "backups",
  "monitoring",
  "files",
  "performance",
  "builder",
  "ai",
  "automations",
  "migration",
  "cluster",
  "security",
] as const;

export type ModuleId = (typeof moduleIds)[number];

export type ModuleLoader = () => Promise<{ default: ModuleUI }>;

/** Lazy imports of each module's entry file. */
export const moduleRegistry: Readonly<Record<ModuleId, ModuleLoader>> = {
  network: () => import("./network/index"),
  web: () => import("./web/index"),
  hosting: () => import("./hosting/index"),
  databases: () => import("./databases/index"),
  dns: () => import("./dns/index"),
  mail: () => import("./mail/index"),
  containers: () => import("./containers/index"),
  vms: () => import("./vms/index"),
  storage: () => import("./storage/index"),
  shares: () => import("./shares/index"),
  home: () => import("./home/index"),
  marketplace: () => import("./marketplace/index"),
  backups: () => import("./backups/index"),
  monitoring: () => import("./monitoring/index"),
  files: () => import("./files/index"),
  performance: () => import("./performance/index"),
  builder: () => import("./builder/index"),
  ai: () => import("./ai/index"),
  automations: () => import("./automations/index"),
  migration: () => import("./migration/index"),
  cluster: () => import("./cluster/index"),
  security: () => import("./security/index"),
};

/** Whether a string is the id of a bundled module. */
export function isModuleId(id: string): id is ModuleId {
  return Object.hasOwn(moduleRegistry, id);
}

/** Loads the entry files of the given modules, in the given order. */
export async function loadModules(ids: readonly ModuleId[] = moduleIds): Promise<ModuleUI[]> {
  return Promise.all(
    ids.map(async (id) => {
      const { default: module } = await moduleRegistry[id]();
      if (module.id !== id) {
        throw new Error(`ui/src/modules/${id}/index.ts declares the module "${module.id}"`);
      }
      return module;
    }),
  );
}
