import { Signpost } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "dns",
  name: m("dns.name"),
  description: m("dns.description"),
  icon: Signpost,
});
