import { Label as LabelPrimitive } from "radix-ui";
import {
  type ComponentProps,
  createContext,
  type ReactElement,
  type ReactNode,
  useContext,
  useId,
} from "react";
import { cn } from "@/lib/utils";

/** A label for a control. */
export function Label({ className, ...props }: ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn(
        "flex select-none items-center gap-2 font-medium text-sm leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

interface FieldIds {
  control: string;
  description: string;
  error: string;
  hasDescription: boolean;
  hasError: boolean;
}

const FieldContext = createContext<FieldIds | null>(null);

/**
 * The attributes a control inside a Field needs: its id, and what describes
 * it and says it's invalid.
 */
export function useFieldControl(): {
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: true;
} {
  const field = useContext(FieldContext);
  if (!field) {
    return {};
  }
  const describedBy = [
    field.hasDescription ? field.description : null,
    field.hasError ? field.error : null,
  ]
    .filter(Boolean)
    .join(" ");
  return {
    id: field.control,
    ...(describedBy ? { "aria-describedby": describedBy } : {}),
    ...(field.hasError ? { "aria-invalid": true as const } : {}),
  };
}

export interface FieldProps {
  label: ReactNode;
  /** Help shown under the label, in plain words. */
  description?: ReactNode;
  /** What's wrong with the value and what to do. */
  error?: ReactNode;
  /** Marks the field as required next to its label. */
  required?: boolean;
  /** Text marking a required field, translated by the caller. */
  requiredLabel?: string;
  /** The id of the control, when it needs a fixed one. */
  id?: string;
  className?: string;
  /**
   * The control. A function gets the attributes that connect it to the
   * label, help and error; an element can call useFieldControl instead.
   */
  children: ReactElement | ((control: ReturnType<typeof useFieldControl>) => ReactElement);
}

/** A label, a control, help text and an error message, connected for screen readers. */
export function Field({
  label,
  description,
  error,
  required,
  requiredLabel,
  id,
  className,
  children,
}: FieldProps) {
  const generated = useId();
  const base = id ?? `field-${generated}`;
  const ids: FieldIds = {
    control: base,
    description: `${base}-description`,
    error: `${base}-error`,
    hasDescription: description !== undefined && description !== null && description !== "",
    hasError: error !== undefined && error !== null && error !== "" && error !== false,
  };
  return (
    <FieldContext value={ids}>
      <div data-slot="field" className={cn("flex flex-col gap-1.5", className)}>
        <Label htmlFor={ids.control}>
          {label}
          {required ? (
            <span className="font-normal text-muted-foreground text-xs">{requiredLabel}</span>
          ) : null}
        </Label>
        {ids.hasDescription ? (
          <p id={ids.description} className="text-muted-foreground text-xs">
            {description}
          </p>
        ) : null}
        <FieldControl>{children}</FieldControl>
        {ids.hasError ? (
          <p id={ids.error} className="text-danger text-xs" role="alert">
            {error}
          </p>
        ) : null}
      </div>
    </FieldContext>
  );
}

function FieldControl({ children }: { children: FieldProps["children"] }) {
  const control = useFieldControl();
  return typeof children === "function" ? children(control) : children;
}
