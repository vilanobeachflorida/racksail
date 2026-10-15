import { createContext, type ReactNode, useContext } from "react";
import type { IntlShape, MessageDescriptor } from "react-intl";
import type { HomeCard, ModuleUI, NavEntry } from "@/modules/types";

const ModulesContext = createContext<readonly ModuleUI[]>([]);

/** Makes the loaded modules available to the shell. */
export function ModulesProvider({
  modules,
  children,
}: {
  modules: readonly ModuleUI[];
  children: ReactNode;
}) {
  return <ModulesContext value={modules}>{children}</ModulesContext>;
}

/** The modules whose entries the shell loaded. */
export function useModules(): readonly ModuleUI[] {
  return useContext(ModulesContext);
}

/** The path of a module's screen, from a path relative to the module. */
export function modulePath(module: Pick<ModuleUI, "id">, to: string): string {
  const rest = to.replace(/^\/+/, "");
  return rest === "" ? `/${module.id}` : `/${module.id}/${rest}`;
}

export interface ResolvedNavEntry extends NavEntry {
  readonly module: ModuleUI;
  /** The full path the entry links to. */
  readonly href: string;
}

function byOrderThenLabel<T extends { order?: number }>(
  intl: IntlShape,
  label: (item: T) => MessageDescriptor,
) {
  return (a: T, b: T) =>
    (a.order ?? 0) - (b.order ?? 0) ||
    intl.formatMessage(label(a)).localeCompare(intl.formatMessage(label(b)), intl.locale);
}

/** The icon-rail entries of all modules, sorted by order, then label. */
export function navEntries(modules: readonly ModuleUI[], intl: IntlShape): ResolvedNavEntry[] {
  return modules
    .flatMap((module) =>
      (module.nav ?? []).map((entry) => ({ ...entry, module, href: modulePath(module, entry.to) })),
    )
    .sort(byOrderThenLabel<ResolvedNavEntry>(intl, (e) => e.label));
}

export interface ResolvedHomeCard extends HomeCard {
  readonly module: ModuleUI;
  /** Unique across modules. */
  readonly key: string;
}

/** The home cards of all modules, sorted by order, then title. */
export function homeCards(modules: readonly ModuleUI[], intl: IntlShape): ResolvedHomeCard[] {
  return modules
    .flatMap((module) =>
      (module.homeCards ?? []).map((card) => ({ ...card, module, key: `${module.id}.${card.id}` })),
    )
    .sort(byOrderThenLabel<ResolvedHomeCard>(intl, (c) => c.title));
}
