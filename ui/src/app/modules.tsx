import { createContext, type ReactNode, useContext, useMemo } from "react";
import type { IntlShape, MessageDescriptor } from "react-intl";
import type { ModuleId } from "@/modules/registry";
import type { CommandAction, HomeCard, ModuleUI, NavEntry } from "@/modules/types";

/** Where a module is in its life, as the module manager reports it. */
export type ModuleState = "off" | "on" | "active" | "stopping" | "removing";

interface ModulesValue {
  modules: readonly ModuleUI[];
  /** Module states, or null while the panel doesn't report them. */
  states: Partial<Record<ModuleId, ModuleState>> | null;
  can: (permission: string) => boolean;
}

const allowAll = () => true;

const ModulesContext = createContext<ModulesValue>({ modules: [], states: null, can: allowAll });

export interface ModulesProviderProps {
  modules: readonly ModuleUI[];
  /**
   * Which modules are on. Without it every module counts as on, as in
   * development before the panel reports states.
   */
  states?: Partial<Record<ModuleId, ModuleState>> | null;
  /** Whether the signed-in person has a permission; everything is allowed without it. */
  can?: (permission: string) => boolean;
  children: ReactNode;
}

/** Makes the loaded modules, their states and the person's permissions available to the shell. */
export function ModulesProvider({
  modules,
  states = null,
  can = allowAll,
  children,
}: ModulesProviderProps) {
  const value = useMemo(() => ({ modules, states, can }), [modules, states, can]);
  return <ModulesContext value={value}>{children}</ModulesContext>;
}

/** Whether a module is visible: on, or active because something uses it. */
export function isOn(states: ModulesValue["states"], id: ModuleId): boolean {
  if (states === null) {
    return true;
  }
  const state = states[id];
  return state === "on" || state === "active";
}

/** Module states, or null while the panel doesn't report them. */
export function useModuleStates(): ModulesValue["states"] {
  return useContext(ModulesContext).states;
}

/** The modules whose entries the shell loaded, whatever their state. */
export function useModules(): readonly ModuleUI[] {
  return useContext(ModulesContext).modules;
}

/** The modules that are on, which are the ones the shell shows. */
export function useActiveModules(): readonly ModuleUI[] {
  const { modules, states } = useContext(ModulesContext);
  return useMemo(() => modules.filter((m) => isOn(states, m.id)), [modules, states]);
}

/** Checks the signed-in person's permissions; entries without a permission are always allowed. */
export function usePermission(): (permission: string | undefined) => boolean {
  const { can } = useContext(ModulesContext);
  return (permission) => permission === undefined || can(permission);
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

type Allowed = (permission: string | undefined) => boolean;

/** The icon-rail entries of all modules, sorted by order, then label. */
export function navEntries(
  modules: readonly ModuleUI[],
  intl: IntlShape,
  allowed: Allowed = () => true,
): ResolvedNavEntry[] {
  return modules
    .flatMap((module) =>
      (module.nav ?? [])
        .filter((entry) => allowed(entry.permission))
        .map((entry) => ({ ...entry, module, href: modulePath(module, entry.to) })),
    )
    .sort(byOrderThenLabel<ResolvedNavEntry>(intl, (e) => e.label));
}

export interface ResolvedHomeCard extends HomeCard {
  readonly module: ModuleUI;
  /** Unique across modules. */
  readonly key: string;
}

/** The home cards of all modules, sorted by order, then title. */
export function homeCards(
  modules: readonly ModuleUI[],
  intl: IntlShape,
  allowed: Allowed = () => true,
): ResolvedHomeCard[] {
  return modules
    .flatMap((module) =>
      (module.homeCards ?? [])
        .filter((card) => allowed(card.permission))
        .map((card) => ({ ...card, module, key: `${module.id}.${card.id}` })),
    )
    .sort(byOrderThenLabel<ResolvedHomeCard>(intl, (c) => c.title));
}

export interface ResolvedCommand extends CommandAction {
  readonly module: ModuleUI;
  readonly key: string;
}

/** The command-bar actions of all modules the person may run. */
export function moduleCommands(
  modules: readonly ModuleUI[],
  allowed: Allowed = () => true,
): ResolvedCommand[] {
  return modules.flatMap((module) =>
    (module.commands ?? [])
      .filter((c) => allowed(c.permission))
      .map((c) => ({ ...c, module, key: `${module.id}.${c.id}` })),
  );
}
