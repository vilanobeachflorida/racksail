import { useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { type FormResult, SchemaForm } from "@/design/components/schema-form";
import { Button } from "@/design/components/ui/button";
import { Checkbox } from "@/design/components/ui/choice";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { Badge } from "@/design/components/ui/display";
import { Field, Label } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import { problemMessage } from "@/lib/errors";
import type { UIHints } from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import { usePermission } from "../../modules";
import {
  createSigninPolicy,
  createSigninProvider,
  deleteSigninProvider,
  getKind,
  listSigninPolicies,
  listSigninProviders,
  type PolicySpec,
  type ProviderSpec,
  type Role,
  type SigninProvider,
  updateSigninPolicy,
  updateSigninProvider,
} from "../api";
import { Item, ItemList, ListSkeleton, PageHeader, Section } from "../components";
import { roleName } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { PreviewDialog } from "../preview";

/** The policy of a server that hasn't set one, as identity.DefaultPolicy says. */
const defaults: PolicySpec = {
  sessionIdleMinutes: 60,
  sessionLifetimeHours: 12,
  rememberDeviceDays: 30,
  stepUpMinutes: 10,
  lockoutAttempts: 10,
  lockoutMinutes: 15,
  signInAlerts: "new-device",
};

const roles: Role[] = ["admin", "reseller", "owner", "collaborator", "readonly"];

function NumberField({
  label,
  help,
  value,
  min,
  max,
  onChange,
  disabled,
}: {
  label: string;
  help: string;
  value: number;
  min: number;
  max: number;
  onChange: (value: number) => void;
  disabled: boolean;
}) {
  return (
    <Field label={label} description={help}>
      <Input
        type="number"
        inputMode="numeric"
        min={min}
        max={max}
        value={Number.isFinite(value) ? value : ""}
        onChange={(e) => onChange(e.target.valueAsNumber)}
        required
        disabled={disabled}
        className="max-w-40"
      />
    </Field>
  );
}

function Policy() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const can = usePermission();
  const policies = useQuery({
    queryKey: ["signin-policy"],
    queryFn: () => listSigninPolicies(api),
  });
  const current = policies.data?.[0];
  const [spec, setSpec] = useState<PolicySpec>(defaults);
  const [allowFrom, setAllowFrom] = useState("");
  const [previewing, setPreviewing] = useState(false);
  useEffect(() => {
    const s = { ...defaults, ...(current?.spec ?? {}) };
    setSpec(s);
    setAllowFrom((s.allowFrom ?? []).join("\n"));
  }, [current]);
  const editable = can(current ? "core.signin_policy.update" : "core.signin_policy.create");
  if (policies.isPending) {
    return <ListSkeleton rows={4} />;
  }
  if (policies.isError) {
    return (
      <ErrorState
        title={intl.formatMessage(t.policyFailed)}
        error={policies.error}
        onRetry={() => void policies.refetch()}
      />
    );
  }
  const full = (): PolicySpec => ({
    ...spec,
    allowFrom: allowFrom.split(/[\s,]+/).filter(Boolean),
    requireTwoFactor: spec.requireTwoFactor ?? [],
  });
  const send = (dryRun: boolean) =>
    current
      ? updateSigninPolicy(api, current.id, current.revision, full(), dryRun)
      : createSigninPolicy(api, full(), dryRun);
  const set = (key: keyof PolicySpec) => (value: number) => setSpec({ ...spec, [key]: value });
  const required = spec.requireTwoFactor ?? [];
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setPreviewing(true);
  };
  return (
    <form onSubmit={submit} className="flex flex-col gap-6">
      <Section
        title={intl.formatMessage(t.policySessions)}
        description={intl.formatMessage(t.policySessionsDescription)}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <NumberField
            label={intl.formatMessage(t.policyIdle)}
            help={intl.formatMessage(t.policyIdleHelp)}
            value={spec.sessionIdleMinutes}
            min={5}
            max={1440}
            onChange={set("sessionIdleMinutes")}
            disabled={!editable}
          />
          <NumberField
            label={intl.formatMessage(t.policyLifetime)}
            help={intl.formatMessage(t.policyLifetimeHelp)}
            value={spec.sessionLifetimeHours}
            min={1}
            max={720}
            onChange={set("sessionLifetimeHours")}
            disabled={!editable}
          />
          <NumberField
            label={intl.formatMessage(t.policyRemember)}
            help={intl.formatMessage(t.policyRememberHelp)}
            value={spec.rememberDeviceDays}
            min={0}
            max={365}
            onChange={set("rememberDeviceDays")}
            disabled={!editable}
          />
          <NumberField
            label={intl.formatMessage(t.policyStepUp)}
            help={intl.formatMessage(t.policyStepUpHelp)}
            value={spec.stepUpMinutes}
            min={1}
            max={120}
            onChange={set("stepUpMinutes")}
            disabled={!editable}
          />
        </div>
      </Section>
      <Section
        title={intl.formatMessage(t.policyLockouts)}
        description={intl.formatMessage(t.policyLockoutsDescription)}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <NumberField
            label={intl.formatMessage(t.policyAttempts)}
            help={intl.formatMessage(t.policyAttemptsHelp)}
            value={spec.lockoutAttempts}
            min={3}
            max={100}
            onChange={set("lockoutAttempts")}
            disabled={!editable}
          />
          <NumberField
            label={intl.formatMessage(t.policyLockout)}
            help={intl.formatMessage(t.policyLockoutHelp)}
            value={spec.lockoutMinutes}
            min={1}
            max={1440}
            onChange={set("lockoutMinutes")}
            disabled={!editable}
          />
        </div>
      </Section>
      <Section
        title={intl.formatMessage(t.policyAccess)}
        description={intl.formatMessage(t.policyAccessDescription)}
      >
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 font-medium text-sm">
            {intl.formatMessage(t.policyRequire)}
          </legend>
          <p className="text-muted-foreground text-xs">{intl.formatMessage(t.policyRequireHelp)}</p>
          <div className="flex flex-wrap gap-x-6 gap-y-2">
            {roles.map((r) => (
              <div key={r} className="flex items-center gap-2">
                <Checkbox
                  id={`require-${r}`}
                  checked={required.includes(r)}
                  disabled={!editable}
                  onCheckedChange={(v) =>
                    setSpec({
                      ...spec,
                      requireTwoFactor:
                        v === true ? [...required, r] : required.filter((x) => x !== r),
                    })
                  }
                />
                <Label htmlFor={`require-${r}`} className="font-normal">
                  {roleName(intl, r)}
                </Label>
              </div>
            ))}
          </div>
        </fieldset>
        <Field
          label={intl.formatMessage(t.policyAllowFrom)}
          description={intl.formatMessage(t.policyAllowFromHelp)}
        >
          <Textarea
            value={allowFrom}
            onChange={(e) => setAllowFrom(e.target.value)}
            spellCheck={false}
            disabled={!editable}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={intl.formatMessage(t.policyAlerts)}
            description={intl.formatMessage(t.policyAlertsHelp)}
          >
            <Select
              value={spec.signInAlerts ?? "new-device"}
              onValueChange={(v) =>
                setSpec({ ...spec, signInAlerts: v as PolicySpec["signInAlerts"] })
              }
              disabled={!editable}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="new-device">
                  {intl.formatMessage(t.policyAlertsNewDevice)}
                </SelectItem>
                <SelectItem value="every">{intl.formatMessage(t.policyAlertsEvery)}</SelectItem>
                <SelectItem value="off">{intl.formatMessage(t.policyAlertsOff)}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <NumberField
            label={intl.formatMessage(t.policyTokenDays)}
            help={intl.formatMessage(t.policyTokenDaysHelp)}
            value={spec.tokenMaxDays ?? 0}
            min={0}
            max={3650}
            onChange={set("tokenMaxDays")}
            disabled={!editable}
          />
        </div>
      </Section>
      {editable ? (
        <Button type="submit" className="self-start">
          {intl.formatMessage(t.previewButton)}
        </Button>
      ) : null}
      {previewing ? (
        <PreviewDialog
          open
          title={intl.formatMessage(t.policyChangeTitle)}
          preview={() => send(true)}
          apply={() => send(false)}
          onApplied={() => {
            setPreviewing(false);
            toast.success(intl.formatMessage(t.policySaved));
            void client.invalidateQueries({ queryKey: ["signin-policy"] });
          }}
          onClose={() => setPreviewing(false)}
        />
      ) : null}
    </form>
  );
}

interface ProviderForm {
  schema: JSONSchema;
  hints: UIHints;
}

function ProviderEditor({
  provider,
  form,
  onDone,
  onClose,
}: {
  provider: SigninProvider | null;
  form: ProviderForm;
  onDone: () => void;
  onClose: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const [result, setResult] = useState<FormResult | null>(null);
  const send = (dryRun: boolean) => {
    const spec = result?.spec as ProviderSpec;
    const secrets = result?.secrets ?? {};
    return provider
      ? updateSigninProvider(api, provider.id, provider.revision, spec, secrets, dryRun)
      : createSigninProvider(api, spec, secrets, dryRun);
  };
  return (
    <>
      <SchemaForm
        schema={form.schema}
        hints={form.hints}
        mode={provider ? "update" : "create"}
        {...(provider ? { initialValue: provider.spec } : {})}
        submitLabel={intl.formatMessage(t.previewButton)}
        onSubmit={setResult}
        onCancel={onClose}
      />
      {result ? (
        <PreviewDialog
          open
          title={intl.formatMessage(provider ? t.providerChangeTitle : t.providerAddTitle)}
          preview={() => send(true)}
          apply={() => send(false)}
          onApplied={() => {
            setResult(null);
            onDone();
          }}
          onClose={() => setResult(null)}
        />
      ) : null}
    </>
  );
}

/**
 * Why a provider can't sign anyone in, when its status says nothing on the
 * panel provides what it names any more, as when its add-on was removed.
 */
export function providerProblem(intl: IntlShape, p: SigninProvider): string | null {
  const status = p.status as { code?: string; message?: string } | undefined;
  if (status?.code !== missingProvider) {
    return null;
  }
  return problemMessage(intl, {
    type: "about:blank",
    code: missingProvider,
    params: { provider: p.spec.provider },
    ...(status.message ? { detail: status.message } : {}),
  });
}

const missingProvider = "auth.provider_missing";

function Providers() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const can = usePermission();
  const providers = useQuery({
    queryKey: ["signin-providers"],
    queryFn: () => listSigninProviders(api),
  });
  const kind = useQuery({
    queryKey: ["kinds", "core.signin_provider"],
    queryFn: () => getKind(api, "core.signin_provider"),
    staleTime: 300_000,
  });
  const [editing, setEditing] = useState<SigninProvider | "new" | null>(null);
  const [deleting, setDeleting] = useState<SigninProvider | null>(null);
  const reload = () => client.invalidateQueries({ queryKey: ["signin-providers"] });
  const form: ProviderForm | null = kind.data
    ? { schema: kind.data.spec as JSONSchema, hints: (kind.data.ui ?? {}) as UIHints }
    : null;
  const add =
    can("core.signin_provider.create") && form ? (
      <Button onClick={() => setEditing("new")}>
        <Plus aria-hidden="true" />
        {intl.formatMessage(t.providerAdd)}
      </Button>
    ) : null;
  return (
    <Section
      title={intl.formatMessage(t.providersTitle)}
      description={intl.formatMessage(t.providersDescription)}
      actions={add}
    >
      {providers.isPending ? <ListSkeleton rows={2} /> : null}
      {providers.isError ? (
        <ErrorState
          title={intl.formatMessage(t.providersFailed)}
          error={providers.error}
          onRetry={() => void providers.refetch()}
        />
      ) : null}
      {providers.data?.length === 0 ? (
        <p className="text-muted-foreground">{intl.formatMessage(t.providersNone)}</p>
      ) : null}
      {providers.data && providers.data.length > 0 ? (
        <ItemList label={intl.formatMessage(t.providersTitle)}>
          {providers.data.map((p) => {
            const problem = providerProblem(intl, p);
            return (
              <Item
                key={p.id}
                icon={<KeyRound aria-hidden="true" />}
                title={p.spec.title}
                badge={
                  <>
                    <Badge variant="outline">
                      {intl.formatMessage(
                        p.spec.capability === "mfa" ? t.providerSecondStep : t.providerSignIn,
                      )}
                    </Badge>
                    {p.spec.disabled ? (
                      <Badge variant="neutral">{intl.formatMessage(t.stateOff)}</Badge>
                    ) : null}
                    {problem ? (
                      <Badge variant="warning">{intl.formatMessage(t.providerMissing)}</Badge>
                    ) : null}
                  </>
                }
                meta={problem ?? p.spec.provider}
                actions={
                  <>
                    {can("core.signin_provider.update") && form ? (
                      <Button variant="ghost" size="sm" onClick={() => setEditing(p)}>
                        {intl.formatMessage(t.edit)}
                      </Button>
                    ) : null}
                    {can("core.signin_provider.delete") ? (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={intl.formatMessage(t.providerRemove, { name: p.spec.title })}
                        onClick={() => setDeleting(p)}
                      >
                        <Trash2 aria-hidden="true" />
                      </Button>
                    ) : null}
                  </>
                }
              />
            );
          })}
        </ItemList>
      ) : null}
      <Dialog
        open={editing !== null}
        onOpenChange={(open) => (open ? undefined : setEditing(null))}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {intl.formatMessage(editing === "new" ? t.providerAddTitle : t.providerChangeTitle)}
            </DialogTitle>
            <DialogDescription>{intl.formatMessage(t.providersDescription)}</DialogDescription>
          </DialogHeader>
          {editing !== null && form ? (
            <ProviderEditor
              provider={editing === "new" ? null : editing}
              form={form}
              onDone={() => {
                setEditing(null);
                toast.success(intl.formatMessage(t.providerSaved));
                void reload();
              }}
              onClose={() => setEditing(null)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
      {deleting ? (
        <PreviewDialog
          open
          title={intl.formatMessage(t.providerRemove, { name: deleting.spec.title })}
          preview={() => deleteSigninProvider(api, deleting.id, deleting.revision, true)}
          apply={() => deleteSigninProvider(api, deleting.id, deleting.revision, false)}
          onApplied={() => {
            setDeleting(null);
            void reload();
          }}
          onClose={() => setDeleting(null)}
        />
      ) : null}
    </Section>
  );
}

function SignInSettings() {
  const intl = useIntl();
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.signInSettingsTitle)}
        description={intl.formatMessage(t.signInSettingsDescription)}
      />
      <Policy />
      <Providers />
    </div>
  );
}

/** How people sign in to the panel: sessions, lockouts, second steps, addresses and providers. */
export default function SignInSettingsPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={4} />}>
      <SignInSettings />
    </IdentityMessages>
  );
}
