import {
  Activity,
  Bell,
  ChartLine,
  Cloud,
  Database,
  FileText,
  Globe,
  HardDrive,
  KeyRound,
  Lock,
  type LucideIcon,
  Mail,
  Puzzle,
  Server,
  Shield,
  ShieldCheck,
  Users,
  Wrench,
} from "lucide-react";

/**
 * The icons an add-on's entry can name, by their names in Lucide. The panel
 * bundles these few rather than every icon; any other name shows the add-on
 * icon. They load with the parts of the panel that show entries.
 */
export const addonIcons: Readonly<Record<string, LucideIcon>> = {
  activity: Activity,
  bell: Bell,
  "chart-line": ChartLine,
  cloud: Cloud,
  database: Database,
  "file-text": FileText,
  globe: Globe,
  "hard-drive": HardDrive,
  "key-round": KeyRound,
  lock: Lock,
  mail: Mail,
  puzzle: Puzzle,
  server: Server,
  shield: Shield,
  "shield-check": ShieldCheck,
  users: Users,
  wrench: Wrench,
};

/** The icon of an add-on's entry. */
export function addonIcon(name: string | undefined): LucideIcon {
  return (name !== undefined && Object.hasOwn(addonIcons, name) && addonIcons[name]) || Puzzle;
}
