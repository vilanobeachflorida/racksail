import { Mail } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "mail",
  name: m("mail.name"),
  description: m("mail.description"),
  icon: Mail,
});
