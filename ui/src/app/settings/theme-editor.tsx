import { Check, Download, Eye, EyeOff, Plus, RotateCcw, Trash2, Upload, X } from "lucide-react";
import { type ChangeEvent, useEffect, useId, useMemo, useRef, useState } from "react";
import { type IntlShape, type MessageDescriptor, useIntl } from "react-intl";
import { issueMessage } from "@/design/components/schema-form";
import { Button } from "@/design/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Switch } from "@/design/components/ui/choice";
import { Alert, Badge } from "@/design/components/ui/display";
import { Field, Label } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/design/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/design/components/ui/tabs";
import { toast } from "@/design/components/ui/toaster";
import { type ContrastRow, checkTheme, consoleColors, contrastTable } from "@/design/theme/check";
import { isColor } from "@/design/theme/color";
import { resolveLayers } from "@/design/theme/layers";
import { useTheme } from "@/design/theme/provider";
import { builtinTheme, builtinThemes } from "@/design/theme/spec";
import {
  type ConsoleKey,
  consoleKeys,
  type PaletteKey,
  paletteKeys,
  type Theme,
  type ThemeProblem,
} from "@/design/theme/types";
import {
  readThemeDocument,
  readThemeFile,
  type ThemeReading,
  themeToJSON,
  themeToYAML,
} from "@/design/theme/validate";
import { messages } from "../messages";
import { ThemePreview } from "./theme-preview";

type Mode = "light" | "dark";

/** Explains a theme problem in the user's language. */
export function problemMessage(intl: IntlShape, p: ThemeProblem): string {
  const token = (name: string) =>
    p.mode === "console" ? consoleLabel(intl, name) : tokenLabel(intl, name);
  const mode = intl.formatMessage(messages.themeModeName, { mode: p.mode });
  if (p.kind === "font") {
    return intl.formatMessage(messages.themeProblemFont, {
      font: p.purpose ?? "",
      which: p.foreground,
    });
  }
  if (p.kind === "alpha") {
    return intl.formatMessage(messages.themeProblemAlpha, { mode, token: token(p.foreground) });
  }
  if (p.kind === "meaning") {
    return intl.formatMessage(messages.themeProblemMeaning, {
      mode,
      token: token(p.foreground),
      hue: hueName(intl, p.foreground, p.purpose ?? ""),
    });
  }
  if (p.kind === "color") {
    return intl.formatMessage(messages.themeProblemColor, { mode, token: token(p.foreground) });
  }
  return intl.formatMessage(messages.themeProblemContrast, {
    mode,
    foreground: token(p.foreground),
    background: token(p.background ?? ""),
    ratio: intl.formatNumber(p.ratio ?? 0, { maximumFractionDigits: 2 }),
    minimum: intl.formatNumber(p.minimum ?? 0, { maximumFractionDigits: 1 }),
  });
}

const tokenMessages: Record<string, keyof typeof messages> = {
  surface: "tokenSurface",
  surfaceRaised: "tokenSurfaceRaised",
  surfaceSunken: "tokenSurfaceSunken",
  text: "tokenText",
  textMuted: "tokenTextMuted",
  border: "tokenBorder",
  borderStrong: "tokenBorderStrong",
  brand: "tokenBrand",
  brandText: "tokenBrandText",
  accent: "tokenAccent",
  accentText: "tokenAccentText",
  success: "tokenSuccess",
  successText: "tokenSuccessText",
  warning: "tokenWarning",
  warningText: "tokenWarningText",
  danger: "tokenDanger",
  dangerText: "tokenDangerText",
  info: "tokenInfo",
  infoText: "tokenInfoText",
  focus: "tokenFocus",
  overlay: "tokenOverlay",
  accentSubtle: "tokenAccentSubtle",
  successSubtle: "tokenSuccessSubtle",
  warningSubtle: "tokenWarningSubtle",
  dangerSubtle: "tokenDangerSubtle",
  infoSubtle: "tokenInfoSubtle",
};

const consoleMessages: Record<ConsoleKey, keyof typeof messages> = {
  background: "consoleBackground",
  foreground: "consoleForeground",
  accent: "consoleAccent",
  success: "consoleSuccess",
  warning: "consoleWarning",
  danger: "consoleDanger",
};

/** A console color's name for people: "Text" for foreground. */
export function consoleLabel(intl: IntlShape, name: string): string {
  const key = consoleMessages[name as ConsoleKey];
  return key ? intl.formatMessage(messages[key]) : name;
}

const builtinMessages: Record<string, keyof typeof messages> = {
  racksail: "themeBuiltinRacksail",
  "high-contrast": "themeBuiltinHighContrast",
  compact: "themeBuiltinCompact",
};

/** A built-in theme's name in the person's language; other themes keep their own. */
export function builtinName(intl: IntlShape, theme: Pick<Theme, "id" | "name">): string {
  const key = builtinMessages[theme.id];
  return key ? intl.formatMessage(messages[key]) : theme.name;
}

/** A token's name for people: "Raised surface" for surfaceRaised, "Chart color 3" for chart3. */
export function tokenLabel(intl: IntlShape, name: string): string {
  const chart = /^chart(\d+)$/.exec(name);
  if (chart) {
    return intl.formatMessage(messages.tokenChart, { n: Number(chart[1]) });
  }
  const key = tokenMessages[name];
  return key ? intl.formatMessage(messages[key]) : name;
}

// The spec's pairs and meanings describe themselves in English for the
// server's messages; the panel has its own words for each, and falls back
// to the spec's for one added since.
function specMessage(intl: IntlShape, id: string, fallback: string): string {
  return Object.hasOwn(intl.messages, id) ? intl.formatMessage({ id }) : fallback;
}

/** What a checked pair of colors is used for, such as "body text on the page". */
export function pairPurpose(
  intl: IntlShape,
  pair: { foreground: string; background: string; purpose: string },
): string {
  const chart = /^chart(\d+)$/.exec(pair.foreground);
  if (chart) {
    return intl.formatMessage(messages.themePurposeChart, { n: Number(chart[1]) });
  }
  return specMessage(intl, `app.theme.purpose.${pair.foreground}.${pair.background}`, pair.purpose);
}

/** The hue a status color has to keep, such as "green" for success. */
export function hueName(intl: IntlShape, token: string, fallback: string): string {
  return specMessage(intl, `app.theme.hue.${token}`, fallback);
}

function download(name: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function ColorField({
  label,
  value,
  onChange,
  alpha,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  alpha?: boolean;
}) {
  const intl = useIntl();
  const [text, setText] = useState(value);
  useEffect(() => setText(value), [value]);
  const valid = isColor(text) && (alpha || text.length === 7);
  return (
    <Field
      label={label}
      error={
        valid
          ? undefined
          : intl.formatMessage(messages.themeBadColor, { alpha: alpha ? "yes" : "no" })
      }
    >
      {(control) => (
        <div className="flex items-center gap-2">
          <input
            type="color"
            aria-label={intl.formatMessage(messages.themePickColor, { token: label })}
            value={isColor(value) ? value.slice(0, 7) : ""}
            onChange={(e) =>
              onChange(
                alpha && value.length === 9
                  ? `${e.target.value.toUpperCase()}${value.slice(7)}`
                  : e.target.value.toUpperCase(),
              )
            }
            className="h-control-md w-12 shrink-0 cursor-pointer rounded-md border border-input bg-card p-1"
          />
          <Input
            {...control}
            value={text}
            spellCheck={false}
            className="font-mono uppercase"
            onChange={(e) => {
              const next = e.target.value.trim();
              setText(next);
              if (isColor(next) && (alpha || next.length === 7)) {
                onChange(next.toUpperCase());
              }
            }}
          />
        </div>
      )}
    </Field>
  );
}

type CheckedPlace = ContrastRow["mode"];

function ContrastChecker({ theme }: { theme: Theme }) {
  const intl = useIntl();
  const rows = useMemo(() => contrastTable(theme), [theme]);
  const [mode, setMode] = useState<CheckedPlace>("light");
  const [onlyFailing, setOnlyFailing] = useState(false);
  const failing = rows.filter((r) => !r.pass).length;
  const switchId = useId();
  return (
    <Card aria-labelledby={`${switchId}-title`}>
      <CardHeader>
        <CardTitle id={`${switchId}-title`}>
          {intl.formatMessage(messages.themeContrastTitle)}
        </CardTitle>
        <CardDescription>
          {failing === 0
            ? intl.formatMessage(messages.themeContrastAllPass, { count: rows.length })
            : intl.formatMessage(messages.themeContrastFailing, { count: failing })}
        </CardDescription>
      </CardHeader>
      <Tabs
        value={mode}
        onValueChange={(v) => setMode(v as CheckedPlace)}
        className="flex flex-col gap-3"
      >
        <div className="flex flex-wrap items-center justify-between gap-3">
          <TabsList>
            {(["light", "dark", "console"] as const).map((place) => (
              <TabsTrigger key={place} value={place}>
                {intl.formatMessage(messages.themeModeName, { mode: place })}
              </TabsTrigger>
            ))}
          </TabsList>
          <div className="flex items-center gap-2">
            <Switch id={switchId} checked={onlyFailing} onCheckedChange={setOnlyFailing} />
            <Label htmlFor={switchId} className="font-normal">
              {intl.formatMessage(messages.themeContrastOnlyFailing)}
            </Label>
          </div>
        </div>
        {(["light", "dark", "console"] as const).map((m) => (
          <TabsContent key={m} value={m} className="max-h-[28rem] overflow-auto">
            <ContrastTable rows={rows.filter((r) => r.mode === m && (!onlyFailing || !r.pass))} />
          </TabsContent>
        ))}
      </Tabs>
    </Card>
  );
}

function ContrastTable({ rows }: { rows: ContrastRow[] }) {
  const intl = useIntl();
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{intl.formatMessage(messages.themeContrastPair)}</TableHead>
          <TableHead className="text-end">
            {intl.formatMessage(messages.themeContrastRatio)}
          </TableHead>
          <TableHead className="text-end">
            {intl.formatMessage(messages.themeContrastNeeds)}
          </TableHead>
          <TableHead>{intl.formatMessage(messages.themeContrastResult)}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((r) => (
          <TableRow key={`${r.mode}-${r.foreground}-${r.background}`}>
            <TableCell>
              <p className="font-medium">
                {intl.formatMessage(messages.themeContrastOn, {
                  foreground: (r.mode === "console" ? consoleLabel : tokenLabel)(
                    intl,
                    r.foreground,
                  ),
                  background: (r.mode === "console" ? consoleLabel : tokenLabel)(
                    intl,
                    r.background,
                  ),
                })}
              </p>
              <p className="text-muted-foreground text-xs">{pairPurpose(intl, r)}</p>
            </TableCell>
            <TableCell className="text-end tabular-nums">
              {r.ratio === null
                ? intl.formatMessage(messages.themeContrastNoRatio)
                : intl.formatMessage(messages.themeRatio, {
                    ratio: intl.formatNumber(r.ratio, { maximumFractionDigits: 2 }),
                  })}
            </TableCell>
            <TableCell className="text-end tabular-nums">
              {intl.formatMessage(messages.themeRatio, {
                ratio: intl.formatNumber(r.minimum, { maximumFractionDigits: 1 }),
              })}
            </TableCell>
            <TableCell>
              {r.pass ? (
                <Badge variant="success">
                  <Check aria-hidden="true" />
                  {intl.formatMessage(messages.themeContrastPass)}
                </Badge>
              ) : (
                <Badge variant="danger">
                  <X aria-hidden="true" />
                  {intl.formatMessage(messages.themeContrastFail)}
                </Badge>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** A console color: its own value, or empty for Racksail's. */
function ConsoleColorField({
  label,
  value,
  fallback,
  onChange,
}: {
  label: string;
  value: string | undefined;
  fallback: string;
  onChange: (value: string | undefined) => void;
}) {
  const intl = useIntl();
  const [text, setText] = useState(value ?? "");
  useEffect(() => setText(value ?? ""), [value]);
  const valid = text === "" || (isColor(text) && text.length === 7);
  return (
    <Field
      label={label}
      error={valid ? undefined : intl.formatMessage(messages.themeBadColor, { alpha: "no" })}
    >
      {(control) => (
        <div className="flex items-center gap-2">
          <input
            type="color"
            aria-label={intl.formatMessage(messages.themePickColor, { token: label })}
            value={(value ?? fallback).slice(0, 7)}
            onChange={(e) => onChange(e.target.value.toUpperCase())}
            className="h-control-md w-12 shrink-0 cursor-pointer rounded-md border border-input bg-card p-1"
          />
          <Input
            {...control}
            value={text}
            spellCheck={false}
            placeholder={intl.formatMessage(messages.consoleEmpty, { color: fallback })}
            className="font-mono uppercase placeholder:normal-case"
            onChange={(e) => {
              const next = e.target.value.trim();
              setText(next);
              if (next === "") {
                onChange(undefined);
              } else if (isColor(next) && next.length === 7) {
                onChange(next.toUpperCase());
              }
            }}
          />
        </div>
      )}
    </Field>
  );
}

/** The console palette's fields and a terminal drawn in its colors. */
function ConsoleEditor({
  theme,
  onChange,
}: {
  theme: Theme;
  onChange: (key: ConsoleKey, value: string | undefined) => void;
}) {
  const intl = useIntl();
  const colors = consoleColors(theme);
  const fallback = consoleColors({ ...theme, console: {} });
  const line = (key: ConsoleKey, symbol: string, text: string) => (
    <p style={{ color: colors[key] }}>
      <span aria-hidden="true">{symbol} </span>
      {text}
    </p>
  );
  return (
    <div className="flex flex-col gap-4">
      <p className="text-muted-foreground text-sm">
        {intl.formatMessage(messages.consoleDescription)}
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        {consoleKeys.map((key) => (
          <ConsoleColorField
            key={key}
            label={consoleLabel(intl, key)}
            value={theme.console?.[key]}
            fallback={fallback[key] ?? ""}
            onChange={(value) => onChange(key, value)}
          />
        ))}
      </div>
      <section
        aria-label={intl.formatMessage(messages.consolePreview)}
        className="rounded-md p-4 font-mono text-sm"
        style={{ backgroundColor: colors.background, color: colors.foreground }}
      >
        <p style={{ color: colors.accent }}>
          <span aria-hidden="true">$ </span>
          {intl.formatMessage(messages.consoleSampleCommand)}
        </p>
        {line("foreground", " ", intl.formatMessage(messages.consoleSampleAddress))}
        {line("success", "✓", intl.formatMessage(messages.consoleSampleOk))}
        {line("warning", "!", intl.formatMessage(messages.consoleSampleWarning))}
        {line("danger", "✗", intl.formatMessage(messages.consoleSampleDanger))}
      </section>
    </div>
  );
}

/** A font's files in the theme package, one path per line. */
function FontFilesField({
  which,
  files,
  onChange,
}: {
  which: "sans" | "mono";
  files: readonly string[];
  onChange: (files: string[]) => void;
}) {
  const intl = useIntl();
  const [text, setText] = useState(files.join("\n"));
  const joined = files.join("\n");
  useEffect(() => setText(joined), [joined]);
  return (
    <Field
      label={intl.formatMessage(messages.themeFontFiles, { which })}
      description={intl.formatMessage(messages.themeFontFilesHelp)}
    >
      <Textarea
        value={text}
        rows={2}
        spellCheck={false}
        className="font-mono"
        onChange={(e) => {
          setText(e.target.value);
          onChange(
            e.target.value
              .split("\n")
              .map((line) => line.trim())
              .filter((line) => line !== ""),
          );
        }}
      />
    </Field>
  );
}

function readingErrors(intl: IntlShape, reading: ThemeReading): string[] {
  if (reading.error === "too-large") {
    return [intl.formatMessage(messages.themeImportTooLarge)];
  }
  if (reading.error === "unreadable") {
    return [intl.formatMessage(messages.themeImportUnreadable)];
  }
  if (reading.error === "protected") {
    return [
      intl.formatMessage(messages.themeImportProtected, {
        tokens: reading.protectedKeys.join(", "),
      }),
    ];
  }
  return reading.issues.map(
    (i) =>
      `${i.path || intl.formatMessage(messages.themeImportDocument)}: ${issueMessage(intl, i)}`,
  );
}

/**
 * The theme editor: change every token of a theme with a live preview on
 * real components in both modes and a contrast checker, start from a
 * built-in theme or an imported file, try it on the whole panel, and
 * export it as a theme.yaml for a theme package.
 */
export default function ThemeEditor() {
  const intl = useIntl();
  const t = (d: MessageDescriptor, v?: Record<string, string | number>) => intl.formatMessage(d, v);
  const { layers, preview, setPreview } = useTheme();
  const current = useMemo(() => resolveLayers(layers), [layers]);
  const [theme, setTheme] = useState<Theme>(() => structuredClone(preview ?? current));
  const [mode, setMode] = useState<Mode>("light");
  const [importErrors, setImportErrors] = useState<string[]>([]);
  const fileInput = useRef<HTMLInputElement>(null);
  const trying = preview !== null;
  const problems = useMemo(() => checkTheme(theme), [theme]);
  const schemaIssues = useMemo(() => readThemeDocument(theme).issues, [theme]);
  // Free-text lengths and shadows go straight into the page's CSS, so a
  // theme with problems could make the whole panel unreadable, the button
  // that stops it included.
  const blocked = problems.length > 0 || schemaIssues.length > 0;
  const [stoppedTrying, setStoppedTrying] = useState(false);

  useEffect(() => {
    if (!trying) {
      return;
    }
    if (blocked) {
      setPreview(null);
      setStoppedTrying(true);
      return;
    }
    setPreview(theme);
  }, [theme, trying, blocked, setPreview]);
  // Leaving the editor stops trying the theme on the whole panel.
  useEffect(() => () => setPreview(null), [setPreview]);

  const update = (change: (draft: Theme) => void) => {
    setTheme((t0) => {
      const next = structuredClone(t0);
      change(next);
      return next;
    });
  };
  const setField = (
    field: "name" | "id" | "version" | "license" | "author" | "description",
    value: string | undefined,
  ) =>
    update((d) => {
      if (field === "author" || field === "description") {
        d[field] = value;
      } else {
        d[field] = value ?? "";
      }
    });
  const setColor = (m: Mode, key: PaletteKey, value: string) =>
    update((d) => {
      d.tokens[m][key] = value;
    });

  const onImport = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) {
      return;
    }
    const reading = await readThemeFile(await file.text());
    if (reading.theme) {
      setTheme(reading.theme);
      setImportErrors([]);
      toast.success(t(messages.themeImported, { name: reading.theme.name }));
    } else {
      setImportErrors(readingErrors(intl, reading));
    }
  };

  const fileName = theme.id || "theme";
  return (
    <div className="mx-auto flex max-w-7xl flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="font-semibold text-2xl">{t(messages.themeEditorTitle)}</h1>
          <p className="max-w-2xl text-muted-foreground">{t(messages.themeEditorDescription)}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            onValueChange={(id) => {
              setTheme(id === "current" ? structuredClone(current) : builtinTheme(id));
              setImportErrors([]);
            }}
          >
            <SelectTrigger className="w-56" aria-label={t(messages.themeStartFrom)}>
              <SelectValue placeholder={t(messages.themeStartFrom)} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="current">{t(messages.themeCurrent)}</SelectItem>
              {builtinThemes().map((b) => (
                <SelectItem key={b.id} value={b.id}>
                  {builtinName(intl, b)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <input
            ref={fileInput}
            type="file"
            accept=".yaml,.yml,.json,application/json"
            className="hidden"
            tabIndex={-1}
            aria-label={t(messages.themeImport)}
            onChange={(e) => void onImport(e)}
          />
          <Button variant="outline" onClick={() => fileInput.current?.click()}>
            <Upload aria-hidden="true" />
            {t(messages.themeImport)}
          </Button>
          <Button
            variant="outline"
            disabled={blocked}
            onClick={async () =>
              download(`${fileName}.yaml`, await themeToYAML(theme), "application/yaml")
            }
          >
            <Download aria-hidden="true" />
            {t(messages.themeExportYAML)}
          </Button>
          <Button
            variant="outline"
            disabled={blocked}
            onClick={() => download(`${fileName}.json`, themeToJSON(theme), "application/json")}
          >
            <Download aria-hidden="true" />
            {t(messages.themeExportJSON)}
          </Button>
          <Button
            variant={trying ? "secondary" : "default"}
            disabled={!trying && blocked}
            aria-describedby={!trying && blocked ? "theme-try-blocked" : undefined}
            onClick={() => {
              setStoppedTrying(false);
              setPreview(trying ? null : theme);
            }}
          >
            {trying ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
            {t(trying ? messages.themeStopTrying : messages.themeTry)}
          </Button>
        </div>
      </div>

      {importErrors.length > 0 ? (
        <Alert tone="danger" title={t(messages.themeImportFailed)}>
          <ul className="list-disc ps-5">
            {importErrors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {blocked ? (
        <Alert
          tone="warning"
          title={t(messages.themeProblems, { count: problems.length + schemaIssues.length })}
        >
          <p id="theme-try-blocked" className="mb-2">
            {t(stoppedTrying ? messages.themeTryStopped : messages.themeTryBlocked)}
          </p>
          <ul className="list-disc ps-5">
            {schemaIssues.map((i) => (
              <li key={`${i.path}-${i.keyword}`}>{`${i.path}: ${issueMessage(intl, i)}`}</li>
            ))}
            {problems.map((p) => (
              <li key={`${p.mode}-${p.kind}-${p.foreground}-${p.background ?? ""}`}>
                {problemMessage(intl, p)}
              </li>
            ))}
          </ul>
        </Alert>
      ) : (
        <p className="flex items-center gap-2 text-sm text-success">
          <Check className="size-4" aria-hidden="true" />
          {t(messages.themeAllGood)}
        </p>
      )}

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
        <Tabs defaultValue="colors" className="min-w-0">
          <TabsList>
            <TabsTrigger value="colors">{t(messages.themeTabColors)}</TabsTrigger>
            <TabsTrigger value="type">{t(messages.themeTabType)}</TabsTrigger>
            <TabsTrigger value="shape">{t(messages.themeTabShape)}</TabsTrigger>
            <TabsTrigger value="console">{t(messages.themeTabConsole)}</TabsTrigger>
            <TabsTrigger value="details">{t(messages.themeTabDetails)}</TabsTrigger>
          </TabsList>
          <TabsContent value="colors">
            <Tabs
              value={mode}
              onValueChange={(v) => setMode(v as Mode)}
              className="flex flex-col gap-4"
            >
              <TabsList>
                <TabsTrigger value="light">
                  {t(messages.themeModeName, { mode: "light" })}
                </TabsTrigger>
                <TabsTrigger value="dark">
                  {t(messages.themeModeName, { mode: "dark" })}
                </TabsTrigger>
              </TabsList>
              {(["light", "dark"] as const).map((m) => (
                <TabsContent key={m} value={m} className="flex flex-col gap-4">
                  <div className="grid gap-4 sm:grid-cols-2">
                    {paletteKeys.map((key) => (
                      <ColorField
                        key={key}
                        label={tokenLabel(intl, key)}
                        value={theme.tokens[m][key] ?? ""}
                        alpha={key === "overlay"}
                        onChange={(v) => setColor(m, key, v)}
                      />
                    ))}
                  </div>
                  <fieldset className="flex flex-col gap-3">
                    <legend className="mb-2 font-semibold text-sm">{t(messages.themeChart)}</legend>
                    <div className="grid gap-4 sm:grid-cols-2">
                      {theme.tokens[m].chart.map((c, i) => (
                        <div
                          // biome-ignore lint/suspicious/noArrayIndexKey: chart colors are positional
                          key={i}
                          className="flex items-end gap-2"
                        >
                          <div className="min-w-0 flex-1">
                            <ColorField
                              label={tokenLabel(intl, `chart${i + 1}`)}
                              value={c}
                              onChange={(v) =>
                                update((d) => {
                                  d.tokens[m].chart[i] = v;
                                })
                              }
                            />
                          </div>
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={theme.tokens[m].chart.length <= 6}
                            aria-label={t(messages.themeRemoveChart, { n: i + 1 })}
                            onClick={() =>
                              update((d) => {
                                d.tokens[m].chart.splice(i, 1);
                              })
                            }
                          >
                            <Trash2 aria-hidden="true" />
                          </Button>
                        </div>
                      ))}
                    </div>
                    <Button
                      variant="outline"
                      size="sm"
                      className="self-start"
                      disabled={theme.tokens[m].chart.length >= 12}
                      onClick={() =>
                        update((d) => {
                          d.tokens[m].chart.push(d.tokens[m].accent);
                        })
                      }
                    >
                      <Plus aria-hidden="true" />
                      {t(messages.themeAddChart)}
                    </Button>
                  </fieldset>
                </TabsContent>
              ))}
            </Tabs>
          </TabsContent>
          <TabsContent value="type" className="grid gap-4 sm:grid-cols-2">
            <Field label={t(messages.themeFontSans)} description={t(messages.themeFontHelp)}>
              <Input
                value={theme.tokens.typography?.sans?.family ?? ""}
                onChange={(e) =>
                  update((d) => {
                    d.tokens.typography = {
                      ...d.tokens.typography,
                      sans: { ...d.tokens.typography?.sans, family: e.target.value },
                    };
                  })
                }
              />
            </Field>
            <Field label={t(messages.themeFontMono)}>
              <Input
                value={theme.tokens.typography?.mono?.family ?? ""}
                onChange={(e) =>
                  update((d) => {
                    d.tokens.typography = {
                      ...d.tokens.typography,
                      mono: { ...d.tokens.typography?.mono, family: e.target.value },
                    };
                  })
                }
              />
            </Field>
            {(["sans", "mono"] as const).map((which) => (
              <FontFilesField
                key={which}
                which={which}
                files={theme.tokens.typography?.[which]?.files ?? []}
                onChange={(files) =>
                  update((d) => {
                    const font = { family: "", ...d.tokens.typography?.[which] };
                    if (files.length > 0) {
                      font.files = files;
                    } else {
                      delete font.files;
                    }
                    d.tokens.typography = { ...d.tokens.typography, [which]: font };
                  })
                }
              />
            ))}
            <Field label={t(messages.themeBaseSize)}>
              <Input
                value={theme.tokens.typography?.baseSize ?? ""}
                onChange={(e) =>
                  update((d) => {
                    d.tokens.typography = { ...d.tokens.typography, baseSize: e.target.value };
                  })
                }
              />
            </Field>
            <Field label={t(messages.themeScale)} description={t(messages.themeScaleHelp)}>
              <Input
                type="number"
                step={0.025}
                min={1}
                max={2}
                value={theme.tokens.typography?.scale ?? ""}
                onChange={(e) =>
                  update((d) => {
                    const n = e.target.valueAsNumber;
                    d.tokens.typography = {
                      ...d.tokens.typography,
                      scale: Number.isNaN(n) ? undefined : n,
                    };
                  })
                }
              />
            </Field>
            {(["regular", "medium", "semibold", "bold"] as const).map((w) => (
              <Field key={w} label={t(messages.themeWeight, { weight: w })}>
                <Input
                  type="number"
                  step={100}
                  min={100}
                  max={900}
                  value={theme.tokens.typography?.weights?.[w] ?? ""}
                  onChange={(e) =>
                    update((d) => {
                      const n = e.target.valueAsNumber;
                      d.tokens.typography = {
                        ...d.tokens.typography,
                        weights: {
                          ...d.tokens.typography?.weights,
                          [w]: Number.isNaN(n) ? undefined : n,
                        },
                      };
                    })
                  }
                />
              </Field>
            ))}
          </TabsContent>
          <TabsContent value="shape" className="grid gap-4 sm:grid-cols-2">
            <Field label={t(messages.themeDensity)}>
              {(control) => (
                <Select
                  value={theme.tokens.density ?? "comfortable"}
                  onValueChange={(v) =>
                    update((d) => {
                      d.tokens.density = v as "comfortable" | "compact";
                    })
                  }
                >
                  <SelectTrigger {...control}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="comfortable">{t(messages.densityComfortable)}</SelectItem>
                    <SelectItem value="compact">{t(messages.densityCompact)}</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </Field>
            {(["sm", "md", "lg", "xl"] as const).map((size) => (
              <Field key={`radius-${size}`} label={t(messages.themeRadius, { size })}>
                <Input
                  value={theme.tokens.radius?.[size] ?? ""}
                  onChange={(e) =>
                    update((d) => {
                      d.tokens.radius = { ...d.tokens.radius, [size]: e.target.value };
                    })
                  }
                />
              </Field>
            ))}
            {(["sm", "md", "lg"] as const).map((size) => (
              <Field
                key={`shadow-${size}`}
                label={t(messages.themeShadow, { size })}
                description={t(messages.themeShadowHelp)}
                className="sm:col-span-2"
              >
                <Input
                  value={theme.tokens.shadows?.[size] ?? ""}
                  onChange={(e) =>
                    update((d) => {
                      d.tokens.shadows = { ...d.tokens.shadows, [size]: e.target.value };
                    })
                  }
                />
              </Field>
            ))}
            {(["fast", "normal", "slow"] as const).map((speed) => (
              <Field key={speed} label={t(messages.themeMotion, { speed })}>
                <Input
                  value={theme.tokens.motion?.[speed] ?? ""}
                  onChange={(e) =>
                    update((d) => {
                      d.tokens.motion = { ...d.tokens.motion, [speed]: e.target.value };
                    })
                  }
                />
              </Field>
            ))}
            <Field label={t(messages.themeFocusWidth)}>
              <Input
                value={theme.tokens.focusRing?.width ?? ""}
                onChange={(e) =>
                  update((d) => {
                    d.tokens.focusRing = { ...d.tokens.focusRing, width: e.target.value };
                  })
                }
              />
            </Field>
            <Field label={t(messages.themeFocusOffset)}>
              <Input
                value={theme.tokens.focusRing?.offset ?? ""}
                onChange={(e) =>
                  update((d) => {
                    d.tokens.focusRing = { ...d.tokens.focusRing, offset: e.target.value };
                  })
                }
              />
            </Field>
          </TabsContent>
          <TabsContent value="console">
            <ConsoleEditor
              theme={theme}
              onChange={(key, value) =>
                update((d) => {
                  const next = { ...d.console };
                  if (value === undefined) {
                    delete next[key];
                  } else {
                    next[key] = value;
                  }
                  d.console = Object.keys(next).length > 0 ? next : undefined;
                })
              }
            />
          </TabsContent>
          <TabsContent value="details" className="grid gap-4 sm:grid-cols-2">
            <Field label={t(messages.themeName)}>
              <Input value={theme.name} onChange={(e) => setField("name", e.target.value)} />
            </Field>
            <Field label={t(messages.themeId)} description={t(messages.themeIdHelp)}>
              <Input
                value={theme.id}
                onChange={(e) => setField("id", e.target.value)}
                spellCheck={false}
              />
            </Field>
            <Field label={t(messages.themeVersion)}>
              <Input value={theme.version} onChange={(e) => setField("version", e.target.value)} />
            </Field>
            <Field label={t(messages.themeLicense)} description={t(messages.themeLicenseHelp)}>
              <Input value={theme.license} onChange={(e) => setField("license", e.target.value)} />
            </Field>
            <Field label={t(messages.themeAuthor)}>
              <Input
                value={theme.author ?? ""}
                onChange={(e) => setField("author", e.target.value || undefined)}
              />
            </Field>
            <Field label={t(messages.themeDescriptionField)} className="sm:col-span-2">
              <Textarea
                value={theme.description ?? ""}
                onChange={(e) => setField("description", e.target.value || undefined)}
              />
            </Field>
          </TabsContent>
        </Tabs>
        <div className="flex min-w-0 flex-col gap-4">
          <div className="flex items-center justify-between gap-2">
            <h2 className="font-semibold text-lg">{t(messages.themePreview)}</h2>
            <Button variant="ghost" size="sm" onClick={() => setTheme(structuredClone(current))}>
              <RotateCcw aria-hidden="true" />
              {t(messages.themeReset)}
            </Button>
          </div>
          <ThemePreview theme={theme} mode="light" />
          <ThemePreview theme={theme} mode="dark" />
          <ContrastChecker theme={theme} />
        </div>
      </div>
    </div>
  );
}
