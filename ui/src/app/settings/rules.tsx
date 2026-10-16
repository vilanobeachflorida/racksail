import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { ScrollText, Webhook } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { type MessageDescriptor, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/design/components/ui/card";
import { Switch } from "@/design/components/ui/choice";
import { Alert, Badge } from "@/design/components/ui/display";
import { Label } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/design/components/ui/table";
import { toast } from "@/design/components/ui/toaster";
import type { ApiClient } from "@/lib/api/client";
import { type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { addonEntriesKey } from "../addons";
import { messages } from "../messages";
import { ruleSettingsMessages as m } from "../screens/messages";
import { ChangeDialog, useChangeFlow } from "./change-flow";
import { useResourceUpdates, useTimelineUpdates } from "./live";

type PolicySpec = Schemas["PolicySpec"];
type TestResult = Schemas["TestResult"];
type HookInfo = Schemas["HookInfo"];
type SafeModeSource = Schemas["SafeModeSource"];
type Scope = Schemas["Scope"];
type Level = Scope["level"];

/** Whose rules are shown and saved: the server's, a reseller's or an account's. */
interface Owner {
  level: Level;
  /** The reseller's or account's ID. */
  id: string;
}

const levels: Level[] = ["server", "reseller", "account"];

function scopeOf(owner: Owner): Scope {
  return owner.level === "server"
    ? { level: "server" }
    : { level: owner.level, id: owner.id.trim() };
}

function complete(owner: Owner): boolean {
  return owner.level === "server" || owner.id.trim() !== "";
}

/** A rule of any owner, as the list shows it. */
interface Rule {
  id: string;
  revision: number;
  spec: PolicySpec;
}

// One stream tells the whole screen about rules and add-ons, whose hooks
// come and go with them.
const watchedKinds = [
  "core.policy",
  "core.reseller_policy",
  "core.account_policy",
  "core.addon",
  "core.account_addon",
];

/** The rules an owner has; each owner's are a kind of their own. */
async function listRules(api: ApiClient, owner: Owner): Promise<Rule[]> {
  const scope = `${owner.level}:${owner.id.trim()}`;
  switch (owner.level) {
    case "reseller":
      return (
        (await unwrap(api.GET("/api/v1/core/reseller-policies", { params: { query: { scope } } })))
          .items ?? []
      );
    case "account":
      return (
        (await unwrap(api.GET("/api/v1/core/account-policies", { params: { query: { scope } } })))
          .items ?? []
      );
    default:
      return (await unwrap(api.GET("/api/v1/core/policies"))).items ?? [];
  }
}

/** Saves a new rule for an owner, as a preview when dryRun is set. */
function createRule(api: ApiClient, owner: Owner, spec: PolicySpec, dryRun: boolean) {
  const params = { params: { query: { dryRun } } };
  switch (owner.level) {
    case "reseller":
      return unwrap(
        api.POST("/api/v1/core/reseller-policies", {
          ...params,
          body: { spec, scope: scopeOf(owner) },
        }),
      );
    case "account":
      return unwrap(
        api.POST("/api/v1/core/account-policies", {
          ...params,
          body: { spec, scope: scopeOf(owner) },
        }),
      );
    default:
      return unwrap(api.POST("/api/v1/core/policies", { ...params, body: { spec } }));
  }
}

/** Changes an owner's rule, as a preview when dryRun is set. */
function updateRule(api: ApiClient, owner: Owner, rule: Rule, spec: PolicySpec, dryRun: boolean) {
  const params = { params: { path: { id: rule.id }, query: { dryRun } } };
  const body = { spec, revision: rule.revision };
  switch (owner.level) {
    case "reseller":
      return unwrap(api.PUT("/api/v1/core/reseller-policies/{id}", { ...params, body }));
    case "account":
      return unwrap(api.PUT("/api/v1/core/account-policies/{id}", { ...params, body }));
    default:
      return unwrap(api.PUT("/api/v1/core/policies/{id}", { ...params, body }));
  }
}

/** Picks an owner: the server, or a reseller or an account by its ID. */
function OwnerFields({
  id,
  label,
  idLabel = m.rulesOwnerId,
  value,
  onChange,
}: {
  id: string;
  label: string;
  /** Names the field of the reseller's or account's ID, by the level chosen. */
  idLabel?: MessageDescriptor;
  value: Owner;
  onChange: (owner: Owner) => void;
}) {
  const intl = useIntl();
  return (
    <div className="flex flex-wrap items-end gap-3">
      <div className="flex min-w-48 flex-col gap-1.5">
        <Label htmlFor={`${id}-level`}>{label}</Label>
        <Select
          value={value.level}
          onValueChange={(v) => onChange({ ...value, level: v as Level })}
        >
          <SelectTrigger id={`${id}-level`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {levels.map((level) => (
              <SelectItem key={level} value={level}>
                {intl.formatMessage(m.rulesOwnerValue, { level })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {value.level !== "server" ? (
        <div className="flex min-w-60 flex-1 flex-col gap-1.5">
          <Label htmlFor={`${id}-id`}>{intl.formatMessage(idLabel, { level: value.level })}</Label>
          <Input
            id={`${id}-id`}
            className="font-mono"
            placeholder={value.level === "reseller" ? "rsl_..." : "acct_..."}
            value={value.id}
            onChange={(e) => onChange({ ...value, id: e.target.value })}
          />
        </div>
      ) : null}
    </div>
  );
}

/** Splits a comma-separated list, leaving out empty entries. */
function list(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** Reads "field = expression" lines as default rules' fields. */
function defaults(text: string): Schemas["FieldDefault"][] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .map((line) => {
      const at = line.indexOf("=");
      return at < 0
        ? { field: line, expression: "" }
        : { field: line.slice(0, at).trim(), expression: line.slice(at + 1).trim() };
    });
}

function parseJSON(text: string): unknown {
  return text.trim() === "" ? undefined : JSON.parse(text);
}

function RuleList({ owner }: { owner: Owner }) {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow();
  const ready = complete(owner);
  const rules = useQuery({
    queryKey: ["rules", owner.level, owner.id.trim()],
    queryFn: () => listRules(api, owner),
    enabled: ready,
  });
  useResourceUpdates(watchedKinds, [["rules"]]);
  const toggle = (rule: Rule) =>
    flow.preview((dryRun) =>
      updateRule(api, owner, rule, { ...rule.spec, enabled: !rule.spec.enabled }, dryRun),
    );
  if (rules.isError) {
    return <ErrorState title={intl.formatMessage(m.rulesLoadFailed)} error={rules.error} />;
  }
  const items = rules.data ?? [];
  return (
    <Card aria-labelledby="rules-list">
      <CardHeader>
        <CardTitle id="rules-list">
          {intl.formatMessage(m.rulesListTitle, { level: owner.level })}
        </CardTitle>
        <CardDescription>
          {intl.formatMessage(m.rulesListHelp, { level: owner.level })}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {!ready ? (
          <p className="text-muted-foreground text-sm">
            {intl.formatMessage(m.rulesOwnerMissing, { level: owner.level })}
          </p>
        ) : rules.isPending ? (
          <div className="flex flex-col gap-2" aria-busy="true">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : items.length === 0 ? (
          <EmptyState icon={ScrollText} title={intl.formatMessage(m.rulesNone)} />
        ) : (
          <Table aria-labelledby="rules-list">
            <TableHeader>
              <TableRow>
                <TableHead>{intl.formatMessage(m.rulesName)}</TableHead>
                <TableHead>{intl.formatMessage(m.rulesType)}</TableHead>
                <TableHead>{intl.formatMessage(m.rulesKinds)}</TableHead>
                <TableHead>{intl.formatMessage(m.rulesEnabled)}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((rule) => (
                <TableRow key={rule.id}>
                  <TableCell className="font-medium">{rule.spec.name}</TableCell>
                  <TableCell>
                    <Badge variant="outline">
                      {intl.formatMessage(m.rulesTypeValue, { type: rule.spec.type })}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    {(rule.spec.kinds ?? []).length > 0
                      ? intl.formatList(rule.spec.kinds ?? [], { type: "conjunction" })
                      : intl.formatMessage(m.rulesEveryKind)}
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={rule.spec.enabled ?? false}
                      disabled={flow.busy}
                      aria-label={intl.formatMessage(m.rulesToggle, { name: rule.spec.name })}
                      onCheckedChange={() => toggle(rule)}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

function Result({ result }: { result: TestResult }) {
  const intl = useIntl();
  return (
    <section aria-labelledby="rules-result" className="flex flex-col gap-3">
      <h3 id="rules-result" className="font-semibold">
        {intl.formatMessage(m.rulesResult)}
      </h3>
      {result.error ? (
        <Alert tone="danger" title={intl.formatMessage(m.rulesResultError)}>
          {result.error}
        </Alert>
      ) : !result.applies ? (
        <Alert tone="info" title={intl.formatMessage(m.rulesResultSkipped)} />
      ) : result.refused ? (
        <Alert tone="warning" title={intl.formatMessage(m.rulesResultRefused)}>
          {result.message}
        </Alert>
      ) : (
        <Alert tone="success" title={intl.formatMessage(m.rulesResultAllowed)} />
      )}
      {(result.fields ?? []).length > 0 ? (
        <ul className="flex flex-col gap-1 text-sm">
          {(result.fields ?? []).map((f) => (
            <li key={f.field}>
              {intl.formatMessage(f.set ? m.rulesFieldSet : m.rulesFieldKept, {
                field: (
                  <code key="field" className="font-mono text-xs">
                    {f.field}
                  </code>
                ),
                value: (
                  <code key="value" className="font-mono text-xs">
                    {JSON.stringify(f.value)}
                  </code>
                ),
              })}
            </li>
          ))}
        </ul>
      ) : null}
      <p className="text-muted-foreground text-sm">
        {intl.formatMessage(m.rulesCost, { cost: result.cost, limit: result.costLimit })}
      </p>
      {(result.points ?? []).length > 0 ? (
        <p className="text-muted-foreground text-sm">
          {intl.formatMessage(m.rulesPoints, {
            points: intl.formatList(result.points ?? [], { type: "conjunction" }),
          })}
        </p>
      ) : null}
    </section>
  );
}

function Tester({ owner }: { owner: Owner }) {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow();
  const id = useId();
  // Whose resource the change is about; the rule's owner's own by default.
  const [resourceOwner, setResourceOwner] = useState<Owner | null>(null);
  const belongsTo = resourceOwner ?? owner;
  const [rule, setRule] = useState({
    name: "",
    type: "validate" as PolicySpec["type"],
    kinds: "",
    operations: "create",
    expression: "",
    message: "",
    defaults: "",
  });
  const [change, setChange] = useState({ kind: "", operation: "create", spec: "{}", oldSpec: "" });
  const [result, setResult] = useState<TestResult | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const spec = (): PolicySpec => ({
    name: rule.name,
    type: rule.type,
    kinds: list(rule.kinds),
    operations: list(rule.operations),
    expression: rule.type === "validate" ? rule.expression : undefined,
    message: rule.type === "validate" ? rule.message : undefined,
    defaults: rule.type === "default" ? defaults(rule.defaults) : undefined,
    enabled: true,
  });

  const test = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setProblem(null);
    try {
      const out = await unwrap(
        api.POST("/api/v1/rules/test", {
          body: {
            rule: spec(),
            owner: scopeOf(owner),
            operation: change.operation,
            kind: change.kind,
            scope: scopeOf(belongsTo),
            spec: parseJSON(change.spec),
            oldSpec: parseJSON(change.oldSpec),
          },
        }),
      );
      setResult(out);
    } catch (error) {
      setResult(null);
      setProblem(
        error instanceof SyntaxError
          ? intl.formatMessage(m.rulesBadJSON)
          : errorMessage(intl, error),
      );
    } finally {
      setBusy(false);
    }
  };

  const save = () => flow.preview((dryRun) => createRule(api, owner, spec(), dryRun));

  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.rulesTesterTitle)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.rulesTesterHelp)}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-4" onSubmit={test}>
          <fieldset className="grid gap-3 md:grid-cols-2">
            <legend className="mb-2 font-semibold">{intl.formatMessage(m.rulesRule)}</legend>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-name`}>{intl.formatMessage(m.rulesName)}</Label>
              <Input
                id={`${id}-name`}
                required
                value={rule.name}
                onChange={(e) => setRule({ ...rule, name: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-type`}>{intl.formatMessage(m.rulesType)}</Label>
              <Select
                value={rule.type}
                onValueChange={(v) => setRule({ ...rule, type: v as PolicySpec["type"] })}
              >
                <SelectTrigger id={`${id}-type`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="validate">
                    {intl.formatMessage(m.rulesTypeValue, { type: "validate" })}
                  </SelectItem>
                  <SelectItem value="default">
                    {intl.formatMessage(m.rulesTypeValue, { type: "default" })}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-kinds`}>{intl.formatMessage(m.rulesKinds)}</Label>
              <Input
                id={`${id}-kinds`}
                placeholder="web.site"
                value={rule.kinds}
                onChange={(e) => setRule({ ...rule, kinds: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-ops`}>{intl.formatMessage(m.rulesOperations)}</Label>
              <Input
                id={`${id}-ops`}
                value={rule.operations}
                onChange={(e) => setRule({ ...rule, operations: e.target.value })}
              />
            </div>
            {rule.type === "validate" ? (
              <>
                <div className="flex flex-col gap-1.5 md:col-span-2">
                  <Label htmlFor={`${id}-expr`}>{intl.formatMessage(m.rulesExpression)}</Label>
                  <Textarea
                    id={`${id}-expr`}
                    className="font-mono"
                    value={rule.expression}
                    onChange={(e) => setRule({ ...rule, expression: e.target.value })}
                  />
                  <p className="text-muted-foreground text-xs">
                    {intl.formatMessage(m.rulesExpressionHelp)}
                  </p>
                </div>
                <div className="flex flex-col gap-1.5 md:col-span-2">
                  <Label htmlFor={`${id}-message`}>{intl.formatMessage(m.rulesMessage)}</Label>
                  <Input
                    id={`${id}-message`}
                    value={rule.message}
                    onChange={(e) => setRule({ ...rule, message: e.target.value })}
                  />
                </div>
              </>
            ) : (
              <div className="flex flex-col gap-1.5 md:col-span-2">
                <Label htmlFor={`${id}-defaults`}>{intl.formatMessage(m.rulesDefaults)}</Label>
                <Textarea
                  id={`${id}-defaults`}
                  className="font-mono"
                  value={rule.defaults}
                  onChange={(e) => setRule({ ...rule, defaults: e.target.value })}
                />
                <p className="text-muted-foreground text-xs">
                  {intl.formatMessage(m.rulesDefaultsHelp)}
                </p>
              </div>
            )}
          </fieldset>
          <fieldset className="grid gap-3 md:grid-cols-2">
            <legend className="mb-2 font-semibold">{intl.formatMessage(m.rulesChange)}</legend>
            <div className="md:col-span-2">
              <OwnerFields
                id={`${id}-scope`}
                label={intl.formatMessage(m.rulesChangeScope)}
                idLabel={m.rulesChangeScopeId}
                value={belongsTo}
                onChange={setResourceOwner}
              />
              <p className="mt-1 text-muted-foreground text-xs">
                {intl.formatMessage(m.rulesChangeScopeHelp)}
              </p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-kind`}>{intl.formatMessage(m.rulesChangeKind)}</Label>
              <Input
                id={`${id}-kind`}
                required
                placeholder="web.site"
                value={change.kind}
                onChange={(e) => setChange({ ...change, kind: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-op`}>{intl.formatMessage(m.rulesChangeOperation)}</Label>
              <Input
                id={`${id}-op`}
                required
                value={change.operation}
                onChange={(e) => setChange({ ...change, operation: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5 md:col-span-2">
              <Label htmlFor={`${id}-spec`}>{intl.formatMessage(m.rulesChangeSpec)}</Label>
              <Textarea
                id={`${id}-spec`}
                className="font-mono"
                value={change.spec}
                onChange={(e) => setChange({ ...change, spec: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5 md:col-span-2">
              <Label htmlFor={`${id}-old`}>{intl.formatMessage(m.rulesChangeOldSpec)}</Label>
              <Textarea
                id={`${id}-old`}
                className="font-mono"
                value={change.oldSpec}
                onChange={(e) => setChange({ ...change, oldSpec: e.target.value })}
              />
            </div>
          </fieldset>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" busy={busy} disabled={!complete(owner) || !complete(belongsTo)}>
              {intl.formatMessage(m.rulesTry)}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={rule.name === "" || flow.busy || !complete(owner)}
              onClick={save}
            >
              {intl.formatMessage(m.rulesSave, { level: owner.level })}
            </Button>
          </div>
          {problem ? <Alert tone="danger" title={problem} /> : null}
          {result ? <Result result={result} /> : null}
        </form>
      </CardContent>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

/** The hooks attached, in the order they run, and switching one the breaker turned off back on. */
function Hooks() {
  const intl = useIntl();
  const api = useApi();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<string | null>(null);
  const hooks = useQuery({
    queryKey: ["hooks"],
    queryFn: () => unwrap(api.GET("/api/v1/hooks")),
    retry: false,
  });
  // Hooks come and go with add-ons, rules and the circuit breaker.
  useTimelineUpdates(["core.hook.disabled", "core.safe_mode.*"], [["hooks"], ["hook-points"]]);
  useResourceUpdates(watchedKinds, [["hooks"], ["hook-points"]]);
  // Without core.hook.read the server says no; the card is for those who may see it.
  if (hooks.isError) {
    return null;
  }
  if (hooks.isPending) {
    return (
      <Card aria-labelledby="hooks-title" aria-busy="true">
        <CardHeader>
          <CardTitle id="hooks-title">{intl.formatMessage(m.hooksTitle)}</CardTitle>
          <CardDescription>{intl.formatMessage(m.hooksHelp)}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </CardContent>
      </Card>
    );
  }
  const enable = async (hook: HookInfo) => {
    const key = `${hook.owner} ${hook.point}`;
    setPending(key);
    try {
      await unwrap(
        api.POST("/api/v1/hooks/enable", { body: { owner: hook.owner, point: hook.point } }),
      );
      toast.success(intl.formatMessage(m.hooksEnabled, { owner: hook.label || hook.owner }));
      await queryClient.invalidateQueries({ queryKey: ["hooks"] });
      await queryClient.invalidateQueries({ queryKey: ["hook-points"] });
    } catch (error) {
      toast.error(errorMessage(intl, error));
    } finally {
      setPending(null);
    }
  };
  const list = hooks.data.hooks ?? [];
  return (
    <Card aria-labelledby="hooks-title">
      <CardHeader>
        <CardTitle id="hooks-title">{intl.formatMessage(m.hooksTitle)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.hooksHelp)}</CardDescription>
      </CardHeader>
      <CardContent>
        {list.length === 0 ? (
          <EmptyState icon={Webhook} title={intl.formatMessage(m.hooksNone)} />
        ) : (
          <Table aria-labelledby="hooks-title">
            <TableHeader>
              <TableRow>
                <TableHead>{intl.formatMessage(m.hooksOwner)}</TableHead>
                <TableHead>{intl.formatMessage(m.hooksPoint)}</TableHead>
                <TableHead>{intl.formatMessage(m.hooksKind)}</TableHead>
                <TableHead>{intl.formatMessage(m.hooksPriority)}</TableHead>
                <TableHead>{intl.formatMessage(m.hooksState)}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((h) => (
                <TableRow key={`${h.owner} ${h.point} ${h.kind} ${h.label ?? ""}`}>
                  <TableCell>
                    <div className="flex flex-col items-start gap-0.5">
                      <span className="font-medium">{h.label || h.owner}</span>
                      {h.label ? (
                        <span className="font-mono text-muted-foreground text-xs">{h.owner}</span>
                      ) : null}
                      {h.override ? (
                        <Badge variant="danger" title={intl.formatMessage(m.hooksOverrideHelp)}>
                          {intl.formatMessage(m.hooksOverride)}
                        </Badge>
                      ) : null}
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{h.point}</TableCell>
                  <TableCell>{intl.formatMessage(m.hooksKindValue, { kind: h.kind })}</TableCell>
                  <TableCell>{intl.formatNumber(h.priority)}</TableCell>
                  <TableCell>
                    {h.disabled ? (
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant="danger">{intl.formatMessage(m.hooksOff)}</Badge>
                        <Button
                          size="sm"
                          variant="outline"
                          busy={pending === `${h.owner} ${h.point}`}
                          disabled={pending !== null}
                          aria-label={intl.formatMessage(m.hooksEnableLabel, {
                            owner: h.label || h.owner,
                            point: h.point,
                          })}
                          onClick={() => enable(h)}
                        >
                          {intl.formatMessage(m.hooksEnable)}
                        </Button>
                      </div>
                    ) : (h.failures ?? 0) > 0 ? (
                      <Badge variant="warning">
                        {intl.formatMessage(m.hooksFailures, { count: h.failures })}
                      </Badge>
                    ) : (
                      <span className="text-muted-foreground text-sm">
                        {intl.formatMessage(m.hooksRunning)}
                      </span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

// The points are many, one for every stage of every operation on every
// kind, so the list shows the first ones that match.
const pointsShown = 50;

/** Every point hooks can attach to, found by the start of its name. */
function Points() {
  const intl = useIntl();
  const api = useApi();
  const id = useId();
  const [prefix, setPrefix] = useState("");
  const points = useQuery({
    queryKey: ["hook-points", prefix],
    queryFn: () => unwrap(api.GET("/api/v1/hooks/points", { params: { query: { prefix } } })),
    placeholderData: keepPreviousData,
    retry: false,
  });
  if (points.isError) {
    return null;
  }
  const all = points.data?.points ?? [];
  const shown = all.slice(0, pointsShown);
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.pointsTitle)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.pointsHelp)}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-prefix`}>{intl.formatMessage(m.pointsFilter)}</Label>
          <Input
            id={`${id}-prefix`}
            className="font-mono"
            placeholder="web.site."
            value={prefix}
            onChange={(e) => setPrefix(e.target.value.trim())}
          />
        </div>
        {points.isPending ? (
          <div className="flex flex-col gap-2" aria-busy="true">
            <Skeleton className="h-10" />
            <Skeleton className="h-10" />
          </div>
        ) : all.length === 0 ? (
          <p className="text-muted-foreground text-sm">{intl.formatMessage(m.pointsNone)}</p>
        ) : (
          <ul
            className="flex flex-col divide-y divide-border"
            aria-label={intl.formatMessage(m.pointsTitle)}
          >
            {shown.map((p) => (
              <li key={p.point} className="flex flex-col gap-1 py-2">
                <div className="flex flex-wrap items-center gap-2">
                  <code className="font-mono text-sm">{p.point}</code>
                  {p.config ? (
                    <Badge variant="outline">
                      {intl.formatMessage(p.checked ? m.pointsChecked : m.pointsConfig)}
                    </Badge>
                  ) : null}
                  <Badge>
                    {intl.formatMessage(m.pointsHooks, { count: (p.hooks ?? []).length })}
                  </Badge>
                </div>
                {p.description ? (
                  <p className="text-muted-foreground text-sm">{p.description}</p>
                ) : null}
                {(p.invariants ?? []).length > 0 ? (
                  <p className="text-muted-foreground text-xs">
                    {intl.formatMessage(m.pointsInvariants, {
                      settings: intl.formatList(p.invariants ?? [], { type: "conjunction" }),
                    })}
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        )}
        {all.length > shown.length ? (
          <p className="text-muted-foreground text-sm">
            {intl.formatMessage(m.pointsShown, { shown: shown.length, total: all.length })}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

/** Says why safe mode is on, in the words of what turned it on. */
export function SafeModeSourceLine({ source }: { source: SafeModeSource }) {
  const intl = useIntl();
  return (
    <li>
      {intl.formatMessage(m.safeModeSourceLine, {
        source:
          source.kind === "boot"
            ? intl.formatMessage(m.safeModeBoot)
            : intl.formatMessage(m.safeModeSource, {
                kind: source.kind,
                reason: source.reason || intl.formatMessage(m.safeModeNoReason),
              }),
        hasSince: source.since ? "yes" : "no",
        when: source.since
          ? intl.formatDate(source.since, { dateStyle: "medium", timeStyle: "short" })
          : "",
        since: (chunks) => (
          <span key="since" className="text-muted-foreground">
            {chunks}
          </span>
        ),
      })}
    </li>
  );
}

/** Safe mode: whether add-ons run, and turning them all off or on again. */
function SafeMode() {
  const intl = useIntl();
  const api = useApi();
  const queryClient = useQueryClient();
  const id = useId();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const status = useQuery({
    queryKey: ["safe-mode"],
    queryFn: () => unwrap(api.GET("/api/v1/safe-mode")),
    retry: false,
  });
  // Safe mode also comes on from the console and when add-ons crash, and
  // takes away what add-ons add to the panel until it's off.
  useTimelineUpdates(["core.safe_mode.*"], [["safe-mode"], addonEntriesKey]);
  if (status.isError) {
    return null;
  }
  if (status.isPending) {
    return <Skeleton className="h-32" aria-busy="true" />;
  }
  const run = async (on: boolean) => {
    setBusy(true);
    try {
      const next = on
        ? await unwrap(api.POST("/api/v1/safe-mode", { body: { reason: reason.trim() } }))
        : await unwrap(api.DELETE("/api/v1/safe-mode"));
      queryClient.setQueryData(["safe-mode"], next);
      setReason("");
      // Add-ons' hooks, and what they add to the panel, go in safe mode and
      // come back after it.
      await queryClient.invalidateQueries({ queryKey: ["hooks"] });
      await queryClient.invalidateQueries({ queryKey: ["hook-points"] });
      await queryClient.invalidateQueries({ queryKey: addonEntriesKey });
    } catch (error) {
      toast.error(errorMessage(intl, error));
    } finally {
      setBusy(false);
    }
  };
  const st = status.data;
  const sources = st.sources ?? [];
  // The boot option lasts until the server starts without it; the rest can be turned off here.
  const removable = sources.some((s) => s.kind !== "boot");
  const boot = sources.some((s) => s.kind === "boot");
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.safeModeTitle)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.safeModeHelp)}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {st.active ? (
          <Alert tone="warning" title={intl.formatMessage(m.safeModeOn)}>
            <ul className="flex list-disc flex-col gap-1 ps-4">
              {sources.map((s) => (
                <SafeModeSourceLine key={s.kind} source={s} />
              ))}
            </ul>
          </Alert>
        ) : (
          <p className="text-sm">{intl.formatMessage(m.safeModeOff)}</p>
        )}
        {st.active ? (
          <div className="flex flex-col gap-2">
            {removable ? (
              <div>
                <Button variant="outline" busy={busy} onClick={() => run(false)}>
                  {intl.formatMessage(m.safeModeLeave)}
                </Button>
              </div>
            ) : null}
            {boot ? (
              <p className="text-muted-foreground text-sm">
                {intl.formatMessage(m.safeModeBootStays)}
              </p>
            ) : null}
          </div>
        ) : (
          <div className="flex flex-wrap items-end gap-2">
            <div className="flex min-w-60 flex-1 flex-col gap-1.5">
              <Label htmlFor={`${id}-reason`}>{intl.formatMessage(m.safeModeReason)}</Label>
              <Input
                id={`${id}-reason`}
                maxLength={500}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </div>
            <Button
              variant="destructive"
              busy={busy}
              disabled={reason.trim() === ""}
              onClick={() => run(true)}
            >
              {intl.formatMessage(m.safeModeEnter)}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** The rules of the server, a reseller or an account, the tester that tries a rule before it is saved, the hooks and safe mode. */
export default function RulesSettings() {
  const intl = useIntl();
  const id = useId();
  const [owner, setOwner] = useState<Owner>({ level: "server", id: "" });
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.rulesTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.rulesDescription)}</p>
      </div>
      <div className="flex flex-col gap-1.5">
        <OwnerFields
          id={`${id}-owner`}
          label={intl.formatMessage(m.rulesOwner)}
          value={owner}
          onChange={setOwner}
        />
        <p className="text-muted-foreground text-sm">{intl.formatMessage(m.rulesOwnerHelp)}</p>
      </div>
      <RuleList owner={owner} />
      <Tester owner={owner} />
      <Hooks />
      <Points />
      <SafeMode />
    </div>
  );
}
