import { Link } from "@tanstack/react-router";
import { Paintbrush } from "lucide-react";
import { useId } from "react";
import { type MessageDescriptor, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/design/components/ui/card";
import { RadioGroup, RadioGroupItem } from "@/design/components/ui/choice";
import { Label } from "@/design/components/ui/field";
import { type Preferences, preferenceOptions } from "@/design/theme/prefs";
import { useTheme } from "@/design/theme/provider";
import { messages } from "../messages";
import { ThemeLayers } from "./theme-layers";

type Option<K extends keyof Preferences> = {
  value: Preferences[K];
  label: string;
  description?: string;
};

function Choice<K extends keyof Preferences>({
  name,
  title,
  description,
  options,
}: {
  name: K;
  title: string;
  description: string;
  options: Option<K>[];
}) {
  const { preferences, setPreferences } = useTheme();
  const id = useId();
  return (
    <Card aria-labelledby={`${id}-title`}>
      <CardHeader>
        <CardTitle id={`${id}-title`}>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <RadioGroup
        aria-labelledby={`${id}-title`}
        value={preferences[name]}
        onValueChange={(v) => setPreferences({ [name]: v } as Partial<Preferences>)}
        className="gap-3"
      >
        {options.map((o) => (
          <div key={o.value} className="flex items-start gap-3">
            <RadioGroupItem id={`${id}-${o.value}`} value={o.value} className="mt-0.5" />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor={`${id}-${o.value}`}>{o.label}</Label>
              {o.description ? (
                <p className="text-muted-foreground text-xs">{o.description}</p>
              ) : null}
            </div>
          </div>
        ))}
      </RadioGroup>
    </Card>
  );
}

/**
 * Personal display settings, the top theme layer: mode, contrast, density,
 * text size and motion, over the themes the server and reseller set.
 */
export default function AppearanceSettings() {
  const intl = useIntl();
  const t = (d: MessageDescriptor) => intl.formatMessage(d);
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{t(messages.appearanceTitle)}</h1>
        <p className="text-muted-foreground">{t(messages.appearanceDescription)}</p>
      </div>
      <Choice
        name="mode"
        title={t(messages.appearanceMode)}
        description={t(messages.appearanceModeHelp)}
        options={preferenceOptions.mode.map((value) => ({
          value,
          label: t(
            { system: messages.modeSystem, light: messages.modeLight, dark: messages.modeDark }[
              value
            ],
          ),
        }))}
      />
      <Choice
        name="contrast"
        title={t(messages.appearanceContrast)}
        description={t(messages.appearanceContrastHelp)}
        options={preferenceOptions.contrast.map((value) => ({
          value,
          label: t(
            {
              system: messages.contrastSystem,
              standard: messages.contrastStandard,
              high: messages.contrastHigh,
            }[value],
          ),
        }))}
      />
      <Choice
        name="density"
        title={t(messages.appearanceDensity)}
        description={t(messages.appearanceDensityHelp)}
        options={preferenceOptions.density.map((value) => ({
          value,
          label: t(
            {
              theme: messages.densityTheme,
              comfortable: messages.densityComfortable,
              compact: messages.densityCompact,
            }[value],
          ),
        }))}
      />
      <Choice
        name="textSize"
        title={t(messages.appearanceTextSize)}
        description={t(messages.appearanceTextSizeHelp)}
        options={preferenceOptions.textSize.map((value) => ({
          value,
          label: intl.formatMessage(messages.textSizeOption, { size: value }),
        }))}
      />
      <Choice
        name="motion"
        title={t(messages.appearanceMotion)}
        description={t(messages.appearanceMotionHelp)}
        options={preferenceOptions.motion.map((value) => ({
          value,
          label: t(
            {
              system: messages.motionSystem,
              reduce: messages.motionReduce,
              full: messages.motionFull,
            }[value],
          ),
        }))}
      />
      <ThemeLayers />
      <Card>
        <CardHeader>
          <CardTitle>{t(messages.themeEditorTitle)}</CardTitle>
          <CardDescription>{t(messages.themeEditorTeaser)}</CardDescription>
        </CardHeader>
        <Button asChild variant="outline" className="self-start">
          <Link to="/settings/appearance/theme">
            <Paintbrush aria-hidden="true" />
            {t(messages.themeEditorOpen)}
          </Link>
        </Button>
      </Card>
    </div>
  );
}
