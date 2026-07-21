import { Badge } from "@/components/ui/badge";
import { titleCase } from "@/lib/utils";
import type { BookingStatus } from "@/lib/types";

const variants: Record<
  string,
  "success" | "warning" | "destructive" | "info" | "secondary" | "outline"
> = {
  confirmed: "success",
  completed: "info",
  pending: "warning",
  rescheduled: "secondary",
  cancelled: "destructive",
  no_show: "outline",
};

export function BookingStatusBadge({ status }: { status: BookingStatus }) {
  return (
    <Badge variant={variants[status] ?? "secondary"}>{titleCase(status)}</Badge>
  );
}
