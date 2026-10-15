import { useIntl } from "react-intl";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { messages } from "./messages";
import { moduleCommands, useActiveModules, usePermission } from "./modules";
import { useShell } from "./shell-state";
import { ShortcutKeys } from "./shortcuts";

/** The shell's own shortcuts, shown in the help and bound in the shell. */
export const shellShortcuts = [
  { keys: "mod+k", label: messages.shortcutCommandBar },
  { keys: "mod+j", label: messages.shortcutJobs },
  { keys: "mod+.", label: messages.shortcutAssistant },
  { keys: "shift+?", label: messages.shortcutHelp },
  { keys: "g h", label: messages.shortcutHome },
  { keys: "g s", label: messages.shortcutSettings },
  { keys: "escape", label: messages.shortcutEscape },
] as const;

/** Lists every keyboard shortcut: the shell's and those modules give their commands. */
export function ShortcutsHelp() {
  const intl = useIntl();
  const shell = useShell();
  const modules = useActiveModules();
  const allowed = usePermission();
  const fromModules = moduleCommands(modules, allowed).filter((c) => c.shortcut);
  return (
    <Dialog open={shell.shortcutsOpen} onOpenChange={shell.setShortcutsOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{intl.formatMessage(messages.shortcutsTitle)}</DialogTitle>
          <DialogDescription>{intl.formatMessage(messages.shortcutsDescription)}</DialogDescription>
        </DialogHeader>
        <dl className="grid grid-cols-[1fr_auto] gap-x-6 gap-y-2 text-sm">
          {shellShortcuts
            .filter((s) => s.keys !== "mod+." || shell.assistant)
            .map((s) => (
              <div key={s.keys} className="contents">
                <dt>{intl.formatMessage(s.label)}</dt>
                <dd>
                  <ShortcutKeys keys={s.keys} />
                </dd>
              </div>
            ))}
          {fromModules.map((c) => (
            <div key={c.key} className="contents">
              <dt>{intl.formatMessage(c.label)}</dt>
              <dd>
                <ShortcutKeys keys={c.shortcut ?? ""} />
              </dd>
            </div>
          ))}
        </dl>
      </DialogContent>
    </Dialog>
  );
}
