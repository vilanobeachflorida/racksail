import { Eye } from "lucide-react";
import { useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { toast } from "@/design/components/ui/toaster";
import { unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { messages } from "../messages";
import { page } from "./page";
import { useSession } from "./session";

/**
 * Says, the whole time an admin views the panel as someone else, whose panel
 * it is and that everything is recorded with both their names. It takes the
 * protected palette, so no theme can hide it.
 */
export function ImpersonationBanner() {
  const intl = useIntl();
  const api = useApi();
  const { session } = useSession();
  const [busy, setBusy] = useState(false);
  const viewing = session?.impersonation;
  if (!session || !viewing) {
    return null;
  }
  const user = session.user.name;
  const stop = async () => {
    setBusy(true);
    try {
      await unwrap(api.DELETE("/api/v1/session/impersonation"));
      // Everything loaded so far was the other person's; start afresh.
      page.load("/");
    } catch (error) {
      toast.error(
        intl.formatMessage(messages.impersonationStopFailed, {
          user,
          reason: errorMessage(intl, error),
        }),
      );
      setBusy(false);
    }
  };
  return (
    <section
      aria-label={intl.formatMessage(messages.impersonationLabel)}
      className="flex shrink-0 flex-wrap items-center justify-center gap-x-4 gap-y-2 bg-critical-warning px-4 py-2 text-critical-warning-foreground text-sm"
    >
      <p className="flex items-center gap-2">
        <Eye className="size-4 shrink-0" aria-hidden="true" />
        {intl.formatMessage(messages.impersonationBanner, {
          user,
          admin: viewing.name,
          time: intl.formatTime(new Date(viewing.endsAt), { timeStyle: "short" }),
        })}
      </p>
      <Button size="sm" variant="outline" onClick={() => void stop()} busy={busy}>
        {intl.formatMessage(messages.impersonationStop, { user })}
      </Button>
    </section>
  );
}
