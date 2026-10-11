import { cn } from "@/lib/utils";

import { FACTOR_LABEL, type Opportunity } from "./meta";

/** Score with a hover/focus breakdown of the weighted factors. */
export function ScoreBadge({ opp }: { opp: Pick<Opportunity, "score" | "score_breakdown"> }) {
  const factors = opp.score_breakdown.factors ?? {};
  const pct = Math.round(opp.score * 100);
  return (
    <span className="group relative inline-flex">
      <span
        tabIndex={0}
        className={cn(
          "inline-flex h-6 min-w-10 items-center justify-center rounded-md px-1.5 text-xs font-semibold tabular-nums outline-none focus-visible:ring-2",
          pct >= 80 ? "bg-emerald-600 text-white" : pct >= 65 ? "bg-emerald-100 text-emerald-900 dark:bg-emerald-900 dark:text-emerald-100" : "bg-muted text-foreground",
        )}
        aria-label={`Score ${pct}`}
      >
        {pct}
      </span>
      <span role="tooltip" className="invisible absolute top-7 right-0 z-20 w-52 rounded-md border bg-popover p-2 text-xs text-popover-foreground shadow-md group-focus-within:visible group-hover:visible">
        {Object.entries(factors).map(([k, v]) => (
          <span key={k} className="flex items-center justify-between gap-2 py-0.5">
            <span>{FACTOR_LABEL[k] ?? k}</span>
            <span className="flex items-center gap-1.5">
              <span className="h-1.5 w-16 overflow-hidden rounded bg-muted"><span className="block h-full bg-primary" style={{ width: `${Math.round(v * 100)}%` }} /></span>
              <span className="w-7 text-right tabular-nums">{Math.round(v * 100)}</span>
            </span>
          </span>
        ))}
      </span>
    </span>
  );
}
