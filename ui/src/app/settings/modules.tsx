import { useQuery } from "@tanstack/react-query";
import { Blocks, Cpu, HardDrive, MemoryStick } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/design/components/ui/card";
import { Checkbox } from "@/design/components/ui/choice";
import { Alert, Badge } from "@/design/components/ui/display";
import { Label } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { Skeleton } from "@/design/components/ui/skeleton";
import { EmptyState, ErrorState, type Status, StatusDot } from "@/design/components/ui/states";
import { type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { useFormat } from "@/lib/format";
import { messages as m } from "../messages";
import { ChangeDialog, useChangeFlow } from "./change-flow";

type ModuleInfo = Schemas["ModuleInfo"];
type ModuleState = ModuleInfo["state"];

const stateStatus: Record<ModuleState, Status> = {
  off: "off",
  on: "ok",
  active: "ok",
  stopping: "pending",
  removing: "pending",
};

const stateLabel = {
  off: m.modulesStateOff,
  on: m.modulesStateOn,
  active: m.modulesStateActive,
  stopping: m.modulesStateStopping,
  removing: m.modulesStateRemoving,
} as const;

/** One module: what it is, what it costs, what it needs and what it can't do here. */
function ModuleCard({
  info,
  busy,
  onTurnOn,
  onTurnOff,
  onRemove,
}: {
  info: ModuleInfo;
  busy: boolean;
  onTurnOn: () => void;
  onTurnOff: () => void;
  onRemove: (keepData: boolean) => void;
}) {
  const intl = useIntl();
  const f = useFormat();
  const id = useId();
  const [keepData, setKeepData] = useState(true);
  const man = info.manifest;
  const on = info.state === "on" || info.state === "active";
  const problems = info.hardwareProblems ?? [];
  const resources = info.usage.resources ?? 0;
  const mb = (n: number) => f.bytes(n * 1024 * 1024, { binary: true });
  return (
    <Card aria-labelledby={`${id}-name`} data-module={man.id}>
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle id={`${id}-name`}>{man.name}</CardTitle>
          <StatusDot
            className="text-sm"
            status={stateStatus[info.state]}
            label={intl.formatMessage(stateLabel[info.state])}
          />
          {man.required ? (
            <Badge variant="outline">{intl.formatMessage(m.modulesRequired)}</Badge>
          ) : null}
        </div>
        <CardDescription>{man.description}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <dl className="grid grid-cols-1 gap-1 sm:grid-cols-3">
          <div className="flex items-center gap-1.5">
            <MemoryStick className="size-4 text-muted-foreground" aria-hidden="true" />
            <dt className="sr-only">{intl.formatMessage(m.modulesIdleMemory)}</dt>
            <dd>
              {intl.formatMessage(m.modulesIdleMemoryValue, { size: mb(man.cost.idleMemoryMB) })}
            </dd>
          </div>
          <div className="flex items-center gap-1.5">
            <Cpu className="size-4 text-muted-foreground" aria-hidden="true" />
            <dt className="sr-only">{intl.formatMessage(m.modulesActiveMemory)}</dt>
            <dd>
              {intl.formatMessage(m.modulesActiveMemoryValue, {
                size: mb(man.cost.activeMemoryMB),
              })}
            </dd>
          </div>
          <div className="flex items-center gap-1.5">
            <HardDrive className="size-4 text-muted-foreground" aria-hidden="true" />
            <dt className="sr-only">{intl.formatMessage(m.modulesDisk)}</dt>
            <dd>{intl.formatMessage(m.modulesDiskValue, { size: mb(man.cost.diskMB) })}</dd>
          </div>
        </dl>
        {resources > 0 ? <p>{intl.formatMessage(m.modulesInUse, { count: resources })}</p> : null}
        {(info.needs ?? []).length > 0 ? (
          <p>
            {intl.formatMessage(m.modulesNeeds, {
              modules: intl.formatList(info.needs ?? [], { type: "conjunction" }),
            })}
          </p>
        ) : null}
        {(info.dependents ?? []).length > 0 ? (
          <p>
            {intl.formatMessage(m.modulesDependents, {
              modules: intl.formatList(info.dependents ?? [], { type: "conjunction" }),
            })}
          </p>
        ) : null}
        {problems.length > 0 ? (
          <Alert tone="warning" title={intl.formatMessage(m.modulesCantRun)}>
            <ul className="list-disc ps-4">
              {problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          </Alert>
        ) : null}
        {info.error ? (
          <Alert tone="danger" title={intl.formatMessage(m.modulesFailed)}>
            {info.error}
          </Alert>
        ) : null}
      </CardContent>
      <CardFooter className="flex flex-wrap items-center gap-2">
        {on ? (
          <Button
            variant="outline"
            disabled={busy || man.required || (resources > 0 && !info.suspends)}
            onClick={onTurnOff}
          >
            {intl.formatMessage(m.modulesTurnOff)}
          </Button>
        ) : (
          <Button disabled={busy || problems.length > 0 || !info.resource} onClick={onTurnOn}>
            {intl.formatMessage(m.modulesTurnOn)}
          </Button>
        )}
        {info.state === "off" && ((info.installed ?? []).length > 0 || resources > 0) ? (
          <>
            <Button
              variant="destructive"
              disabled={busy || !info.resource}
              onClick={() => onRemove(keepData)}
            >
              {intl.formatMessage(m.modulesRemove)}
            </Button>
            <div className="flex items-center gap-2">
              <Checkbox
                id={`${id}-keep`}
                checked={keepData}
                onCheckedChange={(v) => setKeepData(v === true)}
              />
              <Label htmlFor={`${id}-keep`}>{intl.formatMessage(m.modulesKeepData)}</Label>
            </div>
          </>
        ) : null}
      </CardFooter>
    </Card>
  );
}

/** Every module, with turning them on, off and removing them, each previewed first. */
export default function ModulesSettings() {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow();
  const [query, setQuery] = useState("");
  const [profile, setProfile] = useState<string>("");
  const [skipped, setSkipped] = useState<Schemas["ProfileChange"]["skipped"]>(null);
  const searchId = useId();
  const list = useQuery({
    queryKey: ["modules"],
    queryFn: () => unwrap(api.GET("/api/v1/modules")),
  });
  const profiles = useQuery({
    queryKey: ["modules", "profiles"],
    queryFn: () => unwrap(api.GET("/api/v1/modules/profiles")),
  });

  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    const found = (list.data?.modules ?? []).filter(
      (info) =>
        q === "" ||
        `${info.manifest.id} ${info.manifest.name} ${info.manifest.description}`
          .toLowerCase()
          .includes(q),
    );
    const byCategory = new Map<string, ModuleInfo[]>();
    for (const info of found) {
      const list = byCategory.get(info.manifest.category) ?? [];
      list.push(info);
      byCategory.set(info.manifest.category, list);
    }
    return [...byCategory.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [list.data, query]);

  const action = (info: ModuleInfo, name: "turn_on" | "turn_off") => () =>
    flow.preview((dryRun) => {
      const params = { params: { path: { id: info.resource ?? "" }, query: { dryRun } } };
      return name === "turn_on"
        ? unwrap(api.POST("/api/v1/core/modules/{id}/actions/turn_on", params))
        : unwrap(api.POST("/api/v1/core/modules/{id}/actions/turn_off", params));
    });

  const remove = (info: ModuleInfo) => (keepData: boolean) =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/core/modules/{id}/actions/remove", {
          params: { path: { id: info.resource ?? "" }, query: { dryRun } },
          body: { confirm: info.manifest.id, keepData },
        }),
      ),
    );

  const applyProfile = () =>
    flow.preview(async (dryRun) => {
      const out = await unwrap(
        api.POST("/api/v1/modules/profiles/{profile}", {
          params: {
            path: { profile: profile as Schemas["Profile"]["id"] },
            query: { dryRun },
          },
        }),
      );
      setSkipped(out.skipped ?? null);
      return out.changeSet;
    });

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(m.modulesTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.modulesDescription)}</p>
      </div>

      <Card aria-labelledby="modules-profiles">
        <CardHeader>
          <CardTitle id="modules-profiles">{intl.formatMessage(m.modulesProfiles)}</CardTitle>
          <CardDescription>{intl.formatMessage(m.modulesProfilesHelp)}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-60 flex-col gap-1.5">
            <Label htmlFor="modules-profile">{intl.formatMessage(m.modulesProfile)}</Label>
            <Select value={profile} onValueChange={setProfile}>
              <SelectTrigger id="modules-profile">
                <SelectValue placeholder={intl.formatMessage(m.modulesProfileChoose)} />
              </SelectTrigger>
              <SelectContent>
                {(profiles.data?.profiles ?? []).map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button disabled={profile === "" || flow.busy} onClick={applyProfile}>
            {intl.formatMessage(m.modulesProfileApply)}
          </Button>
          {profile !== "" ? (
            <p className="w-full text-muted-foreground text-sm">
              {(profiles.data?.profiles ?? []).find((p) => p.id === profile)?.description}
            </p>
          ) : null}
          {skipped && skipped.length > 0 ? (
            <Alert tone="info" title={intl.formatMessage(m.modulesSkipped)} className="w-full">
              <ul className="list-disc ps-4">
                {skipped.map((s) => (
                  <li key={s.module}>
                    {intl.formatMessage(m.modulesSkippedItem, {
                      module: s.module,
                      reason: s.reason,
                    })}
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
        </CardContent>
      </Card>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={searchId}>{intl.formatMessage(m.modulesSearch)}</Label>
        <Input
          id={searchId}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      {list.isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          <Skeleton className="h-48" />
          <Skeleton className="h-48" />
        </div>
      ) : list.isError ? (
        <ErrorState title={intl.formatMessage(m.modulesLoadFailed)} error={list.error} />
      ) : groups.length === 0 ? (
        <EmptyState icon={Blocks} title={intl.formatMessage(m.modulesNone)} />
      ) : (
        groups.map(([category, infos]) => (
          <section
            key={category}
            className="flex flex-col gap-3"
            aria-labelledby={`modules-${category}`}
          >
            <h2 id={`modules-${category}`} className="font-semibold text-lg capitalize">
              {category}
            </h2>
            <div className="grid gap-4 md:grid-cols-2">
              {infos.map((info) => (
                <ModuleCard
                  key={info.manifest.id}
                  info={info}
                  busy={flow.busy}
                  onTurnOn={action(info, "turn_on")}
                  onTurnOff={action(info, "turn_off")}
                  onRemove={remove(info)}
                />
              ))}
            </div>
          </section>
        ))
      )}
      <ChangeDialog flow={flow} />
    </div>
  );
}
