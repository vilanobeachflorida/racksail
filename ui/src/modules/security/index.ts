import { ShieldCheck } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "security",
  name: m("security.name"),
  description: m("security.description"),
  icon: ShieldCheck,
});
