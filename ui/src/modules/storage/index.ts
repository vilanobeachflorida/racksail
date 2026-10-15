import { HardDrive } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "storage",
  name: m("storage.name"),
  description: m("storage.description"),
  icon: HardDrive,
});
