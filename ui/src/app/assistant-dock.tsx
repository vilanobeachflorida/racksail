import { type KeyboardEvent, lazy, type PointerEvent, Suspense, useMemo, useRef } from "react";
import { useIntl } from "react-intl";
import { Sheet, SheetContent, SheetTitle } from "@/design/components/ui/dialog";
import { Skeleton } from "@/design/components/ui/skeleton";
import { useMediaQuery } from "@/lib/media";
import type { AssistantContribution } from "@/modules/types";
import { messages } from "./messages";
import { dockLimits, useShell } from "./shell-state";

// One lazy component per panel loader, so re-rendering never reloads it.
const panels = new WeakMap<AssistantContribution["panel"], ReturnType<typeof lazy>>();

function usePanel(assistant: AssistantContribution) {
  return useMemo(() => {
    let panel = panels.get(assistant.panel);
    if (!panel) {
      panel = lazy(assistant.panel);
      panels.set(assistant.panel, panel);
    }
    return panel;
  }, [assistant]);
}

function PanelBody({
  assistant,
  onClose,
}: {
  assistant: AssistantContribution;
  onClose: () => void;
}) {
  const intl = useIntl();
  const shell = useShell();
  const Panel = usePanel(assistant);
  return (
    <Suspense
      fallback={
        <div
          role="status"
          aria-label={intl.formatMessage(messages.assistantLoading)}
          className="flex flex-col gap-3 p-4"
        >
          <Skeleton className="h-6 w-1/2" />
          <Skeleton className="h-24 w-full" />
        </div>
      }
    >
      <Panel
        onClose={onClose}
        {...(shell.question
          ? { question: shell.question.text, questionId: shell.question.id }
          : {})}
      />
    </Suspense>
  );
}

/**
 * The assistant dock: the right third of the screen, resizable and
 * collapsible, and a bottom sheet on phones. It isn't there at all while
 * the assistant module is off.
 */
export function AssistantDock() {
  const intl = useIntl();
  const shell = useShell();
  const wide = useMediaQuery("(min-width: 64rem)");
  const dragging = useRef<{ startX: number; startShare: number } | null>(null);
  const assistant = shell.assistant;
  if (!assistant) {
    return null;
  }
  const close = () => shell.setDockOpen(false);

  if (!wide) {
    return (
      <Sheet open={shell.dock.open} onOpenChange={shell.setDockOpen}>
        <SheetContent side="bottom" className="h-[85dvh] p-0" aria-describedby={undefined}>
          <SheetTitle className="sr-only">{intl.formatMessage(messages.assistantLabel)}</SheetTitle>
          <div id="assistant" className="min-h-0 flex-1 overflow-hidden">
            {shell.dock.open ? <PanelBody assistant={assistant} onClose={close} /> : null}
          </div>
        </SheetContent>
      </Sheet>
    );
  }
  if (!shell.dock.open) {
    return null;
  }

  const rtl = document.documentElement.dir === "rtl";
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    dragging.current = { startX: e.clientX, startShare: shell.dock.width };
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = dragging.current;
    if (!d) {
      return;
    }
    // Dragging toward the content makes the dock wider.
    const delta = (d.startX - e.clientX) * (rtl ? -1 : 1);
    shell.setDockWidth(d.startShare + delta / window.innerWidth);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 0.05 : 0.01;
    const toward = rtl ? "ArrowRight" : "ArrowLeft";
    const away = rtl ? "ArrowLeft" : "ArrowRight";
    if (e.key === toward) {
      shell.setDockWidth(shell.dock.width + step);
    } else if (e.key === away) {
      shell.setDockWidth(shell.dock.width - step);
    } else if (e.key === "Home") {
      shell.setDockWidth(dockLimits.min);
    } else if (e.key === "End") {
      shell.setDockWidth(dockLimits.max);
    } else {
      return;
    }
    e.preventDefault();
  };

  return (
    <aside
      id="assistant"
      tabIndex={-1}
      aria-label={intl.formatMessage(messages.assistantLabel)}
      className="relative flex min-h-0 flex-col border-border border-s bg-card outline-none"
      style={{ width: `${(shell.dock.width * 100).toFixed(2)}vw` }}
    >
      {/* biome-ignore lint/a11y/useSemanticElements: a focusable separator is the ARIA pattern for a resizable pane */}
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label={intl.formatMessage(messages.assistantResize)}
        aria-valuemin={Math.round(dockLimits.min * 100)}
        aria-valuemax={Math.round(dockLimits.max * 100)}
        aria-valuenow={Math.round(shell.dock.width * 100)}
        tabIndex={0}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={() => {
          dragging.current = null;
        }}
        onKeyDown={onKeyDown}
        className="absolute inset-y-0 -start-1 z-10 w-2 cursor-col-resize hover:bg-primary/30 focus-visible:bg-primary/30 focus-visible:outline-none"
      />
      <PanelBody assistant={assistant} onClose={close} />
    </aside>
  );
}
