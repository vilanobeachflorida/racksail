import { createContext, type ReactNode, useCallback, useContext, useMemo, useState } from "react";
import { safeStorage } from "@/design/theme/prefs";
import { useMediaQuery } from "@/lib/media";
import type { AssistantContribution } from "@/modules/types";
import { isOn, useModuleStates } from "./modules";

/** The dock's width as a share of the window, which people can drag. */
export const dockLimits = { min: 0.2, max: 0.5, initial: 1 / 3 };
const dockKey = "racksail.shell.dock";

/** Screens at least this wide show the assistant beside the content; narrower ones in a sheet. */
export const dockQuery = "(min-width: 64rem)";

interface DockState {
  open: boolean;
  width: number;
}

function loadDock(): DockState {
  try {
    const raw = safeStorage()?.getItem(dockKey);
    const parsed = raw ? (JSON.parse(raw) as Partial<DockState>) : {};
    const width = typeof parsed.width === "number" ? parsed.width : dockLimits.initial;
    return {
      open: parsed.open !== false,
      width: Math.min(dockLimits.max, Math.max(dockLimits.min, width)),
    };
  } catch {
    return { open: true, width: dockLimits.initial };
  }
}

export interface ShellState {
  commandOpen: boolean;
  setCommandOpen: (open: boolean) => void;
  jobsOpen: boolean;
  setJobsOpen: (open: boolean) => void;
  shortcutsOpen: boolean;
  setShortcutsOpen: (open: boolean) => void;
  navOpen: boolean;
  setNavOpen: (open: boolean) => void;
  /** The assistant module's contribution, while the module is on. */
  assistant: AssistantContribution | null;
  /** Whether the assistant shows beside the content rather than in a sheet. */
  docked: boolean;
  /** Whether the assistant is showing, in the dock or in the sheet. */
  assistantOpen: boolean;
  setAssistantOpen: (open: boolean) => void;
  /** The dock's width as a share of the window. */
  dockWidth: number;
  setDockWidth: (share: number) => void;
  /** A question from the command bar for the assistant, and a counter that changes with each ask. */
  question: { text: string; id: number } | null;
  ask: (text: string) => void;
}

const ShellContext = createContext<ShellState | null>(null);

/** The state of the shell's drawers, dialogs and assistant dock. */
export function ShellStateProvider({
  assistant,
  children,
}: {
  assistant: AssistantContribution | null;
  children: ReactNode;
}) {
  const states = useModuleStates();
  const available = assistant && isOn(states, "ai") ? assistant : null;
  const docked = useMediaQuery(dockQuery);
  const [commandOpen, setCommandOpen] = useState(false);
  const [jobsOpen, setJobsOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const [dock, setDock] = useState<DockState>(loadDock);
  // The sheet covers most of a phone's screen, so it opens only when asked
  // for and isn't remembered; the dock stays as it was left.
  const [sheetOpen, setSheetOpen] = useState(false);
  const [question, setQuestion] = useState<{ text: string; id: number } | null>(null);

  const saveDock = useCallback((next: DockState) => {
    setDock(next);
    try {
      safeStorage()?.setItem(dockKey, JSON.stringify(next));
    } catch {
      // The dock keeps its size for this visit.
    }
  }, []);

  const value = useMemo<ShellState>(() => {
    const setAssistantOpen = (open: boolean) => {
      if (docked) {
        saveDock({ ...dock, open });
      } else {
        setSheetOpen(open);
      }
    };
    return {
      commandOpen,
      setCommandOpen,
      jobsOpen,
      setJobsOpen,
      shortcutsOpen,
      setShortcutsOpen,
      navOpen,
      setNavOpen,
      assistant: available,
      docked,
      assistantOpen: available !== null && (docked ? dock.open : sheetOpen),
      setAssistantOpen,
      dockWidth: dock.width,
      setDockWidth: (share) =>
        saveDock({ ...dock, width: Math.min(dockLimits.max, Math.max(dockLimits.min, share)) }),
      question,
      ask: (text) => {
        setQuestion((q) => ({ text, id: (q?.id ?? 0) + 1 }));
        setAssistantOpen(true);
      },
    };
  }, [
    commandOpen,
    jobsOpen,
    shortcutsOpen,
    navOpen,
    available,
    docked,
    dock,
    sheetOpen,
    saveDock,
    question,
  ]);
  return <ShellContext value={value}>{children}</ShellContext>;
}

export function useShell(): ShellState {
  const value = useContext(ShellContext);
  if (!value) {
    throw new Error("useShell needs a ShellStateProvider above it");
  }
  return value;
}
