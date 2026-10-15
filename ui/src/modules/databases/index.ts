import { Database } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "databases",
  name: m("databases.name"),
  description: m("databases.description"),
  icon: Database,
});
