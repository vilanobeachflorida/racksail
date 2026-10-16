import { useMatches, useNavigate } from "@tanstack/react-router";
import {
  Boxes,
  Clock,
  Contrast,
  FileSearch,
  History,
  House,
  Keyboard,
  ListChecks,
  ListTree,
  Monitor,
  Moon,
  Palette,
  Pencil,
  Play,
  Plus,
  Search,
  Settings,
  SlidersHorizontal,
  Sparkles,
  Sun,
  Trash2,
  Undo2,
} from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { useMemo, useState } from "react";
import { useIntl } from "react-intl";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from "@/design/components/ui/command";
import { DialogOverlay } from "@/design/components/ui/dialog";
import { useTheme } from "@/design/theme/provider";
import { useChangeTracker } from "./changes/tracker";
import { useGeneralPreferences } from "./general";
import { IdentityCommands } from "./identity/commands";
import { messages } from "./messages";
import { moduleCommands, navEntries, useActiveModules, useModules, usePermission } from "./modules";
import { commandMessages as cm } from "./screens/messages";
import { loadRecent } from "./screens/recent";
import {
  actionTitle,
  kindAt,
  kindNoun,
  kindPath,
  kindPlural,
  kindTitle,
  moduleName,
  resourcePath,
  resourceTitle,
  useKinds,
} from "./screens/resources/kinds";
import { useKindsFor } from "./screens/resources/operations";
import { useResource } from "./screens/resources/queries";
import { minSearch, useResourceSearch, useSettledText } from "./screens/search/api";
import { useShell } from "./shell-state";
import { ariaKeyShortcuts, ShortcutKeys } from "./shortcuts";

/** Who may see the event timeline, as internal/api checks it. */
const eventReadPermission = "core.event.read";

/** The resource the screen behind the bar shows, when it shows one through the generic screens. */
function useShownResource() {
  const matches = useMatches();
  const params = (matches.at(-1)?.params ?? {}) as {
    module?: string;
    plural?: string;
    id?: string;
  };
  const kinds = useKinds().data ?? [];
  const kind =
    params.module && params.plural ? kindAt(kinds, params.module, params.plural) : undefined;
  const resource = useResource(params.id ? kind : undefined, params.id);
  return kind && resource.data && !resource.data.deletedAt
    ? { kind, resource: resource.data }
    : null;
}

/**
 * The command bar (⌘K): go anywhere, find resources of any kind, start a
 * new one, run what the screen's resource offers and the actions modules
 * add, reach people and access and your own account, open what was opened
 * recently, undo the last change, change the appearance, and, with the
 * assistant on, ask a question or run one of its / commands from the same
 * input.
 */
export default function CommandBar() {
  const intl = useIntl();
  const shell = useShell();
  const navigate = useNavigate();
  const modules = useActiveModules();
  const allModules = useModules();
  const allowed = usePermission();
  const tracker = useChangeTracker();
  const { preferences, setPreferences } = useTheme();
  const { preferences: general } = useGeneralPreferences();
  const [query, setQuery] = useState("");
  const assistant = shell.assistant;
  const slash = query.startsWith("/");
  const listable = useKindsFor("list");
  const creatable = useKindsFor("create");
  const shown = useShownResource();
  const text = query.trim();
  const searching = !slash && text.length >= minSearch;
  // The panel is asked once typing settles, as the search screen does.
  const found = useResourceSearch(useSettledText(searching ? text : ""), 5);
  const kinds = useKinds().data ?? [];
  const recent = useMemo(
    () => (shell.commandOpen && general.rememberRecent ? loadRecent() : []),
    [shell.commandOpen, general.rememberRecent],
  );
  const close = () => {
    shell.setCommandOpen(false);
    setQuery("");
  };
  const go = (to: string) => {
    close();
    void navigate({ to });
  };
  const ask = (question: string) => {
    close();
    shell.ask(question);
  };
  const settingsPages = modules.flatMap((m) =>
    (m.settings ?? [])
      .filter((p) => allowed(p.permission))
      .map((p) => ({ key: `${m.id}.${p.id}`, title: p.title, to: `/settings/${m.id}/${p.id}` })),
  );
  const moduleTitle = (id: string) => moduleName(intl, allModules, id, messages.resourcesPanel);
  const pages = [
    { to: "/jobs", label: messages.jobsAll, icon: ListChecks, show: true },
    {
      to: "/events",
      label: messages.eventsTitle,
      icon: History,
      show: allowed(eventReadPermission),
    },
    { to: "/changes", label: messages.changesTitle, icon: Undo2, show: true },
    { to: "/resources", label: messages.resourcesTitle, icon: Boxes, show: true },
    { to: "/search", label: messages.searchTitle, icon: FileSearch, show: true },
  ].filter((p) => p.show);
  const results = searching ? (found.data?.results ?? []) : [];
  const may = (operation: string) => (shown ? allowed(`${shown.kind.name}.${operation}`) : false);
  const shownName = shown ? resourceTitle(shown.kind, shown.resource) : "";
  const shownPath = shown ? resourcePath(shown.kind, shown.resource.id) : "";
  const last = tracker.last;

  return (
    <DialogPrimitive.Root
      open={shell.commandOpen}
      onOpenChange={(open) => {
        shell.setCommandOpen(open);
        if (!open) {
          setQuery("");
        }
      }}
    >
      <DialogPrimitive.Portal>
        <DialogOverlay />
        <DialogPrimitive.Content
          className="data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed start-1/2 top-[12dvh] z-50 w-[calc(100%-2rem)] max-w-xl overflow-hidden rounded-xl border border-border bg-popover shadow-lg duration-(--rs-motion-fast) data-[state=closed]:animate-out data-[state=open]:animate-in ltr:-translate-x-1/2 rtl:translate-x-1/2"
          aria-describedby={undefined}
        >
          <DialogPrimitive.Title className="sr-only">
            {intl.formatMessage(messages.commandLabel)}
          </DialogPrimitive.Title>
          <Command
            label={intl.formatMessage(messages.commandLabel)}
            shouldFilter={!slash || !assistant}
          >
            <CommandInput
              value={query}
              onValueChange={setQuery}
              placeholder={intl.formatMessage(
                assistant ? messages.commandPlaceholder : messages.commandPlaceholderNoAssistant,
              )}
            />
            <CommandList label={intl.formatMessage(messages.commandResults)}>
              <CommandEmpty>{intl.formatMessage(messages.commandEmpty)}</CommandEmpty>
              {assistant && query.trim() !== "" ? (
                <CommandGroup heading={intl.formatMessage(messages.commandAssistant)} forceMount>
                  {slash ? (
                    (assistant.slashCommands ?? [])
                      .filter((c) => `/${c.name}`.startsWith(query.split(" ")[0] ?? ""))
                      .map((c) => (
                        <CommandItem
                          key={c.name}
                          value={`/${c.name}`}
                          onSelect={() =>
                            ask(query.startsWith(`/${c.name}`) ? query : `/${c.name} `)
                          }
                        >
                          <Sparkles aria-hidden="true" />
                          <span className="font-mono">/{c.name}</span>{" "}
                          <span className="text-muted-foreground">
                            {intl.formatMessage(c.description)}
                          </span>
                        </CommandItem>
                      ))
                  ) : (
                    <CommandItem value={`ask ${query}`} onSelect={() => ask(query)} forceMount>
                      <Sparkles aria-hidden="true" />
                      {intl.formatMessage(messages.commandAsk, { question: query })}
                    </CommandItem>
                  )}
                </CommandGroup>
              ) : null}
              {slash && assistant ? null : (
                <>
                  {results.length > 0 ? (
                    <CommandGroup heading={intl.formatMessage(cm.resources)} forceMount>
                      {results.map((r) => {
                        const kind = kinds.find((k) => k.name === r.kind);
                        return kind ? (
                          <CommandItem
                            key={r.id}
                            value={`resource ${r.id}`}
                            keywords={[r.title, r.subtitle ?? "", r.id]}
                            forceMount
                            onSelect={() => go(resourcePath(kind, r.id))}
                          >
                            <Search aria-hidden="true" />
                            <span className="truncate">{r.title}</span>{" "}
                            <span className="shrink-0 text-muted-foreground text-xs">
                              {kindTitle(intl, kind)}
                            </span>
                          </CommandItem>
                        ) : null;
                      })}
                      <CommandItem
                        value="search everything"
                        forceMount
                        onSelect={() => {
                          close();
                          void navigate({ to: "/search", search: { q: text } });
                        }}
                      >
                        <FileSearch aria-hidden="true" />
                        {intl.formatMessage(cm.allResults, { q: text })}
                      </CommandItem>
                    </CommandGroup>
                  ) : null}
                  {shown ? (
                    <CommandGroup
                      heading={intl.formatMessage(cm.thisResource, { name: shownName })}
                    >
                      {may("update") ? (
                        <CommandItem
                          value={intl.formatMessage(cm.edit, { name: shownName })}
                          onSelect={() => go(`${shownPath}/edit`)}
                        >
                          <Pencil aria-hidden="true" />
                          {intl.formatMessage(cm.edit, { name: shownName })}
                        </CommandItem>
                      ) : null}
                      {(shown.kind.actions ?? [])
                        .filter((a) => may(a.name))
                        .map((a) => (
                          <CommandItem
                            key={a.name}
                            value={`${actionTitle(intl, shown.kind, a)} ${shownName}`}
                            onSelect={() => {
                              close();
                              void navigate({ to: shownPath, search: { action: a.name } });
                            }}
                          >
                            <Play aria-hidden="true" />
                            {actionTitle(intl, shown.kind, a)}
                          </CommandItem>
                        ))}
                      {may("delete") ? (
                        <CommandItem
                          value={intl.formatMessage(cm.delete, { name: shownName })}
                          onSelect={() => {
                            close();
                            void navigate({ to: shownPath, search: { action: "delete" } });
                          }}
                        >
                          <Trash2 aria-hidden="true" />
                          {intl.formatMessage(cm.delete, { name: shownName })}
                        </CommandItem>
                      ) : null}
                    </CommandGroup>
                  ) : null}
                  {recent.length > 0 ? (
                    <CommandGroup heading={intl.formatMessage(cm.recent)}>
                      {recent.map((r) => (
                        <CommandItem
                          key={r.path}
                          value={`recent ${r.path}`}
                          keywords={[r.title, r.what]}
                          onSelect={() => go(r.path)}
                        >
                          <Clock aria-hidden="true" />
                          <span className="truncate">{r.title}</span>{" "}
                          <span className="shrink-0 text-muted-foreground text-xs">{r.what}</span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  ) : null}
                  <CommandGroup heading={intl.formatMessage(messages.commandNavigation)}>
                    <CommandItem
                      value={intl.formatMessage(messages.navHome)}
                      onSelect={() => go("/")}
                    >
                      <House aria-hidden="true" />
                      {intl.formatMessage(messages.navHome)}
                    </CommandItem>
                    {navEntries(modules, intl, allowed).map((entry) => (
                      <CommandItem
                        key={`${entry.module.id}.${entry.id}`}
                        value={`${intl.formatMessage(entry.label)} ${intl.formatMessage(entry.module.name)}`}
                        onSelect={() => go(entry.href)}
                      >
                        <entry.icon aria-hidden="true" />
                        {intl.formatMessage(entry.label)}{" "}
                        <span className="text-muted-foreground text-xs">
                          {intl.formatMessage(entry.module.name)}
                        </span>
                      </CommandItem>
                    ))}
                    {pages.map((p) => (
                      <CommandItem
                        key={p.to}
                        value={intl.formatMessage(p.label)}
                        onSelect={() => go(p.to)}
                      >
                        <p.icon aria-hidden="true" />
                        {intl.formatMessage(p.label)}
                      </CommandItem>
                    ))}
                    {listable.map((kind) => (
                      <CommandItem
                        key={kind.name}
                        value={`list ${kind.name}`}
                        keywords={[kindPlural(intl, kind), moduleTitle(kind.module)]}
                        onSelect={() => go(kindPath(kind))}
                      >
                        <ListTree aria-hidden="true" />
                        {kindPlural(intl, kind)}{" "}
                        <span className="text-muted-foreground text-xs">
                          {moduleTitle(kind.module)}
                        </span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                  <IdentityCommands close={close} />
                  {creatable.length > 0 || moduleCommands(modules, allowed).length > 0 || last ? (
                    <CommandGroup heading={intl.formatMessage(messages.commandActions)}>
                      {last ? (
                        <CommandItem
                          value={intl.formatMessage(cm.undoLast, { title: last.title })}
                          onSelect={() => {
                            close();
                            void tracker.undo(last.id);
                          }}
                        >
                          <Undo2 aria-hidden="true" />
                          {intl.formatMessage(cm.undoLast, { title: last.title })}
                        </CommandItem>
                      ) : null}
                      {creatable.map((kind) => (
                        <CommandItem
                          key={kind.name}
                          value={`new ${kind.name}`}
                          keywords={[intl.formatMessage(cm.create, { kind: kindNoun(intl, kind) })]}
                          onSelect={() => go(`${kindPath(kind)}/new`)}
                        >
                          <Plus aria-hidden="true" />
                          {intl.formatMessage(cm.create, { kind: kindNoun(intl, kind) })}
                        </CommandItem>
                      ))}
                      {moduleCommands(modules, allowed).map((c) => (
                        <CommandItem
                          key={c.key}
                          value={`${intl.formatMessage(c.label)} ${(c.keywords ?? []).join(" ")}`}
                          aria-keyshortcuts={c.shortcut ? ariaKeyShortcuts(c.shortcut) : undefined}
                          onSelect={() => {
                            close();
                            void c.run({ navigate: (to) => void navigate({ to }) });
                          }}
                        >
                          {c.icon ? <c.icon aria-hidden="true" /> : null}
                          {intl.formatMessage(c.label)}
                          {c.shortcut ? (
                            <CommandShortcut>
                              <ShortcutKeys keys={c.shortcut} />
                            </CommandShortcut>
                          ) : null}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  ) : null}
                  <CommandSeparator />
                  <CommandGroup heading={intl.formatMessage(messages.commandSettings)}>
                    <CommandItem
                      value={intl.formatMessage(messages.navSettings)}
                      onSelect={() => go("/settings")}
                    >
                      <Settings aria-hidden="true" />
                      {intl.formatMessage(messages.navSettings)}
                    </CommandItem>
                    <CommandItem
                      value={intl.formatMessage(messages.generalTitle)}
                      onSelect={() => go("/settings/general")}
                    >
                      <SlidersHorizontal aria-hidden="true" />
                      {intl.formatMessage(messages.generalTitle)}
                    </CommandItem>
                    <CommandItem
                      value={intl.formatMessage(messages.appearanceTitle)}
                      onSelect={() => go("/settings/appearance")}
                    >
                      <Palette aria-hidden="true" />
                      {intl.formatMessage(messages.appearanceTitle)}
                    </CommandItem>
                    {settingsPages.map((p) => (
                      <CommandItem
                        key={p.key}
                        value={intl.formatMessage(p.title)}
                        onSelect={() => go(p.to)}
                      >
                        <Settings aria-hidden="true" />
                        {intl.formatMessage(p.title)}
                      </CommandItem>
                    ))}
                    {preferences.mode !== "dark" ? (
                      <CommandItem
                        value={intl.formatMessage(messages.commandDark)}
                        onSelect={() => {
                          setPreferences({ mode: "dark" });
                          close();
                        }}
                      >
                        <Moon aria-hidden="true" />
                        {intl.formatMessage(messages.commandDark)}
                      </CommandItem>
                    ) : null}
                    {preferences.mode !== "light" ? (
                      <CommandItem
                        value={intl.formatMessage(messages.commandLight)}
                        onSelect={() => {
                          setPreferences({ mode: "light" });
                          close();
                        }}
                      >
                        <Sun aria-hidden="true" />
                        {intl.formatMessage(messages.commandLight)}
                      </CommandItem>
                    ) : null}
                    {preferences.mode !== "system" ? (
                      <CommandItem
                        value={intl.formatMessage(messages.commandSystem)}
                        onSelect={() => {
                          setPreferences({ mode: "system" });
                          close();
                        }}
                      >
                        <Monitor aria-hidden="true" />
                        {intl.formatMessage(messages.commandSystem)}
                      </CommandItem>
                    ) : null}
                    <CommandItem
                      value={intl.formatMessage(
                        preferences.contrast === "high"
                          ? messages.commandStandardContrast
                          : messages.commandHighContrast,
                      )}
                      onSelect={() => {
                        setPreferences({
                          contrast: preferences.contrast === "high" ? "standard" : "high",
                        });
                        close();
                      }}
                    >
                      <Contrast aria-hidden="true" />
                      {intl.formatMessage(
                        preferences.contrast === "high"
                          ? messages.commandStandardContrast
                          : messages.commandHighContrast,
                      )}
                    </CommandItem>
                    <CommandItem
                      value={intl.formatMessage(messages.jobsOpen)}
                      aria-keyshortcuts={ariaKeyShortcuts("mod+j")}
                      onSelect={() => {
                        close();
                        shell.setJobsOpen(true);
                      }}
                    >
                      <ListChecks aria-hidden="true" />
                      {intl.formatMessage(messages.jobsOpen)}
                      <CommandShortcut>
                        <ShortcutKeys keys="mod+j" />
                      </CommandShortcut>
                    </CommandItem>
                    <CommandItem
                      value={intl.formatMessage(messages.shortcutsTitle)}
                      aria-keyshortcuts={ariaKeyShortcuts("shift+?")}
                      onSelect={() => {
                        close();
                        shell.setShortcutsOpen(true);
                      }}
                    >
                      <Keyboard aria-hidden="true" />
                      {intl.formatMessage(messages.shortcutsTitle)}
                      <CommandShortcut>
                        <ShortcutKeys keys="shift+?" />
                      </CommandShortcut>
                    </CommandItem>
                  </CommandGroup>
                </>
              )}
            </CommandList>
          </Command>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
