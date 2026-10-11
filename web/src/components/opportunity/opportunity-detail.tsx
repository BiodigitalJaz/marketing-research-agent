"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";

import { DismissDialog } from "@/components/inbox/dismiss-dialog";
import { age, deadlineLabel, FACTOR_LABEL, FLAG_LABEL, LANE_LABEL, TYPE_LABEL } from "@/components/inbox/meta";
import { ScoreBadge } from "@/components/inbox/score-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { api, errorMessage, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { DraftsPanel } from "./drafts-panel";

const OUTCOMES: { v: "no_response" | "reply" | "dm" | "meeting" | "lead" | "client"; label: string }[] = [
  { v: "no_response", label: "No response" },
  { v: "reply", label: "Got a reply" },
  { v: "dm", label: "Moved to DM" },
  { v: "meeting", label: "Meeting booked" },
  { v: "lead", label: "Became a lead" },
  { v: "client", label: "Became a customer" },
];

type StatusBody = { status: "saved" | "acted" | "dismissed" | "snoozed" | "new"; reason?: string; note?: string; snoozed_until?: string };

export function OpportunityDetail({ id }: { id: string }) {
  const { canEdit } = useSession();
  const qc = useQueryClient();
  const [dismissing, setDismissing] = useState(false);
  const [outcome, setOutcome] = useState("");
  const key = ["opportunity", id];
  const { data: opp, error, isPending } = useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/opportunities/{id}", { params: { path: { id } } })),
    refetchInterval: (q) => (q.state.data?.drafting.state === "pending" ? 2500 : false),
  });

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["opportunities"] });
  };
  async function setStatus(body: StatusBody) {
    try {
      unwrap(await api.POST("/opportunities/{id}/status", { params: { path: { id } }, body: body as never }));
      toast.success(body.status === "dismissed" ? "Dismissed" : body.status === "snoozed" ? "Snoozed" : body.status === "new" ? "Restored" : `Marked ${body.status}`);
      refresh();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }
  async function logEvent(body: { event: "opened_link" | "outcome"; outcome?: (typeof OUTCOMES)[number]["v"] }) {
    try {
      unwrap(await api.POST("/opportunities/{id}/events", { params: { path: { id } }, body }));
      if (body.event === "outcome") {
        toast.success("Outcome logged");
        setOutcome("");
      }
      refresh();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }

  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (error || !opp) return <p className="text-sm text-destructive">{errorMessage(error)}</p>;

  const flags = opp.score_breakdown.flags ?? [];
  const dl = opp.deadline ? deadlineLabel(opp.deadline) : null;
  const item = opp.item;
  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1">
        <Link href="/inbox" className="text-sm text-muted-foreground hover:underline">← Inbox</Link>
        <div className="flex items-start justify-between gap-3">
          <h1 className="text-xl font-semibold break-words">{item.title || item.url}</h1>
          <ScoreBadge opp={opp} />
        </div>
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
          <span>{LANE_LABEL[opp.lane]}</span>
          <span>· {TYPE_LABEL[opp.opp_type] ?? opp.opp_type}</span>
          <span>· {item.community || item.source_name}</span>
          {item.author_handle && <span>· {item.author_handle}</span>}
          <span>· {age(item.published_at ?? item.fetched_at)}</span>
          {dl && <Badge variant={dl.urgent ? "destructive" : "outline"}>{dl.text}</Badge>}
          {opp.status !== "new" && <Badge variant="secondary">{opp.status}</Badge>}
        </p>
      </div>

      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" onClick={() => { window.open(item.url, "_blank", "noopener,noreferrer"); void logEvent({ event: "opened_link" }); }}>
          Open original
        </Button>
        {canEdit && opp.status !== "saved" && <Button size="sm" variant="outline" onClick={() => void setStatus({ status: "saved" })}>Save</Button>}
        {canEdit && <Button size="sm" variant="outline" onClick={() => void setStatus({ status: "snoozed", snoozed_until: new Date(Date.now() + 86_400_000).toISOString() })}>Snooze 1d</Button>}
        {canEdit && opp.status !== "acted" && <Button size="sm" variant="outline" onClick={() => void setStatus({ status: "acted" })}>Mark acted</Button>}
        {canEdit && opp.status !== "dismissed" && <Button size="sm" variant="ghost" onClick={() => setDismissing(true)}>Dismiss</Button>}
        {canEdit && opp.status !== "new" && <Button size="sm" variant="ghost" onClick={() => void setStatus({ status: "new" })}>Restore</Button>}
      </div>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <DraftsPanel opp={opp} canEdit={canEdit} />

        <aside className="flex min-w-0 flex-col gap-4">
          <section className="flex flex-col gap-2">
            <h2 className="font-semibold">Why it surfaced</h2>
            <p className="text-sm">{opp.explanation}</p>
            {flags.length > 0 && (
              <div className="flex flex-wrap gap-1">
                {flags.map((f) => <Badge key={f} variant={f === "performance_claim_risk" || f === "hype_or_scam" ? "destructive" : "outline"}>{FLAG_LABEL[f] ?? f}</Badge>)}
              </div>
            )}
            <table className="w-full text-sm">
              <tbody>
                {Object.entries(opp.score_breakdown.factors ?? {}).map(([k, v]) => (
                  <tr key={k} className="border-b last:border-0"><td className="py-1">{FACTOR_LABEL[k] ?? k}</td><td className="py-1 text-right tabular-nums">{Math.round(v * 100)}</td></tr>
                ))}
              </tbody>
            </table>
          </section>

          <section className="flex flex-col gap-2">
            <h2 className="font-semibold">Original post</h2>
            <blockquote className="max-h-96 overflow-y-auto rounded-md border-l-4 bg-muted/40 p-3 text-sm whitespace-pre-wrap">{item.body}</blockquote>
            <p className="text-xs text-muted-foreground">From {item.source_name}. Collected content is shown as-is.</p>
          </section>

          <section className="flex flex-col gap-2">
            <h2 className="font-semibold">Outcome</h2>
            <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); if (outcome) void logEvent({ event: "outcome", outcome: outcome as (typeof OUTCOMES)[number]["v"] }); }}>
              <NativeSelect aria-label="Outcome" className="w-48" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
                <option value="">Choose an outcome…</option>
                {OUTCOMES.map((o) => <option key={o.v} value={o.v}>{o.label}</option>)}
              </NativeSelect>
              <Button type="submit" size="sm" variant="outline" disabled={!outcome}>Log outcome</Button>
            </form>
            {opp.events.length > 0 && (
              <ul className="flex flex-col gap-0.5 text-xs text-muted-foreground">
                {opp.events.map((e, i) => (
                  <li key={i}>{new Date(e.created_at).toLocaleString()}: {e.event.replace("_", " ")}{e.reason && ` (${e.reason})`}{e.outcome && ` → ${OUTCOMES.find((o) => o.v === e.outcome)?.label ?? e.outcome}`}</li>
                ))}
              </ul>
            )}
          </section>
        </aside>
      </div>

      <DismissDialog key={String(dismissing)} open={dismissing} onCancel={() => setDismissing(false)}
        onConfirm={(reason, note) => { setDismissing(false); void setStatus({ status: "dismissed", reason, note: note || undefined }); }} />
    </div>
  );
}
