import { cva, type VariantProps } from "class-variance-authority";
import { X } from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import type { ComponentProps } from "react";
import { useIntl } from "react-intl";
import { cn } from "@/lib/utils";
import { designMessages } from "../../messages";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;
export const DialogPortal = DialogPrimitive.Portal;

/** The dimmed backdrop behind dialogs and sheets. */
export const overlayClasses =
  "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed inset-0 z-50 bg-overlay data-[state=closed]:animate-out data-[state=open]:animate-in";

export function DialogOverlay({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      data-slot="dialog-overlay"
      className={cn(overlayClasses, className)}
      {...props}
    />
  );
}

function CloseButton({ className }: { className?: string }) {
  const intl = useIntl();
  return (
    <DialogPrimitive.Close
      className={cn(
        "absolute end-3 top-3 inline-flex size-control-sm items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:focus-ring",
        className,
      )}
      aria-label={intl.formatMessage(designMessages.close)}
    >
      <X className="size-4" aria-hidden="true" />
    </DialogPrimitive.Close>
  );
}

export interface DialogContentProps extends ComponentProps<typeof DialogPrimitive.Content> {
  /** Leaves out the close button, for dialogs that must be answered. */
  hideClose?: boolean;
}

/** A dialog in the middle of the screen; on phones it fills the width. */
export function DialogContent({ className, children, hideClose, ...props }: DialogContentProps) {
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        data-slot="dialog-content"
        className={cn(
          "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 fixed start-1/2 top-1/2 z-50 grid max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-lg -translate-y-1/2 gap-4 overflow-y-auto rounded-lg border border-border bg-popover p-card text-popover-foreground shadow-lg duration-(--rs-motion-fast) data-[state=closed]:animate-out data-[state=open]:animate-in ltr:-translate-x-1/2 rtl:translate-x-1/2",
          className,
        )}
        {...props}
      >
        {children}
        {hideClose ? null : <CloseButton />}
      </DialogPrimitive.Content>
    </DialogPortal>
  );
}

export function DialogHeader({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("flex flex-col gap-1.5 pe-8 text-start", className)} {...props} />;
}

export function DialogFooter({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)}
      {...props}
    />
  );
}

export function DialogTitle({ className, ...props }: ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      className={cn("font-semibold text-lg leading-tight", className)}
      {...props}
    />
  );
}

export function DialogDescription({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      className={cn("text-muted-foreground text-sm", className)}
      {...props}
    />
  );
}

export const sheetVariants = cva(
  "fixed z-50 flex flex-col gap-4 bg-popover text-popover-foreground shadow-lg transition ease-in-out duration-(--rs-motion-normal) data-[state=closed]:animate-out data-[state=open]:animate-in",
  {
    variants: {
      side: {
        end: "inset-y-0 end-0 h-full w-[min(100%,28rem)] border-s border-border data-[state=closed]:slide-out-to-end data-[state=open]:slide-in-from-end",
        start:
          "inset-y-0 start-0 h-full w-[min(100%,20rem)] border-e border-border data-[state=closed]:slide-out-to-start data-[state=open]:slide-in-from-start",
        bottom:
          "inset-x-0 bottom-0 max-h-[85dvh] rounded-t-xl border-border border-t data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom",
      },
    },
    defaultVariants: { side: "end" },
  },
);

export interface SheetContentProps
  extends ComponentProps<typeof DialogPrimitive.Content>,
    VariantProps<typeof sheetVariants> {
  hideClose?: boolean;
}

export const Sheet = DialogPrimitive.Root;
export const SheetTrigger = DialogPrimitive.Trigger;
export const SheetClose = DialogPrimitive.Close;
export const SheetTitle = DialogTitle;
export const SheetDescription = DialogDescription;

/** A panel that slides in from an edge: the end side, the start side or the bottom. */
export function SheetContent({
  className,
  children,
  side,
  hideClose,
  ...props
}: SheetContentProps) {
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        data-slot="sheet-content"
        className={cn(sheetVariants({ side }), "p-card", className)}
        {...props}
      >
        {side === "bottom" ? (
          <div
            aria-hidden="true"
            className="mx-auto -mt-2 h-1 w-10 shrink-0 rounded-full bg-border-strong"
          />
        ) : null}
        {children}
        {hideClose ? null : <CloseButton />}
      </DialogPrimitive.Content>
    </DialogPortal>
  );
}
