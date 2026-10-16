import { useParams } from "@tanstack/react-router";
import { useIntl } from "react-intl";
import { AddonFrame } from "../addon-frame";
import { addonPath, useAddonEntries } from "../addons";
import { messages } from "../messages";
import { usePageTitle } from "../page-title";

/**
 * One of an add-on's own pages, which its navigation entries, commands
 * and settings pages open: the page fills the screen, in the frame that
 * keeps it apart from the panel.
 */
export default function AddonScreen() {
  const intl = useIntl();
  const params = useParams({ strict: false }) as { addon?: string; _splat?: string };
  const addon = params.addon ?? "";
  const path = params._splat ?? "";
  const entry = useAddonEntries().find((e) => e.addon === addon && addonPath(e.ui.path) === path);
  const title = entry?.ui.label ?? intl.formatMessage(messages.addonsPage, { addon });
  usePageTitle(title);
  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <div className="flex flex-col gap-1">
        <h1 className="font-semibold text-2xl">{title}</h1>
        {entry ? (
          <p className="text-muted-foreground text-sm">
            {intl.formatMessage(messages.addonsFrom, { addon: entry.label })}
          </p>
        ) : null}
      </div>
      <AddonFrame addon={addon} path={path} title={title} className="min-h-96 flex-1" />
    </div>
  );
}
