import { Check, ChevronsUpDown, Eraser } from "lucide-react";
import { type ReactNode, useId, useMemo, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/design/components/ui/card";
import { RadioGroup, RadioGroupItem, Switch } from "@/design/components/ui/choice";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/design/components/ui/command";
import { Label } from "@/design/components/ui/field";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { toast } from "@/design/components/ui/toaster";
import { availableLocales, useLocaleChoice } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { type GeneralPreferences, useGeneralPreferences } from "../general";
import { messages } from "../messages";
import { generalMessages as m } from "../screens/messages";
import { clearRecent } from "../screens/recent";

/** The time zone this browser is in. */
function browserZone(): string {
  return new Intl.DateTimeFormat().resolvedOptions().timeZone;
}

/** Every time zone the browser knows, UTC first. */
function timeZones(): string[] {
  const zones = Intl.supportedValuesOf("timeZone").filter((z) => z !== "UTC");
  return ["UTC", ...zones];
}

/** A time zone's name as people read it: America/New_York becomes America/New York. */
function zoneName(zone: string): string {
  return zone.replaceAll("_", " ");
}

function Setting({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: (labelledBy: string) => ReactNode;
}) {
  const id = useId();
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      {children(`${id}-title`)}
    </Card>
  );
}

/**
 * Picks a time zone from every one the browser knows, by typing part of
 * its name in a list that opens under the button.
 */
function TimeZonePicker({
  value,
  onChange,
  labelledBy,
}: {
  value: string;
  onChange: (zone: string) => void;
  labelledBy: string;
}) {
  const intl = useIntl();
  const [open, setOpen] = useState(false);
  const zones = useMemo(timeZones, []);
  const here = useMemo(browserZone, []);
  const button = useRef<HTMLButtonElement>(null);
  const valueId = useId();
  const listId = useId();
  const close = () => {
    setOpen(false);
    button.current?.focus();
  };
  const choose = (zone: string) => {
    onChange(zone);
    close();
  };
  // Zone names are the database's identifiers, not words to translate.
  const browserLabel = intl.formatMessage(m.timeZoneBrowser, {
    zone: (
      <span key="zone" translate="no">
        {zoneName(here)}
      </span>
    ),
  });
  return (
    <div className="flex w-full max-w-md flex-col gap-2">
      <Button
        ref={button}
        variant="outline"
        aria-expanded={open}
        aria-controls={listId}
        aria-labelledby={`${labelledBy} ${valueId}`}
        className="justify-between font-normal"
        onClick={() => setOpen((o) => !o)}
      >
        <span id={valueId} className="truncate">
          {value ? <span translate="no">{zoneName(value)}</span> : browserLabel}
        </span>
        <ChevronsUpDown className="text-muted-foreground" aria-hidden="true" />
      </Button>
      {open ? (
        <Command
          id={listId}
          label={intl.formatMessage(m.timeZone)}
          className="h-auto rounded-md border border-border"
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault();
              close();
            }
          }}
        >
          <CommandInput autoFocus placeholder={intl.formatMessage(m.timeZoneSearch)} />
          <CommandList className="max-h-64">
            <CommandEmpty>{intl.formatMessage(m.timeZoneNone)}</CommandEmpty>
            <CommandItem value={`browser ${here}`} onSelect={() => choose("")}>
              <Check
                className={cn(value === "" ? "opacity-100" : "opacity-0")}
                aria-hidden="true"
              />
              {browserLabel}
            </CommandItem>
            {zones.map((zone) => (
              <CommandItem
                key={zone}
                value={zone}
                keywords={[zoneName(zone)]}
                onSelect={() => choose(zone)}
              >
                <Check
                  className={cn(value === zone ? "opacity-100" : "opacity-0")}
                  aria-hidden="true"
                />
                <span translate="no">{zoneName(zone)}</span>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      ) : null}
    </div>
  );
}

/** A moment to show what each clock looks like: a quarter to four in the afternoon. */
const sample = new Date(Date.UTC(2026, 0, 15, 15, 45));

/**
 * How the panel works for the person in this browser: the language, the
 * time zone and clock times show in, letter shortcuts, and whether the
 * command bar remembers what they opened.
 */
export default function GeneralSettings() {
  const intl = useIntl();
  const { preferences, setPreferences } = useGeneralPreferences();
  const [locale, setLocale] = useLocaleChoice();
  // An example time, marked up as the time it is.
  const clock = (hourCycle: "h12" | "h23" | undefined) => (
    <time key="example" dateTime="15:45">
      {intl.formatTime(sample, {
        timeStyle: "short",
        timeZone: "UTC",
        ...(hourCycle ? { hourCycle } : {}),
      })}
    </time>
  );
  const clocks: { value: GeneralPreferences["hourCycle"]; label: ReactNode }[] = [
    { value: "", label: intl.formatMessage(m.clockLanguage, { example: clock(undefined) }) },
    { value: "h12", label: intl.formatMessage(m.clock12, { example: clock("h12") }) },
    { value: "h23", label: intl.formatMessage(m.clock24, { example: clock("h23") }) },
  ];
  const lettersId = useId();
  const recentId = useId();
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.generalTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(m.description)}</p>
      </div>
      <Setting
        title={intl.formatMessage(messages.appearanceLanguage)}
        description={intl.formatMessage(messages.appearanceLanguageHelp)}
      >
        {(labelledBy) => (
          <Select
            value={locale ?? "browser"}
            onValueChange={(v) => setLocale(v === "browser" ? null : v)}
          >
            <SelectTrigger className="max-w-xs" aria-labelledby={labelledBy}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="browser">
                {intl.formatMessage(messages.languageBrowser)}
              </SelectItem>
              {availableLocales().map((l) => (
                <SelectItem key={l.tag} value={l.tag}>
                  {l.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Setting>
      <Setting
        title={intl.formatMessage(m.timeZone)}
        description={intl.formatMessage(m.timeZoneHelp)}
      >
        {(labelledBy) => (
          <TimeZonePicker
            value={preferences.timeZone}
            onChange={(timeZone) => setPreferences({ timeZone })}
            labelledBy={labelledBy}
          />
        )}
      </Setting>
      <Setting title={intl.formatMessage(m.clock)} description={intl.formatMessage(m.clockHelp)}>
        {(labelledBy) => (
          <RadioGroup
            aria-labelledby={labelledBy}
            value={preferences.hourCycle || "language"}
            onValueChange={(v) =>
              setPreferences({
                hourCycle: v === "language" ? "" : (v as GeneralPreferences["hourCycle"]),
              })
            }
            className="gap-3"
          >
            {clocks.map((c) => {
              const value = c.value || "language";
              return (
                <div key={value} className="flex items-center gap-3">
                  <RadioGroupItem id={`${labelledBy}-${value}`} value={value} />
                  <Label htmlFor={`${labelledBy}-${value}`}>{c.label}</Label>
                </div>
              );
            })}
          </RadioGroup>
        )}
      </Setting>
      <Setting
        title={intl.formatMessage(m.letters)}
        description={intl.formatMessage(m.lettersHelp)}
      >
        {() => (
          <div className="flex items-center gap-3">
            <Switch
              id={lettersId}
              checked={preferences.letterShortcuts}
              onCheckedChange={(letterShortcuts) => setPreferences({ letterShortcuts })}
            />
            <Label htmlFor={lettersId}>{intl.formatMessage(m.lettersOn)}</Label>
          </div>
        )}
      </Setting>
      <Setting title={intl.formatMessage(m.recent)} description={intl.formatMessage(m.recentHelp)}>
        {() => (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              <Switch
                id={recentId}
                checked={preferences.rememberRecent}
                onCheckedChange={(rememberRecent) => {
                  setPreferences({ rememberRecent });
                  if (!rememberRecent) {
                    clearRecent();
                  }
                }}
              />
              <Label htmlFor={recentId}>{intl.formatMessage(m.recentOn)}</Label>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                clearRecent();
                toast.success(intl.formatMessage(m.recentCleared));
              }}
            >
              <Eraser aria-hidden="true" />
              {intl.formatMessage(m.recentClear)}
            </Button>
          </div>
        )}
      </Setting>
    </div>
  );
}
