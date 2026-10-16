import type { ReactNode } from "react";
import { Card } from "@/design/components/ui/card";
import { BrandLogo } from "../brand";

/** The panel's frame around the sign-in screens: the brand and a card. */
export function AuthFrame({
  title,
  description,
  children,
  footer,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-background px-4 py-10 text-foreground">
      <main className="flex w-full max-w-sm flex-col gap-6">
        <div className="flex justify-center">
          <BrandLogo />
        </div>
        <Card className="gap-6">
          <div className="flex flex-col gap-1">
            <h1 className="font-semibold text-xl">{title}</h1>
            {description ? <p className="text-muted-foreground text-sm">{description}</p> : null}
          </div>
          {children}
        </Card>
        {footer ? <div className="text-center text-muted-foreground text-sm">{footer}</div> : null}
      </main>
    </div>
  );
}
