import {
  Activity,
  BookOpenText,
  Boxes,
  CircleDollarSign,
  KeyRound,
  ScrollText,
  Server,
  Settings,
} from "lucide-react";
import { type NavGroup } from "../types";

export const navGroups: NavGroup[] = [
  {
    title: "nav.groupGateway",
    items: [
      { title: "nav.overview", url: "/", icon: Activity },
      { title: "nav.providers", url: "/providers", icon: Server },
      { title: "nav.models", url: "/models", icon: Boxes },
      { title: "nav.keys", url: "/keys", icon: KeyRound },
    ],
  },
  {
    title: "nav.groupObserve",
    items: [
      { title: "nav.logs", url: "/logs", icon: ScrollText },
      { title: "nav.pricing", url: "/pricing", icon: CircleDollarSign },
    ],
  },
  {
    title: "nav.groupSystem",
    items: [
      { title: "nav.settings", url: "/settings", icon: Settings },
      { title: "nav.docs", url: "/docs", icon: BookOpenText },
    ],
  },
];
