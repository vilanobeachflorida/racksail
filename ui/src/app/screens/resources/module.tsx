import { Link, useRouterState } from "@tanstack/react-router";
import { Boxes, ChevronRight } from "lucide-react";
import { useIntl } from "react-intl";
import { isOn, useModuleStates, useModules } from "@/app/modules";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { isModuleId } from "@/modules/registry";
import { messages as shellMessages } from "../../messages";
import { ScreenSkeleton } from "../catalog";
import { resourceMessages as m } from "../messages";
import { type Kind, kindDescription, kindPath, kindPlural, moduleName, useKinds } from "./kinds";
import { ModuleOff } from "./layout";

/** A kind as a row of a list that leads to its resources. */
export function KindEntry({ kind }: { kind: Kind }) {
  const intl = useIntl();
  return (
    <li>
      <Link
        to={kindPath(kind)}
        className="flex items-center gap-3 rounded-lg border border-border bg-card p-4 hover:bg-accent focus-visible:focus-ring"
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="font-medium">{kindPlural(intl, kind)}</span>
          <span className="text-muted-foreground text-sm">{kindDescription(intl, kind)}</span>
        </span>
        <ChevronRight
          className="size-4 shrink-0 text-muted-foreground rtl:rotate-180"
          aria-hidden="true"
        />
      </Link>
    </li>
  );
}

/**
 * A module's page, until the module has one of its own: what it manages,
 * each kind leading to its list.
 */
export default function ModuleIndex() {
  const intl = useIntl();
  const modules = useModules();
  const states = useModuleStates();
  const module = useRouterState({ select: (s) => s.location.pathname.split("/")[1] ?? "" });
  const kinds = useKinds();
  if (kinds.isPending) {
    return <ScreenSkeleton />;
  }
  if (isModuleId(module) && !isOn(states, module)) {
    return <ModuleOff module={module} />;
  }
  const ui = modules.find((m) => m.id === module);
  const own = (kinds.data ?? []).filter((k) => k.module === module && !k.ui.hidden);
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">
          {moduleName(intl, modules, module, shellMessages.resourcesPanel)}
        </h1>
        <p className="text-muted-foreground">
          {ui ? intl.formatMessage(ui.description) : intl.formatMessage(m.panelDescription)}
        </p>
      </div>
      {kinds.isError ? (
        <ErrorState
          title={intl.formatMessage(m.kindsFailed)}
          error={kinds.error}
          onRetry={() => void kinds.refetch()}
        />
      ) : own.length === 0 ? (
        <EmptyState
          icon={Boxes}
          title={intl.formatMessage(m.moduleEmpty)}
          description={intl.formatMessage(m.moduleEmptyHelp)}
        />
      ) : (
        <ul className="flex flex-col gap-2">
          {own.map((kind) => (
            <KindEntry key={kind.name} kind={kind} />
          ))}
        </ul>
      )}
    </div>
  );
}
