import { useQuery } from "@tanstack/react-query";
import { Blocks, Cpu, Download, HardDrive, MemoryStick } from "lucide-react";
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
import { ConfirmDialog } from "@/design/components/ui/confirm-dialog";
import {
  Alert,
  Badge,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/design/components/ui/display";
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
import { toast } from "@/design/components/ui/toaster";
import { type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { useFormat } from "@/lib/format";
import { messages } from "../messages";
import { moduleSettingsMessages as m } from "../screens/messages";
import { ChangeDialog, useChangeFlow } from "./change-flow";
import { saveJSON, useResourceUpdates } from "./live";

type ModuleInfo = Schemas["ModuleInfo"];
type ModuleState = ModuleInfo["state"];
type ModuleCategory = ModuleInfo["manifest"]["category"];

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

/** Categories in the order the page lists them, with their headings. */
const categories: readonly ModuleCategory[] = [
  "core",
  "hosting",
  "apps",
  "storage",
  "operations",
  "home",
  "assistant",
];

const categoryLabel = {
  core: m.modulesCategoryCore,
  hosting: m.modulesCategoryHosting,
  apps: m.modulesCategoryApps,
  storage: m.modulesCategoryStorage,
  operations: m.modulesCategoryOperations,
  home: m.modulesCategoryHome,
  assistant: m.modulesCategoryAssistant,
} as const satisfies Record<ModuleCategory, unknown>;

/**
 * Whether a module can keep its resources when it goes off: it is off
 * already, has none, or keeps them dormant while it is off.
 */
function keepsResources(info: ModuleInfo): boolean {
  return info.state === "off" || (info.usage.resources ?? 0) === 0 || info.suspends === true;
}

/** Software a module brings along rather than installs: built in or already on the server. */
const bundled = new Set(["embedded", "present"]);

/** What a module installs the first time something needs it, asked of the server when opened. */
function SoftwareList({ id }: { id: string }) {
  const intl = useIntl();
  const api = useApi();
  const f = useFormat();
  const software = useQuery({
    // Apart from ["modules"], which every change to a module reads again.
    queryKey: ["module-software", id],
    queryFn: () => unwrap(api.GET("/api/v1/modules/{id}/software", { params: { path: { id } } })),
  });
  if (software.isPending) {
    return (
      <div className="flex flex-col gap-2" aria-busy="true">
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-4 w-1/2" />
      </div>
    );
  }
  if (software.isError) {
    return (
      <Alert tone="warning" title={intl.formatMessage(m.modulesSoftwareFailed)}>
        {errorMessage(intl, software.error)}
      </Alert>
    );
  }
  const list = (software.data.software ?? []).filter((s) => !bundled.has(s.source.type));
  if (list.length === 0) {
    return <p className="text-muted-foreground">{intl.formatMessage(m.modulesSoftwareNone)}</p>;
  }
  return (
    <ul className="flex flex-col gap-2">
      {list.map((s) => (
        <li key={s.id} className="flex flex-col gap-0.5">
          <span className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{s.name}</span>
            {s.installed ? (
              <Badge variant="outline">{intl.formatMessage(m.modulesSoftwareInstalled)}</Badge>
            ) : null}
            {s.optIn ? (
              <Badge variant="outline">{intl.formatMessage(m.modulesSoftwareOptIn)}</Badge>
            ) : null}
          </span>
          <span className="text-muted-foreground text-xs">
            {intl.formatMessage(m.modulesSoftwareSizes, {
              download: f.bytes(s.downloadBytes ?? 0),
              disk: f.bytes(s.diskBytes ?? 0),
              license: s.license,
            })}
          </span>
          {s.install ? <span className="text-muted-foreground text-xs">{s.install}</span> : null}
          {(s.packages ?? []).length > 0 ? (
            <span className="font-mono text-muted-foreground text-xs">
              {intl.formatList(
                (s.packages ?? []).map((p) => `${p.name} ${p.version}`),
                { type: "unit" },
              )}
            </span>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/**
 * Asks before removing a module: whether to keep its data, offering to
 * download its resources first when they'd be deleted, and its name
 * typed to confirm. A module that can't keep its resources while it is
 * off is removed with them. Confirming previews the removal.
 */
function RemoveDialog({
  info,
  open,
  onOpenChange,
  onRemove,
}: {
  info: ModuleInfo;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onRemove: (confirm: string, keepData: boolean) => Promise<void>;
}) {
  const intl = useIntl();
  const api = useApi();
  const id = useId();
  const canKeep = keepsResources(info);
  const [keepData, setKeepData] = useState(canKeep);
  const [exporting, setExporting] = useState(false);
  const man = info.manifest;
  const resources = info.usage.resources ?? 0;
  const download = async () => {
    setExporting(true);
    try {
      const data = await unwrap(
        api.GET("/api/v1/modules/{id}/export", { params: { path: { id: man.id } } }),
      );
      const file = `racksail-${man.id}-${data.exported.slice(0, 10)}.json`;
      saveJSON(file, data);
      toast.success(intl.formatMessage(m.modulesExported, { file }));
    } catch (error) {
      toast.error(errorMessage(intl, error));
    } finally {
      setExporting(false);
    }
  };
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setKeepData(canKeep);
        }
        onOpenChange(next);
      }}
      title={intl.formatMessage(m.modulesRemoveTitle, { name: man.name })}
      description={intl.formatMessage(m.modulesRemoveBody, { name: man.name })}
      confirmLabel={intl.formatMessage(m.modulesRemovePreview)}
      // The button only works once the name typed matches, so the name is
      // what the person typed.
      typedConfirmation={man.name}
      onConfirm={() => onRemove(man.name, keepData)}
    >
      <div className="flex items-center gap-2">
        <Checkbox
          id={`${id}-keep`}
          checked={keepData}
          disabled={!canKeep}
          aria-describedby={canKeep ? undefined : `${id}-cant-keep`}
          onCheckedChange={(v) => setKeepData(v === true)}
        />
        <Label htmlFor={`${id}-keep`}>{intl.formatMessage(m.modulesKeepData)}</Label>
      </div>
      {canKeep ? null : (
        <p id={`${id}-cant-keep`} className="text-muted-foreground text-sm">
          {intl.formatMessage(m.modulesRemoveCantKeep, { name: man.name, count: resources })}
        </p>
      )}
      {!keepData && info.exportable ? (
        <div className="flex flex-col gap-2 text-sm">
          <p>{intl.formatMessage(m.modulesRemoveDeletes, { count: resources })}</p>
          <div>
            <Button variant="outline" size="sm" busy={exporting} onClick={download}>
              <Download aria-hidden="true" />
              {intl.formatMessage(m.modulesExport)}
            </Button>
          </div>
        </div>
      ) : null}
    </ConfirmDialog>
  );
}

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
  onRemove: () => void;
}) {
  const intl = useIntl();
  const f = useFormat();
  const id = useId();
  const [showSoftware, setShowSoftware] = useState(false);
  const man = info.manifest;
  const on = info.state === "on" || info.state === "active";
  const problems = info.hardwareProblems ?? [];
  const resources = info.usage.resources ?? 0;
  // Turned off, it would lose its resources, so it can only be removed with them.
  const stuck = on && !man.required && !keepsResources(info);
  // A removed module's software is gone but for what other modules still
  // use, so only its kept resources are left to remove.
  const removable =
    !man.required &&
    (stuck ||
      (info.state === "off" &&
        (resources > 0 || (!info.removed && (info.installed ?? []).length > 0))));
  const mb = (n: number) => f.bytes(n * 1024 * 1024, { binary: true });
  const installs = (man.upstream ?? []).filter((u) => !bundled.has(u.source.type));
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
          {info.removed && info.state === "off" ? (
            <Badge variant="outline">{intl.formatMessage(m.modulesRemoved)}</Badge>
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
        {installs.length > 0 ? (
          <Collapsible open={showSoftware} onOpenChange={setShowSoftware}>
            <CollapsibleTrigger>
              {intl.formatMessage(m.modulesSoftware, {
                names: intl.formatList(
                  installs.map((u) => u.name),
                  { type: "conjunction" },
                ),
              })}
            </CollapsibleTrigger>
            <CollapsibleContent className="pt-2">
              {showSoftware ? <SoftwareList id={man.id} /> : null}
            </CollapsibleContent>
          </Collapsible>
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
        {stuck ? (
          <p id={`${id}-stuck`} className="text-muted-foreground">
            {intl.formatMessage(m.modulesCantTurnOff, { name: man.name, count: resources })}
          </p>
        ) : null}
      </CardContent>
      <CardFooter className="flex flex-wrap items-center gap-2">
        {on ? (
          <Button
            variant="outline"
            disabled={busy || man.required || stuck}
            aria-describedby={stuck ? `${id}-stuck` : undefined}
            onClick={onTurnOff}
          >
            {intl.formatMessage(m.modulesTurnOff)}
          </Button>
        ) : (
          <Button
            disabled={busy || problems.length > 0 || !info.resource || info.state !== "off"}
            onClick={onTurnOn}
          >
            {intl.formatMessage(m.modulesTurnOn)}
          </Button>
        )}
        {removable ? (
          <Button variant="destructive" disabled={busy || !info.resource} onClick={onRemove}>
            {intl.formatMessage(m.modulesRemove)}
          </Button>
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
  const [removing, setRemoving] = useState<ModuleInfo | null>(null);
  const searchId = useId();
  const list = useQuery({
    queryKey: ["modules"],
    queryFn: () => unwrap(api.GET("/api/v1/modules")),
  });
  const profiles = useQuery({
    queryKey: ["modules", "profiles"],
    queryFn: () => unwrap(api.GET("/api/v1/modules/profiles")),
  });
  // States change as resources come to use a module and as changes apply.
  useResourceUpdates(["core.module"], [["modules"]]);

  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    const found = (list.data?.modules ?? []).filter(
      (info) =>
        q === "" ||
        `${info.manifest.id} ${info.manifest.name} ${info.manifest.description}`
          .toLowerCase()
          .includes(q),
    );
    const byCategory = new Map<ModuleCategory, ModuleInfo[]>();
    for (const info of found) {
      const list = byCategory.get(info.manifest.category) ?? [];
      list.push(info);
      byCategory.set(info.manifest.category, list);
    }
    return [...byCategory.entries()].sort(
      ([a], [b]) => categories.indexOf(a) - categories.indexOf(b),
    );
  }, [list.data, query]);

  // The module manager turns a module on with the modules it needs.
  const turnOn = (info: ModuleInfo) => () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/modules/{id}/actions/turn_on", {
          params: { path: { id: info.manifest.id }, query: { dryRun } },
        }),
      ),
    );

  const turnOff = (info: ModuleInfo) => () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/modules/{id}/actions/turn_off", {
          params: { path: { id: info.manifest.id }, query: { dryRun } },
        }),
      ),
    );

  const remove = (info: ModuleInfo) => (confirm: string, keepData: boolean) =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/modules/{id}/actions/remove", {
          params: { path: { id: info.manifest.id }, query: { dryRun } },
          body: { confirm, keepData },
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
        <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.modulesTitle)}</h1>
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
        <div className="grid gap-4 md:grid-cols-2" aria-busy="true">
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
            <h2 id={`modules-${category}`} className="font-semibold text-lg">
              {intl.formatMessage(categoryLabel[category])}
            </h2>
            <div className="grid gap-4 md:grid-cols-2">
              {infos.map((info) => (
                <ModuleCard
                  key={info.manifest.id}
                  info={info}
                  busy={flow.busy}
                  onTurnOn={turnOn(info)}
                  onTurnOff={turnOff(info)}
                  onRemove={() => setRemoving(info)}
                />
              ))}
            </div>
          </section>
        ))
      )}
      {removing ? (
        <RemoveDialog
          key={removing.manifest.id}
          info={removing}
          open
          onOpenChange={(open) => (open ? undefined : setRemoving(null))}
          onRemove={async (confirm, keepData) => {
            await remove(removing)(confirm, keepData);
            setRemoving(null);
          }}
        />
      ) : null}
      <ChangeDialog flow={flow} />
    </div>
  );
}
