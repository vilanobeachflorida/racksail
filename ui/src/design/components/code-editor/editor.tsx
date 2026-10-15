import {
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
} from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import {
  bracketMatching,
  foldGutter,
  foldKeymap,
  HighlightStyle,
  indentOnInput,
  syntaxHighlighting,
} from "@codemirror/language";
import { type Diagnostic, linter, lintGutter, lintKeymap } from "@codemirror/lint";
import { highlightSelectionMatches, search, searchKeymap } from "@codemirror/search";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  placeholder as placeholderExtension,
} from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { useEffect, useId, useRef } from "react";
import { useIntl } from "react-intl";
import { cn } from "@/lib/utils";
import { designMessages as m } from "../../messages";
import { type Language, languages } from "./languages";

/**
 * Colors come from theme tokens through CSS custom properties, so the
 * editor follows the theme and the mode without being rebuilt. Syntax
 * colors use tokens that reach 4.5:1 on raised surfaces, because code is
 * text.
 */
const editorTheme = EditorView.theme({
  "&": {
    color: "var(--rs-text)",
    backgroundColor: "var(--rs-surface-raised)",
    fontSize: "var(--rs-text-sm)",
    border: "1px solid var(--rs-border-strong)",
    borderRadius: "var(--rs-radius-md)",
    overflow: "hidden",
  },
  "&.cm-focused": {
    outline: "var(--rs-focus-width) solid var(--rs-focus)",
    outlineOffset: "var(--rs-focus-offset)",
  },
  ".cm-content": {
    fontFamily: "var(--rs-font-mono)",
    caretColor: "var(--rs-text)",
    padding: "0.5rem 0",
  },
  ".cm-scroller": { fontFamily: "var(--rs-font-mono)", lineHeight: "1.6" },
  ".cm-cursor, .cm-dropCursor": { borderInlineStartColor: "var(--rs-text)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "var(--rs-accent-subtle)",
  },
  ".cm-activeLine": {
    backgroundColor: "color-mix(in srgb, var(--rs-surface-sunken) 60%, transparent)",
  },
  ".cm-gutters": {
    backgroundColor: "var(--rs-surface-sunken)",
    color: "var(--rs-text-muted)",
    borderInlineEnd: "1px solid var(--rs-border)",
  },
  ".cm-activeLineGutter": { backgroundColor: "var(--rs-surface-sunken)", color: "var(--rs-text)" },
  ".cm-foldPlaceholder": {
    backgroundColor: "var(--rs-surface-sunken)",
    border: "1px solid var(--rs-border)",
    color: "var(--rs-text-muted)",
  },
  ".cm-tooltip": {
    backgroundColor: "var(--rs-surface-raised)",
    color: "var(--rs-text)",
    border: "1px solid var(--rs-border)",
    borderRadius: "var(--rs-radius-sm)",
    boxShadow: "var(--rs-shadow-md)",
  },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
    backgroundColor: "var(--rs-accent-subtle)",
    color: "var(--rs-text)",
  },
  ".cm-panels": { backgroundColor: "var(--rs-surface-sunken)", color: "var(--rs-text)" },
  ".cm-panels.cm-panels-bottom": { borderTop: "1px solid var(--rs-border)" },
  ".cm-searchMatch": {
    backgroundColor: "var(--rs-warning-subtle)",
    outline: "1px solid var(--rs-warning)",
  },
  ".cm-searchMatch.cm-searchMatch-selected": { backgroundColor: "var(--rs-accent-subtle)" },
  ".cm-selectionMatch": { backgroundColor: "var(--rs-info-subtle)" },
  ".cm-matchingBracket, .cm-nonmatchingBracket": { outline: "1px solid var(--rs-border-strong)" },
  ".cm-diagnostic-error": { borderInlineStart: "3px solid var(--rs-danger)" },
  ".cm-diagnostic-warning": { borderInlineStart: "3px solid var(--rs-warning)" },
  ".cm-diagnostic-info": { borderInlineStart: "3px solid var(--rs-info)" },
  ".cm-lintRange-error": { textDecoration: "underline wavy var(--rs-danger)" },
  ".cm-lintRange-warning": { textDecoration: "underline wavy var(--rs-warning)" },
  ".cm-placeholder": { color: "var(--rs-text-muted)" },
});

const highlight = HighlightStyle.define([
  {
    tag: [tags.keyword, tags.operatorKeyword, tags.modifier, tags.controlKeyword],
    color: "var(--rs-accent)",
  },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: "var(--rs-success)" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "var(--rs-warning)" },
  {
    tag: [tags.comment, tags.meta, tags.documentMeta],
    color: "var(--rs-text-muted)",
    fontStyle: "italic",
  },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "var(--rs-info)" },
  {
    tag: [tags.function(tags.variableName), tags.function(tags.propertyName)],
    color: "var(--rs-info)",
  },
  { tag: [tags.propertyName, tags.attributeName], color: "var(--rs-text)", fontWeight: "500" },
  { tag: [tags.tagName, tags.heading], color: "var(--rs-accent)", fontWeight: "600" },
  { tag: tags.link, color: "var(--rs-accent)", textDecoration: "underline" },
  { tag: tags.invalid, color: "var(--rs-danger)" },
  { tag: tags.emphasis, fontStyle: "italic" },
  { tag: tags.strong, fontWeight: "700" },
  { tag: tags.deleted, color: "var(--rs-danger)" },
  { tag: tags.inserted, color: "var(--rs-success)" },
]);

export interface CodeEditorProps {
  value: string;
  onChange?: (value: string) => void;
  language?: Language | null;
  readOnly?: boolean;
  /** Names the editor for screen readers, such as the file name. */
  label: string;
  /** Problems to underline and list, from a linter or the server. */
  diagnostics?: readonly Diagnostic[];
  placeholder?: string;
  /** The editor's height; it scrolls inside. */
  height?: string;
  className?: string;
  /** Called when the person presses Mod-S, to save without leaving the keyboard. */
  onSave?: (value: string) => void;
}

/**
 * The code editor: CodeMirror 6 with line numbers, folding, search,
 * brackets, autocompletion and diagnostics, in the theme's colors. Tab
 * indents; press Escape first to move focus out with Tab.
 */
export default function CodeEditor({
  value,
  onChange,
  language,
  readOnly = false,
  label,
  diagnostics,
  placeholder,
  height = "20rem",
  className,
  onSave,
}: CodeEditorProps) {
  const intl = useIntl();
  const hintId = useId();
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const languageSlot = useRef(new Compartment());
  const readOnlySlot = useRef(new Compartment());
  const lintSlot = useRef(new Compartment());
  const callbacks = useRef({ onChange, onSave });
  useEffect(() => {
    callbacks.current = { onChange, onSave };
  });

  // The editor is created once; later changes go in as transactions, so
  // the cursor, history and scroll position survive them.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the editor keeps its own state after creation
  useEffect(() => {
    const parent = host.current;
    if (!parent) {
      return;
    }
    const extensions: Extension[] = [
      lineNumbers(),
      highlightActiveLineGutter(),
      highlightSpecialChars(),
      history(),
      foldGutter(),
      drawSelection(),
      indentOnInput(),
      syntaxHighlighting(highlight),
      bracketMatching(),
      closeBrackets(),
      autocompletion(),
      highlightActiveLine(),
      highlightSelectionMatches(),
      search({ top: false }),
      lintGutter(),
      keymap.of([
        {
          key: "Mod-s",
          preventDefault: true,
          run: (v) => {
            callbacks.current.onSave?.(v.state.doc.toString());
            return callbacks.current.onSave !== undefined;
          },
        },
        ...closeBracketsKeymap,
        ...defaultKeymap,
        ...searchKeymap,
        ...historyKeymap,
        ...foldKeymap,
        ...completionKeymap,
        ...lintKeymap,
        indentWithTab,
      ]),
      editorTheme,
      EditorView.contentAttributes.of({ "aria-label": label, "aria-describedby": hintId }),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) {
          callbacks.current.onChange?.(update.state.doc.toString());
        }
      }),
      languageSlot.current.of([]),
      readOnlySlot.current.of([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
      ]),
      lintSlot.current.of([]),
      placeholder ? placeholderExtension(placeholder) : [],
    ];
    const created = new EditorView({
      parent,
      state: EditorState.create({ doc: value, extensions }),
    });
    view.current = created;
    return () => {
      created.destroy();
      view.current = null;
    };
  }, []);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  useEffect(() => {
    view.current?.dispatch({
      effects: readOnlySlot.current.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
      ]),
    });
  }, [readOnly]);

  useEffect(() => {
    let cancelled = false;
    const load = language ? languages[language] : null;
    if (!load) {
      view.current?.dispatch({ effects: languageSlot.current.reconfigure([]) });
      return;
    }
    load().then((extension) => {
      if (!cancelled) {
        view.current?.dispatch({ effects: languageSlot.current.reconfigure(extension) });
      }
    });
    return () => {
      cancelled = true;
    };
  }, [language]);

  useEffect(() => {
    const list = diagnostics ?? [];
    view.current?.dispatch({
      effects: lintSlot.current.reconfigure(
        list.length > 0 ? linter(() => [...list], { delay: 0 }) : [],
      ),
    });
  }, [diagnostics]);

  return (
    <>
      <p id={hintId} className="sr-only">
        {intl.formatMessage(m.codeLeave)}
      </p>
      <div
        ref={host}
        data-slot="code-editor"
        className={cn("min-w-0 [&_.cm-editor]:h-full", className)}
        style={{ height }}
      />
    </>
  );
}
