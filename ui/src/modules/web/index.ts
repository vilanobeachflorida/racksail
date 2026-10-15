import { Globe } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "web",
  name: m("web.name"),
  description: m("web.description"),
  icon: Globe,
});
