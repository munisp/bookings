"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  LayoutDashboard,
  CalendarCheck,
  CalendarDays,
  Store,
  Users,
  Clock,
  BookOpen,
  Mic,
  Globe,
  CreditCard,
  UsersRound,
  BarChart3,
  LineChart,
  Settings,
} from "lucide-react";
import { cn } from "@/lib/utils";

/** Self-hosted Twenty CRM UI (SPEC-CRM §A). */
const CRM_URL = "http://localhost:3100";
/** Grafana dashboards (observability profile, SPEC-W3 §1). */
const GRAFANA_URL = "http://localhost:3002";

type NavItem =
  | { segment: string; label: string; icon: typeof LayoutDashboard }
  | { external: string; label: string; icon: typeof LayoutDashboard };

const items: NavItem[] = [
  { segment: "", label: "Overview", icon: LayoutDashboard },
  { segment: "bookings", label: "Bookings", icon: CalendarCheck },
  { segment: "schedule", label: "My Schedule", icon: CalendarDays },
  { segment: "offerings", label: "Offerings", icon: Store },
  { segment: "team", label: "Team", icon: Users },
  { segment: "availability", label: "Availability", icon: Clock },
  { segment: "knowledge", label: "Knowledge", icon: BookOpen },
  { segment: "voice-agent", label: "Voice Agent", icon: Mic },
  { segment: "public-site", label: "Public Site", icon: Globe },
  { segment: "billing", label: "Billing", icon: CreditCard },
  { segment: "analytics", label: "Analytics", icon: BarChart3 },
  { external: CRM_URL, label: "CRM", icon: UsersRound },
  { external: GRAFANA_URL, label: "Grafana", icon: LineChart },
  { segment: "settings", label: "Settings", icon: Settings },
];

const linkClass = (active: boolean) =>
  cn(
    "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors",
    active
      ? "bg-secondary text-secondary-foreground"
      : "text-muted-foreground hover:bg-accent hover:text-foreground",
  );

export function OrgNav({ orgSlug }: { orgSlug: string }) {
  const pathname = usePathname();
  const base = `/app/${orgSlug}`;

  return (
    <nav className="flex flex-col gap-0.5 px-3">
      {items.map((item) => {
        const Icon = item.icon;
        if ("external" in item) {
          return (
            <a
              key={item.label}
              href={item.external}
              target="_blank"
              rel="noreferrer"
              className={linkClass(false)}
            >
              <Icon className="h-4 w-4 shrink-0" />
              {item.label}
            </a>
          );
        }
        const href = item.segment ? `${base}/${item.segment}` : base;
        const active =
          item.segment === "" ? pathname === base : pathname.startsWith(href);
        return (
          <Link key={href} href={href} className={linkClass(active)}>
            <Icon className="h-4 w-4 shrink-0" />
            {item.label}
          </Link>
        );
      })}
    </nav>
  );
}
