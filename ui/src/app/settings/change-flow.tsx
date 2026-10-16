import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useIntl } from "react-intl";
import { ChangePreview } from "@/design/components/change-preview";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { toast } from "@/design/components/ui/toaster";
import { unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { ChangeSet } from "@/lib/changes";
import { errorMessage } from "@/lib/errors";
import { settingsChangeMessages as m } from "../screens/messages";

/** Sends a change, as a preview when dryRun is true. */
export type ChangeRequest = (dryRun: boolean) => Promise<unknown>;

interface Pending {
  preview: ChangeSet;
  request: ChangeRequest;
  /** Applying sends the request again rather than the preview's ID. */
  resend: boolean;
}

export interface PreviewOptions {
  /**
   * Apply by sending the request again: for changes that carry secret
   * values, which the panel doesn't keep for a preview, and for requests
   * the panel authorizes as they are sent, such as a person's own
   * notification preferences.
   */
  resend?: boolean;
}

/**
 * Runs a change the way every change runs: a preview first, applied only
 * when the person agrees. A preview is applied as it was shown, or sent
 * again when the request asks for that.
 */
export function useChangeFlow({ onApplied }: { onApplied?: () => void } = {}) {
  const api = useApi();
  const intl = useIntl();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);

  const preview = async (request: ChangeRequest, options: PreviewOptions = {}) => {
    setBusy(true);
    try {
      const cs = (await request(true)) as ChangeSet;
      setPending({ preview: cs, request, resend: options.resend ?? false });
    } catch (error) {
      toast.error(errorMessage(intl, error));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    if (!pending) {
      return;
    }
    setBusy(true);
    try {
      if (pending.resend) {
        await pending.request(false);
      } else {
        await unwrap(
          api.POST("/api/v1/changesets/{id}/apply", {
            params: { path: { id: pending.preview.id } },
          }),
        );
      }
      toast.success(intl.formatMessage(m.changeApplied, { title: pending.preview.title }));
      setPending(null);
      await queryClient.invalidateQueries();
      onApplied?.();
    } catch (error) {
      toast.error(errorMessage(intl, error));
    } finally {
      setBusy(false);
    }
  };

  return { pending, busy, preview, apply, cancel: () => setPending(null) };
}

export type ChangeFlow = ReturnType<typeof useChangeFlow>;

/** Shows a change's preview in a dialog, with its apply and cancel buttons. */
export function ChangeDialog({ flow }: { flow: ChangeFlow }) {
  const intl = useIntl();
  const cs = flow.pending?.preview;
  return (
    <Dialog open={cs !== undefined} onOpenChange={(open) => (open ? undefined : flow.cancel())}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        <DialogTitle className="sr-only">{intl.formatMessage(m.changePreviewTitle)}</DialogTitle>
        <DialogDescription className="sr-only">
          {intl.formatMessage(m.changePreviewDescription)}
        </DialogDescription>
        {cs ? (
          <ChangePreview
            changeSet={cs}
            applying={flow.busy}
            onApply={flow.apply}
            onCancel={flow.cancel}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
