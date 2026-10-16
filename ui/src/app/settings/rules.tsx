import { useQuery } from "@tanstack/react-query";
import { ScrollText } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { useIntl } from "react-intl";
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
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/design/components/ui/table";
import { type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { messages as m } from "../messages";
import { ChangeDialog, useChangeFlow } from "./change-flow";

type PolicySpec = Schemas["PolicySpec"];
type TestResult = Schemas["TestResult"];

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

function RuleList() {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow();
  const rules = useQuery({
    queryKey: ["rules"],
    queryFn: () => unwrap(api.GET("/api/v1/core/policies")),
  });
  const toggle = (rule: Schemas["CorePolicy"]) =>
    flow.preview((dryRun) =>
      unwrap(
        api.PUT("/api/v1/core/policies/{id}", {
          params: { path: { id: rule.id }, query: { dryRun } },
          body: { spec: { ...rule.spec, enabled: !rule.spec.enabled }, revision: rule.revision },
        }),
      ),
    );
  if (rules.isError) {
    return <ErrorState title={intl.formatMessage(m.rulesLoadFailed)} error={rules.error} />;
  }
  const items = rules.data?.items ?? [];
  return (
    <Card aria-labelledby="rules-list">
      <CardHeader>
        <CardTitle id="rules-list">{intl.formatMessage(m.rulesListTitle)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.rulesListHelp)}</CardDescription>
      </CardHeader>
      <CardContent>
        {items.length === 0 && !rules.isPending ? (
          <EmptyState icon={ScrollText} title={intl.formatMessage(m.rulesNone)} />
        ) : (
          <Table>
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

function Tester() {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow();
  const id = useId();
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
            owner: { level: "server" },
            operation: change.operation,
            kind: change.kind,
            scope: { level: "server" },
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

  const save = () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/core/policies", {
          params: { query: { dryRun } },
          body: { spec: spec() },
        }),
      ),
    );

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
            <Button type="submit" busy={busy}>
              {intl.formatMessage(m.rulesTry)}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={rule.name === "" || flow.busy}
              onClick={save}
            >
              {intl.formatMessage(m.rulesSave)}
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

/** The server's rules, and the tester that tries a rule on a change before it is saved. */
export default function RulesSettings() {
  const intl = useIntl();
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(m.rulesTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.rulesDescription)}</p>
      </div>
      <RuleList />
      <Tester />
    </div>
  );
}
