import { useQuery } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { type FormEvent, useEffect, useId, useState } from "react";
import { useIntl } from "react-intl";
import { SchemaForm } from "@/design/components/schema-form";
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
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { EmptyState } from "@/design/components/ui/states";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/design/components/ui/table";
import { toast } from "@/design/components/ui/toaster";
import { ApiError, type Schemas, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { errorMessage } from "@/lib/errors";
import { useFormat } from "@/lib/format";
import type { UIHints } from "@/lib/forms/model";
import { availableLocales } from "@/lib/i18n";
import type { JSONSchema } from "@/lib/schema";
import { messages } from "../messages";
import { notifySettingsMessages as m } from "../screens/messages";
import { ChangeDialog, useChangeFlow } from "./change-flow";
import { useRefreshAt, useResourceUpdates, useTimelineUpdates } from "./live";

type Severity = "info" | "notice" | "warning" | "error" | "critical";
const severities: Severity[] = ["info", "notice", "warning", "error", "critical"];
type Preferences = Schemas["NotificationPreferences"];

// One stream tells the whole screen what changed. Sending a notification
// records how it went in its channel's or the relay's status, so their
// notices also say the log changed.
const watchedKinds = [
  "core.notification_channel",
  "core.smtp_relay",
  "core.notification_preferences",
];
type QuietHours = NonNullable<Preferences["quietHours"]>;

function patterns(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

const forbidden = (error: unknown) => error instanceof ApiError && error.status === 403;

function SeveritySelect({
  id,
  value,
  onChange,
  allowEmpty,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  allowEmpty?: string;
}) {
  const intl = useIntl();
  return (
    <Select
      value={value === "" ? "any" : value}
      onValueChange={(v) => onChange(v === "any" ? "" : v)}
    >
      <SelectTrigger id={id}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {allowEmpty ? <SelectItem value="any">{allowEmpty}</SelectItem> : null}
        {severities.map((s) => (
          <SelectItem key={s} value={s}>
            {intl.formatMessage(m.notifySeverity, { severity: s })}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/** Quiet hours, used by people and by channels alike. */
function QuietHoursFields({
  value,
  onChange,
}: {
  value: QuietHours | undefined;
  onChange: (q: QuietHours | undefined) => void;
}) {
  const intl = useIntl();
  const id = useId();
  const on = value !== undefined;
  const q: QuietHours = value ?? { start: "22:00", end: "07:00" };
  return (
    <fieldset className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <Switch id={`${id}-on`} checked={on} onCheckedChange={(v) => onChange(v ? q : undefined)} />
        <Label htmlFor={`${id}-on`}>{intl.formatMessage(m.notifyQuietHours)}</Label>
      </div>
      {on ? (
        <div className="grid gap-3 md:grid-cols-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-start`}>{intl.formatMessage(m.notifyQuietStart)}</Label>
            <Input
              id={`${id}-start`}
              type="time"
              value={q.start}
              onChange={(e) => onChange({ ...q, start: e.target.value })}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-end`}>{intl.formatMessage(m.notifyQuietEnd)}</Label>
            <Input
              id={`${id}-end`}
              type="time"
              value={q.end}
              onChange={(e) => onChange({ ...q, end: e.target.value })}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-tz`}>{intl.formatMessage(m.notifyQuietZone)}</Label>
            <Input
              id={`${id}-tz`}
              placeholder={Intl.DateTimeFormat().resolvedOptions().timeZone}
              value={q.timeZone ?? ""}
              onChange={(e) => onChange({ ...q, timeZone: e.target.value || undefined })}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-urgent`}>{intl.formatMessage(m.notifyQuietUrgent)}</Label>
            <SeveritySelect
              id={`${id}-urgent`}
              value={q.urgent ?? "critical"}
              onChange={(v) => onChange({ ...q, urgent: (v || undefined) as QuietHours["urgent"] })}
            />
          </div>
          <div className="flex items-center gap-2 md:col-span-2">
            <Switch
              id={`${id}-drop`}
              checked={q.drop ?? false}
              onCheckedChange={(v) => onChange({ ...q, drop: v })}
            />
            <Label htmlFor={`${id}-drop`}>{intl.formatMessage(m.notifyQuietDrop)}</Label>
          </div>
        </div>
      ) : null}
    </fieldset>
  );
}

function MyPreferences() {
  const intl = useIntl();
  const api = useApi();
  const id = useId();
  const prefs = useQuery({
    queryKey: ["notifications", "preferences"],
    queryFn: () => unwrap(api.GET("/api/v1/notifications/preferences")),
    retry: false,
  });
  const flow = useChangeFlow();
  const [draft, setDraft] = useState<Preferences | null>(null);
  const [events, setEvents] = useState("");
  const [muted, setMuted] = useState("");
  useResourceUpdates(watchedKinds, [["notifications", "preferences"]]);
  useEffect(() => {
    if (prefs.data && draft === null) {
      const { updatedAt: _, ...current } = prefs.data;
      setDraft(current);
      setEvents((prefs.data.events ?? []).join(", "));
      setMuted((prefs.data.muted ?? []).join(", "));
    }
  }, [prefs.data, draft]);
  if (prefs.isError) {
    return (
      <Alert tone="info" title={intl.formatMessage(m.notifyMineUnavailable)}>
        {errorMessage(intl, prefs.error)}
      </Alert>
    );
  }
  if (!draft) {
    return null;
  }
  // Preferences change as every change does, previewed first. Their
  // preview is sent again to apply it, since only this request carries
  // the person's grant on their own preferences.
  const save = (event: FormEvent) => {
    event.preventDefault();
    const body: Preferences = { ...draft, events: patterns(events), muted: patterns(muted) };
    void flow.preview(
      (dryRun) =>
        unwrap(
          api.PUT("/api/v1/notifications/preferences", { params: { query: { dryRun } }, body }),
        ),
      { resend: true },
    );
  };
  const test = async () => {
    try {
      await unwrap(api.POST("/api/v1/notifications/preferences/test"));
      toast.success(intl.formatMessage(m.notifyMineTestSent));
    } catch (error) {
      toast.error(errorMessage(intl, error));
    }
  };
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.notifyMine)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.notifyMineHelp)}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-4" onSubmit={save}>
          <div className="flex items-center gap-2">
            <Switch
              id={`${id}-email`}
              checked={draft.email}
              onCheckedChange={(v) => setDraft({ ...draft, email: v })}
            />
            <Label htmlFor={`${id}-email`}>{intl.formatMessage(m.notifyMineEmail)}</Label>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-events`}>{intl.formatMessage(m.notifyEvents)}</Label>
              <Input
                id={`${id}-events`}
                placeholder="*.failed, security.*"
                value={events}
                onChange={(e) => setEvents(e.target.value)}
              />
              <p className="text-muted-foreground text-xs">
                {intl.formatMessage(m.notifyMineEventsHelp)}
              </p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-min`}>{intl.formatMessage(m.notifyMinSeverity)}</Label>
              <SeveritySelect
                id={`${id}-min`}
                value={draft.minSeverity ?? ""}
                allowEmpty={intl.formatMessage(m.notifyDefaultWarning)}
                onChange={(v) =>
                  setDraft({
                    ...draft,
                    minSeverity: (v || undefined) as Preferences["minSeverity"],
                  })
                }
              />
            </div>
            <div className="flex flex-col gap-1.5 md:col-span-2">
              <Label htmlFor={`${id}-muted`}>{intl.formatMessage(m.notifyMuted)}</Label>
              <Input id={`${id}-muted`} value={muted} onChange={(e) => setMuted(e.target.value)} />
            </div>
          </div>
          <QuietHoursFields
            value={draft.quietHours}
            onChange={(q) => setDraft({ ...draft, quietHours: q })}
          />
          <div className="flex flex-wrap gap-2">
            <Button type="submit" busy={flow.busy}>
              {intl.formatMessage(m.notifySave)}
            </Button>
            <Button type="button" variant="outline" disabled={!draft.email} onClick={test}>
              {intl.formatMessage(m.notifyMineTest)}
            </Button>
          </div>
        </form>
      </CardContent>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

function AddChannel({ onDone }: { onDone: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const flow = useChangeFlow({ onApplied: onDone });
  const id = useId();
  const types = useQuery({
    queryKey: ["notifications", "types"],
    queryFn: () => unwrap(api.GET("/api/v1/notifications/types")),
  });
  const [name, setName] = useState("");
  const [type, setType] = useState("");
  const [events, setEvents] = useState("*.failed, core.safe_mode.*, core.hook.disabled");
  const [minSeverity, setMinSeverity] = useState("warning");
  const [locale, setLocale] = useState("");
  const [quiet, setQuiet] = useState<QuietHours | undefined>(undefined);
  const chosen = (types.data?.types ?? []).find((t) => t.name === type);
  const hints: UIHints = {
    fields: Object.fromEntries(
      (chosen?.secrets ?? []).map((s) => [s, { sensitive: true, widget: "password" as const }]),
    ),
  };
  const submit = ({ spec, secrets }: { spec: unknown; secrets: Record<string, string> }) =>
    flow.preview(
      (dryRun) =>
        unwrap(
          api.POST("/api/v1/core/notification-channels", {
            params: { query: { dryRun } },
            body: {
              spec: {
                name,
                type,
                settings: spec,
                events: patterns(events),
                minSeverity: (minSeverity || undefined) as Schemas["ChannelSpec"]["minSeverity"],
                quietHours: quiet,
                locale: locale || undefined,
              },
              secrets: Object.fromEntries(
                Object.entries(secrets).map(([k, v]) => [`settings.${k}`, v]),
              ),
            },
          }),
        ),
      { resend: Object.keys(secrets).length > 0 },
    );
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.notifyAddChannel)}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-3 md:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-name`}>{intl.formatMessage(m.notifyChannelName)}</Label>
            <Input id={`${id}-name`} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-type`}>{intl.formatMessage(m.notifyChannelType)}</Label>
            <Select value={type} onValueChange={setType}>
              <SelectTrigger id={`${id}-type`}>
                <SelectValue placeholder={intl.formatMessage(m.notifyChannelTypeChoose)} />
              </SelectTrigger>
              <SelectContent>
                {(types.data?.types ?? []).map((t) => (
                  <SelectItem key={t.name} value={t.name}>
                    {t.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-events`}>{intl.formatMessage(m.notifyEvents)}</Label>
            <Input id={`${id}-events`} value={events} onChange={(e) => setEvents(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-min`}>{intl.formatMessage(m.notifyMinSeverity)}</Label>
            <SeveritySelect
              id={`${id}-min`}
              value={minSeverity}
              allowEmpty={intl.formatMessage(m.notifyAnySeverity)}
              onChange={setMinSeverity}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-locale`}>{intl.formatMessage(m.notifyChannelLanguage)}</Label>
            <Select
              value={locale === "" ? "default" : locale}
              onValueChange={(v) => setLocale(v === "default" ? "" : v)}
            >
              <SelectTrigger id={`${id}-locale`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">
                  {intl.formatMessage(m.notifyChannelLanguageDefault)}
                </SelectItem>
                {availableLocales(false)
                  .filter((l) => l.tag !== "en")
                  .map((l) => (
                    <SelectItem key={l.tag} value={l.tag}>
                      {l.name}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <QuietHoursFields value={quiet} onChange={setQuiet} />
        {chosen ? (
          <>
            <p className="text-muted-foreground text-sm">{chosen.description}</p>
            <SchemaForm
              key={chosen.name}
              schema={(chosen.schema ?? { type: "object" }) as JSONSchema}
              hints={hints}
              submitLabel={intl.formatMessage(m.notifyPreview)}
              busy={flow.busy}
              onSubmit={submit}
            />
          </>
        ) : null}
      </CardContent>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

function Channels() {
  const intl = useIntl();
  const api = useApi();
  const f = useFormat();
  const flow = useChangeFlow();
  const [adding, setAdding] = useState(false);
  const channels = useQuery({
    queryKey: ["notifications", "channels"],
    queryFn: () => unwrap(api.GET("/api/v1/core/notification-channels")),
    retry: false,
  });
  useResourceUpdates(watchedKinds, [["notifications", "channels"]]);
  if (channels.isError && forbidden(channels.error)) {
    return null;
  }
  const test = async (id: string) => {
    try {
      const cs = await unwrap(
        api.POST("/api/v1/core/notification-channels/{id}/actions/test", {
          params: { path: { id } },
        }),
      );
      const out = (cs.changes?.[0]?.output ?? {}) as { notification?: Schemas["Notification"] };
      if (out.notification?.state === "sent") {
        toast.success(intl.formatMessage(m.notifyTestSent));
      } else {
        toast.error(
          intl.formatMessage(m.notifyTestFailed, { reason: out.notification?.lastError ?? "" }),
        );
      }
    } catch (error) {
      toast.error(errorMessage(intl, error));
    }
  };
  const remove = (id: string) =>
    flow.preview((dryRun) =>
      unwrap(
        api.DELETE("/api/v1/core/notification-channels/{id}", {
          params: { path: { id }, query: { dryRun } },
        }),
      ),
    );
  const items = channels.data?.items ?? [];
  return (
    <section className="flex flex-col gap-3" aria-labelledby="notify-channels">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-col gap-1">
          <h2 id="notify-channels" className="font-semibold text-lg">
            {intl.formatMessage(m.notifyChannels)}
          </h2>
          <p className="text-muted-foreground text-sm">
            {intl.formatMessage(m.notifyChannelsHelp)}
          </p>
        </div>
        <Button variant="outline" onClick={() => setAdding((v) => !v)}>
          {intl.formatMessage(adding ? m.notifyCancel : m.notifyAddChannel)}
        </Button>
      </div>
      {adding ? <AddChannel onDone={() => setAdding(false)} /> : null}
      {items.length === 0 && !channels.isPending ? (
        <EmptyState icon={Bell} title={intl.formatMessage(m.notifyNoChannels)} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{intl.formatMessage(m.notifyChannelName)}</TableHead>
              <TableHead>{intl.formatMessage(m.notifyChannelType)}</TableHead>
              <TableHead>{intl.formatMessage(m.notifyEvents)}</TableHead>
              <TableHead>{intl.formatMessage(m.notifyLastSent)}</TableHead>
              <TableHead>
                <span className="sr-only">{intl.formatMessage(m.notifyActions)}</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((c) => (
              <TableRow key={c.id}>
                <TableCell className="font-medium">
                  {c.spec.name}
                  {c.spec.disabled ? (
                    <Badge variant="outline" className="ms-2">
                      {intl.formatMessage(m.notifyDisabled)}
                    </Badge>
                  ) : null}
                  {c.status?.lastError ? (
                    <p className="text-danger text-xs">{c.status.lastError}</p>
                  ) : null}
                </TableCell>
                <TableCell>{c.spec.type}</TableCell>
                <TableCell className="font-mono text-xs">
                  {(c.spec.events ?? []).join(", ")}
                </TableCell>
                <TableCell>
                  {c.status?.lastSent
                    ? f.relative(new Date(c.status.lastSent))
                    : intl.formatMessage(m.notifyNever)}
                </TableCell>
                <TableCell className="flex gap-2">
                  <Button size="sm" variant="outline" onClick={() => test(c.id)}>
                    {intl.formatMessage(m.notifyTest)}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => remove(c.id)}>
                    {intl.formatMessage(m.notifyDelete)}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <ChangeDialog flow={flow} />
    </section>
  );
}

function Relay() {
  const intl = useIntl();
  const api = useApi();
  const f = useFormat();
  const flow = useChangeFlow();
  const id = useId();
  const relays = useQuery({
    queryKey: ["notifications", "relay"],
    queryFn: () => unwrap(api.GET("/api/v1/core/mail-relays")),
    retry: false,
  });
  useResourceUpdates(watchedKinds, [["notifications", "relay"]]);
  const current = relays.data?.items?.[0];
  const [spec, setSpec] = useState<Schemas["RelaySpec"]>({
    host: "",
    from: "",
    security: "starttls",
  });
  const [password, setPassword] = useState("");
  const [to, setTo] = useState("");
  // The form follows the relay's settings when they change, not every
  // time its status does, such as after each email sent.
  const saved = JSON.stringify(current?.spec ?? null);
  useEffect(() => {
    const next = JSON.parse(saved) as Schemas["RelaySpec"] | null;
    if (next) {
      setSpec(next);
    }
  }, [saved]);
  if (relays.isError && forbidden(relays.error)) {
    return null;
  }
  const save = (event: FormEvent) => {
    event.preventDefault();
    const secrets = password !== "" ? { password } : undefined;
    const body = { ...spec, password: undefined };
    flow.preview(
      (dryRun) =>
        current
          ? unwrap(
              api.PUT("/api/v1/core/mail-relays/{id}", {
                params: { path: { id: current.id }, query: { dryRun } },
                body: {
                  spec: { ...body, password: secrets ? undefined : current.spec.password },
                  secrets,
                  revision: current.revision,
                },
              }),
            )
          : unwrap(
              api.POST("/api/v1/core/mail-relays", {
                params: { query: { dryRun } },
                body: { spec: body, secrets },
              }),
            ),
      { resend: secrets !== undefined },
    );
  };
  const test = async () => {
    if (!current) {
      return;
    }
    try {
      const cs = await unwrap(
        api.POST("/api/v1/core/mail-relays/{id}/actions/test", {
          params: { path: { id: current.id } },
          body: { to },
        }),
      );
      const out = (cs.changes?.[0]?.output ?? {}) as Schemas["RelayTestOutput"];
      if (out.sent) {
        toast.success(intl.formatMessage(m.notifyRelaySent, { to }));
      } else {
        toast.error(intl.formatMessage(m.notifyTestFailed, { reason: out.error ?? "" }));
      }
    } catch (error) {
      toast.error(errorMessage(intl, error));
    }
  };
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{intl.formatMessage(m.notifyRelay)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.notifyRelayHelp)}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {current?.status?.lastError ? (
          <Alert tone="warning" title={intl.formatMessage(m.notifyRelayLastError)}>
            {current.status.lastError}
          </Alert>
        ) : current?.status?.lastSent ? (
          <p className="text-muted-foreground text-sm">
            {intl.formatMessage(m.notifyRelayLastSent, {
              when: f.relative(new Date(current.status.lastSent)),
            })}
          </p>
        ) : null}
        <form className="grid gap-3 md:grid-cols-2" onSubmit={save}>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-host`}>{intl.formatMessage(m.notifyRelayHost)}</Label>
            <Input
              id={`${id}-host`}
              required
              value={spec.host}
              onChange={(e) => setSpec({ ...spec, host: e.target.value })}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-port`}>{intl.formatMessage(m.notifyRelayPort)}</Label>
            <Input
              id={`${id}-port`}
              type="number"
              min={1}
              max={65535}
              value={spec.port ?? ""}
              onChange={(e) =>
                setSpec({
                  ...spec,
                  port: e.target.value === "" ? undefined : Number(e.target.value),
                })
              }
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-security`}>{intl.formatMessage(m.notifyRelaySecurity)}</Label>
            <Select
              value={spec.security ?? "starttls"}
              onValueChange={(v) =>
                setSpec({ ...spec, security: v as Schemas["RelaySpec"]["security"] })
              }
            >
              <SelectTrigger id={`${id}-security`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(["starttls", "tls", "none"] as const).map((s) => (
                  <SelectItem key={s} value={s}>
                    {intl.formatMessage(m.notifyRelaySecurityValue, { security: s })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-from`}>{intl.formatMessage(m.notifyRelayFrom)}</Label>
            <Input
              id={`${id}-from`}
              type="email"
              required
              value={spec.from}
              onChange={(e) => setSpec({ ...spec, from: e.target.value })}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-user`}>{intl.formatMessage(m.notifyRelayUser)}</Label>
            <Input
              id={`${id}-user`}
              autoComplete="off"
              value={spec.username ?? ""}
              onChange={(e) => setSpec({ ...spec, username: e.target.value || undefined })}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-password`}>{intl.formatMessage(m.notifyRelayPassword)}</Label>
            <Input
              id={`${id}-password`}
              type="password"
              autoComplete="new-password"
              placeholder={
                current?.spec.password ? intl.formatMessage(m.notifyRelayPasswordKept) : ""
              }
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <div className="flex flex-wrap items-end gap-2 md:col-span-2">
            <Button type="submit" disabled={flow.busy}>
              {intl.formatMessage(m.notifyPreview)}
            </Button>
            {current ? (
              <>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={`${id}-to`}>{intl.formatMessage(m.notifyRelayTestTo)}</Label>
                  <Input
                    id={`${id}-to`}
                    type="email"
                    value={to}
                    onChange={(e) => setTo(e.target.value)}
                  />
                </div>
                <Button type="button" variant="outline" disabled={to === ""} onClick={test}>
                  {intl.formatMessage(m.notifyRelayTest)}
                </Button>
              </>
            ) : null}
          </div>
        </form>
      </CardContent>
      <ChangeDialog flow={flow} />
    </Card>
  );
}

/** How long a delivery may take to send before the log stops checking on it. */
const sendingFor = 60_000;

/**
 * When the log should be read again for the deliveries that wait, which no
 * stream reports on: when the first is due to be sent again or to leave
 * quiet hours, and, for one being sent as the log was read, a few seconds
 * later.
 */
function firstDue(
  deliveries: readonly { state: string; createdAt: string; nextAttempt?: string }[],
  readAt: number,
): number | null {
  let first: number | null = null;
  for (const d of deliveries) {
    if (d.state !== "pending" && d.state !== "held") {
      continue;
    }
    let at: number;
    if (d.nextAttempt) {
      at = Date.parse(d.nextAttempt);
    } else if (readAt - Date.parse(d.createdAt) < sendingFor) {
      at = readAt + 5000;
    } else {
      continue;
    }
    if (!Number.isNaN(at) && (first === null || at < first)) {
      first = at;
    }
  }
  return first;
}

function Log() {
  const intl = useIntl();
  const api = useApi();
  const f = useFormat();
  const log = useQuery({
    queryKey: ["notifications", "log"],
    queryFn: () => unwrap(api.GET("/api/v1/notifications", { params: { query: { limit: 50 } } })),
    retry: false,
  });
  useResourceUpdates(watchedKinds, [["notifications", "log"]]);
  // Events turn into notifications, some held for quiet hours, which no
  // status says; the log reads again a moment after the timeline moves.
  useTimelineUpdates(null, [["notifications", "log"]], 2000);
  const sent = log.data?.notifications ?? [];
  useRefreshAt(firstDue(sent, log.dataUpdatedAt), [["notifications", "log"]]);
  if (log.isError || sent.length === 0) {
    return null;
  }
  return (
    <section className="flex flex-col gap-3" aria-labelledby="notify-log">
      <h2 id="notify-log" className="font-semibold text-lg">
        {intl.formatMessage(m.notifyLog)}
      </h2>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{intl.formatMessage(m.notifyLogWhen)}</TableHead>
            <TableHead>{intl.formatMessage(m.notifyLogWhat)}</TableHead>
            <TableHead>{intl.formatMessage(m.notifyLogState)}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sent.map((n) => (
            <TableRow key={n.id}>
              <TableCell>{f.relative(new Date(n.createdAt))}</TableCell>
              <TableCell>
                {n.title}
                {n.lastError ? <p className="text-danger text-xs">{n.lastError}</p> : null}
              </TableCell>
              <TableCell>
                <Badge
                  variant={
                    n.state === "failed" ? "danger" : n.state === "sent" ? "success" : "neutral"
                  }
                >
                  {intl.formatMessage(m.notifyState, { state: n.state })}
                </Badge>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  );
}

/** Notifications: what reaches you, the server's channels, the mail relay and what was sent. */
export default function NotificationSettings() {
  const intl = useIntl();
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.notifyTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.notifyDescription)}</p>
      </div>
      <MyPreferences />
      <Channels />
      <Relay />
      <Log />
    </div>
  );
}
