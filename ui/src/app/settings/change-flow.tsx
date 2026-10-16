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
import { messages } from "../messages";

/** Sends a change, as a preview when dryRun is true. */
export type ChangeRequest = (dryRun: boolean) => Promise<unknown>;

interface Pending {
  preview: ChangeSet;
  request: ChangeRequest;
  /** The change carries secret values, which a preview can't keep. */
  secrets: boolean;
}

/**
 * Runs a change the way every change runs: a preview first, applied only
 * when the person agrees. A preview without secrets is applied as it was
 * shown; one with secrets is sent again, since the panel keeps no secret it
 * only previewed.
 */
export function useChangeFlow({ onApplied }: { onApplied?: () => void } = {}) {
  const api = useApi();
  const intl = useIntl();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);

  const preview = async (request: ChangeRequest, options: { secrets?: boolean } = {}) => {
    setBusy(true);
    try {
      const cs = (await request(true)) as ChangeSet;
      setPending({ preview: cs, request, secrets: options.secrets ?? false });
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
      if (pending.secrets) {
        await pending.request(false);
      } else {
        await unwrap(
          api.POST("/api/v1/changesets/{id}/apply", {
            params: { path: { id: pending.preview.id } },
          }),
        );
      }
      toast.success(intl.formatMessage(messages.changeApplied, { title: pending.preview.title }));
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
        <DialogTitle className="sr-only">
          {intl.formatMessage(messages.changePreviewTitle)}
        </DialogTitle>
        <DialogDescription className="sr-only">
          {intl.formatMessage(messages.changePreviewDescription)}
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
