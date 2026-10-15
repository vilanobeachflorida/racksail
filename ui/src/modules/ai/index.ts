import { Sparkles } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "ai",
  name: m("ai.name"),
  description: m("ai.description"),
  icon: Sparkles,
});
