import { ArchiveRestore } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "backups",
  name: m("backups.name"),
  description: m("backups.description"),
  icon: ArchiveRestore,
});
