import { cva, type VariantProps } from "class-variance-authority";
import { Slot } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export const buttonVariants = cva(
  "inline-flex shrink-0 select-none items-center justify-center gap-2 whitespace-nowrap rounded-md border border-transparent font-medium text-sm transition-colors focus-visible:focus-ring disabled:pointer-events-none disabled:opacity-50 aria-busy:cursor-progress [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        destructive: "bg-destructive text-destructive-foreground hover:bg-destructive/90",
        // Destructive confirmations take the protected palette, which no
        // theme can change.
        critical:
          "bg-critical-danger text-critical-danger-foreground [--rs-focus:var(--rs-critical-focus)] hover:bg-critical-danger/90",
        outline: "border-input bg-card hover:bg-accent hover:text-accent-foreground",
        secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "h-auto px-0 text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-control-md px-4",
        sm: "h-control-sm px-3 text-xs",
        lg: "h-control-lg px-6",
        icon: "size-control-md",
        "icon-sm": "size-control-sm",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export interface ButtonProps extends ComponentProps<"button">, VariantProps<typeof buttonVariants> {
  /** Renders the child element, such as a link, with the button's styles. */
  asChild?: boolean;
  /** Marks the button as working on something; it can't be pressed again meanwhile. */
  busy?: boolean;
}

/** A button. Icon-only buttons need an aria-label. */
export function Button({
  className,
  variant,
  size,
  asChild = false,
  busy = false,
  disabled,
  type,
  ...props
}: ButtonProps) {
  const Component = asChild ? Slot.Root : "button";
  return (
    <Component
      data-slot="button"
      className={cn(buttonVariants({ variant, size }), className)}
      aria-busy={busy || undefined}
      disabled={asChild ? undefined : disabled || busy}
      type={asChild ? undefined : (type ?? "button")}
      {...props}
    />
  );
}
