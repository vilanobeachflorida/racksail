import { useMemo } from "react";
import { useIntl } from "react-intl";
import { type ResourceChange, resourceTitle } from "@/lib/changes";
import { kindTitle, useKinds } from "./kinds";

/**
 * Names for the change preview: a kind's name in the person's language, and
 * a resource's name from its kind's title field, which critical changes ask
 * to be typed.
 */
export function useKindLabels() {
  const intl = useIntl();
  const kinds = useKinds().data;
  return useMemo(() => {
    const find = (name: string) => kinds?.find((k) => k.name === name);
    return {
      kind: (name: string) => {
        const kind = find(name);
        return kind ? kindTitle(intl, kind) : name;
      },
      resourceName: (change: ResourceChange) =>
        resourceTitle(change, find(change.kind)?.ui.titleField),
    };
  }, [intl, kinds]);
}
