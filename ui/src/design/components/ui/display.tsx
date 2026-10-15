import { cva, type VariantProps } from "class-variance-authority";
import { ChevronRight, CircleAlert, CircleCheck, Info, TriangleAlert } from "lucide-react";
import {
  Collapsible as CollapsiblePrimitive,
  Progress as ProgressPrimitive,
  ScrollArea as ScrollAreaPrimitive,
  Separator as SeparatorPrimitive,
} from "radix-ui";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/utils";

export const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 font-medium text-xs [&_svg]:size-3",
  {
    variants: {
      variant: {
        neutral: "border-border bg-muted text-foreground",
        brand: "border-transparent bg-brand text-brand-foreground",
        accent: "border-transparent bg-primary-subtle text-foreground",
        success: "border-transparent bg-success text-success-foreground",
        warning: "border-transparent bg-warning text-warning-foreground",
        danger: "border-transparent bg-danger text-danger-foreground",
        info: "border-transparent bg-info text-info-foreground",
        outline: "border-border-strong text-foreground",
      },
    },
    defaultVariants: { variant: "neutral" },
  },
);

/** A short label: a state, a count or a category. */
export function Badge({
  className,
  variant,
  ...props
}: ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return (
    <span data-slot="badge" className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

const alertVariants = cva(
  "relative grid grid-cols-[auto_1fr] items-start gap-x-3 gap-y-1 rounded-lg border p-4 text-foreground text-sm [&>svg]:mt-0.5 [&>svg]:size-4",
  {
    variants: {
      tone: {
        info: "border-info/40 bg-info-subtle [&>svg]:text-info",
        success: "border-success/40 bg-success-subtle [&>svg]:text-success",
        warning: "border-warning/40 bg-warning-subtle [&>svg]:text-warning",
        danger: "border-danger/40 bg-danger-subtle [&>svg]:text-danger",
      },
    },
    defaultVariants: { tone: "info" },
  },
);

const alertIcons = {
  info: Info,
  success: CircleCheck,
  warning: TriangleAlert,
  danger: CircleAlert,
};

export interface AlertProps
  extends Omit<ComponentProps<"div">, "title">,
    VariantProps<typeof alertVariants> {
  title?: ReactNode;
  /** Buttons or links that act on the alert. */
  actions?: ReactNode;
}

/**
 * A message that stays on the page: something to know, a warning or what
 * went wrong. Its text keeps the text color; the icon and border carry the
 * tone, so it reads in every theme.
 */
export function Alert({ className, tone, title, actions, children, ...props }: AlertProps) {
  const Icon = alertIcons[tone ?? "info"];
  return (
    <div
      data-slot="alert"
      role={tone === "danger" || tone === "warning" ? "alert" : "status"}
      className={cn(alertVariants({ tone }), className)}
      {...props}
    >
      <Icon aria-hidden="true" />
      <div className="flex min-w-0 flex-col gap-1">
        {title ? <p className="font-semibold leading-tight">{title}</p> : null}
        {children ? <div className="text-foreground">{children}</div> : null}
        {actions ? <div className="mt-2 flex flex-wrap gap-2">{actions}</div> : null}
      </div>
    </div>
  );
}

/** A thin line between groups. */
export function Separator({
  className,
  orientation = "horizontal",
  decorative = true,
  ...props
}: ComponentProps<typeof SeparatorPrimitive.Root>) {
  return (
    <SeparatorPrimitive.Root
      data-slot="separator"
      decorative={decorative}
      orientation={orientation}
      className={cn(
        "shrink-0 bg-border",
        orientation === "horizontal" ? "h-px w-full" : "h-full w-px",
        className,
      )}
      {...props}
    />
  );
}

/** A key or key combination, as in a shortcut. */
export function Kbd({ className, ...props }: ComponentProps<"kbd">) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-border bg-muted px-1 font-mono font-medium text-muted-foreground text-xs",
        className,
      )}
      {...props}
    />
  );
}

/** A scrolling region with thin scroll bars that follow the theme. */
export function ScrollArea({
  className,
  children,
  viewportRef,
  ...props
}: ComponentProps<typeof ScrollAreaPrimitive.Root> & {
  viewportRef?: ComponentProps<typeof ScrollAreaPrimitive.Viewport>["ref"];
}) {
  return (
    <ScrollAreaPrimitive.Root
      data-slot="scroll-area"
      className={cn("relative overflow-hidden", className)}
      {...props}
    >
      <ScrollAreaPrimitive.Viewport
        ref={viewportRef}
        className="size-full rounded-[inherit] focus-visible:focus-ring"
      >
        {children}
      </ScrollAreaPrimitive.Viewport>
      <ScrollAreaPrimitive.Scrollbar
        orientation="vertical"
        className="flex w-2.5 touch-none select-none p-px"
      >
        <ScrollAreaPrimitive.Thumb className="relative flex-1 rounded-full bg-border-strong" />
      </ScrollAreaPrimitive.Scrollbar>
      <ScrollAreaPrimitive.Scrollbar
        orientation="horizontal"
        className="flex h-2.5 touch-none select-none flex-col p-px"
      >
        <ScrollAreaPrimitive.Thumb className="relative flex-1 rounded-full bg-border-strong" />
      </ScrollAreaPrimitive.Scrollbar>
      <ScrollAreaPrimitive.Corner />
    </ScrollAreaPrimitive.Root>
  );
}

/** How far along something is, such as a job; without a value it shows that work goes on. */
export function Progress({
  className,
  value,
  tone = "accent",
  ...props
}: ComponentProps<typeof ProgressPrimitive.Root> & {
  tone?: "accent" | "success" | "warning" | "danger";
}) {
  const fill = {
    accent: "bg-primary",
    success: "bg-success",
    warning: "bg-warning",
    danger: "bg-danger",
  }[tone];
  const known = typeof value === "number";
  return (
    <ProgressPrimitive.Root
      data-slot="progress"
      value={value}
      className={cn("relative h-1.5 w-full overflow-hidden rounded-full bg-muted", className)}
      {...props}
    >
      <ProgressPrimitive.Indicator
        className={cn(
          "h-full transition-[width] duration-(--rs-motion-normal)",
          fill,
          known ? "rounded-full" : "w-1/3 animate-pulse",
        )}
        style={known ? { width: `${Math.max(0, Math.min(100, value ?? 0))}%` } : undefined}
      />
    </ProgressPrimitive.Root>
  );
}

export const Collapsible = CollapsiblePrimitive.Root;
export const CollapsibleContent = CollapsiblePrimitive.Content;

/** The button that opens and closes a collapsible section, with a turning chevron. */
export function CollapsibleTrigger({
  className,
  children,
  ...props
}: ComponentProps<typeof CollapsiblePrimitive.Trigger>) {
  return (
    <CollapsiblePrimitive.Trigger
      className={cn(
        "group inline-flex items-center gap-1.5 rounded-md font-medium text-sm hover:text-foreground focus-visible:focus-ring",
        className,
      )}
      {...props}
    >
      <ChevronRight
        className="size-4 transition-transform group-data-[state=open]:rotate-90 rtl:-scale-x-100 rtl:group-data-[state=open]:-rotate-90"
        aria-hidden="true"
      />
      {children}
    </CollapsiblePrimitive.Trigger>
  );
}
