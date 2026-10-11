"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { age, deadlineLabel, FLAG_LABEL, LANE_LABEL, type Opportunity, TYPE_LABEL } from "./meta";
import { ScoreBadge } from "./score-badge";

type Props = {
  opp: Opportunity;
  selected: boolean;
  canEdit: boolean;
  onSelect: () => void;
  onOpen: () => void;
  onDetail: () => void;
  onSave: () => void;
  onDismiss: () => void;
  onSnooze: (days: number) => void;
  onActed: () => void;
};

export function OpportunityCard({ opp, selected, canEdit, onSelect, onOpen, onDetail, onSave, onDismiss, onSnooze, onActed }: Props) {
  const flags = opp.score_breakdown.flags ?? [];
  const dl = opp.deadline ? deadlineLabel(opp.deadline) : null;
  return (
    <li
      data-selected={selected}
      onClick={onSelect}
      className={cn("flex flex-col gap-2 rounded-lg border p-4 transition-colors", selected && "border-primary ring-1 ring-primary")}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <button type="button" className="text-left font-medium break-words hover:underline" onClick={(e) => { e.stopPropagation(); onDetail(); }}>
            {opp.title || opp.url}
          </button>
          <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <span>{LANE_LABEL[opp.lane]}</span>
            <span>· {TYPE_LABEL[opp.opp_type] ?? opp.opp_type}</span>
            <span>· {opp.community || opp.source_name}</span>
            <span>· {age(opp.published_at ?? opp.fetched_at)}</span>
            {dl && <Badge variant={dl.urgent ? "destructive" : "outline"}>{dl.text}</Badge>}
            {opp.status !== "new" && <Badge variant="secondary">{opp.status}</Badge>}
          </p>
        </div>
        <ScoreBadge opp={opp} />
      </div>
      {opp.explanation && <p className="text-sm">{opp.explanation}</p>}
      {flags.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {flags.map((f) => (
            <Badge key={f} variant={f === "performance_claim_risk" || f === "hype_or_scam" ? "destructive" : "outline"}>{FLAG_LABEL[f] ?? f}</Badge>
          ))}
        </div>
      )}
      <div className="flex flex-wrap gap-2" onClick={(e) => e.stopPropagation()}>
        <Button size="sm" variant="outline" onClick={onOpen}>Open original</Button>
        {canEdit && opp.status !== "saved" && <Button size="sm" variant="outline" onClick={onSave}>Save</Button>}
        {canEdit && <Button size="sm" variant="outline" onClick={() => onSnooze(1)}>Snooze 1d</Button>}
        {canEdit && <Button size="sm" variant="outline" onClick={onActed}>Mark acted</Button>}
        {canEdit && opp.status !== "dismissed" && <Button size="sm" variant="ghost" onClick={onDismiss}>Dismiss</Button>}
      </div>
    </li>
  );
}
