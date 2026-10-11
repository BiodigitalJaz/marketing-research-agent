"use client";

import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "@/lib/api/client";

export const monitorProgressKey = (id: string) => ["monitors", id, "progress"] as const;

const ACTIVE = new Set(["queued", "collecting", "scoring"]);

/** Live progress of a monitor's current or latest run: queued, collecting sources, scoring items, done. */
export function MonitorProgress({ id }: { id: string }) {
  const { data: p } = useQuery({
    queryKey: monitorProgressKey(id),
    queryFn: async () => unwrap(await api.GET("/monitors/{id}/progress", { params: { path: { id } } })),
    refetchInterval: (q) => (q.state.data && ACTIVE.has(q.state.data.phase) ? 2000 : 30_000),
  });
  if (!p || p.phase === "idle") return null;

  switch (p.phase) {
    case "queued":
      return <Bar label="Queued: waiting for the worker" />;
    case "collecting":
      return (
        <Bar
          value={p.sources_done}
          max={Math.max(p.sources_total, 1)}
          label={`Collecting: ${p.sources_done} of ${p.sources_total} sources · ${p.collected} new item${p.collected === 1 ? "" : "s"} so far`}
        />
      );
    case "scoring":
      return (
        <Bar
          value={p.processed}
          max={Math.max(p.collected, 1)}
          label={`Scoring: ${p.processed} of ${p.collected} items · ${p.opportunities} opportunit${p.opportunities === 1 ? "y" : "ies"} so far`}
        />
      );
    default:
      return (
        <p className="text-xs text-muted-foreground">
          Latest run: {p.collected} new item{p.collected === 1 ? "" : "s"}, all scored · {p.opportunities} opportunit{p.opportunities === 1 ? "y" : "ies"}
        </p>
      );
  }
}

function Bar({ value, max, label }: { value?: number; max?: number; label: string }) {
  const determinate = value !== undefined && max !== undefined;
  const pct = determinate ? Math.min(100, Math.round((value / max) * 100)) : 0;
  return (
    <div className="flex max-w-md flex-col gap-1">
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={determinate ? max : undefined}
        aria-valuenow={determinate ? value : undefined}
        className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
      >
        <div className={determinate ? "h-full bg-primary transition-[width] duration-500" : "h-full w-1/3 animate-pulse bg-primary/60"} style={determinate ? { width: `${pct}%` } : undefined} />
      </div>
      <p className="text-xs text-muted-foreground">{label}</p>
    </div>
  );
}
