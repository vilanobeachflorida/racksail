import { useEffect, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { ChangePreview } from "@/design/components/change-preview";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { Alert } from "@/design/components/ui/display";
import type { ChangeSet as Previewed } from "@/lib/changes";
import type { ChangeSet } from "./api";
import { ListSkeleton } from "./components";
import { t } from "./messages";
import { useStepUp } from "./step-up";
import { failureMessage } from "./util";

const kinds: Record<string, typeof t.kindAccount> = {
  "core.account": t.kindAccount,
  "core.signin_policy": t.kindSigninPolicy,
  "core.signin_provider": t.kindSigninProvider,
};

/**
 * Previews a change before it's made: it asks the panel for the change set
 * of a dry run, shows it, and makes the change when the person applies it.
 * Applying runs the same request again for real, so it goes through the
 * change pipeline's checks once more.
 */
export function PreviewDialog<T>({
  open,
  title,
  description,
  preview,
  apply,
  onApplied,
  onClose,
}: {
  open: boolean;
  title: string;
  description?: string;
  preview: () => Promise<ChangeSet>;
  apply: () => Promise<T>;
  onApplied: (result: T) => void;
  onClose: () => void;
}) {
  const intl = useIntl();
  const { guard } = useStepUp();
  const [changeSet, setChangeSet] = useState<ChangeSet | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [applying, setApplying] = useState(false);
  // The preview asks for what the form says when the dialog opens.
  const latest = useRef(preview);
  latest.current = preview;
  useEffect(() => {
    if (!open) {
      return;
    }
    let live = true;
    setChangeSet(null);
    setError(null);
    latest.current().then(
      (cs) => live && setChangeSet(cs),
      (e: unknown) => live && setError(e),
    );
    return () => {
      live = false;
    };
  }, [open]);
  const run = async () => {
    setApplying(true);
    setError(null);
    try {
      onApplied(await guard(apply));
    } catch (e) {
      setError(e);
    } finally {
      setApplying(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {description ?? intl.formatMessage(t.previewDescription)}
          </DialogDescription>
        </DialogHeader>
        {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
        {changeSet ? (
          <ChangePreview
            changeSet={changeSet as unknown as Previewed}
            kindLabel={(kind) => (kinds[kind] ? intl.formatMessage(kinds[kind]) : kind)}
            onApply={run}
            onCancel={onClose}
            applying={applying}
          />
        ) : error ? null : (
          <ListSkeleton rows={4} />
        )}
      </DialogContent>
    </Dialog>
  );
}
