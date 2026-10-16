import { addonIcon } from "./addon-icons";
import { type AddonEntry, addonEntryKey, addonScreenPath } from "./addons";
import { RailLink } from "./rail";

/**
 * The navigation entries of the add-ons the person may use, after the
 * panel's own. The rail loads them, with the icons they name, only when
 * there are some.
 */
export default function AddonNavLinks({
  entries,
  wide,
  onNavigate,
}: {
  entries: readonly AddonEntry[];
  wide?: boolean;
  onNavigate?: () => void;
}) {
  return entries.map((entry) => (
    <li key={addonEntryKey(entry)}>
      <RailLink
        to={addonScreenPath(entry)}
        icon={addonIcon(entry.ui.icon)}
        label={entry.ui.label}
        onNavigate={onNavigate}
        wide={wide}
      />
    </li>
  ));
}
