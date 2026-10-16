import { useIntl } from "react-intl";
import { Mark } from "@/design/components/mark";
import { useTheme } from "@/design/theme/provider";
import { cn } from "@/lib/utils";
import { messages } from "./messages";

/** The name the panel goes by: a server's or reseller's brand, or Racksail. */
export function useBrandName(): string {
  const intl = useIntl();
  return useTheme().brandName ?? intl.formatMessage(messages.name);
}

/**
 * The logo in the top bar: a server's or reseller's logo when its theme has
 * one, with its mark on narrow screens, or else Racksail's mark and name.
 * It's decorative; the link around it carries the name.
 */
export function BrandLogo() {
  const { assets, mode } = useTheme();
  const name = useBrandName();
  const logo =
    mode === "dark" ? (assets.logoDark ?? assets.logoLight) : (assets.logoLight ?? assets.logoDark);
  if (!logo) {
    return (
      <>
        <Mark className="h-7" />
        <span className="hidden lg:inline">{name}</span>
      </>
    );
  }
  return (
    <>
      {assets.mark ? <img src={assets.mark} alt="" className="h-7 w-auto lg:hidden" /> : null}
      <img
        src={logo}
        alt=""
        className={cn("h-7 w-auto max-w-48 object-contain", assets.mark && "hidden lg:block")}
      />
    </>
  );
}

/** The mark: a server's or reseller's when its theme has one, or Racksail's. */
export function BrandMark({ className }: { className?: string }) {
  const { assets } = useTheme();
  return assets.mark ? (
    <img src={assets.mark} alt="" className={cn("w-auto", className)} />
  ) : (
    <Mark className={className} />
  );
}
