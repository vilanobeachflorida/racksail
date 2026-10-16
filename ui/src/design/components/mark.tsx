import type { SVGProps } from "react";
import { cn } from "@/lib/utils";
import { horizontalLogo, mark, smallMark, stackedLogo, wordmark } from "../brand";

interface ArtProps extends SVGProps<SVGSVGElement> {
  /** Makes the drawing an image with this name. Without it, it's decorative. */
  label?: string;
}

function Art({ label, className, viewBox, children, ...props }: ArtProps & { viewBox: string }) {
  return label ? (
    <svg viewBox={viewBox} role="img" aria-label={label} className={className} {...props}>
      <title>{label}</title>
      {children}
    </svg>
  ) : (
    <svg viewBox={viewBox} aria-hidden="true" focusable="false" className={className} {...props}>
      {children}
    </svg>
  );
}

export interface MarkProps extends ArtProps {
  /**
   * The redrawn 16-pixel mark is clearer at icon sizes up to 24 pixels;
   * the full drawing suits everything larger.
   */
  small?: boolean;
}

/**
 * The Racksail mark. It's drawn with theme colors rather than the brand
 * files' fixed ones, so it reads on dark surfaces too: the ink takes the
 * brand color in light mode and the text color in dark mode.
 */
export function Mark({ small, className, ...props }: MarkProps) {
  const art = small ? smallMark : mark;
  return (
    <Art viewBox={art.viewBox} className={cn("h-6 w-auto", className)} {...props}>
      <path className="fill-mark-ink" d={art.ink} />
      <path className="fill-mark-sea" d={art.sea} />
    </Art>
  );
}

/** The wordmark alone. */
export function Wordmark({ className, ...props }: ArtProps) {
  return (
    <Art viewBox={wordmark.viewBox} className={cn("h-5 w-auto", className)} {...props}>
      <path className="fill-mark-ink" d={wordmark.word} />
    </Art>
  );
}

/** The logo: the mark with the wordmark beside it, or stacked under it. */
export function Logo({ stacked, className, ...props }: ArtProps & { stacked?: boolean }) {
  const art = stacked ? stackedLogo : horizontalLogo;
  return (
    <Art
      viewBox={art.viewBox}
      className={cn(stacked ? "h-24 w-auto" : "h-8 w-auto", className)}
      {...props}
    >
      <path className="fill-mark-ink" d={art.ink} />
      <path className="fill-mark-sea" d={art.sea} />
      <path className="fill-mark-ink" d={art.word} />
    </Art>
  );
}
