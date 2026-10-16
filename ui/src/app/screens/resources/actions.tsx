import { type ReactNode, useCallback, useState } from "react";
import { useIntl } from "react-intl";
import { SchemaForm } from "@/design/components/schema-form";
import { Skeleton } from "@/design/components/ui/skeleton";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { ChangeSet, ResourceChange } from "@/lib/changes";
import type { JSONSchema } from "@/lib/schema";
import { submitChanges } from "../changes/api";
import { ChangeDialog } from "../changes/dialog";
import { type ChangeRequest, useChangeFlow } from "../changes/flow";
import { resourceMessages as m } from "../messages";
import { deleteResource, runAction } from "./api";
import {
  actionSummary,
  actionTitle,
  type Kind,
  type KindAction,
  kindNoun,
  kindNouns,
  type Resource,
  resourceTitle,
  useKindSchemas,
} from "./kinds";
import { useKindLabels } from "./labels";
import { SchemaFields } from "./values";

/** A delete, or one of a kind's actions, on one resource or several. */
export interface Launch {
  kind: Kind;
  resources: Resource[];
  /** The action to run; without it, the resources are deleted. */
  action?: KindAction;
  /** Called with the applied change set. */
  onApplied?: (applied: ChangeSet) => void;
}

/** Whether deleting a kind's resources asks for the name to be typed, as for what matters most. */
function typedDelete(kind: Kind): boolean {
  return kind.risks.delete === "high" || kind.risks.delete === "critical";
}

/** Asks for an action's input, with the form its schema makes. */
function ActionInput({
  kind,
  action,
  onSubmit,
  onCancel,
  busy,
  problems,
}: {
  kind: Kind;
  action: KindAction;
  onSubmit: (input: unknown) => void;
  onCancel: () => void;
  busy: boolean;
  problems: ApiError | null;
}) {
  const intl = useIntl();
  const schemas = useKindSchemas(kind.name);
  const schema = schemas.data?.inputs?.[action.name] as JSONSchema | undefined;
  if (!schema) {
    return <Skeleton className="h-32 w-full" />;
  }
  return (
    <SchemaForm
      schema={schema}
      submitLabel={intl.formatMessage(m.preview)}
      busy={busy}
      problemPrefix="input"
      problems={problems?.problem.errors ?? []}
      onSubmit={(result) => onSubmit(result.spec)}
      onCancel={onCancel}
    />
  );
}

/** Builds the change a launch makes: one resource's own operation, or one change set for several. */
function useLaunchRequest() {
  const api = useApi();
  return useCallback(
    ({ kind, resources, action }: Launch, input?: unknown): ChangeRequest => {
      const [only] = resources;
      if (resources.length === 1 && only) {
        return {
          send: action
            ? (options) => runAction(api, kind, only.id, action.name, input, options)
            : (options) => deleteResource(api, kind, only.id, only.revision, options),
        };
      }
      return {
        send: (options) =>
          submitChanges(
            api,
            {
              changes: resources.map((r) =>
                action
                  ? {
                      op: "action" as const,
                      kind: kind.name,
                      id: r.id,
                      action: action.name,
                      ...(input === undefined ? {} : { input }),
                    }
                  : { op: "delete" as const, kind: kind.name, id: r.id, revision: r.revision },
              ),
            },
            options,
          ),
      };
    },
    [api],
  );
}

/**
 * Deletes resources or runs a kind's actions on them through the change
 * preview: the action's input first when it takes one, then what the
 * change does, then applying it. Several resources make one change set,
 * applied and undone together. An action on one resource that answers when
 * done is followed in the dialog until it has, with its result.
 */
export function useResourceChanges(): { launch: (launch: Launch) => void; dialog: ReactNode } {
  const intl = useIntl();
  const labels = useKindLabels();
  const requestFor = useLaunchRequest();
  const [current, setCurrent] = useState<Launch | null>(null);
  const [open, setOpen] = useState(false);
  const [problems, setProblems] = useState<ApiError | null>(null);
  const waits = Boolean(current?.action && !current.action.long && current.resources.length === 1);
  const flow = useChangeFlow({
    onApplied: (applied) => current?.onApplied?.(applied),
    keep: waits,
  });
  const schemas = useKindSchemas(current?.kind.name);

  const launch = useCallback(
    (l: Launch) => {
      setCurrent(l);
      setProblems(null);
      setOpen(true);
      if (l.action?.hasInput) {
        flow.reset();
      } else {
        // What kept the change from planning shows in the dialog.
        flow.preview(requestFor(l)).catch(() => {});
      }
    },
    [flow, requestFor],
  );

  if (!current) {
    return { launch, dialog: null };
  }
  const { kind, resources, action } = current;
  const [only] = resources;
  const name = only ? resourceTitle(kind, only) : "";
  const several = resources.length > 1;
  const title = action
    ? several
      ? intl.formatMessage(m.actionOnSeveral, {
          action: actionTitle(intl, kind, action),
          count: resources.length,
        })
      : actionTitle(intl, kind, action)
    : several
      ? intl.formatMessage(m.deleteSeveral, {
          count: resources.length,
          kinds: kindNouns(intl, kind),
        })
      : intl.formatMessage(m.deleteOne, { kind: kindNoun(intl, kind), name });
  const description = action ? actionSummary(intl, kind, action) : intl.formatMessage(m.deleteHelp);
  const typed = !several && ((!action && typedDelete(kind)) || action?.destructive === true);
  const output = action
    ? (schemas.data?.outputs?.[action.name] as JSONSchema | undefined)
    : undefined;
  const dialog = (
    <ChangeDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setCurrent(null);
        }
      }}
      title={title}
      description={description}
      flow={flow}
      kindLabel={labels.kind}
      resourceName={labels.resourceName}
      {...(typed ? { confirmText: name } : {})}
      form={
        action?.hasInput ? (
          <ActionInput
            kind={kind}
            action={action}
            busy={flow.state.step === "previewing"}
            problems={problems}
            onCancel={() => {
              setOpen(false);
              setCurrent(null);
            }}
            onSubmit={(input) => {
              setProblems(null);
              flow.preview(requestFor(current, input)).catch((error: unknown) => {
                // Problems with the input show under its fields.
                if (error instanceof ApiError && (error.problem.errors?.length ?? 0) > 0) {
                  setProblems(error);
                  flow.reset();
                }
              });
            }}
          />
        ) : undefined
      }
      renderOutput={(change: ResourceChange) => (
        <SchemaFields
          value={change.output}
          schema={output}
          base=""
          context={{ kind, kinds: [], root: output ?? {}, hints: {} }}
        />
      )}
    />
  );
  return { launch, dialog };
}
