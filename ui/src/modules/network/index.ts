import { Network } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "network",
  name: m("network.name"),
  description: m("network.description"),
  icon: Network,
});
