import { useEffect, useRef } from "react";
import { Kbd } from "@/design/components/ui/display";
import { withKeys } from "@/lib/utils";

/** Whether the platform's command key is ⌘ rather than Ctrl. */
export function isApple(): boolean {
  return (
    typeof navigator !== "undefined" &&
    /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent)
  );
}

/**
 * A keyboard shortcut: keys joined by "+", such as "mod+k" or "shift+?",
 * where mod is ⌘ on Apple devices and Ctrl elsewhere. A space separates
 * the keys of a sequence: "g h" is G, then H.
 */
export type Shortcut = string;

interface Chord {
  key: string;
  mod: boolean;
  shift: boolean;
  alt: boolean;
}

function parseChord(chord: string): Chord {
  const parts = chord.toLowerCase().split("+");
  const key = parts.pop() ?? "";
  return {
    key,
    mod: parts.includes("mod"),
    shift: parts.includes("shift"),
    alt: parts.includes("alt"),
  };
}

function matches(chord: Chord, event: KeyboardEvent): boolean {
  const mod = isApple() ? event.metaKey : event.ctrlKey;
  const key = event.key.toLowerCase();
  // Shift is implied by the character for keys like ? and isn't checked for them.
  const shiftMatters = /^[a-z0-9]$/.test(chord.key) || chord.key.length > 1;
  return (
    key === chord.key &&
    mod === chord.mod &&
    event.altKey === chord.alt &&
    (!shiftMatters || event.shiftKey === chord.shift)
  );
}

function typing(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  return (
    target.isContentEditable ||
    target.closest("input, textarea, select, [contenteditable='true'], .cm-editor, .xterm") !== null
  );
}

export interface Binding {
  keys: Shortcut;
  run: (event: KeyboardEvent) => void;
  /** Works while typing in a field; only sensible for shortcuts with mod. */
  global?: boolean;
}

/**
 * Listens for keyboard shortcuts on the whole page. Shortcuts without the
 * command key don't fire while someone types in a field.
 */
export function useShortcuts(bindings: readonly Binding[]): void {
  const latest = useRef(bindings);
  useEffect(() => {
    latest.current = bindings;
  });
  useEffect(() => {
    let pending: { first: Chord; until: number } | null = null;
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) {
        return;
      }
      const inField = typing(event.target);
      const now = Date.now();
      for (const binding of latest.current) {
        const chords = binding.keys.split(" ").map(parseChord);
        const allowed = !inField || (binding.global ?? chords[0]?.mod === true);
        if (!allowed) {
          continue;
        }
        if (chords.length === 1 && chords[0] && matches(chords[0], event)) {
          event.preventDefault();
          pending = null;
          binding.run(event);
          return;
        }
        if (chords.length === 2 && chords[0] && chords[1]) {
          if (
            pending &&
            pending.until > now &&
            pending.first.key === chords[0].key &&
            matches(chords[1], event)
          ) {
            event.preventDefault();
            pending = null;
            binding.run(event);
            return;
          }
        }
      }
      // Remember a key that starts a sequence, for a second.
      const starts = latest.current.some((b) => {
        const chords = b.keys.split(" ").map(parseChord);
        return chords.length === 2 && chords[0] && matches(chords[0], event) && !inField;
      });
      pending = starts ? { first: parseChord(event.key.toLowerCase()), until: now + 1000 } : null;
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}

/** Shows a shortcut the way the platform writes it: ⌘ K or Ctrl K. */
export function ShortcutKeys({ keys }: { keys: Shortcut }) {
  const apple = isApple();
  const label = (k: string) => {
    switch (k) {
      case "mod":
        return apple ? "⌘" : "Ctrl";
      case "shift":
        return apple ? "⇧" : "Shift";
      case "alt":
        return apple ? "⌥" : "Alt";
      default:
        return k.length === 1 ? k.toUpperCase() : k;
    }
  };
  return (
    <span className="inline-flex items-center gap-1">
      {withKeys(keys.split(" "), (chord) => chord).map(([key, chord]) => (
        <span key={key} className="inline-flex items-center gap-0.5">
          {chord.split("+").map((k) => (
            <Kbd key={k}>{label(k)}</Kbd>
          ))}
        </span>
      ))}
    </span>
  );
}
