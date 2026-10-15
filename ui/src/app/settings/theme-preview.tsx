import { Globe, House, Server, Settings } from "lucide-react";
import { useId, useInsertionEffect, useMemo } from "react";
import { type MessageDescriptor, useIntl } from "react-intl";
import { Sparkline } from "@/design/components/charts/sparkline";
import { Mark } from "@/design/components/mark";
import { Button } from "@/design/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Checkbox, Switch } from "@/design/components/ui/choice";
import { Alert, Badge, Progress } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import { StatTile, StatusDot } from "@/design/components/ui/states";
import { renderScopedCSS } from "@/design/theme/css";
import type { Theme } from "@/design/theme/types";
import { withKeys } from "@/lib/utils";
import { messages } from "../messages";

const series = [3, 5, 4, 6, 8, 7, 9, 12, 10, 13, 12, 15];

/**
 * Real components drawn with a theme inside one element, in light or dark
 * mode, without changing the rest of the page.
 */
export function ThemePreview({
  theme,
  mode,
  highContrast = false,
}: {
  theme: Theme;
  mode: "light" | "dark";
  highContrast?: boolean;
}) {
  const intl = useIntl();
  const t = (d: MessageDescriptor, v?: Record<string, string | number>) => intl.formatMessage(d, v);
  const raw = useId();
  const scope = `rs-preview-${raw.replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const css = useMemo(
    () => renderScopedCSS(theme, `.${scope}`, mode, highContrast),
    [theme, scope, mode, highContrast],
  );
  useInsertionEffect(() => {
    const style = document.createElement("style");
    style.textContent = css;
    document.head.append(style);
    return () => style.remove();
  }, [css]);
  const mark =
    mode === "dark"
      ? "[--mark-ink:var(--rs-text)] [--mark-sea:var(--rs-brand)]"
      : "[--mark-ink:var(--rs-brand)] [--mark-sea:var(--rs-focus)]";
  return (
    <section
      aria-label={t(messages.themePreviewLabel, { mode })}
      className={`${scope} ${mark} overflow-hidden rounded-lg border border-border bg-background font-sans text-foreground`}
      style={{ colorScheme: mode }}
    >
      <div className="flex h-12 items-center gap-2 border-border border-b bg-card px-3">
        <Mark className="h-6" />
        <span className="font-semibold">{t(messages.name)}</span>
        <span className="ms-auto text-muted-foreground text-xs">
          {t(messages.themePreviewSample)}
        </span>
      </div>
      <div className="flex">
        <div className="flex w-14 flex-col items-center gap-2 border-border border-e bg-card py-3 text-muted-foreground">
          <House className="size-5 text-foreground" aria-hidden="true" />
          <Globe className="size-5" aria-hidden="true" />
          <Server className="size-5" aria-hidden="true" />
          <Settings className="size-5" aria-hidden="true" />
        </div>
        <div className="flex min-w-0 flex-1 flex-col gap-4 p-4">
          <div className="flex flex-col gap-1">
            <p className="font-semibold text-xl">{t(messages.themePreviewHeading)}</p>
            <p className="text-muted-foreground text-sm">{t(messages.themePreviewBody)}</p>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <StatTile
              label={t(messages.themePreviewRequests)}
              value={intl.formatNumber(12480)}
              delta={intl.formatNumber(0.12, { style: "percent", signDisplay: "always" })}
              trend="up-good"
              chart={<Sparkline values={series} label={t(messages.themePreviewRequests)} />}
            />
            <Card className="gap-2">
              <CardHeader>
                <CardTitle className="text-sm">{t(messages.themePreviewServices)}</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-1.5">
                <StatusDot status="ok" label="web" />
                <StatusDot status="warning" label="mail" />
                <StatusDot status="error" label="backups" />
              </CardContent>
            </Card>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button>{t(messages.themePreviewPrimary)}</Button>
            <Button variant="outline">{t(messages.themePreviewSecondary)}</Button>
            <Button variant="destructive">{t(messages.themePreviewDanger)}</Button>
            <Button variant="link">{t(messages.themePreviewLink)}</Button>
          </div>
          <div className="flex flex-wrap gap-2">
            <Badge variant="success">{t(messages.themePreviewBadge, { tone: "success" })}</Badge>
            <Badge variant="warning">{t(messages.themePreviewBadge, { tone: "warning" })}</Badge>
            <Badge variant="danger">{t(messages.themePreviewBadge, { tone: "danger" })}</Badge>
            <Badge variant="info">{t(messages.themePreviewBadge, { tone: "info" })}</Badge>
            <Badge variant="brand">{t(messages.name)}</Badge>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label={t(messages.themePreviewField)}
              description={t(messages.themePreviewFieldHelp)}
            >
              <Input defaultValue="example.com" />
            </Field>
            <div className="flex flex-col justify-end gap-3">
              <div className="flex items-center gap-2 text-sm">
                <Checkbox defaultChecked aria-label={t(messages.themePreviewOption)} />
                {t(messages.themePreviewOption)}
              </div>
              <div className="flex items-center gap-2 text-sm">
                <Switch defaultChecked aria-label={t(messages.themePreviewToggle)} />
                {t(messages.themePreviewToggle)}
              </div>
            </div>
          </div>
          <Progress value={64} aria-label={t(messages.themePreviewProgress)} />
          <Alert tone="warning" title={t(messages.themePreviewAlertTitle)}>
            {t(messages.themePreviewAlertBody)}
          </Alert>
          <ul className="flex flex-wrap gap-1.5" aria-label={t(messages.themeChart)}>
            {withKeys(theme.tokens[mode].chart, (color) => color).map(([key, color], i) => (
              <li
                key={key}
                className="h-4 w-8 rounded-sm"
                title={color}
                style={{ backgroundColor: `var(--rs-chart${i + 1})` }}
              />
            ))}
          </ul>
          <div
            data-critical=""
            className="critical-area rounded-md border-2 border-critical-danger p-3 text-sm"
          >
            <p className="font-semibold text-critical-danger">
              {t(messages.themePreviewProtected)}
            </p>
            <p>{t(messages.themePreviewProtectedBody)}</p>
          </div>
        </div>
      </div>
    </section>
  );
}
