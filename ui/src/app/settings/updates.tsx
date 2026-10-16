import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useId, useState } from "react";
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
import { Switch } from "@/design/components/ui/choice";
import { Alert } from "@/design/components/ui/display";
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
import { ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { useFormat } from "@/lib/format";
import { messages as m } from "../messages";
import { ChangeDialog, useChangeFlow } from "./change-flow";
import { useResourceUpdates } from "./live";

type Settings = Schemas["CoreUpdateSettings"];
type Spec = Schemas["SettingsSpec"];
type PackageUpdates = Schemas["PackageUpdates"];

// Settings without package updates take Debian's security updates, as the
// server does.
const defaultPackages: PackageUpdates = { security: true };

function Releases({ settings }: { settings: Settings }) {
  const intl = useIntl();
  const api = useApi();
  const f = useFormat();
  const queryClient = useQueryClient();
  const flow = useChangeFlow();
  const [checking, setChecking] = useState(false);
  const st = settings.status;
  const params = (dryRun: boolean) => ({
    params: { path: { id: settings.id }, query: { dryRun } },
  });
  const check = async () => {
    setChecking(true);
    try {
      await unwrap(
        api.POST("/api/v1/core/update-settings/{id}/actions/check", {
          params: { path: { id: settings.id } },
        }),
      );
      await queryClient.invalidateQueries({ queryKey: ["updates"] });
    } catch (error) {
      toast.error(errorMessage(intl, error));
    } finally {
      setChecking(false);
    }
  };
  const install = () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/core/update-settings/{id}/actions/install", {
          ...params(dryRun),
          body: { version: st?.available?.version },
        }),
      ),
    );
  const rollback = () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/core/update-settings/{id}/actions/rollback", {
          ...params(dryRun),
          body: {},
        }),
      ),
    );
  const last = st?.last;
  return (
    <Card aria-labelledby="updates-releases">
      <CardHeader>
        <CardTitle id="updates-releases">{intl.formatMessage(m.updatesReleases)}</CardTitle>
        <CardDescription>
          {intl.formatMessage(m.updatesRunning, { version: st?.running ?? "" })}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {st?.available ? (
          <Alert
            tone="info"
            title={intl.formatMessage(m.updatesAvailable, { version: st.available.version })}
          >
            <p>
              {intl.formatMessage(m.updatesPublished, {
                when: f.date(new Date(st.available.published)),
              })}
            </p>
            {st.available.notesUrl ? (
              <a
                className="text-primary underline-offset-4 hover:underline"
                href={st.available.notesUrl}
                target="_blank"
                rel="noreferrer"
              >
                {intl.formatMessage(m.updatesNotes)}
              </a>
            ) : null}
          </Alert>
        ) : st?.checkedAt ? (
          <p className="text-sm">
            {intl.formatMessage(m.updatesUpToDate, { when: f.relative(new Date(st.checkedAt)) })}
          </p>
        ) : null}
        {st?.checkError ? (
          <Alert tone="warning" title={intl.formatMessage(m.updatesCheckFailed)}>
            {st.checkError}
          </Alert>
        ) : null}
        {last ? (
          <p className="text-sm">
            {intl.formatMessage(m.updatesLast, {
              state: last.state === "rolled-back" ? "rolledBack" : last.state,
              from: last.from,
              to: last.to,
              reason: last.reason ?? "",
            })}
          </p>
        ) : null}
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button variant="outline" busy={checking} onClick={check}>
          {intl.formatMessage(m.updatesCheck)}
        </Button>
        <Button disabled={!st?.available || flow.busy} onClick={install}>
          {intl.formatMessage(m.updatesInstall)}
        </Button>
        {st?.canRollBack ? (
          <Button variant="destructive" disabled={flow.busy} onClick={rollback}>
            {intl.formatMessage(m.updatesRollback)}
          </Button>
        ) : null}
      </CardFooter>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

function SettingsForm({ settings }: { settings: Settings }) {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow();
  const id = useId();
  const [spec, setSpec] = useState<Spec>(settings.spec);
  const [mirrors, setMirrors] = useState((settings.spec.mirrors ?? []).join("\n"));
  // The form follows the settings when they change, not every time their
  // status does, as it does while a check or an update runs.
  const saved = JSON.stringify(settings.spec);
  useEffect(() => {
    const next = JSON.parse(saved) as Spec;
    setSpec(next);
    setMirrors((next.mirrors ?? []).join("\n"));
  }, [saved]);
  const save = (event: FormEvent) => {
    event.preventDefault();
    const body: Spec = {
      ...spec,
      mirrors: mirrors
        .split(/\s+/)
        .map((s) => s.trim())
        .filter((s) => s !== ""),
    };
    flow.preview((dryRun) =>
      unwrap(
        api.PUT("/api/v1/core/update-settings/{id}", {
          params: { path: { id: settings.id }, query: { dryRun } },
          body: { spec: body, revision: settings.revision },
        }),
      ),
    );
  };
  const window = spec.window;
  const packages = spec.packages ?? defaultPackages;
  const setPackages = (next: Partial<PackageUpdates>) =>
    setSpec({ ...spec, packages: { ...packages, ...next } });
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.updatesSettings)}</CardTitle>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-4" onSubmit={save}>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-channel`}>{intl.formatMessage(m.updatesChannel)}</Label>
            <Select
              value={spec.channel}
              onValueChange={(v) => setSpec({ ...spec, channel: v as Spec["channel"] })}
            >
              <SelectTrigger id={`${id}-channel`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(["stable", "beta", "nightly"] as const).map((c) => (
                  <SelectItem key={c} value={c}>
                    {intl.formatMessage(m.updatesChannelValue, { channel: c })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">
              {intl.formatMessage(m.updatesChannelHelp)}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Switch
              id={`${id}-auto`}
              checked={spec.automatic ?? false}
              onCheckedChange={(v) => setSpec({ ...spec, automatic: v })}
            />
            <Label htmlFor={`${id}-auto`}>{intl.formatMessage(m.updatesAutomatic)}</Label>
          </div>
          <fieldset className="flex flex-col gap-3">
            <legend className="mb-1 font-medium text-sm">
              {intl.formatMessage(m.updatesPackages)}
            </legend>
            <div className="flex items-center gap-2">
              <Switch
                id={`${id}-security`}
                checked={packages.security}
                onCheckedChange={(v) => setPackages({ security: v })}
              />
              <Label htmlFor={`${id}-security`}>
                {intl.formatMessage(m.updatesPackagesSecurity)}
              </Label>
            </div>
            <div className="flex items-center gap-2">
              <Switch
                id={`${id}-scheduled`}
                checked={packages.scheduled ?? false}
                onCheckedChange={(v) => setPackages({ scheduled: v })}
              />
              <Label htmlFor={`${id}-scheduled`}>
                {intl.formatMessage(m.updatesPackagesScheduled)}
              </Label>
            </div>
            <div className="flex items-center gap-2">
              <Switch
                id={`${id}-reboot`}
                checked={packages.reboot ?? false}
                onCheckedChange={(v) => setPackages({ reboot: v })}
              />
              <Label htmlFor={`${id}-reboot`}>{intl.formatMessage(m.updatesPackagesReboot)}</Label>
            </div>
            {packages.reboot ? (
              <div className="flex max-w-48 flex-col gap-1.5">
                <Label htmlFor={`${id}-reboot-time`}>
                  {intl.formatMessage(m.updatesPackagesRebootTime)}
                </Label>
                <Input
                  id={`${id}-reboot-time`}
                  type="time"
                  value={packages.rebootTime ?? "04:00"}
                  onChange={(e) => setPackages({ rebootTime: e.target.value || undefined })}
                />
              </div>
            ) : null}
            <p className="text-muted-foreground text-xs">
              {intl.formatMessage(m.updatesPackagesHelp)}
            </p>
          </fieldset>
          {spec.automatic || packages.scheduled ? (
            <fieldset className="grid gap-3 md:grid-cols-3">
              <legend className="mb-1 text-sm">{intl.formatMessage(m.updatesWindow)}</legend>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-start`}>{intl.formatMessage(m.updatesWindowStart)}</Label>
                <Input
                  id={`${id}-start`}
                  type="time"
                  value={window?.start ?? "03:00"}
                  onChange={(e) =>
                    setSpec({
                      ...spec,
                      window: {
                        start: e.target.value,
                        end: window?.end ?? "05:00",
                        timeZone: window?.timeZone,
                      },
                    })
                  }
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-end`}>{intl.formatMessage(m.updatesWindowEnd)}</Label>
                <Input
                  id={`${id}-end`}
                  type="time"
                  value={window?.end ?? "05:00"}
                  onChange={(e) =>
                    setSpec({
                      ...spec,
                      window: {
                        start: window?.start ?? "03:00",
                        end: e.target.value,
                        timeZone: window?.timeZone,
                      },
                    })
                  }
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-tz`}>{intl.formatMessage(m.updatesWindowZone)}</Label>
                <Input
                  id={`${id}-tz`}
                  placeholder="UTC"
                  value={window?.timeZone ?? ""}
                  onChange={(e) =>
                    setSpec({
                      ...spec,
                      window: {
                        start: window?.start ?? "03:00",
                        end: window?.end ?? "05:00",
                        timeZone: e.target.value || undefined,
                      },
                    })
                  }
                />
              </div>
            </fieldset>
          ) : null}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-mirrors`}>{intl.formatMessage(m.updatesMirrors)}</Label>
            <Textarea
              id={`${id}-mirrors`}
              className="font-mono"
              value={mirrors}
              onChange={(e) => setMirrors(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              {intl.formatMessage(m.updatesMirrorsHelp)}
            </p>
          </div>
          <div>
            <Button type="submit" disabled={flow.busy}>
              {intl.formatMessage(m.updatesSave)}
            </Button>
          </div>
        </form>
      </CardContent>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

function Debian({ settings }: { settings: Settings }) {
  const intl = useIntl();
  const api = useApi();
  const f = useFormat();
  const flow = useChangeFlow();
  const os = settings.status?.os;
  if (!os) {
    return null;
  }
  const params = (dryRun: boolean) => ({
    params: { path: { id: settings.id }, query: { dryRun } },
  });
  const upgrade = () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/core/update-settings/{id}/actions/upgrade_os", {
          ...params(dryRun),
          body: { to: os.next ?? "" },
        }),
      ),
    );
  const upgradePackages = () =>
    flow.preview((dryRun) =>
      unwrap(
        api.POST("/api/v1/core/update-settings/{id}/actions/upgrade_packages", params(dryRun)),
      ),
    );
  const last = settings.status?.packages;
  return (
    <Card aria-labelledby="updates-debian">
      <CardHeader>
        <CardTitle id="updates-debian">{intl.formatMessage(m.updatesDebian)}</CardTitle>
        <CardDescription>
          {intl.formatMessage(m.updatesDebianRunning, {
            version: os.version,
            codename: os.codename,
          })}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {os.rebootRequired ? (
          <Alert tone="warning" title={intl.formatMessage(m.updatesReboot)}>
            {(os.rebootReasons ?? []).length > 0 ? (
              <p>{intl.formatList(os.rebootReasons ?? [], { type: "conjunction" })}</p>
            ) : null}
            {os.rebootAt ? (
              <p>
                {intl.formatMessage(m.updatesRebootAt, { when: f.dateTime(new Date(os.rebootAt)) })}
              </p>
            ) : null}
          </Alert>
        ) : null}
        {last?.error ? (
          <Alert
            tone="danger"
            title={intl.formatMessage(m.updatesPackagesFailed, {
              when: f.relative(new Date(last.at)),
            })}
          >
            {last.error}
          </Alert>
        ) : last ? (
          <p className="text-sm">
            {intl.formatMessage(m.updatesPackagesLast, {
              count: last.upgraded,
              when: f.relative(new Date(last.at)),
            })}
          </p>
        ) : null}
        {os.next ? (
          <p className="text-sm">
            {intl.formatMessage(m.updatesDebianNext, { codename: os.next })}
          </p>
        ) : null}
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button variant="outline" disabled={flow.busy} onClick={upgradePackages}>
          {intl.formatMessage(m.updatesPackagesUpgrade)}
        </Button>
        {os.next ? (
          <Button variant="outline" disabled={flow.busy} onClick={upgrade}>
            {intl.formatMessage(m.updatesDebianUpgrade, { codename: os.next })}
          </Button>
        ) : null}
      </CardFooter>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

/** Updates: the running release, newer ones, how they install and Debian's major upgrades. */
export default function UpdateSettings() {
  const intl = useIntl();
  const api = useApi();
  const settings = useQuery({
    queryKey: ["updates"],
    queryFn: () => unwrap(api.GET("/api/v1/core/update-settings")),
  });
  // Checks, installs and package upgrades report in the settings' status
  // as they go.
  useResourceUpdates(["core.update_settings"], [["updates"]]);
  const current = settings.data?.items?.[0];
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(m.updatesTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.updatesDescription)}</p>
      </div>
      {settings.isPending ? (
        <div className="flex flex-col gap-6" aria-busy="true">
          <Skeleton className="h-40" />
          <Skeleton className="h-64" />
        </div>
      ) : settings.isError ? (
        <ErrorState title={intl.formatMessage(m.updatesLoadFailed)} error={settings.error} />
      ) : !current ? (
        <Alert tone="info" title={intl.formatMessage(m.updatesNotSetUp)} />
      ) : (
        <>
          <Releases settings={current} />
          <SettingsForm settings={current} />
          <Debian settings={current} />
        </>
      )}
    </div>
  );
}
