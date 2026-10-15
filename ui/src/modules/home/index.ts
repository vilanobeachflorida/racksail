import { House } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "home",
  name: m("home.name"),
  description: m("home.description"),
  icon: House,
});
