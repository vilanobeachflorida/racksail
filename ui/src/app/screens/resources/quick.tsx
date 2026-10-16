import { useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useIntl } from "react-intl";
import { messages } from "@/app/messages";
import { moduleCommands, useActiveModules, usePermission } from "@/app/modules";
import { Button } from "@/design/components/ui/button";
import { ScreenMessages } from "../catalog";
import { kindNoun, kindPath } from "./kinds";
import { useKindsFor } from "./operations";

/**
 * Starts new resources of the kinds the person may create, up to count:
 * those of modules that are on and offer no commands of their own, since
 * such a module starts its resources itself, and the panel's own.
 */
function NewResources({ count }: { count: number }) {
  const intl = useIntl();
  const navigate = useNavigate();
  const allowed = usePermission();
  const modules = useActiveModules();
  const commanding = new Set<string>(moduleCommands(modules, allowed).map((c) => c.module.id));
  const kinds = useKindsFor("create")
    .filter((k) => !commanding.has(k.module))
    .slice(0, count);
  return (
    <>
      {kinds.map((kind) => (
        <Button
          key={kind.name}
          variant="outline"
          className="justify-start"
          onClick={() => void navigate({ to: `${kindPath(kind)}/new` })}
        >
          <Plus aria-hidden="true" />
          {intl.formatMessage(messages.quickActionsCreate, { kind: kindNoun(intl, kind) })}
        </Button>
      ))}
    </>
  );
}

/**
 * The home screen's quick actions for kinds of resources, after what modules
 * offer. The core's kinds' words load with them, and nothing shows until
 * they have.
 */
export default function NewResourceActions({ count }: { count: number }) {
  if (count <= 0) {
    return null;
  }
  return (
    <ScreenMessages catalog="kinds" fallback={null} optional>
      <NewResources count={count} />
    </ScreenMessages>
  );
}
