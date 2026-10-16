import { Boxes } from "lucide-react";
import { useIntl } from "react-intl";
import { isOn, useModuleStates, useModules } from "@/app/modules";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { isModuleId } from "@/modules/registry";
import { messages as shellMessages } from "../../messages";
import { ScreenSkeleton } from "../catalog";
import { resourceMessages as m } from "../messages";
import { type Kind, moduleName, useKinds } from "./kinds";
import { KindEntry } from "./module";

/**
 * Everything the panel manages, kind by kind and module by module: the
 * panel's own kinds first, then those of each module that is on.
 */
export default function ResourcesIndex() {
  const intl = useIntl();
  const modules = useModules();
  const states = useModuleStates();
  const kinds = useKinds();
  if (kinds.isPending) {
    return <ScreenSkeleton />;
  }
  const groups = new Map<string, Kind[]>();
  for (const kind of kinds.data ?? []) {
    if (kind.ui.hidden || (isModuleId(kind.module) && !isOn(states, kind.module))) {
      continue;
    }
    groups.set(kind.module, [...(groups.get(kind.module) ?? []), kind]);
  }
  const order = [...groups.keys()].sort((a, b) =>
    a === "core"
      ? -1
      : b === "core"
        ? 1
        : moduleName(intl, modules, a, shellMessages.resourcesPanel).localeCompare(
            moduleName(intl, modules, b, shellMessages.resourcesPanel),
            intl.locale,
          ),
  );
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-8">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">
          {intl.formatMessage(shellMessages.resourcesTitle)}
        </h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.indexDescription)}</p>
      </div>
      {kinds.isError ? (
        <ErrorState
          title={intl.formatMessage(m.kindsFailed)}
          error={kinds.error}
          onRetry={() => void kinds.refetch()}
        />
      ) : order.length === 0 ? (
        <EmptyState
          icon={Boxes}
          title={intl.formatMessage(m.indexEmpty)}
          description={intl.formatMessage(m.indexEmptyHelp)}
        />
      ) : (
        order.map((module) => (
          <section key={module} className="flex flex-col gap-3" aria-labelledby={`kinds-${module}`}>
            <h2 id={`kinds-${module}`} className="font-semibold text-lg">
              {moduleName(intl, modules, module, shellMessages.resourcesPanel)}
            </h2>
            <ul className="flex flex-col gap-2">
              {(groups.get(module) ?? []).map((kind) => (
                <KindEntry key={kind.name} kind={kind} />
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  );
}
