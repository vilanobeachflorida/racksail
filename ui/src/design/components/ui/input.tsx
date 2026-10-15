import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export const controlClasses =
  "w-full min-w-0 rounded-md border border-input bg-card text-sm text-foreground shadow-xs transition-colors placeholder:text-muted-foreground focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-danger read-only:bg-muted";

/** A text input. Pair it with a Field for its label, help and errors. */
export function Input({ className, type = "text", ...props }: ComponentProps<"input">) {
  return (
    <input
      data-slot="input"
      type={type}
      className={cn(
        controlClasses,
        "h-control-md px-3 py-1 file:me-3 file:border-0 file:bg-transparent file:font-medium file:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

/** A multi-line text input that grows with its content. */
export function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(controlClasses, "field-sizing-content min-h-20 px-3 py-2", className)}
      {...props}
    />
  );
}
