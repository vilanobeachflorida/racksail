import { lazy, type ReactNode, Suspense } from "react";
import { useIntl } from "react-intl";
import type { ExternalToast, toast as sonnerToast } from "sonner";
import { useOptionalTheme } from "@/design/theme/provider";
import { cn } from "@/lib/utils";
import { designMessages } from "../../messages";

// Sonner loads after the shell, the first time a toast is shown or the
// toaster renders. Toasts made before it has loaded wait for it; Sonner
// shows those made before its toaster subscribed once it does.
type SonnerModule = typeof import("sonner");
let sonner: Promise<SonnerModule> | undefined;
const loadSonner = () => {
  sonner ??= import("sonner");
  return sonner;
};

const Sonner = lazy(() => loadSonner().then((module) => ({ default: module.Toaster })));

type Message = Parameters<typeof sonnerToast>[0];
type PromiseData = Parameters<typeof sonnerToast.promise<unknown>>[1];

let lastId = 0;

// Every toast gets its id here, so callers have it at once to update or
// dismiss the toast, whether or not Sonner has loaded.
function show(
  make: (toast: typeof sonnerToast, id: string | number) => void,
  id: string | number = `toast-${++lastId}`,
): string | number {
  void loadSonner().then((module) => make(module.toast, id));
  return id;
}

/** Shows a toast: a message, or one that's a success, an error, a warning or information. */
export const toast = Object.assign(
  (message: Message, data?: ExternalToast) =>
    show((t, id) => t(message, { ...data, id }), data?.id),
  {
    success: (message: Message, data?: ExternalToast) =>
      show((t, id) => t.success(message, { ...data, id }), data?.id),
    error: (message: Message, data?: ExternalToast) =>
      show((t, id) => t.error(message, { ...data, id }), data?.id),
    warning: (message: Message, data?: ExternalToast) =>
      show((t, id) => t.warning(message, { ...data, id }), data?.id),
    info: (message: Message, data?: ExternalToast) =>
      show((t, id) => t.info(message, { ...data, id }), data?.id),
    /** Shows a promise's progress and outcome in one toast. */
    promise: (promise: Promise<unknown>, data: NonNullable<PromiseData>) =>
      show((t, id) => void t.promise(promise, { ...data, id }), data.id),
    dismiss: (id?: string | number) => {
      void loadSonner().then((module) => module.toast.dismiss(id));
    },
  },
);

const toastClasses =
  "group pointer-events-auto flex w-[min(100vw-2rem,24rem)] items-start gap-3 rounded-lg border border-border bg-popover p-4 text-popover-foreground text-sm shadow-lg";

/**
 * Where toasts appear: the bottom end corner on wide screens and the
 * bottom on phones, in the theme's colors and the page's direction.
 */
export function Toaster() {
  const intl = useIntl();
  const theme = useOptionalTheme();
  return (
    <Suspense fallback={null}>
      <Sonner
        position={document.documentElement.dir === "rtl" ? "bottom-left" : "bottom-right"}
        theme={theme?.mode ?? "system"}
        dir={document.documentElement.dir === "rtl" ? "rtl" : "ltr"}
        // Sonner would add its shortcut to the label in English.
        customAriaLabel={intl.formatMessage(designMessages.notificationsLabel, {
          keys: `${intl.formatMessage(designMessages.keyName, { key: "alt" })}+T`,
        })}
        toastOptions={{
          unstyled: true,
          closeButtonAriaLabel: intl.formatMessage(designMessages.close),
          classNames: {
            toast: toastClasses,
            title: "font-medium",
            description: "text-muted-foreground",
            content: "flex min-w-0 flex-1 flex-col gap-0.5",
            icon: "mt-0.5 shrink-0 [&_svg]:size-4",
            success: "[&_[data-icon]]:text-success",
            error: "border-danger/50 [&_[data-icon]]:text-danger",
            warning: "[&_[data-icon]]:text-warning",
            info: "[&_[data-icon]]:text-info",
            actionButton: cn(
              "ms-auto inline-flex h-control-sm shrink-0 items-center rounded-md border border-input bg-card px-3 font-medium text-xs hover:bg-accent focus-visible:focus-ring",
            ),
            cancelButton:
              "inline-flex h-control-sm shrink-0 items-center rounded-md px-2 text-muted-foreground text-xs hover:bg-accent",
            closeButton: "text-muted-foreground",
          },
        }}
      />
    </Suspense>
  );
}

export interface UndoToastOptions {
  /** What was done, already translated: "Deleted example.com". */
  message: ReactNode;
  description?: ReactNode;
  /** Takes the change back; a change set's undo is a change set too. */
  undo: () => Promise<unknown>;
  /** Translated texts for the undo button and what happens after. */
  labels: { undo: string; undoing: string; undone: string; failed: (error: unknown) => string };
  /** How long the toast stays, in milliseconds; long enough to read and react. */
  duration?: number;
}

/**
 * Says something was done and offers to take it back. Pressing Undo runs
 * the undo and replaces the toast with its progress and outcome.
 */
export function toastWithUndo({
  message,
  description,
  undo,
  labels,
  duration = 10_000,
}: UndoToastOptions) {
  const id = toast(message, {
    description,
    duration,
    action: {
      label: labels.undo,
      onClick: () => {
        toast.promise(undo(), {
          id,
          loading: labels.undoing,
          success: labels.undone,
          error: (error: unknown) => labels.failed(error),
        });
      },
    },
  });
  return id;
}
