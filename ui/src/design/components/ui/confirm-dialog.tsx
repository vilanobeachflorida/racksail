import { TriangleAlert } from "lucide-react";
import { AlertDialog as AlertDialogPrimitive } from "radix-ui";
import { type ReactNode, useId, useState } from "react";
import { useIntl } from "react-intl";
import { cn } from "@/lib/utils";
import { designMessages } from "../../messages";
import { Button } from "./button";
import { DialogOverlay } from "./dialog";
import { controlClasses } from "./input";

export interface ConfirmDialogProps {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** The element that opens the dialog, when it isn't controlled. */
  trigger?: ReactNode;
  title: ReactNode;
  /** What will happen, in plain words: what is removed and whether it can be undone. */
  description: ReactNode;
  /** The label of the action, a verb: "Delete site". */
  confirmLabel: ReactNode;
  cancelLabel?: ReactNode;
  /**
   * Makes the person type this text before the action is enabled, for
   * changes that destroy data.
   */
  typedConfirmation?: string;
  /** Destructive actions use the protected danger color. */
  destructive?: boolean;
  /** More detail shown inside the protected area, such as what the change preview found. */
  children?: ReactNode;
  busy?: boolean;
  onConfirm: () => void | Promise<void>;
}

/**
 * Asks before something that can't be taken back or that is risky. The
 * dialog's colors come from the protected palette, so no theme or custom
 * CSS can make a destructive action look harmless.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  trigger,
  title,
  description,
  confirmLabel,
  cancelLabel,
  typedConfirmation,
  destructive = true,
  children,
  busy,
  onConfirm,
}: ConfirmDialogProps) {
  const intl = useIntl();
  const [typed, setTyped] = useState("");
  const [internalOpen, setInternalOpen] = useState(false);
  const inputId = useId();
  const matches = typedConfirmation === undefined || typed === typedConfirmation;
  const setOpen = (next: boolean) => {
    if (!next) {
      setTyped("");
    }
    if (open === undefined) {
      setInternalOpen(next);
    }
    onOpenChange?.(next);
  };
  const confirm = async () => {
    // A failed action keeps the dialog open; the caller shows what went wrong.
    await onConfirm();
    setOpen(false);
  };
  return (
    <AlertDialogPrimitive.Root open={open ?? internalOpen} onOpenChange={setOpen}>
      {trigger ? (
        <AlertDialogPrimitive.Trigger asChild>{trigger}</AlertDialogPrimitive.Trigger>
      ) : null}
      <AlertDialogPrimitive.Portal>
        <DialogOverlay />
        <AlertDialogPrimitive.Content
          data-critical=""
          className="data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 critical-area fixed start-1/2 top-1/2 z-50 grid max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-md -translate-y-1/2 gap-4 overflow-y-auto rounded-lg border-2 p-card shadow-lg duration-(--rs-motion-fast) data-[state=closed]:animate-out data-[state=open]:animate-in ltr:-translate-x-1/2 rtl:translate-x-1/2"
        >
          <div className="flex items-start gap-3">
            {destructive ? (
              <TriangleAlert
                className="mt-0.5 size-5 shrink-0 text-critical-danger"
                aria-hidden="true"
              />
            ) : null}
            <div className="flex min-w-0 flex-col gap-1.5">
              <AlertDialogPrimitive.Title className="font-semibold text-lg leading-tight">
                {title}
              </AlertDialogPrimitive.Title>
              <AlertDialogPrimitive.Description className="text-sm">
                {description}
              </AlertDialogPrimitive.Description>
            </div>
          </div>
          {children}
          {typedConfirmation !== undefined ? (
            <div className="flex flex-col gap-1.5">
              <label htmlFor={inputId} className="font-medium text-sm">
                {intl.formatMessage(designMessages.typeToConfirm, {
                  phrase: (
                    <code className="rounded-xs bg-critical-text/10 px-1">{typedConfirmation}</code>
                  ),
                })}
              </label>
              <input
                id={inputId}
                className={cn(
                  controlClasses,
                  "h-control-md border-critical-border bg-critical-surface px-3 font-mono text-critical-text",
                )}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={typed !== "" && !matches ? true : undefined}
              />
            </div>
          ) : null}
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <AlertDialogPrimitive.Cancel asChild>
              <Button
                variant="outline"
                className="border-critical-border bg-critical-surface text-critical-text"
              >
                {cancelLabel ?? intl.formatMessage(designMessages.cancel)}
              </Button>
            </AlertDialogPrimitive.Cancel>
            <Button
              variant={destructive ? "critical" : "default"}
              disabled={!matches}
              busy={busy}
              onClick={() => {
                confirm().catch(() => {});
              }}
            >
              {confirmLabel}
            </Button>
          </div>
        </AlertDialogPrimitive.Content>
      </AlertDialogPrimitive.Portal>
    </AlertDialogPrimitive.Root>
  );
}
