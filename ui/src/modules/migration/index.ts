import { Truck } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "migration",
  name: m("migration.name"),
  description: m("migration.description"),
  icon: Truck,
});
