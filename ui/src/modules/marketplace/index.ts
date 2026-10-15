import { Store } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "marketplace",
  name: m("marketplace.name"),
  description: m("marketplace.description"),
  icon: Store,
});
