import { Activity } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "monitoring",
  name: m("monitoring.name"),
  description: m("monitoring.description"),
  icon: Activity,
});
