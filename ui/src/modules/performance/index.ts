import { Gauge } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "performance",
  name: m("performance.name"),
  description: m("performance.description"),
  icon: Gauge,
});
