import { Server } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "cluster",
  name: m("cluster.name"),
  description: m("cluster.description"),
  icon: Server,
});
