import { Hammer } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "builder",
  name: m("builder.name"),
  description: m("builder.description"),
  icon: Hammer,
});
