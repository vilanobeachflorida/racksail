import { BriefcaseBusiness } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "hosting",
  name: m("hosting.name"),
  description: m("hosting.description"),
  icon: BriefcaseBusiness,
});
