"use client";

import * as React from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { cn, toISODate } from "@/lib/utils";

/**
 * calendar-lite: a compact month grid for picking a single date.
 * `selected`/`onSelect` use local-time YYYY-MM-DD strings.
 */
export function CalendarLite({
  selected,
  onSelect,
  minDate,
  className,
}: {
  selected?: string;
  onSelect: (date: string) => void;
  /** YYYY-MM-DD; days before this are disabled */
  minDate?: string;
  className?: string;
}) {
  const today = toISODate(new Date());
  const initial = selected ?? today;
  const [viewYear, setViewYear] = React.useState(() => +initial.slice(0, 4));
  const [viewMonth, setViewMonth] = React.useState(() => +initial.slice(5, 7) - 1);

  const firstOfMonth = new Date(viewYear, viewMonth, 1);
  const startWeekday = firstOfMonth.getDay();
  const daysInMonth = new Date(viewYear, viewMonth + 1, 0).getDate();

  const move = (delta: number) => {
    const d = new Date(viewYear, viewMonth + delta, 1);
    setViewYear(d.getFullYear());
    setViewMonth(d.getMonth());
  };

  const monthLabel = new Intl.DateTimeFormat("en-US", {
    month: "long",
    year: "numeric",
  }).format(firstOfMonth);

  const cells: (string | null)[] = [];
  for (let i = 0; i < startWeekday; i++) cells.push(null);
  for (let d = 1; d <= daysInMonth; d++) {
    cells.push(toISODate(new Date(viewYear, viewMonth, d)));
  }

  return (
    <div
      className={cn(
        "w-fit rounded-lg border border-border bg-card p-3",
        className,
      )}
    >
      <div className="mb-2 flex items-center justify-between">
        <button
          type="button"
          onClick={() => move(-1)}
          aria-label="Previous month"
          className="rounded-md p-1 hover:bg-accent cursor-pointer"
        >
          <ChevronLeft className="h-4 w-4" />
        </button>
        <span className="text-sm font-medium">{monthLabel}</span>
        <button
          type="button"
          onClick={() => move(1)}
          aria-label="Next month"
          className="rounded-md p-1 hover:bg-accent cursor-pointer"
        >
          <ChevronRight className="h-4 w-4" />
        </button>
      </div>
      <div className="grid grid-cols-7 gap-1 text-center text-xs text-muted-foreground">
        {["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"].map((d) => (
          <div key={d} className="py-1">
            {d}
          </div>
        ))}
        {cells.map((iso, i) =>
          iso === null ? (
            <div key={`blank-${i}`} />
          ) : (
            <button
              key={iso}
              type="button"
              disabled={minDate !== undefined && iso < minDate}
              onClick={() => onSelect(iso)}
              className={cn(
                "h-8 w-8 rounded-md text-sm transition-colors cursor-pointer",
                iso === selected
                  ? "bg-primary text-primary-foreground"
                  : "hover:bg-accent",
                iso === today && iso !== selected && "border border-ring",
                "disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:bg-transparent",
              )}
            >
              {+iso.slice(8, 10)}
            </button>
          ),
        )}
      </div>
    </div>
  );
}
