import { Link } from "@tanstack/react-router";
import { CircleAlert } from "lucide-react";
import { useIntl } from "react-intl";
import { Mark } from "@/design/components/mark";
import { Button } from "@/design/components/ui/button";
import { errorMessage } from "@/lib/errors";
import { messages } from "./messages";

/** Shown for an address no route matches. */
export function NotFound() {
  const intl = useIntl();
  return (
    <div className="mx-auto flex max-w-xl flex-col items-start gap-4 py-12">
      <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.notFoundTitle)}</h1>
      <p className="text-muted-foreground">{intl.formatMessage(messages.notFoundBody)}</p>
      <Button asChild>
        <Link to="/">{intl.formatMessage(messages.notFoundHome)}</Link>
      </Button>
    </div>
  );
}

/** Shown in place of a screen that threw while loading or rendering. */
export function RouteError({ error }: { error: unknown }) {
  const intl = useIntl();
  return (
    <div role="alert" className="mx-auto flex max-w-xl flex-col items-start gap-4 py-12">
      <h1 className="flex items-center gap-2 font-semibold text-2xl">
        <CircleAlert className="size-6 text-danger" aria-hidden="true" />
        {intl.formatMessage(messages.errorTitle)}
      </h1>
      <p className="text-muted-foreground">{errorMessage(intl, error)}</p>
      <Button variant="outline" onClick={() => window.location.reload()}>
        {intl.formatMessage(messages.errorReload)}
      </Button>
    </div>
  );
}

/** Shown when the panel's code or messages couldn't load at all. */
export function BootError({ error }: { error: unknown }) {
  const intl = useIntl();
  return (
    <div role="alert" className="mx-auto flex min-h-dvh max-w-xl flex-col justify-center gap-4 p-6">
      <Mark className="h-10 self-start" />
      <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.bootTitle)}</h1>
      <p>{errorMessage(intl, error)}</p>
      <p className="text-muted-foreground">{intl.formatMessage(messages.bootBody)}</p>
      <Button className="self-start" onClick={() => window.location.reload()}>
        {intl.formatMessage(messages.errorReload)}
      </Button>
    </div>
  );
}
