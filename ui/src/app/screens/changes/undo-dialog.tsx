import { useEffect, useState } from "react";
import { useIntl } from "react-intl";
import { useApi } from "@/lib/api/context";
import type { ChangeSet } from "@/lib/changes";
import { changeMessages as m } from "../messages";
import { useKindLabels } from "../resources/labels";
import { undoChangeSet } from "./api";
import { ChangeDialog } from "./dialog";
import { useChangeFlow } from "./flow";

/**
 * Shows the preview of an undo that removes data or is risky before it is
 * applied, as the change tracker asks when someone undoes such a change.
 */
export default function UndoDialog({
  preview,
  of,
  onClose,
}: {
  preview: ChangeSet;
  /** The change set the undo undoes. */
  of: string;
  onClose: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const labels = useKindLabels();
  const [open, setOpen] = useState(true);
  const flow = useChangeFlow({
    onApplied: () => {
      setOpen(false);
      onClose();
    },
  });
  const { show } = flow;
  useEffect(() => {
    show(preview, { send: (options) => undoChangeSet(api, of, options) });
  }, [show, preview, api, of]);
  return (
    <ChangeDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          onClose();
        }
      }}
      title={intl.formatMessage(m.undoTitle)}
      description={intl.formatMessage(m.undoDescription)}
      flow={flow}
      kindLabel={labels.kind}
      resourceName={labels.resourceName}
    />
  );
}
