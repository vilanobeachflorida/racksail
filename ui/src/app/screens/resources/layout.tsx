import { Link, Outlet, useParams } from "@tanstack/react-router";
import { BookOpen, PowerOff } from "lucide-react";
import { useIntl } from "react-intl";
import { NotFound } from "@/app/errors";
import { isOn, useModuleStates, useModules } from "@/app/modules";
import { usePageTitle } from "@/app/page-title";
import { docsURL } from "@/app/topbar";
import { Button } from "@/design/components/ui/button";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { isModuleId } from "@/modules/registry";
import { messages as shellMessages } from "../../messages";
import { ScreenSkeleton } from "../catalog";
import { resourceMessages as m } from "../messages";
import {
  type Kind,
  kindAt,
  kindNoun,
  kindNouns,
  kindPlural,
  kindTitle,
  moduleName,
  resourceTitle,
  useKinds,
} from "./kinds";
import { isMissing, useResource } from "./queries";

interface RouteParams {
  module?: string;
  plural?: string;
  id?: string;
}

/** The kind and resource a generic screen's path names. */
export function useRouteKind() {
  const params = useParams({ strict: false }) as RouteParams;
  const kinds = useKinds();
  const kind =
    kinds.data && params.module && params.plural
      ? kindAt(kinds.data, params.module, params.plural)
      : undefined;
  return {
    module: params.module ?? "",
    id: params.id,
    kinds: kinds.data ?? [],
    kind,
    pending: kinds.isPending,
    error: kinds.error,
  };
}

/** Whether a module is one this panel knows: a bundled module, or the core. */
function knownModule(id: string, kinds: readonly Kind[]): boolean {
  return isModuleId(id) || kinds.some((k) => k.module === id);
}

/** What shows for a module that is off: what it would do, and where to read about it. */
export function ModuleOff({ module }: { module: string }) {
  const intl = useIntl();
  const modules = useModules();
  const ui = modules.find((m) => m.id === module);
  return (
    <div className="mx-auto max-w-3xl">
      <EmptyState
        icon={PowerOff}
        title={intl.formatMessage(m.moduleOff, {
          module: moduleName(intl, modules, module, shellMessages.resourcesPanel),
        })}
        description={ui ? intl.formatMessage(ui.description) : undefined}
        action={
          <Button asChild variant="outline">
            <a href={`${docsURL}modules/${module}/`} target="_blank" rel="noreferrer">
              <BookOpen aria-hidden="true" />
              {intl.formatMessage(m.moduleOffDocs)}
            </a>
          </Button>
        }
      />
    </div>
  );
}

/**
 * /<module>: names the module in the breadcrumb, and leads to its kinds'
 * screens. Paths no module or kind has show that the page doesn't exist.
 */
export default function ModuleLayout() {
  const intl = useIntl();
  const modules = useModules();
  const { module, kinds, pending, error } = useRouteKind();
  const known = !pending && knownModule(module, kinds);
  usePageTitle(known ? moduleName(intl, modules, module, shellMessages.resourcesPanel) : undefined);
  if (pending) {
    return <ScreenSkeleton />;
  }
  if (error && kinds.length === 0) {
    return <ErrorState title={intl.formatMessage(m.kindsFailed)} error={error} />;
  }
  if (!known) {
    return <NotFound />;
  }
  return <Outlet />;
}

/** /<module>/<plural>: names the kind in the breadcrumb, while its module is on. */
export function KindLayout() {
  const intl = useIntl();
  const states = useModuleStates();
  const { kind, pending, module } = useRouteKind();
  usePageTitle(kind ? kindPlural(intl, kind) : undefined);
  if (pending) {
    return <ScreenSkeleton />;
  }
  if (!kind) {
    return <NotFound />;
  }
  if (isModuleId(module) && !isOn(states, module)) {
    return <ModuleOff module={module} />;
  }
  return <Outlet />;
}

/** What shows for a resource that isn't there, or no longer is. */
function ResourceGone({ kind }: { kind: Kind }) {
  const intl = useIntl();
  return (
    <div className="mx-auto flex max-w-xl flex-col items-start gap-4 py-12">
      <h1 className="font-semibold text-2xl">
        {intl.formatMessage(m.goneTitle, { kind: kindNoun(intl, kind) })}
      </h1>
      <p className="text-muted-foreground">{intl.formatMessage(m.goneBody)}</p>
      <Button asChild>
        <Link to={`/${kind.module}/${kind.plural}`}>
          {intl.formatMessage(m.goneBack, { kinds: kindNouns(intl, kind) })}
        </Link>
      </Button>
    </div>
  );
}

/** /<module>/<plural>/<id>: names the resource in the breadcrumb and the browser tab. */
export function ResourceLayout() {
  const intl = useIntl();
  const { kind, id } = useRouteKind();
  const resource = useResource(kind, id);
  const title = kind && resource.data ? resourceTitle(kind, resource.data) : undefined;
  usePageTitle(title, title && kind ? `${title} · ${kindTitle(intl, kind)}` : undefined);
  if (!kind) {
    return null;
  }
  if (resource.isPending) {
    return <ScreenSkeleton />;
  }
  if (resource.isError) {
    return isMissing(resource.error) ? (
      <ResourceGone kind={kind} />
    ) : (
      <ErrorState
        title={intl.formatMessage(m.resourceFailed, { kind: kindNoun(intl, kind) })}
        error={resource.error}
        onRetry={() => void resource.refetch()}
      />
    );
  }
  return <Outlet />;
}
