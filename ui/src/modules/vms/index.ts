import { MonitorCog } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "vms",
  name: m("vms.name"),
  description: m("vms.description"),
  icon: MonitorCog,
});
