import { useQuery } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Badge } from "@/design/components/ui/display";
import { Field, Label } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { useApi } from "@/lib/api/context";
import { type Grant, listAccounts, listRoles, type Role, type Scope } from "./api";
import { t } from "./messages";
import { scopeLabel } from "./util";

const roles: Role[] = ["admin", "reseller", "owner", "collaborator", "readonly"];

/** A role's name for people. */
export function roleName(intl: IntlShape, role: string): string {
  switch (role) {
    case "admin":
      return intl.formatMessage(t.roleAdmin);
    case "reseller":
      return intl.formatMessage(t.roleReseller);
    case "owner":
      return intl.formatMessage(t.roleOwner);
    case "collaborator":
      return intl.formatMessage(t.roleCollaborator);
    case "readonly":
      return intl.formatMessage(t.roleReadonly);
  }
  return role;
}

/** The accounts the person can see, by ID, for naming them. */
export function useAccountNames(): ReadonlyMap<string, string> {
  const api = useApi();
  const accounts = useQuery({
    queryKey: ["accounts", "names"],
    queryFn: () => listAccounts(api, { limit: 500 }),
    staleTime: 60_000,
    retry: false,
  });
  return useMemo(
    () =>
      new Map((accounts.data?.items ?? []).map((a) => [a.id, `${a.spec.name} (${a.spec.slug})`])),
    [accounts.data],
  );
}

/** One grant in a few words: "Account owner of Acme (acme)". */
export function grantText(
  intl: IntlShape,
  grant: Grant,
  names?: ReadonlyMap<string, string>,
): string {
  const where = scopeLabel(intl, grant.scope, names);
  const text = intl.formatMessage(t.grantText, { role: roleName(intl, grant.role), where });
  return grant.object
    ? intl.formatMessage(t.grantObject, { grant: text, object: grant.object })
    : text;
}

/** The roles someone has, and where. */
export function GrantSummary({ grants }: { grants: readonly Grant[] }) {
  const intl = useIntl();
  const names = useAccountNames();
  if (grants.length === 0) {
    return <span className="text-muted-foreground">{intl.formatMessage(t.grantsNone)}</span>;
  }
  return (
    <ul className="flex flex-col gap-1">
      {grants.map((g, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a user's grants have no ID of their own
        <li key={i} className="flex flex-wrap items-center gap-2">
          {grantText(intl, g, names)}
          {(g.permissions ?? []).map((p) => (
            <Badge key={p} variant="outline" className="font-mono">
              {p}
            </Badge>
          ))}
        </li>
      ))}
    </ul>
  );
}

type Level = Scope["level"];

/** The roles and where they may be given, from the panel. */
function useRoleLevels(): Record<string, Level[]> {
  const api = useApi();
  const info = useQuery({ queryKey: ["roles"], queryFn: () => listRoles(api), staleTime: 300_000 });
  return useMemo(() => {
    const out: Record<string, Level[]> = {};
    for (const r of info.data?.roles ?? []) {
      out[r.role] = (r.levels ?? []).filter(
        (l): l is Level => l === "server" || l === "reseller" || l === "account",
      );
    }
    return out;
  }, [info.data]);
}

/** Adding one grant: a role, where it applies, and for collaborators what they may do. */
function NewGrant({ onAdd }: { onAdd: (grant: Grant) => void }) {
  const intl = useIntl();
  const api = useApi();
  const id = useId();
  const levels = useRoleLevels();
  const accounts = useQuery({
    queryKey: ["accounts", "names-list"],
    queryFn: () => listAccounts(api, { limit: 500 }),
    retry: false,
  });
  const [role, setRole] = useState<Role>("owner");
  const allowed = levels[role] ?? ["server", "reseller", "account"];
  const [level, setLevel] = useState<Level>("account");
  const where: Level = allowed.includes(level) ? level : (allowed[0] ?? "server");
  const [target, setTarget] = useState("");
  const [object, setObject] = useState("");
  const [permissions, setPermissions] = useState("");
  const list = (text: string) =>
    text
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
  const ready = where === "server" || target.trim() !== "";
  // Collaborators and read-only users can be given single resources.
  const single = role === "collaborator" || role === "readonly";
  const add = () => {
    const grant: Grant = {
      role,
      scope: where === "server" ? { level: "server" } : { level: where, id: target.trim() },
    };
    if (single && object.trim()) {
      grant.object = object.trim();
    }
    if (list(permissions).length > 0) {
      grant.permissions = list(permissions);
    }
    onAdd(grant);
    setTarget("");
    setObject("");
    setPermissions("");
  };
  return (
    <fieldset className="flex flex-col gap-3 rounded-md border border-border border-dashed p-4">
      <legend className="px-1 font-medium text-sm">{intl.formatMessage(t.grantAdd)}</legend>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={intl.formatMessage(t.grantRole)}>
          <Select value={role} onValueChange={(v) => setRole(v as Role)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {roles.map((r) => (
                <SelectItem key={r} value={r}>
                  {roleName(intl, r)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={intl.formatMessage(t.grantWhere)}>
          <Select value={where} onValueChange={(v) => setLevel(v as Level)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {allowed.map((l) => (
                <SelectItem key={l} value={l}>
                  {intl.formatMessage(
                    l === "server"
                      ? t.levelServer
                      : l === "reseller"
                        ? t.levelReseller
                        : t.levelAccount,
                  )}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {where === "account" ? (
          <Field label={intl.formatMessage(t.grantAccount)}>
            {(accounts.data?.items ?? []).length > 0 ? (
              <Select value={target} onValueChange={setTarget}>
                <SelectTrigger>
                  <SelectValue placeholder={intl.formatMessage(t.grantChooseAccount)} />
                </SelectTrigger>
                <SelectContent>
                  {(accounts.data?.items ?? []).map((a) => (
                    <SelectItem key={a.id} value={a.id}>
                      {a.spec.name} ({a.spec.slug})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Input
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                placeholder="acct_…"
                spellCheck={false}
              />
            )}
          </Field>
        ) : null}
        {where === "reseller" ? (
          <Field label={intl.formatMessage(t.grantReseller)}>
            <Input value={target} onChange={(e) => setTarget(e.target.value)} spellCheck={false} />
          </Field>
        ) : null}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {role === "collaborator" ? (
          <Field
            label={intl.formatMessage(t.grantPermissions)}
            description={intl.formatMessage(t.grantPermissionsHelp)}
          >
            <Input
              value={permissions}
              onChange={(e) => setPermissions(e.target.value)}
              spellCheck={false}
            />
          </Field>
        ) : (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-special`}>{intl.formatMessage(t.grantSpecial)}</Label>
            <p className="text-muted-foreground text-xs">
              {intl.formatMessage(t.grantSpecialHelp)}
            </p>
            <Input
              id={`${id}-special`}
              value={permissions}
              onChange={(e) => setPermissions(e.target.value)}
              spellCheck={false}
            />
          </div>
        )}
        {single ? (
          <Field
            label={intl.formatMessage(t.grantOnly)}
            description={intl.formatMessage(t.grantOnlyHelp)}
          >
            <Input value={object} onChange={(e) => setObject(e.target.value)} spellCheck={false} />
          </Field>
        ) : null}
      </div>
      <Button variant="outline" className="self-start" onClick={add} disabled={!ready}>
        <Plus aria-hidden="true" />
        {intl.formatMessage(t.grantAddButton)}
      </Button>
    </fieldset>
  );
}

/** The grants of a user being made or changed: each with a button to take it away, and a way to add one. */
export function GrantEditor({
  value,
  onChange,
}: {
  value: Grant[];
  onChange: (grants: Grant[]) => void;
}) {
  const intl = useIntl();
  const names = useAccountNames();
  return (
    <div className="flex flex-col gap-3">
      {value.length > 0 ? (
        <ul
          aria-label={intl.formatMessage(t.grantsTitle)}
          className="flex flex-col divide-y divide-border rounded-md border border-border"
        >
          {value.map((g, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a user's grants have no ID of their own
            <li key={i} className="flex items-center gap-3 px-3 py-2">
              <span className="flex min-w-0 flex-1 flex-wrap items-center gap-2 text-sm">
                {grantText(intl, g, names)}
                {(g.permissions ?? []).map((p) => (
                  <Badge key={p} variant="outline" className="font-mono">
                    {p}
                  </Badge>
                ))}
              </span>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={intl.formatMessage(t.grantRemove, { grant: grantText(intl, g, names) })}
                onClick={() => onChange(value.filter((_, j) => j !== i))}
              >
                <Trash2 aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground text-sm">{intl.formatMessage(t.grantsNone)}</p>
      )}
      <NewGrant onAdd={(g) => onChange([...value, g])} />
    </div>
  );
}
