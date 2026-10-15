import type { LucideIcon } from "lucide-react";
import type { ComponentType } from "react";
import type { MessageDescriptor } from "react-intl";
import type { ModuleId } from "./registry";

/** A component loaded the first time it's shown. */
export type LazyComponent<Props = object> = () => Promise<{ default: ComponentType<Props> }>;

/** An entry in the icon rail. */
export interface NavEntry {
  /** Unique within the module. */
  readonly id: string;
  readonly label: MessageDescriptor;
  readonly icon: LucideIcon;
  /** A path under the module's base path, such as "/" or "/sites". */
  readonly to: string;
  /** Entries are sorted by order, then by label. */
  readonly order?: number;
  /** A permission the user needs to see the entry. */
  readonly permission?: string;
}

/** A screen of the module, mounted under /<module id>. */
export interface ModuleRoute {
  /**
   * The path relative to the module's base path, in TanStack Router syntax:
   * "/" for the module's first screen, "/sites/$siteId" for a screen with a
   * parameter.
   */
  readonly path: string;
  /** The page title, shown in the browser tab. */
  readonly title: MessageDescriptor;
  readonly screen: LazyComponent;
}

/** A card the home screen can show. */
export interface HomeCard {
  /** Unique within the module. */
  readonly id: string;
  readonly title: MessageDescriptor;
  readonly card: LazyComponent;
  /** How many columns of the home grid the card spans. */
  readonly size?: "small" | "medium" | "large";
  readonly order?: number;
  readonly permission?: string;
}

/** A page under Settings, mounted at /settings/<module id>/<page id>. */
export interface SettingsPage {
  /** Unique within the module; part of the page's path. */
  readonly id: string;
  readonly title: MessageDescriptor;
  readonly description?: MessageDescriptor;
  readonly page: LazyComponent;
  readonly permission?: string;
}

/** What a command-bar action can do when it runs. */
export interface CommandContext {
  /** Goes to a path in the panel. */
  readonly navigate: (to: string) => void;
}

/** An action in the command bar. */
export interface CommandAction {
  /** Unique within the module. */
  readonly id: string;
  readonly label: MessageDescriptor;
  readonly icon?: LucideIcon;
  /** More words the command bar matches, in English; translations add their own. */
  readonly keywords?: readonly string[];
  /** A keyboard shortcut, such as "mod+shift+s". */
  readonly shortcut?: string;
  readonly permission?: string;
  readonly run: (context: CommandContext) => void | Promise<void>;
}

/**
 * Everything a module adds to the panel. The shell reads this from each
 * module's entry file, ui/src/modules/<id>/index.ts; screens, cards and
 * pages load the first time they're shown.
 */
export interface ModuleUI {
  /** The module's id, as in its manifest. */
  readonly id: ModuleId;
  readonly name: MessageDescriptor;
  readonly description: MessageDescriptor;
  readonly icon: LucideIcon;
  readonly nav?: readonly NavEntry[];
  readonly routes?: readonly ModuleRoute[];
  readonly homeCards?: readonly HomeCard[];
  readonly settings?: readonly SettingsPage[];
  readonly commands?: readonly CommandAction[];
}

/** Declares a module's UI. It only exists for type checking. */
export function defineModule(module: ModuleUI): ModuleUI {
  return module;
}
