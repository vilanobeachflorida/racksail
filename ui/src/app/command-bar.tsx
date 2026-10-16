import { useNavigate } from "@tanstack/react-router";
import {
  Contrast,
  House,
  Keyboard,
  ListChecks,
  Monitor,
  Moon,
  Palette,
  Settings,
  Sparkles,
  Sun,
} from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { useState } from "react";
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
import { IdentityCommands } from "./identity/commands";
import { messages } from "./messages";
import { moduleCommands, navEntries, useActiveModules, usePermission } from "./modules";
import { useShell } from "./shell-state";
import { ariaKeyShortcuts, ShortcutKeys } from "./shortcuts";

/**
 * The command bar (⌘K): go anywhere, run any action modules offer, reach
 * people and access and your own account, change the appearance, and, with
 * the assistant on, ask a question or run one of its / commands from the
 * same input.
 */
export default function CommandBar() {
  const intl = useIntl();
  const shell = useShell();
  const navigate = useNavigate();
  const modules = useActiveModules();
  const allowed = usePermission();
  const { preferences, setPreferences } = useTheme();
  const [query, setQuery] = useState("");
  const assistant = shell.assistant;
  const slash = query.startsWith("/");
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
                  </CommandGroup>
                  <IdentityCommands close={close} />
                  {moduleCommands(modules, allowed).length > 0 ? (
                    <CommandGroup heading={intl.formatMessage(messages.commandActions)}>
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
