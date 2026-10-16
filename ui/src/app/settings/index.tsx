import { Link } from "@tanstack/react-router";
import { ChevronRight, Paintbrush, Palette, SlidersHorizontal } from "lucide-react";
import type { ReactNode } from "react";
import { useIntl } from "react-intl";
import { messages } from "../messages";
import { useActiveModules, usePermission } from "../modules";
import { generalMessages } from "../screens/messages";

function Entry({
  to,
  icon,
  title,
  description,
}: {
  to: string;
  icon: ReactNode;
  title: string;
  description?: string;
}) {
  return (
    <li>
      <Link
        to={to}
        className="flex items-center gap-3 rounded-lg border border-border bg-card p-4 hover:bg-accent focus-visible:focus-ring"
      >
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground [&_svg]:size-5">
          {icon}
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="font-medium">{title}</span>
          {description ? (
            <span className="text-muted-foreground text-sm">{description}</span>
          ) : null}
        </span>
        <ChevronRight
          className="size-4 shrink-0 text-muted-foreground rtl:rotate-180"
          aria-hidden="true"
        />
      </Link>
    </li>
  );
}

/** Every settings page: the panel's own, then each module's. */
export default function SettingsIndex() {
  const intl = useIntl();
  const modules = useActiveModules();
  const allowed = usePermission();
  const withPages = modules
    .map((m) => ({ module: m, pages: (m.settings ?? []).filter((p) => allowed(p.permission)) }))
    .filter((m) => m.pages.length > 0);
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-8">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.settingsTitle)}</h1>
        <p className="text-muted-foreground">{intl.formatMessage(messages.settingsDescription)}</p>
      </div>
      <section className="flex flex-col gap-3" aria-labelledby="settings-panel">
        <h2 id="settings-panel" className="font-semibold text-lg">
          {intl.formatMessage(messages.settingsPanel)}
        </h2>
        <ul className="flex flex-col gap-2">
          <Entry
            to="/settings/general"
            icon={<SlidersHorizontal aria-hidden="true" />}
            title={intl.formatMessage(messages.generalTitle)}
            description={intl.formatMessage(generalMessages.teaser)}
          />
          <Entry
            to="/settings/appearance"
            icon={<Palette aria-hidden="true" />}
            title={intl.formatMessage(messages.appearanceTitle)}
            description={intl.formatMessage(messages.appearanceDescription)}
          />
          <Entry
            to="/settings/appearance/theme"
            icon={<Paintbrush aria-hidden="true" />}
            title={intl.formatMessage(messages.themeEditorTitle)}
            description={intl.formatMessage(messages.themeEditorTeaser)}
          />
        </ul>
      </section>
      {withPages.map(({ module, pages }) => (
        <section
          key={module.id}
          className="flex flex-col gap-3"
          aria-labelledby={`settings-${module.id}`}
        >
          <h2
            id={`settings-${module.id}`}
            className="flex items-center gap-2 font-semibold text-lg"
          >
            <module.icon className="size-5 text-muted-foreground" aria-hidden="true" />
            {intl.formatMessage(module.name)}
          </h2>
          <ul className="flex flex-col gap-2">
            {pages.map((p) => (
              <Entry
                key={p.id}
                to={`/settings/${module.id}/${p.id}`}
                icon={<module.icon aria-hidden="true" />}
                title={intl.formatMessage(p.title)}
                description={p.description ? intl.formatMessage(p.description) : undefined}
              />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
