import { Layers, Paintbrush, Server, Store, UserRound } from "lucide-react";
import type { ReactNode } from "react";
import { useIntl } from "react-intl";
import { Card, CardDescription, CardHeader, CardTitle } from "@/design/components/ui/card";
import { resolveLayers } from "@/design/theme/layers";
import { useTheme } from "@/design/theme/provider";
import type { Theme } from "@/design/theme/types";
import { appearanceMessages as m } from "../screens/messages";
import { ThemePreview } from "./theme-preview";

function Layer({ icon, title, detail }: { icon: ReactNode; title: string; detail?: string }) {
  return (
    <li className="flex items-start gap-3">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground [&_svg]:size-4">
        {icon}
      </span>
      <span className="flex min-w-0 flex-col">
        <span className="font-medium text-sm">{title}</span>
        {detail ? <span className="text-muted-foreground text-xs">{detail}</span> : null}
      </span>
    </li>
  );
}

/** Whether contrast is high, by the person's choice or, when they follow it, the system's. */
function highContrast(contrast: string): boolean {
  if (contrast !== "system") {
    return contrast === "high";
  }
  return (
    typeof window.matchMedia === "function" && window.matchMedia("(prefers-contrast: more)").matches
  );
}

/**
 * What the panel looks like for the person and why: Racksail's theme, the
 * server's and their reseller's on top when there are, and their own
 * choices last, with a sample drawn the way they see it.
 */
export function ThemeLayers() {
  const intl = useIntl();
  const { layers, mode, preferences } = useTheme();
  const theme = resolveLayers(layers);
  const detail = (t: Theme) =>
    intl.formatMessage(m.layerDetail, {
      version: t.version,
      author: t.author ?? "",
      hasAuthor: t.author ? "yes" : "no",
    });
  return (
    <Card aria-labelledby="theme-layers-title">
      <CardHeader>
        <CardTitle id="theme-layers-title">{intl.formatMessage(m.layersTitle)}</CardTitle>
        <CardDescription>{intl.formatMessage(m.layersHelp)}</CardDescription>
      </CardHeader>
      <ol className="flex flex-col gap-3" aria-label={intl.formatMessage(m.layersList)}>
        <Layer icon={<Layers aria-hidden="true" />} title={intl.formatMessage(m.layerDefault)} />
        {layers.server ? (
          <Layer
            icon={<Server aria-hidden="true" />}
            title={intl.formatMessage(m.layerServer, { name: layers.server.name })}
            detail={detail(layers.server)}
          />
        ) : null}
        {layers.reseller ? (
          <Layer
            icon={<Store aria-hidden="true" />}
            title={intl.formatMessage(m.layerReseller, { name: layers.reseller.name })}
            detail={detail(layers.reseller)}
          />
        ) : null}
        <Layer
          icon={<UserRound aria-hidden="true" />}
          title={intl.formatMessage(m.layerYou)}
          detail={intl.formatMessage(m.layerYouDetail, {
            mode: preferences.mode,
            contrast: preferences.contrast,
            density: preferences.density,
            size: preferences.textSize,
          })}
        />
      </ol>
      {layers.server || layers.reseller ? null : (
        <p className="flex items-center gap-2 text-muted-foreground text-sm">
          <Paintbrush className="size-4 shrink-0" aria-hidden="true" />
          {intl.formatMessage(m.layersOnlyDefault)}
        </p>
      )}
      <ThemePreview theme={theme} mode={mode} highContrast={highContrast(preferences.contrast)} />
    </Card>
  );
}
