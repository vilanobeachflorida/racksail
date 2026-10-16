import { useQueryClient } from "@tanstack/react-query";
import { type ErrorComponentProps, Link, useRouter, useRouterState } from "@tanstack/react-router";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { ErrorState } from "@/design/components/ui/states";
import { errorDetails, errorMessage } from "@/lib/errors";
import { BrandMark } from "./brand";
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

/**
 * Shown in place of a screen that threw while loading or rendering, in the
 * shell, so the rest of the panel still works: what went wrong, Try again,
 * and the technical details folded away for a bug report.
 */
export function RouteError({ error, reset }: ErrorComponentProps) {
  const intl = useIntl();
  const router = useRouter();
  const queryClient = useQueryClient();
  const address = useRouterState({ select: (state) => state.location.href });
  const tryAgain = async () => {
    // What failed loads again, rather than fail at once from what was kept.
    await queryClient.resetQueries({ predicate: (query) => query.state.status === "error" });
    reset();
    await router.invalidate();
  };
  return (
    <div className="mx-auto flex max-w-xl flex-col py-12">
      <ErrorState
        headingLevel={1}
        title={intl.formatMessage(messages.errorTitle)}
        error={error}
        details={`${address}\n${errorDetails(error)}`}
        onRetry={() => void tryAgain()}
      />
    </div>
  );
}

/** Shown when the panel's code or messages couldn't load at all. */
export function BootError({ error }: { error: unknown }) {
  const intl = useIntl();
  return (
    <div role="alert" className="mx-auto flex min-h-dvh max-w-xl flex-col justify-center gap-4 p-6">
      <BrandMark className="h-10 self-start" />
      <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.bootTitle)}</h1>
      <p>{errorMessage(intl, error)}</p>
      <p className="text-muted-foreground">{intl.formatMessage(messages.bootBody)}</p>
      <Button className="self-start" onClick={() => window.location.reload()}>
        {intl.formatMessage(messages.errorReload)}
      </Button>
    </div>
  );
}
