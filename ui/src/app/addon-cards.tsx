import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { AddonFrame } from "./addon-frame";
import { type AddonEntry, addonEntryKey } from "./addons";

/**
 * The cards add-ons add to the home screen, each the add-on's own page in
 * its frame. The home screen loads them only when there are some.
 */
export default function AddonCards({ entries }: { entries: readonly AddonEntry[] }) {
  return entries.map((entry) => (
    <Card key={addonEntryKey(entry)} aria-label={entry.ui.label}>
      <CardHeader>
        <CardTitle>{entry.ui.label}</CardTitle>
      </CardHeader>
      <CardContent>
        <AddonFrame
          addon={entry.addon}
          path={entry.ui.path ?? ""}
          title={entry.ui.label}
          className="h-48"
        />
      </CardContent>
    </Card>
  ));
}
